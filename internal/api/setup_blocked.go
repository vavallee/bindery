package api

import (
	"context"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

const (
	blockedAreaStorage        = "storage"
	blockedAreaIndexer        = "indexer"
	blockedAreaDownloadClient = "download-client"

	// storageProbeTTL bounds how often the request path touches the disk.
	storageProbeTTL = 30 * time.Second
	// storageProbeWait is how long a request waits for a probe before
	// answering from the last known result. It keeps a hung network mount
	// from hanging the endpoint.
	storageProbeWait = 2 * time.Second
	// blockedPersistFor is how long a condition must have been seen
	// continuously (across requests) before it is reported. A qBittorrent
	// restart or a single failed probe clears itself well inside this.
	blockedPersistFor = 2 * time.Minute
	// blockedStaleAfter is how long a gap between sightings is allowed before
	// the debounce clock restarts. Sightings come from page loads and tab
	// focus (at most once a minute), so a gap this long means nobody was
	// looking and the earlier sighting says nothing about the time between. Ten
	// minutes is five debounce windows: long enough that a user returning to the
	// tab now and then still accumulates, short enough that a blip hours ago
	// cannot pre-date a fresh one.
	blockedStaleAfter = 10 * time.Minute
)

// blockedChecker derives SetupState.Blocked from state Bindery already holds.
//
// "This install has imported before" is read from the history table: any
// bookImported or downloadFolderImported event. That is the data that
// survives. History rows keep their event when the book is deleted
// (book_id is set NULL), downloads rows are trimmed on a retention schedule,
// and the telemetry funnel marker only exists when telemetry is on. Installs
// that only ever scanned a library, or import through Calibre or Audiobookshelf,
// have no such event and are never flagged: they never relied on this pipeline.
//
// What counts as broken, in order:
//
//  1. A destination the importer writes to is not a writable directory: every
//     root folder, or LibraryDir when there are none, plus AudiobookDir when
//     set and there are no root folders.
//  2. A folder the importer reads from is not a readable directory:
//     DownloadDir (when set) and AudiobookDownloadDir. Writability is NOT
//     required: a failed source removal after a move is only a warning in the
//     importer, so a read only download dir (copy mode) still imports. These
//     are skipped when a path remap is configured globally or on an enabled
//     client, because the client's folder then maps elsewhere and the
//     configured one may be unused.
//  3. No enabled indexer.
//  4. No enabled download client.
//  5. Every enabled client's last stored health check is an error.
//
// Folder checks use access(2) and stat only, never a probe file, so a GET
// leaves nothing behind in the library and cannot race the importer.
type blockedChecker struct {
	cfg         *config.Config
	health      *downloader.HealthStore
	history     *db.HistoryRepo
	rootFolders *db.RootFolderRepo

	// Test seams. nil/zero means the real thing.
	now   func() time.Time
	probe func(path string, write bool) bool
	wait  time.Duration

	// mu guards the fields below and is never held across IO.
	mu         sync.Mutex
	storageAt  time.Time
	storageBad bool
	inflight   chan struct{}
	sinceArea  string
	since      time.Time
	lastSeen   time.Time
}

// WithBlockedChecks supplies what the blocked state is derived from. Without
// history the "imported before" gate cannot be evaluated and nothing is ever
// reported blocked.
func (h *SetupStateHandler) WithBlockedChecks(cfg *config.Config, health *downloader.HealthStore, history *db.HistoryRepo, rootFolders *db.RootFolderRepo) *SetupStateHandler {
	h.blocked.cfg = cfg
	h.blocked.health = health
	h.blocked.history = history
	h.blocked.rootFolders = rootFolders
	return h
}

func (bc *blockedChecker) clock() time.Time {
	if bc.now != nil {
		return bc.now()
	}
	return time.Now()
}

func (bc *blockedChecker) apply(ctx context.Context, clients *db.DownloadClientRepo, state *SetupState) {
	if bc.history == nil {
		return
	}
	imported, err := bc.history.HasEventOfType(ctx, models.HistoryEventBookImported, models.HistoryEventDownloadFolderImport)
	if err != nil || !imported {
		bc.reset()
		return
	}
	area, reason := bc.cause(ctx, clients, state)
	if area == "" || !bc.persisted(area) {
		if area == "" {
			bc.reset()
		}
		return
	}
	state.Blocked, state.BlockedArea, state.BlockedReason = true, area, reason
}

func (bc *blockedChecker) cause(ctx context.Context, clients *db.DownloadClientRepo, state *SetupState) (string, string) {
	var list []models.DownloadClient
	if clients != nil {
		list, _ = clients.List(ctx)
	}
	switch {
	case bc.storageBroken(ctx, list):
		return blockedAreaStorage, "A library or download folder is not available (missing or not accessible), so imports that use it will fail. Check Settings, General, Storage."
	case !state.HasIndexer:
		return blockedAreaIndexer, "No indexer is enabled, so nothing can be searched. Enable one in Settings, Indexers."
	case !state.HasClient:
		return blockedAreaDownloadClient, "No download client is enabled, so nothing can be downloaded. Enable one in Settings, Download Clients."
	case bc.allClientsFailing(list):
		return blockedAreaDownloadClient, "Every enabled download client failed its last health check: it could not be reached, or its download folder is not readable. See Settings, Download Clients."
	}
	return "", ""
}

// persisted reports whether area has been the active condition for at least
// blockedPersistFor, restarting the clock when the area changes.
func (bc *blockedChecker) persisted(area string) bool {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	now := bc.clock()
	if bc.sinceArea != area || now.Sub(bc.lastSeen) > blockedStaleAfter {
		bc.sinceArea, bc.since = area, now
	}
	bc.lastSeen = now
	return now.Sub(bc.since) >= blockedPersistFor
}

func (bc *blockedChecker) reset() {
	bc.mu.Lock()
	bc.sinceArea = ""
	bc.mu.Unlock()
}

type dirTarget struct {
	path  string
	write bool
}

// storageTargets lists the folders to check, per the rules on blockedChecker.
func (bc *blockedChecker) storageTargets(ctx context.Context, clients []models.DownloadClient) []dirTarget {
	cfg := bc.cfg
	var out []dirTarget
	var roots []models.RootFolder
	if bc.rootFolders != nil {
		roots, _ = bc.rootFolders.List(ctx)
	}
	if len(roots) > 0 {
		for _, rf := range roots {
			out = append(out, dirTarget{rf.Path, true})
		}
	} else {
		if cfg.LibraryDir != "" {
			out = append(out, dirTarget{cfg.LibraryDir, true})
		}
		if cfg.AudiobookDir != "" {
			out = append(out, dirTarget{cfg.AudiobookDir, true})
		}
	}

	remapped := cfg.DownloadPathRemap != ""
	for _, c := range clients {
		if c.Enabled && c.PathRemap != "" {
			remapped = true
		}
	}
	if !remapped {
		for _, dir := range []string{cfg.DownloadDir, cfg.AudiobookDownloadDir} {
			if dir != "" {
				out = append(out, dirTarget{dir, false})
			}
		}
	}
	return out
}

// storageBroken reports whether any target fails, from a result cached for
// storageProbeTTL. The probe runs in one goroutine at a time and the request
// waits at most storageProbeWait for it, answering from the last known result
// otherwise, so a hung mount cannot hang the endpoint or pile up goroutines.
func (bc *blockedChecker) storageBroken(ctx context.Context, clients []models.DownloadClient) bool {
	if bc.cfg == nil {
		return false
	}
	bc.mu.Lock()
	if !bc.storageAt.IsZero() && bc.clock().Sub(bc.storageAt) < storageProbeTTL {
		bad := bc.storageBad
		bc.mu.Unlock()
		return bad
	}
	bc.mu.Unlock()

	// Database reads happen before the lock-free goroutine starts.
	targets := bc.storageTargets(ctx, clients)

	bc.mu.Lock()
	if bc.inflight == nil {
		done := make(chan struct{})
		bc.inflight = done
		probe := bc.probe
		if probe == nil {
			probe = dirAccessible
		}
		go func() {
			bad := false
			for _, t := range targets {
				if !probe(t.path, t.write) {
					bad = true
					break
				}
			}
			bc.mu.Lock()
			bc.storageBad, bc.storageAt, bc.inflight = bad, bc.clock(), nil
			bc.mu.Unlock()
			close(done)
		}()
	}
	done := bc.inflight
	bc.mu.Unlock()

	wait := bc.wait
	if wait == 0 {
		wait = storageProbeWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.storageBad
}

// allClientsFailing is true when at least one client is enabled and every
// enabled client's stored path check is an error. One healthy client is
// enough to keep the pipeline moving. Unknown, checking and never-probed
// clients are not failures.
func (bc *blockedChecker) allClientsFailing(list []models.DownloadClient) bool {
	if bc.health == nil {
		return false
	}
	enabled := 0
	for _, c := range list {
		if !c.Enabled {
			continue
		}
		enabled++
		if bc.health.ProbeStatus(c.ID) != downloader.HealthError {
			return false
		}
	}
	return enabled > 0
}
