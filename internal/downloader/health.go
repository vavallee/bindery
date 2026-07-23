package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

const (
	HealthOK       = "ok"
	HealthChecking = "checking"
	HealthError    = "error"
)

// eventNotifier publishes a webhook event for a downstream notification
// target. Narrow shape of *notifier.Notifier so the HealthStore can be tested
// without an HTTP fixture (issue #849).
type eventNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

const notifierEventHealth = "health"

// HealthStore keeps non-persistent download-client health diagnostics.
type HealthStore struct {
	mu    sync.RWMutex
	byID  map[int64]models.DownloadClientHealth
	notif eventNotifier
}

func NewHealthStore() *HealthStore {
	return &HealthStore{byID: make(map[int64]models.DownloadClientHealth)}
}

// WithNotifier attaches a webhook event notifier so transitions into
// HealthError publish EventHealth (issue #849). Transitions out of error and
// transitions within error (message changes) are deliberately silent — they
// would either spam the channel or under-report the actionable signal.
func (s *HealthStore) WithNotifier(n eventNotifier) *HealthStore {
	if s == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notif = n
	return s
}

func (s *HealthStore) Set(id int64, health models.DownloadClientHealth) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	prev, hadPrev := s.byID[id]
	s.byID[id] = health
	notif := s.notif
	s.mu.Unlock()

	// Edge-trigger on entry into the error state. "Entry" means the previous
	// status was anything-but-error AND wasn't the transient "checking"
	// placeholder — RefreshDownloadClientHealthAsync writes Checking before
	// every refresh, so without the checking exclusion a persistently-broken
	// client would webhook on every poll cycle (error → checking → error).
	// Repeating error → error stays silent for the same anti-spam reason;
	// out-of-error transitions don't fire because there is no
	// EventHealthRecovered enum today (issue #849, deferred follow-up).
	if notif == nil || health.Status != HealthError {
		return
	}
	if hadPrev && (prev.Status == HealthError || prev.Status == HealthChecking) {
		return
	}
	// Background context: this is a fire-and-forget side effect of a state
	// transition; it must not block Set or be cancelled by the caller's
	// request context tearing down.
	notif.Send(context.Background(), notifierEventHealth, map[string]interface{}{
		"clientId": id,
		"status":   health.Status,
		"message":  health.Message,
	})
}

func (s *HealthStore) Delete(id int64) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *HealthStore) Get(id int64) *models.DownloadClientHealth {
	if s == nil || id == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	health, ok := s.byID[id]
	if !ok {
		return nil
	}
	return &health
}

func (s *HealthStore) Attach(client *models.DownloadClient) {
	if s == nil || client == nil {
		return
	}
	client.Health = s.Get(client.ID)
}

func CheckingHealth() models.DownloadClientHealth {
	return models.DownloadClientHealth{Status: HealthChecking, Message: "Checking qBittorrent category path"}
}

// RefreshDownloadClientHealthAsync fans out one health probe per enabled
// qbittorrent client. When g is non-nil each probe is launched through the
// background-jobs group so it runs on the shutdown-scoped context and is drained
// on process shutdown (#1458); the group's context is used as the parent, so
// parent is ignored in that path. When g is nil (tests, non-wired callers) it
// falls back to an untracked goroutine derived from parent.
func RefreshDownloadClientHealthAsync(parent context.Context, g *jobs.Group, store *HealthStore, clients []models.DownloadClient, downloadDir, audiobookDownloadDir string) {
	if store == nil {
		return
	}
	for i := range clients {
		client := clients[i]
		if !client.Enabled || client.Type != "qbittorrent" {
			continue
		}
		store.Set(client.ID, CheckingHealth())
		probe := func(base context.Context) {
			ctx, cancel := context.WithTimeout(base, 15*time.Second)
			defer cancel()
			store.Set(client.ID, CheckDownloadClientHealth(ctx, &client, downloadDir, audiobookDownloadDir))
		}
		if g != nil {
			g.Go("download-health-refresh", probe)
		} else {
			go probe(parent)
		}
	}
}

func CheckDownloadClientHealth(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir string) models.DownloadClientHealth {
	if client == nil || client.Type != "qbittorrent" {
		return models.DownloadClientHealth{Status: HealthOK, Message: "Download client path check not required"}
	}
	return checkQbittorrentCategoryPath(ctx, client, downloadDir, audiobookDownloadDir)
}

