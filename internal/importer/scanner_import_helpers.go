package importer

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// cleanupMovedSources removes the source files that were successfully moved
// into the library and then prunes any directories left empty by their
// removal, walking upward from each file towards downloadPath (inclusive).
//
// It deliberately uses os.Remove (never os.RemoveAll) for directories: os.Remove
// fails on a non-empty directory, so a downloadPath that is actually a shared
// save root holding sibling torrents' data is left untouched. MoveFileCtx
// already deletes the source file on a successful move, so most entries here
// are no-ops on the file itself; the value is the empty-directory pruning.
func (s *Scanner) cleanupMovedSources(downloadPath string, importedSrcFiles []string) {
	pruneRoot, refusal := s.movePruneRoot(downloadPath, importedSrcFiles)
	switch refusal {
	case "":
	case pruneRefusedUnsafe:
		slog.Warn("move cleanup: refusing to prune unsafe download path", "path", downloadPath)
		return
	default:
		slog.Warn("move cleanup: download path equals or contains a library root, skipping cleanup",
			"downloadPath", filepath.Clean(downloadPath), "pruneRoot", pruneRoot, "libraryRoot", refusal)
		return
	}

	// dirsToPrune collects every directory at or below pruneRoot that may now
	// be empty, deepest-first so children are removed before parents.
	prune := make(map[string]bool)
	for _, src := range importedSrcFiles {
		cleanSrc := filepath.Clean(src)
		// Only touch files that actually live under pruneRoot — never delete
		// something outside the download we were asked to import.
		if cleanSrc != pruneRoot && !pathUnderDir(cleanSrc, pruneRoot) {
			slog.Warn("move cleanup: imported source outside download path, skipping",
				"src", cleanSrc, "pruneRoot", pruneRoot)
			continue
		}
		if err := os.Remove(cleanSrc); err != nil && !os.IsNotExist(err) {
			slog.Warn("move cleanup: failed to remove imported source file", "src", cleanSrc, "error", err)
		}
		// Mark every ancestor directory from the file up to (and including)
		// pruneRoot as a pruning candidate.
		for dir := filepath.Dir(cleanSrc); ; dir = filepath.Dir(dir) {
			prune[dir] = true
			if dir == pruneRoot || !pathUnderDir(dir, pruneRoot) {
				break
			}
		}
	}

	// Remove empty directories deepest-first. os.Remove silently fails (and is
	// skipped) on any directory still holding sibling files or other torrents'
	// data, so a shared save root is preserved automatically.
	dirs := make([]string, 0, len(prune))
	for d := range prune {
		dirs = append(dirs, d)
	}
	// Deepest paths first: longer cleaned paths sort after shorter ancestors.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		// Never prune above pruneRoot.
		if dir != pruneRoot && !pathUnderDir(dir, pruneRoot) {
			continue
		}
		if err := os.Remove(dir); err != nil {
			if !os.IsNotExist(err) {
				// ENOTEMPTY / EEXIST is expected and benign: the directory still
				// holds other data (sibling torrent files, byproducts) and must
				// be kept. Anything else is logged at debug level only.
				slog.Debug("move cleanup: directory not pruned (kept)", "dir", dir, "reason", err)
			}
			continue
		}
		slog.Debug("move cleanup: pruned empty directory", "dir", dir)
	}
}

// pruneRefusedUnsafe is movePruneRoot's refusal for a path that is empty,
// relative or the filesystem root. Any other non-empty refusal is the library
// root the path equals or contains.
const pruneRefusedUnsafe = "unsafe"

// movePruneRoot returns the folder move mode cleanup may prune up to
// (inclusive), and a non-empty refusal when it must not prune at all.
func (s *Scanner) movePruneRoot(downloadPath string, importedSrcFiles []string) (string, string) {
	cleanDownloadPath := filepath.Clean(downloadPath)

	// pruneRoot is the boundary directory the cleanup is allowed to remove up to
	// (inclusive). It is normally the download path itself. But a single-file
	// usenet job resolves downloadPath to the *file*, not its containing folder
	// (SABnzbd/NZBGet report the storage path of the single file), and every
	// guard below is "at or under downloadPath", which a file's parent is not.
	// The result was the now-empty job folder being left behind after an
	// otherwise-clean ebook import (cleb's report; audiobooks escaped it because
	// they move the whole directory via MoveDirCtx). When downloadPath is a
	// file, prune from its parent folder instead.
	pruneRoot := cleanDownloadPath
	if downloadPathIsFile(cleanDownloadPath, importedSrcFiles) {
		pruneRoot = filepath.Dir(cleanDownloadPath)
	}

	// Refuse to prune if pruneRoot is empty, relative, the filesystem root, or
	// equal to / an ancestor of a configured library root. This is a
	// belt-and-braces guard on top of the os.Remove non-empty-dir protection.
	if pruneRoot == "" || pruneRoot == "." ||
		pruneRoot == string(filepath.Separator) ||
		!filepath.IsAbs(pruneRoot) {
		return pruneRoot, pruneRefusedUnsafe
	}
	for _, root := range []string{s.libraryDir, s.audiobookDir} {
		if root == "" {
			continue
		}
		cleanRoot := filepath.Clean(root)
		if pruneRoot == cleanRoot || pathUnderDir(cleanRoot, pruneRoot) {
			return pruneRoot, cleanRoot
		}
	}
	return pruneRoot, ""
}

