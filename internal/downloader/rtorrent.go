package downloader

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/models"
)

// RtorrentStatus renders one rTorrent torrent as the client-specific status
// string carried on LiveStatus.
//
// rTorrent has no status enum: it exposes independent flags (d.complete,
// d.is_active, d.is_open) plus d.message, its per-torrent error slot. The
// mapping below collapses those into the same vocabulary the other torrent
// clients produce, and prefixes a non-empty message with "error: " so
// LiveStatusIsError classifies it the way it classifies a Transmission
// errorString.
//
// d.message only counts as an error while the torrent is still incomplete.
// rTorrent also parks benign tracker chatter there — "Tracker: [Failure reason
// ...]" is routine on a healthy, fully-downloaded torrent that is simply seeding
// to an unhappy tracker — and flagging that would paint the queue row red and
// trip api.LiveStatusIsError's "error" substring match on a download that has
// nothing wrong with it. This matches the two sibling call sites:
// importer.checkRtorrentDownloads (`!t.Complete && msg != ""`) and
// GetStalledIDs (`t.Message != "" && !t.Complete`).
func RtorrentStatus(t rtorrent.Torrent) string {
	if msg := strings.TrimSpace(t.Message); msg != "" && !t.Complete {
		return "error: " + msg
	}
	switch {
	case t.Complete:
		return "seeding"
	case t.IsActive:
		return "downloading"
	default:
		return "stopped"
	}
}

// removeRtorrentDownload erases a torrent from rTorrent and, when the caller
// asked for the data to go too, deletes the payload from Bindery's side first.
//
// The two-step dance exists because rTorrent has no delete-with-data command:
// d.erase drops the item and its session files and documents that "the data
// stored for the item is not touched in any way". Every other Bindery client
// takes a deleteFiles flag straight to the daemon; for rTorrent, Bindery is the
// only process in the picture that can honour it. (Sonarr's rTorrent client
// resolves this the same way.)
//
// Data deletion is best-effort and deliberately conservative — a path Bindery
// cannot see, or one that looks nothing like a torrent payload, leaves the
// files alone and logs why. Erasing the torrent still proceeds: the user asked
// for it gone from the client, and failing the whole removal because a remote
// path was not visible would strand the download row.
func removeRtorrentDownload(ctx context.Context, client *models.DownloadClient, hash string, deleteFiles bool, globalRemap string) error {
	rt := RtorrentFor(client)
	if deleteFiles {
		deleteRtorrentData(ctx, rt, client, hash, globalRemap)
	}
	return rt.RemoveTorrent(ctx, hash)
}

// deleteRtorrentData resolves the torrent's on-disk payload and removes it.
func deleteRtorrentData(ctx context.Context, rt *rtorrent.Client, client *models.DownloadClient, hash, globalRemap string) {
	basePath, err := rt.BasePath(ctx, hash)
	if err != nil {
		slog.Warn("rtorrent: could not resolve the torrent's data path — the torrent will be removed but its files are left on disk",
			"hash", hash, "error", err)
		return
	}
	localPath, reason := resolveRtorrentDataPath(client, basePath, globalRemap)
	if localPath == "" {
		slog.Warn("rtorrent: refusing to delete the torrent's data — the torrent will be removed but its files are left on disk",
			"hash", hash, "client_path", basePath, "reason", reason)
		return
	}
	if err := os.RemoveAll(localPath); err != nil {
		slog.Warn("rtorrent: failed to delete the torrent's data",
			"hash", hash, "path", localPath, "error", err)
		return
	}
	slog.Info("rtorrent: deleted torrent data", "hash", hash, "path", localPath)
}

// resolveRtorrentDataPath maps d.base_path onto a Bindery-side path that is
// safe to delete, or returns ("", reason) when it is not.
//
// The guards matter more here than anywhere else in the downloader package:
// this is the only place Bindery removes a tree at a path a remote service
// chose. A misconfigured rTorrent, a path remap that collapses to a mount
// root, or an rTorrent that reports a relative path must all end in "leave it
// alone", not in a recursive delete of something else.
//
// The remap resolution deliberately matches remapClientPath and the importer's
// Scanner.remapDownloadClientPath: the client's own PathRemap first, and the
// global BINDERY_DOWNLOAD_PATH_REMAP when that leaves the path untouched.
// Skipping the global fallback here would give an operator who configures one
// global remap and no per-client remap working imports and a passing Test
// button, then a "not visible to Bindery" refusal on remove-with-data alone.
func resolveRtorrentDataPath(client *models.DownloadClient, basePath, globalRemap string) (string, string) {
	raw := strings.TrimSpace(basePath)
	if raw == "" {
		// A magnet that never resolved metadata, or a closed item after an
		// rTorrent restart. There is nothing to point at.
		return "", "rTorrent reported no base path for the torrent"
	}
	local := filepath.Clean(remapClientPath(client, raw, globalRemap))
	if !filepath.IsAbs(local) {
		return "", fmt.Sprintf("resolved path %q is not absolute", local)
	}
	// Depth guard: a torrent's base path is always <download dir>/<torrent
	// name>, so it has at least two components. One component means the remap
	// produced a mount root ("/downloads") and deleting it would take out
	// every other download.
	if len(strings.Split(strings.Trim(local, string(filepath.Separator)), string(filepath.Separator))) < 2 {
		return "", fmt.Sprintf("resolved path %q is a filesystem root or a top-level directory, not a torrent payload", local)
	}
	info, err := os.Lstat(local)
	if err != nil {
		return "", fmt.Sprintf("resolved path %q is not visible to Bindery — configure a path remap if rTorrent runs on another host", local)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Removing a symlink would either delete the link and orphan the data,
		// or (if it were followed) delete something entirely outside the
		// download tree. Neither is what the user asked for.
		return "", fmt.Sprintf("resolved path %q is a symlink", local)
	}
	// The Lstat above only inspects the leaf. A symlinked *parent* component
	// passes both it and the depth guard, and os.RemoveAll walks through it —
	// so "/downloads/books" where "books" is fine but "downloads" points at
	// "/" would delete outside the download tree entirely. Requiring the fully
	// resolved path to equal the path we are about to remove rules out a
	// symlink anywhere in the chain, which is the containment property the
	// depth guard alone cannot give.
	resolved, err := filepath.EvalSymlinks(local)
	if err != nil {
		return "", fmt.Sprintf("resolved path %q could not be fully resolved on Bindery's filesystem", local)
	}
	if resolved != local {
		return "", fmt.Sprintf("resolved path %q reaches %q through a symlinked parent directory", local, resolved)
	}
	return local, ""
}