// ExpectedDownloadDirForClient returns the local download directory Bindery
// expects the given client's category to map to for the supplied media type.
// Before #700 this used a fuzzy strings.Contains(category, "audio") heuristic
// because the client had no explicit media-type binding; now the caller
// passes the media type explicitly and we honour that without guessing.
func ExpectedDownloadDirForClient(client *models.DownloadClient, mediaType, downloadDir, audiobookDownloadDir string) string {
	if client == nil {
		return strings.TrimSpace(downloadDir)
	}
	if mediaType == models.MediaTypeAudiobook && strings.TrimSpace(audiobookDownloadDir) != "" {
		return strings.TrimSpace(audiobookDownloadDir)
	}
	return strings.TrimSpace(downloadDir)
}

func TargetDownloadDir(mediaType, downloadDir, audiobookDownloadDir string) string {
	if mediaType == models.MediaTypeAudiobook && strings.TrimSpace(audiobookDownloadDir) != "" {
		return strings.TrimSpace(audiobookDownloadDir)
	}
	return strings.TrimSpace(downloadDir)
}

func checkQbittorrentCategoryPath(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir string) models.DownloadClientHealth {
	category := strings.TrimSpace(client.Category)
	if category == "" {
		return healthError("qBittorrent category is empty; configure a category with a save path")
	}
	expected := filepath.Clean(ExpectedDownloadDirForClient(client, models.MediaTypeEbook, downloadDir, audiobookDownloadDir))
	if expected == "." || expected == "" {
		return healthError("Bindery download directory is empty; check BINDERY_DOWNLOAD_DIR")
	}

	qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)
	categories, err := qb.GetCategories(ctx)
	if err != nil {
		return healthError(fmt.Sprintf("qBittorrent category path check failed: %v", err))
	}

	// Validate the ebook category first; if it fails, return immediately.
	// When CategoryAudiobook is set, validate it against audiobookDownloadDir
	// as well — both must be healthy for the client to be healthy (#700).
	if h := validateQbittorrentCategorySavePath(ctx, qb, client, category, expected, categories); h.Status != HealthOK {
		return h
	}

	audioCategory := strings.TrimSpace(client.CategoryAudiobook)
	if audioCategory != "" && audioCategory != category {
		expectedAudio := filepath.Clean(ExpectedDownloadDirForClient(client, models.MediaTypeAudiobook, downloadDir, audiobookDownloadDir))
		if expectedAudio == "." || expectedAudio == "" {
			return healthError("Bindery audiobook download directory is empty; check BINDERY_AUDIOBOOK_DOWNLOAD_DIR")
		}
		if h := validateQbittorrentCategorySavePath(ctx, qb, client, audioCategory, expectedAudio, categories); h.Status != HealthOK {
			return h
		}
	}

	if audioCategory != "" && audioCategory != category {
		return models.DownloadClientHealth{
			Status:  HealthOK,
			Message: fmt.Sprintf("qBittorrent categories %q and %q (audiobook) both validated", category, audioCategory),
		}
	}
	return models.DownloadClientHealth{
		Status:  HealthOK,
		Message: fmt.Sprintf("qBittorrent category %q saves under %q", category, expected),
	}
}

