package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

// Path-visibility statuses surfaced by the explicit download-client Test action
// (#1182). These are intentionally separate from the persistent health Status
// values (HealthOK/HealthError) because the Test response carries a richer,
// three-way result: a hard connection failure is handled before this runs.
const (
	// PathVisible means Bindery could os.Stat the client's resolved
	// completed-downloads path — imports have a real chance of working.
	PathVisible = "ok"
	// PathNotVisible means the path was resolved but Bindery cannot see it on
	// its own filesystem. This is the silent-failure case (#1182): the
	// connection is fine but nothing will import.
	PathNotVisible = "warning"
	// PathUnknown means the client type does not expose a completed-downloads
	// path Bindery can introspect (SABnzbd, Transmission, Deluge), so the Test
	// stays connection-only with no regression. rTorrent does expose one
	// (directory.default) and is checked.
	PathUnknown = "unknown"
)

// PathVisibility is the result of checking whether Bindery can read the files a
// download client writes on completion. It is attached to the Test response so
// the UI can warn distinctly from a hard connection failure.
type PathVisibility struct {
	Status string `json:"status"`
	// Message is a human-readable, actionable summary. For PathUnknown it is
	// empty (nothing to surface).
	Message string `json:"message,omitempty"`
	// Path is the resolved, path-remapped local path that was stat'd. Empty for
	// PathUnknown.
	Path string `json:"path,omitempty"`
}

// CheckCompletedPathVisibility resolves the client's completed-downloads path,
// applies the client's configured PathRemap, and os.Stats the result to confirm
// Bindery can actually read it. It generalises the qBittorrent-only category
// path check (#700) to the explicit Test action, and extends to NZBGet via its
// per-category DestDir. For client types whose completed path is not
// introspectable it returns PathUnknown so the Test degrades to connection-only.
//
// It assumes the connection has already been verified by TestClient; callers
// should only invoke it after a successful connect so a probe failure here is
// reported as PathUnknown rather than masking a connection error.
func CheckCompletedPathVisibility(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir, globalRemap string) PathVisibility {
	if client == nil {
		return PathVisibility{Status: PathUnknown}
	}
	switch client.Type {
	case "qbittorrent", "nzbget", "rtorrent":
		return completedPathVisibility(ctx, client, downloadDir, audiobookDownloadDir, globalRemap)
	default:
		// CompletedPath can answer for SABnzbd, Transmission and Deluge too,
		// but the health job and the Test button deliberately stay unknown for
		// them: SABnzbd needs a full API key to read its folders, and
		// Transmission and Deluge resolve a download dir per torrent rather
		// than a static completed folder. The on demand diagnose action is
		// where those answers are shown, with that caveat attached.
		return PathVisibility{Status: PathUnknown}
	}
}

// completedPathVisibility stats the path CompletedPath reports for the client.
// Any introspection failure, or a client that reports no path, is PathUnknown
// rather than a warning: the connection already passed, so "can't tell" is the
// honest answer and avoids false alarms.
//
// rTorrent has one global default directory, so both media types land in the
// same place; the hint still names both configured directories when the
// client serves audiobooks separately, rather than pointing only at the ebook
// one (#1984, the half left over from #1993).
func completedPathVisibility(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir, globalRemap string) PathVisibility {
	info, err := CompletedPath(ctx, client)
	if err != nil || strings.TrimSpace(info.Path) == "" {
		return PathVisibility{Status: PathUnknown}
	}
	return statRemappedPath(client, info.Path, clientExpectedHint(client, downloadDir, audiobookDownloadDir), globalRemap)
}

// clientExpectedHint describes the local directories Bindery is configured to
// read for the categories this client serves. When audiobooks use a separate
// category, include both media-specific directories so a failed visibility
// check does not misleadingly point only at the ebook directory (#1984).
//
// Named for clients generally rather than qBittorrent because every
// introspectable type wants the same sentence in its failure message.
func clientExpectedHint(client *models.DownloadClient, downloadDir, audiobookDownloadDir string) string {
	ebookDir := strings.TrimSpace(ExpectedDownloadDirForClient(client, models.MediaTypeEbook, downloadDir, audiobookDownloadDir))
	hints := make([]string, 0, 2)
	if ebookDir != "" {
		hints = append(hints, fmt.Sprintf("the ebook download directory %q", filepath.Clean(ebookDir)))
	}

	if strings.TrimSpace(client.CategoryAudiobook) != "" && strings.TrimSpace(client.CategoryAudiobook) != strings.TrimSpace(client.Category) {
		audiobookDir := strings.TrimSpace(ExpectedDownloadDirForClient(client, models.MediaTypeAudiobook, downloadDir, audiobookDownloadDir))
		if audiobookDir != "" {
			hints = append(hints, fmt.Sprintf("the audiobook download directory %q", filepath.Clean(audiobookDir)))
		}
	}

	return strings.Join(hints, " and ")
}

