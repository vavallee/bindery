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
	// stays connection-only with no regression.
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
	case "qbittorrent":
		return qbittorrentPathVisibility(ctx, client, downloadDir, audiobookDownloadDir, globalRemap)
	case "nzbget":
		return nzbgetPathVisibility(ctx, client, globalRemap)
	default:
		// SABnzbd's complete dir requires get_config (not introspected here),
		// and Transmission/Deluge resolve a download dir per-torrent rather than
		// a static completed folder. Skip gracefully.
		return PathVisibility{Status: PathUnknown}
	}
}

func qbittorrentPathVisibility(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir, globalRemap string) PathVisibility {
	category := strings.TrimSpace(client.Category)
	if category == "" {
		return PathVisibility{Status: PathUnknown}
	}
	qb := QbittorrentFor(client)
	categories, err := qb.GetCategories(ctx)
	if err != nil {
		// Connection already passed; treat an introspection failure as "can't
		// tell" rather than a path warning, to avoid false alarms.
		return PathVisibility{Status: PathUnknown}
	}
	qbCategory, ok := categories[category]
	if !ok {
		return PathVisibility{Status: PathUnknown}
	}
	savePath := strings.TrimSpace(qbCategory.SavePath)
	if savePath == "" {
		if defaultPath, derr := qb.GetDefaultSavePath(ctx); derr == nil {
			savePath = strings.TrimSpace(defaultPath)
		}
	}
	if savePath == "" {
		return PathVisibility{Status: PathUnknown}
	}
	expected := ExpectedDownloadDirForClient(client, models.MediaTypeEbook, downloadDir, audiobookDownloadDir)
	return statRemappedPath(client, savePath, expected, globalRemap)
}

func nzbgetPathVisibility(ctx context.Context, client *models.DownloadClient, globalRemap string) PathVisibility {
	ng := NzbgetFor(client)
	completeDir, err := ng.CompletedDir(ctx, client.Category)
	if err != nil || strings.TrimSpace(completeDir) == "" {
		return PathVisibility{Status: PathUnknown}
	}
	return statRemappedPath(client, completeDir, "", globalRemap)
}

// remapClientPath resolves a client-reported path the same way the importer does
// (see Scanner.remapDownloadClientPath): apply the client's own PathRemap first,
// and only if that leaves the path unchanged fall back to the global
// BINDERY_DOWNLOAD_PATH_REMAP. This keeps the Test action's verdict consistent
// with what the importer will actually resolve at import time (#1182).
func remapClientPath(client *models.DownloadClient, rawPath, globalRemap string) string {
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		if localPath := pathmap.Parse(client.PathRemap).Apply(rawPath); localPath != rawPath {
			return localPath
		}
	}
	return pathmap.Parse(globalRemap).Apply(rawPath)
}

// statRemappedPath resolves a client-reported path via remapClientPath (client
// PathRemap then global remap fallback) and os.Stats the result. expectedHint,
// when non-empty, is included in the warning message as the directory Bindery
// was configured to read from.
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
	msg := fmt.Sprintf("Connected, but Bindery can't read the client's completed-downloads folder at %q — %s.", localPath, hint)
	if local := strings.TrimSpace(clientPath); local != "" && filepath.Clean(local) != localPath {
		msg = fmt.Sprintf("Connected, but Bindery can't read the client's completed-downloads folder. The client writes to %q, which maps to %q inside Bindery, but that path does not exist — %s.", filepath.Clean(local), localPath, hint)
	}
	return PathVisibility{Status: PathNotVisible, Message: msg, Path: localPath}
}