// validateQbittorrentCategorySavePath checks that a single qBittorrent category
// exists and that its (path-remapped) save path falls at or under expected.
func validateQbittorrentCategorySavePath(ctx context.Context, qb *qbittorrent.Client, client *models.DownloadClient, category, expected string, categories map[string]qbittorrent.Category) models.DownloadClientHealth {
	qbCategory, ok := categories[category]
	if !ok {
		return healthError(fmt.Sprintf("qBittorrent category %q was not found; create it with save path %q", category, expected))
	}

	savePath := strings.TrimSpace(qbCategory.SavePath)
	if savePath == "" {
		message := fmt.Sprintf("qBittorrent category %q has no save path; expected %q", category, expected)
		if defaultPath, err := qb.GetDefaultSavePath(ctx); err == nil && strings.TrimSpace(defaultPath) != "" {
			message += fmt.Sprintf(" and qBittorrent default is %q", strings.TrimSpace(defaultPath))
		}
		return healthError(message)
	}

	localPath := filepath.Clean(pathmap.Parse(client.PathRemap).Apply(savePath))
	if !pathIsAtOrUnder(localPath, expected) {
		// #800: the error message above told the user where the paths
		// disagreed but never named the fix. Most users hit this when
		// qBittorrent and Bindery mount the same storage at different paths
		// (e.g. /torrents in qBit, /downloads in Bindery). Spell out the
		// path-remap recipe and reference the two settings that need to
		// match so the user has a concrete next step rather than just a
		// validation refusal.
		hint := fmt.Sprintf("set this client's path remap to translate the qBittorrent prefix to Bindery's (e.g. %q), or mount the same directory at %q inside Bindery and set BINDERY_DOWNLOAD_DIR to match", remapHint(savePath, expected), localPath)
		return healthError(fmt.Sprintf("qBittorrent category %q saves to %q, which maps to %q inside Bindery; expected a path at or under %q — %s", category, savePath, localPath, expected, hint))
	}

	// pathIsAtOrUnder passes — the strings agree. Now verify the resolved
	// path is actually reachable from Bindery's filesystem, because Linux is
	// case-sensitive and a user with a Windows/WSL/Docker setup can produce
	// a textually-correct remap that silently points at nothing (PixieApples,
	// follow-up to #800). When the path is missing but a case-variant exists,
	// name the divergent component so the user knows exactly which letter to
	// fix instead of brute-forcing combinations.
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		if resolved, divergedAt := findCaseInsensitivePath(localPath); resolved != "" {
			return healthError(fmt.Sprintf("qBittorrent category %q saves to %q, which maps to %q inside Bindery — that exact path does not exist, but %q does. Linux is case-sensitive; update the path remap so it produces %q (the segment %q must match the on-disk case).", category, savePath, localPath, resolved, resolved, filepath.Base(divergedAt)))
		}
		return healthError(fmt.Sprintf("qBittorrent category %q saves to %q, which maps to %q inside Bindery — but that path does not exist. Check the path remap and the directory Bindery is mounting.", category, savePath, localPath))
	}

	return models.DownloadClientHealth{Status: HealthOK}
}

// findCaseInsensitivePath walks p from root and, if the literal path does not
// exist but a case-insensitive sibling does at some level, returns the
// case-corrected path and the first divergent component. Returns ("", "") if
// the path can't be resolved even case-insensitively. Used by the qBittorrent
// health-check to give Windows/WSL/Docker users a concrete fix when their
// PathRemap is textually right but on the wrong-cased mount point.
func findCaseInsensitivePath(p string) (resolved, divergedAt string) {
	if !filepath.IsAbs(p) {
		return "", ""
	}
	sep := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(p), sep), sep)
	cur := sep
	for _, part := range parts {
		if part == "" {
			continue
		}
		candidate := filepath.Join(cur, part)
		if _, err := os.Stat(candidate); err == nil {
			cur = candidate
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", ""
		}
		var match string
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), part) {
				match = entry.Name()
				break
			}
		}
		if match == "" {
			return "", ""
		}
		if divergedAt == "" {
			divergedAt = filepath.Join(cur, match)
		}
		cur = filepath.Join(cur, match)
	}
	if divergedAt == "" {
		return "", ""
	}
	return cur, divergedAt
}

func healthError(message string) models.DownloadClientHealth {
	return models.DownloadClientHealth{Status: HealthError, Message: message}
}

// remapHint derives a "src:dst" PathRemap suggestion from the qBittorrent
// save path and Bindery's expected download dir. It strips one path segment
// from each so the suggestion translates the shared parent rather than the
// fully-qualified leaf path: a user with qBit at "/torrents/complete/library"
// and Bindery at "/downloads" gets "/torrents/complete:/downloads", which
// also covers any sibling category save paths under the same root. When
// either path is "/" the hint falls back to the full strings.
func remapHint(savePath, expected string) string {
	src := strings.TrimRight(filepath.Dir(filepath.Clean(savePath)), string(filepath.Separator))
	dst := strings.TrimRight(filepath.Clean(expected), string(filepath.Separator))
	if src == "" || src == "." {
		src = filepath.Clean(savePath)
	}
	if dst == "" {
		dst = "/"
	}
	return src + ":" + dst
}

// pathIsAtOrUnder reports whether candidate is equal to base or is a
// subdirectory of base. Both paths must already be filepath.Clean'd.
// A trailing separator is added to base before the prefix check so that
// "/data/downloads-extra" is not mistakenly accepted as "under" "/data/downloads".
func pathIsAtOrUnder(candidate, base string) bool {
	if candidate == base {
		return true
	}
	return strings.HasPrefix(candidate, base+string(filepath.Separator))
}