// Remap rules reported by RemapClientPath.
const (
	RemapRuleClient = "client"
	RemapRuleGlobal = "global"
	RemapRuleNone   = "none"
)

// RemapClientPath resolves a client-reported path the way the importer, the
// health job and the diagnose action all must: apply the client's own
// PathRemap first, and only if that leaves the path unchanged fall back to the
// global BINDERY_DOWNLOAD_PATH_REMAP. It is the single definition of that
// precedence, so the Test verdict matches what the importer resolves at import
// time (#1182). The second result names the rule that changed the path:
// RemapRuleClient, RemapRuleGlobal or RemapRuleNone.
func RemapClientPath(client *models.DownloadClient, rawPath string, global *pathmap.Remapper) (string, string) {
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		if localPath := pathmap.Parse(client.PathRemap).Apply(rawPath); localPath != rawPath {
			return localPath, RemapRuleClient
		}
	}
	if localPath := global.Apply(rawPath); localPath != rawPath {
		return localPath, RemapRuleGlobal
	}
	return rawPath, RemapRuleNone
}

// ClientSidePath is RemapClientPath run the other way: it renders a path on
// Bindery's filesystem in the download client's own namespace, for a path
// Bindery sends to the client. The precedence is the same, so whatever is
// sent reads back to where it started: the client's own PathRemap first, and
// only if none of its rules covers the path the global
// BINDERY_DOWNLOAD_PATH_REMAP (#2665).
//
// "Covers" means a rule matched, not that the path changed. A client that
// mounts Bindery's download folder at the same path, next to a global remap
// written for other clients, opts out of the global remap with an identity
// rule such as "/downloads:/downloads"; that rule matches and maps the folder
// to itself, so the global remap is not consulted.
func ClientSidePath(client *models.DownloadClient, localPath string, global *pathmap.Remapper) string {
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		if clientPath, matched := pathmap.Parse(client.PathRemap).ApplyInverseMatched(localPath); matched {
			return clientPath
		}
	}
	return global.ApplyInverse(localPath)
}

// remapClientPath is RemapClientPath for callers holding the global remap as
// its raw setting string and needing only the path.
func remapClientPath(client *models.DownloadClient, rawPath, globalRemap string) string {
	localPath, _ := RemapClientPath(client, rawPath, pathmap.Parse(globalRemap))
	return localPath
}

// statRemappedPath resolves a client-reported path via RemapClientPath (client
// PathRemap then global remap fallback) and os.Stats the result. expectedHint,
// when non-empty, is included in the warning message as the configured local
// directory description Bindery was expected to read from.
func statRemappedPath(client *models.DownloadClient, clientPath, expectedHint, globalRemap string) PathVisibility {
	localPath := filepath.Clean(remapClientPath(client, strings.TrimSpace(clientPath), globalRemap))
	if localPath == "." || localPath == "" {
		return PathVisibility{Status: PathUnknown}
	}
	if info, err := os.Stat(localPath); err == nil {
		if !info.IsDir() {
			// A file where a directory is expected is unusual but readable; still
			// report visible since Bindery can reach it.
			return PathVisibility{
				Status:  PathVisible,
				Path:    localPath,
				Message: fmt.Sprintf("Bindery can read the client's completed-downloads path at %q.", localPath),
			}
		}
		return PathVisibility{
			Status:  PathVisible,
			Path:    localPath,
			Message: fmt.Sprintf("Bindery can read the client's completed-downloads folder at %q.", localPath),
		}
	}

	hint := "configure a path remap (Settings → Download clients) or a shared mount so both point at the same storage"
	if strings.TrimSpace(client.PathRemap) == "" {
		hint = "configure a path remap (Settings → Download clients) to translate the client's path to Bindery's, or mount the same storage at the same path in both"
	}
	if raw := strings.TrimSpace(clientPath); pathmap.IsWindowsPath(raw) {
		// A drive path can never exist on Bindery's filesystem, so the generic
		// "or mount the same storage at the same path in both" advice is a dead
		// end for a Windows client (#1971). Name the only fix, with the exact
		// pair to paste in.
		hint = fmt.Sprintf("the client reports a Windows drive path, which Bindery cannot mount at the same location, so a path remap is required (Settings → Download clients) mapping it to the path Bindery sees, e.g. %q", raw+":/downloads")
	}
	msg := fmt.Sprintf("Connected, but Bindery can't read the client's completed-downloads folder at %q — %s.", localPath, hint)
	if local := strings.TrimSpace(clientPath); local != "" && filepath.Clean(local) != localPath {
		msg = fmt.Sprintf("Connected, but Bindery can't read the client's completed-downloads folder. The client writes to %q, which maps to %q inside Bindery, but that path does not exist — %s.", pathmap.CleanClientPath(local), localPath, hint)
	}
	if expected := strings.TrimSpace(expectedHint); expected != "" {
		msg += fmt.Sprintf(" Bindery is configured to read from %s.", expected)
	}
	return PathVisibility{Status: PathNotVisible, Message: msg, Path: localPath}
}