// downloadPathIsFile reports whether cleanDownloadPath refers to a single file
// rather than a download folder. It stats the path first; in move mode the file
// has usually already been renamed away by the time cleanup runs, so it falls
// back to checking whether the path is exactly one of the imported source files
// (a folder never appears in importedSrcFiles, only the files inside it do).
func downloadPathIsFile(cleanDownloadPath string, importedSrcFiles []string) bool {
	if info, err := os.Stat(cleanDownloadPath); err == nil {
		return !info.IsDir()
	}
	for _, src := range importedSrcFiles {
		if filepath.Clean(src) == cleanDownloadPath {
			return true
		}
	}
	return false
}

func safeRemoteID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func cleanupWarnAttrs(clientType, remoteID string, err error) []any {
	attrs := []any{"error", err}
	if clientType != "" {
		attrs = append(attrs, "clientType", clientType)
	}
	if remoteID != "" {
		attrs = append(attrs, "remoteID", remoteID)
	}
	return attrs
}

// detectDownloadFormat inspects a list of file paths and returns the media
// type inferred from their extensions. Any audio extension tips the result to
// audiobook; a list of only ebook files returns ebook. An empty list returns
// ebook as a safe default.
func detectDownloadFormat(files []string) string {
	for _, f := range files {
		if models.IsAudioPath(f) {
			return models.MediaTypeAudiobook
		}
	}
	return models.MediaTypeEbook
}

// supplementClassExts lists the ebook extensions a release ships as SUPPLEMENTS
// at least as often as it ships them as the book itself: a chapter/companion
// PDF, a liner-notes or info .txt/.rtf, occasionally a comic archive. None of
// them is a text-ebook container. Real ebook containers (.epub, .mobi, .azw3,
// .fb2, .djvu, …) are deliberately absent — an epub beside an audiobook IS the
// ebook edition and must still reconcile (#1957).
//
// Membership never disqualifies a file on its own: a PDF-only, comics-only or
// text-only library is a legitimate ebook library, and a supplement-class file
// still claims a book when nothing better competes for it. The set is only ever
// a tie-breaker, at two call sites — the audio-folder guard in the scan loop
// (one of these beside audio is the audiobook's material, not the book's ebook)
// and scanClaimRank below (a real container outranks one of these for the same
// book and format).
var supplementClassExts = map[string]bool{
	".pdf": true, ".txt": true, ".rtf": true, ".cbz": true, ".cbr": true,
}

// scanClaimRank orders the files of one library-scan pass so that a (book,
// format) slot is claimed on the strength of the FILE rather than on where
// filepath.Walk happened to reach it. Rank 0 is a real container — any audio
// file, or an ebook container such as .epub/.mobi/.azw3; rank 1 is a
// supplement-class ebook file.
//
// Sorting a pass by this rank (stably, so equal-rank files keep walk order)
// gives every container its chance to claim before the first supplement is
// considered, so "Burning Chrome (notes).txt" can no longer take the ebook slot
// from "Burning Chrome.epub" merely by sorting ahead of it in the directory
// (#2188). Ranking rather than excluding is what keeps a text-only or PDF-only
// library reconciling: with no container competing for the book, the supplement
// is still the best file there is and claims it.
func scanClaimRank(path string) int {
	if detectDownloadFormat([]string{path}) == models.MediaTypeEbook &&
		supplementClassExts[strings.ToLower(filepath.Ext(path))] {
		return supplementClaimRank
	}
	return containerClaimRank
}

// The two tiers scanClaimRank sorts by, named so its other reader — the
// library-snapshot walk behind FindExisting (#2240) — can test a file's tier
// without a magic number.
const (
	containerClaimRank  = 0
	supplementClaimRank = 1
)

// videoExtensions lists common video container extensions. None of these are
// book files, so a download whose largest file carries one is a movie/TV
// release regardless of what smaller files ride along (#1591).
var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".wmv": true, ".mov": true,
	".mpg": true, ".mpeg": true, ".m2ts": true, ".ts": true, ".vob": true,
	".webm": true, ".flv": true,
}

// largestFileIsVideo reports whether the download's single largest file by
// size is a video file. When the caller supplied an explicit per-torrent file
// list (#903) only those files are considered — downloadPath can be a shared
// download root there, and walking it would judge this download by an
// unrelated sibling's video file. Without an explicit list, downloadPath is a
// per-job directory (SABnzbd/NZBGet) or a single file and is walked directly.
// Unreadable entries are skipped: this is a rejection heuristic, and an
// unstat-able file must not block an otherwise legitimate import.
func largestFileIsVideo(downloadPath string, explicitFiles []string) bool {
	var largestExt string
	var largestSize int64
	consider := func(path string, size int64) {
		if size > largestSize {
			largestSize = size
			largestExt = strings.ToLower(filepath.Ext(path))
		}
	}
	if len(explicitFiles) > 0 {
		for _, f := range explicitFiles {
			if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
				consider(f, fi.Size())
			}
		}
	} else if err := filepath.Walk(downloadPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil //nolint:nilerr // best-effort walk: skip unreadable entries rather than abort the scan
		}
		consider(path, info.Size())
		return nil
	}); err != nil {
		return false
	}
	return videoExtensions[largestExt]
}
