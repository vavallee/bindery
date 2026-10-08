package importer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/models"
)

// How Bindery decides whether a download client's failure verdict may
// blocklist the release (#3024).
//
// SABnzbd messages, and NZBGet's FAILURE/HEALTH and FAILURE/BAD, always do:
// none of them is something the client's own machine causes. NZBGet's
// FAILURE/UNPACK, FAILURE/PAR and FAILURE/SCAN are different, because NZBGet
// also reports a missing unrar, a full disk while extracting, a failed move
// or a par2 memory error with them (nzbget.IsContentFailure). For those three
// Bindery looks for evidence in this order:
//
//  1. The job's own log (nzbget.ClassifyFailureLog). A host fault line holds
//     the blocklist back and opens the breaker for that status; a content
//     line (a CRC error, a missing volume, too little par data) blocklists.
//  2. For FAILURE/UNPACK only, NZBGet's sysinfo, asked lazily and cached
//     (HealthStore.NZBGetUnpackers): while UnRAR is missing nothing is
//     blocklisted for that status.
//  3. The breaker below, which counts failures that had no evidence.
//
// The breaker is per client and per status, so an unpack problem never stops
// a FAILURE/HEALTH or FAILURE/PAR from blocklisting. It opens when
// contentBreakerDistinct different releases fail with the same status within
// contentBreakerWindow and nothing from that client completed that stage in
// between. Three is the smallest count that is not a coincidence of two bad
// uploads, and caps what a silent broken unpacker can do to two releases per
// trip. Two hours covers the burst one sweep sends while staying well under
// the six hour re-grab cooldown, so separate sweeps do not add up.
//
// Only a completion of the same stage closes it: an UNPACK trip closes on a
// job with UnpackStatus SUCCESS, a PAR trip on ParStatus SUCCESS
// (nzbget.StageSucceeded). A plain epub completing proves nothing about
// unrar. Editing the client also closes it, as the user has presumably just
// fixed something.
const (
	contentBreakerWindow   = 2 * time.Hour
	contentBreakerDistinct = 3
	// failureLogLines is how much of a failed job's log is read. Book
	// releases are small, so this reaches back past the unpacker's
	// per file "Extracting" lines to the start of post processing.
	failureLogLines = 500
)

type contentFailure struct {
	guid string
	at   time.Time
}

type breakerKey struct {
	clientID int64
	kind     string
}

type breakerState struct {
	recent []contentFailure
	// reason is why the breaker is open; "" means closed. It stays open past
	// the window, because a broken unpacker does not heal by waiting.
	reason string
}

// contentFailureBreaker is in memory on purpose: a restart re-arms it, which
// costs at most contentBreakerDistinct-1 more blocklist rows for an evidence
// free storm before it opens again.
type contentFailureBreaker struct {
	mu     sync.Mutex
	states map[breakerKey]*breakerState
	now    func() time.Time // test seam; nil means time.Now
}

func (b *contentFailureBreaker) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *contentFailureBreaker) state(k breakerKey) *breakerState {
	if b.states == nil {
		b.states = make(map[breakerKey]*breakerState)
	}
	st := b.states[k]
	if st == nil {
		st = &breakerState{}
		b.states[k] = st
	}
	return st
}

// recordFailure notes an evidence free failure and reports whether its
// release may be blocklisted. tripped is true only on the call that opens the
// breaker; distinct is the number of releases counted in the window.
func (b *contentFailureBreaker) recordFailure(clientID int64, guid, kind string) (allow, tripped bool, distinct int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(breakerKey{clientID, kind})
	now := b.clock()

	cutoff := now.Add(-contentBreakerWindow)
	kept := st.recent[:0]
	for _, f := range st.recent {
		if f.at.After(cutoff) {
			kept = append(kept, f)
		}
	}
	st.recent = append(kept, contentFailure{guid: guid, at: now})

	seen := make(map[string]struct{})
	for _, f := range st.recent {
		seen[f.guid] = struct{}{}
	}
	distinct = len(seen)

	if st.reason != "" {
		return false, false, distinct
	}
	if distinct >= contentBreakerDistinct {
		st.reason = fmt.Sprintf("%d different releases failed with %s within two hours and none completed that step in between", distinct, kind)
		return false, true, distinct
	}
	return true, false, distinct
}

// open opens the breaker for a kind on direct evidence of a host fault. It
// reports whether it was closed before.
func (b *contentFailureBreaker) open(clientID int64, kind, reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(breakerKey{clientID, kind})
	wasClosed := st.reason == ""
	st.reason = reason
	return wasClosed
}

// stageSucceeded clears the count for a kind and closes its breaker. It
// reports whether the breaker was open.
func (b *contentFailureBreaker) stageSucceeded(clientID int64, kind string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := breakerKey{clientID, kind}
	st := b.states[k]
	delete(b.states, k)
	return st != nil && st.reason != ""
}

// reset forgets everything about a client.
func (b *contentFailureBreaker) reset(clientID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.states {
		if k.clientID == clientID {
			delete(b.states, k)
		}
	}
}

// openReasons lists why each open breaker of a client is open, by status.
func (b *contentFailureBreaker) openReasons(clientID int64) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k, st := range b.states {
		if k.clientID == clientID && st.reason != "" {
			out = append(out, k.kind+": "+st.reason)
		}
	}
	sort.Strings(out)
	return out
}

// WithClientHealth lets the importer read NZBGet's missing unpackers from the
// client's health and report a paused blocklist there (#3024).
func (s *Scanner) WithClientHealth(store *downloader.HealthStore) *Scanner {
	s.clientHealth = store
	return s
}

// ResetContentBreaker closes every breaker for a client and clears what it
// reported. The download client API calls it when the client is edited or
// disabled.
func (s *Scanner) ResetContentBreaker(clientID int64) {
	s.contentBreaker.reset(clientID)
	s.clientHealth.ClearAdvisory(clientID, downloader.AdvisoryBlocklist)
}

// syncBreakerAdvisory publishes the client's open breakers on its health, or
// clears the advisory when none is open.
func (s *Scanner) syncBreakerAdvisory(client *models.DownloadClient) {
	reasons := s.contentBreaker.openReasons(client.ID)
	if len(reasons) == 0 {
		s.clientHealth.ClearAdvisory(client.ID, downloader.AdvisoryBlocklist)
		return
	}
	msg := "Automatic blocklisting is paused for some failures, because they look like a problem with the download client rather than the releases (" +
		strings.Join(reasons, "; ") +
		"). Check its unrar and 7-Zip, free space and folder permissions. Failed downloads are retried as usual; blocklisting resumes once the client completes that step again"
	s.clientHealth.SetAdvisory(client.ID, downloader.AdvisoryBlocklist, models.DownloadClientHealth{Status: downloader.HealthError, Message: msg})
}

// handleNZBGetContentFailure decides whether a release NZBGet failed with a
// content status is blocklisted. See the comment at the top of this file.
func (s *Scanner) handleNZBGetContentFailure(ctx context.Context, client *models.DownloadClient, ng *nzbget.Client, dl *models.Download, item nzbget.HistoryItem) {
	status := item.Status
	reason := "downloadFailed: " + status
	if s.blocklist == nil || client == nil || dl == nil {
		return
	}
	if !nzbget.NeedsLogEvidence(status) {
		s.blocklistRejectedRelease(ctx, dl, reason)
		return
	}

	var entries []nzbget.LogEntry
	if ng != nil {
		var err error
		if entries, err = ng.LoadLog(ctx, item.NZBID, failureLogLines); err != nil {
			slog.Debug("download failed: could not read the job log", "client", client.Name, "nzbid", item.NZBID, "error", err)
		}
	}
	evidence, detail := nzbget.ClassifyFailureLog(entries)
	switch evidence {
	case nzbget.EvidenceContent:
		slog.Info("download failed: the job log shows the release is broken", "client", client.Name, "title", dl.Title, "line", detail)
		s.blocklistRejectedRelease(ctx, dl, reason)
		return
	case nzbget.EvidenceHost:
		slog.Warn("download failed: the job log points at the download client, not blocklisting the release",
			"client", client.Name, "title", dl.Title, "status", status, "line", detail)
		s.contentBreaker.open(client.ID, status, fmt.Sprintf("NZBGet logged %q", detail))
		s.syncBreakerAdvisory(client)
		return
	}

	// Only an UNPACK failure with nothing in its log asks NZBGet's sysinfo,
	// and the answer is cached (HealthStore.NZBGetUnpackers). A missing 7-Zip
	// alone does not hold anything back: NZBGet's default SevenZipCmd is
	// often absent, and a RAR release that failed is still the release.
	if status == "FAILURE/UNPACK" && slices.Contains(s.clientHealth.NZBGetUnpackers(ctx, client, false), "UnRAR") {
		slog.Info("download failed: not blocklisting an unpack failure while NZBGet cannot find UnRAR",
			"client", client.Name, "title", dl.Title)
		return
	}

	allow, tripped, distinct := s.contentBreaker.recordFailure(client.ID, dl.GUID, status)
	switch {
	case allow:
		s.blocklistRejectedRelease(ctx, dl, reason)
	case tripped:
		slog.Warn("download client: pausing automatic blocklisting for this failure, too many different releases failed the same way",
			"client", client.Name, "status", status, "releases", distinct, "window", contentBreakerWindow, "title", dl.Title)
		s.syncBreakerAdvisory(client)
	default:
		slog.Info("download failed: not blocklisting the release, automatic blocklisting is paused for this failure",
			"client", client.Name, "status", status, "title", dl.Title, "guid", dl.GUID)
	}
}

// noteNZBGetCompletion closes the breakers whose stage a just completed job
// shows working.
func (s *Scanner) noteNZBGetCompletion(client *models.DownloadClient, item nzbget.HistoryItem) {
	changed := false
	for _, kind := range []string{"FAILURE/UNPACK", "FAILURE/PAR", "FAILURE/SCAN"} {
		if nzbget.StageSucceeded(kind, item) && s.contentBreaker.stageSucceeded(client.ID, kind) {
			slog.Info("download client completed the step again, automatic blocklisting resumes", "client", client.Name, "status", kind)
			changed = true
		}
	}
	if changed {
		s.syncBreakerAdvisory(client)
	}
}
