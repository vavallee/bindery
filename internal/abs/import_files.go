package abs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

type ownershipReconcileResult struct {
	OwnedMarked   int
	PendingManual int
	Message       string
}

func (i *Importer) reconcileOwnedState(ctx context.Context, cfg ImportConfig, author *models.Author, book *models.Book, item NormalizedLibraryItem) ownershipReconcileResult {
	if i.books == nil || book == nil {
		return ownershipReconcileResult{}
	}

	var (
		reconcileMessages []string
		ownedMarked       int
		pendingManual     int
		skipped           []string
		unreadable        bool
		rootAttrs         []any
	)

	if ebookPath := strings.TrimSpace(item.EbookPath); ebookPath != "" {
		ok, changed, message, pathUnreadable := i.reconcileFormatPath(ctx, cfg, author, book, models.MediaTypeEbook, ebookPath)
		if ok {
			if changed {
				ownedMarked++
			}
			reconcileMessages = append(reconcileMessages, "ebook verified")
		} else {
			pendingManual++
			reconcileMessages = append(reconcileMessages, message)
			skipped = append(skipped, message)
			unreadable = unreadable || pathUnreadable
			rootAttrs = append(rootAttrs, "ebookRoots", i.allowedRootsForBook(ctx, author, models.MediaTypeEbook))
		}
	}

	if audiobookPath := strings.TrimSpace(item.Path); audiobookPath != "" && len(item.AudioFiles) > 0 {
		ok, changed, message, pathUnreadable := i.reconcileFormatPath(ctx, cfg, author, book, models.MediaTypeAudiobook, audiobookPath)
		if ok {
			if changed {
				ownedMarked++
			}
			reconcileMessages = append(reconcileMessages, "audiobook verified")
		} else {
			pendingManual++
			reconcileMessages = append(reconcileMessages, message)
			skipped = append(skipped, message)
			unreadable = unreadable || pathUnreadable
			rootAttrs = append(rootAttrs, "audiobookRoots", i.allowedRootsForBook(ctx, author, models.MediaTypeAudiobook))
		}
	}

	if len(skipped) > 0 {
		// One line per item. The reason used to reach only the item's result
		// message, so an import that attached no files at all logged nothing
		// to say why (#2578). An out-of-scope or unmapped path is a
		// configuration choice and logs at INFO; a path inside Bindery storage
		// that cannot be read is a missing mount or a permission problem and
		// logs at WARN.
		level := slog.LevelInfo
		if unreadable {
			level = slog.LevelWarn
		}
		attrs := append([]any{
			"itemID", item.ItemID,
			"title", item.Title,
			"reason", strings.Join(skipped, "; "),
		}, rootAttrs...)
		if remap := strings.TrimSpace(cfg.PathRemap); remap != "" {
			attrs = append(attrs, "pathRemap", remap)
		}
		slog.Log(ctx, level, "abs import: item files not attached, imported metadata only", attrs...)
	}

	if ownedMarked == 0 && pendingManual == 0 {
		return ownershipReconcileResult{}
	}

	return ownershipReconcileResult{
		OwnedMarked:   ownedMarked,
		PendingManual: pendingManual,
		Message:       strings.Join(reconcileMessages, "; "),
	}
}

// reconcileFormatPath records candidatePath as the book's file for format when
// it resolves inside Bindery storage and exists. Otherwise it returns a message
// saying why not; unreadable is set when the path is inside Bindery storage but
// could not be stat'd, which is a mount or permission problem rather than a
// configuration choice.
func (i *Importer) reconcileFormatPath(ctx context.Context, cfg ImportConfig, author *models.Author, book *models.Book, format, candidatePath string) (ok, changed bool, message string, unreadable bool) {
	remappedPath := i.remapABSPath(cfg, candidatePath)
	cleanPath := filepath.Clean(remappedPath)
	if cleanPath == "." || cleanPath == "" {
		return false, false, fmt.Sprintf("%s path missing from ABS metadata; imported metadata only", format), false
	}
	if !i.pathAllowedForBook(ctx, author, format, cleanPath) {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, false, fmt.Sprintf("%s path %q remapped to %q but is still outside Bindery storage; imported metadata only", format, strings.TrimSpace(candidatePath), cleanPath), false
		}
		return false, false, fmt.Sprintf("%s path %q is outside Bindery storage%s; imported metadata only", format, cleanPath, unmappedPathHint(cfg)), false
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, false, fmt.Sprintf("%s path %q remapped to %q is not visible to Bindery; imported metadata only", format, strings.TrimSpace(candidatePath), cleanPath), true
		}
		return false, false, fmt.Sprintf("%s path %q is not visible to Bindery; imported metadata only", format, cleanPath), true
	}
	if format == models.MediaTypeEbook && info.IsDir() {
		return false, false, fmt.Sprintf("%s path %q is a directory; imported metadata only", format, cleanPath), false
	}

	alreadyTracked, err := i.bookAlreadyTracksPath(ctx, book.ID, format, cleanPath)
	if err != nil {
		slog.Warn("abs import: file reconciliation lookup failed", "bookID", book.ID, "format", format, "path", cleanPath, "error", err)
		return false, false, fmt.Sprintf("%s verification could not inspect existing Bindery files; imported metadata only", format), false
	}
	if cfg.DryRun {
		return true, !alreadyTracked, "", false
	}
	if err := i.books.SetFormatFilePath(ctx, book.ID, format, cleanPath); err != nil {
		slog.Warn("abs import: file reconciliation failed", "bookID", book.ID, "format", format, "path", cleanPath, "error", err)
		return false, false, fmt.Sprintf("%s path %q could not be registered in Bindery; imported metadata only", format, cleanPath), false
	}
	i.pruneVanishedFormatPaths(ctx, book.ID, format, cleanPath)
	return true, !alreadyTracked, "", false
}

// unmappedPathHint annotates a skipped path that abs.path_remap is configured
// for but did not rewrite. Without it "outside Bindery storage" reads the same
// whether the remap was wrong or the remapped target is simply not a Bindery
// root, and those are fixed in different places.
func unmappedPathHint(cfg ImportConfig) string {
	if strings.TrimSpace(cfg.PathRemap) == "" {
		return ""
	}
	return " (no abs.path_remap rule matched it)"
}

// pruneVanishedFormatPaths drops this book's other book_files rows OF THE SAME
// FORMAT whose files no longer exist on disk, after a new path has been
// registered for that format (#1692).
//
// SetFormatFilePath is BookRepo.AddBookFile — it appends. So re-filing a book
// in ABS (a genre folder rename, a move to another library) and re-importing
// left the book owning both the new row and the old, dead one, and the book's
// derived ebookFilePath could surface the dead path. A library scan never
// cleaned it up either: isReconcileCandidate skips any book that still resolves
// at least one file.
//
// Deliberately narrow, because "the file is missing" is not always "the file is
// gone" — an unmounted NFS share makes every path vanish at once:
//
//   - Only the book just reconciled, only the format just written, and only
//     after a NEW path for that format was successfully stat'd and stored. A
//     book nothing replaced keeps all its rows, however stale.
//   - The freshly written path is never a candidate.
//   - A stat error that is not "does not exist" (permission, I/O, a stalled
//     mount) leaves the row alone. Only a definite ENOENT prunes.
//
// Best-effort: a failure here must not fail an import that has already
// succeeded, so every error is logged and swallowed.
func (i *Importer) pruneVanishedFormatPaths(ctx context.Context, bookID int64, format, keepPath string) {
	files, err := i.books.ListFiles(ctx, bookID)
	if err != nil {
		slog.Warn("abs import: could not list book files to prune stale paths", "bookID", bookID, "error", err)
		return
	}
	keep := filepath.Clean(keepPath)
	for _, f := range files {
		if f.Format != format || filepath.Clean(f.Path) == keep {
			continue
		}
		if _, statErr := os.Stat(f.Path); !os.IsNotExist(statErr) {
			continue // still there, or we cannot prove it isn't
		}
		if _, err := i.books.RemoveBookFile(ctx, f.Path); err != nil {
			slog.Warn("abs import: could not prune stale book file row",
				"bookID", bookID, "format", format, "path", f.Path, "error", err)
			continue
		}
		slog.Info("abs import: pruned stale book file row after path change",
			"bookID", bookID, "format", format, "stale", f.Path, "current", keep)
	}
}

func (i *Importer) inspectFormatPath(ctx context.Context, cfg ImportConfig, format, candidatePath string) (bool, string) {
	remappedPath := i.remapABSPath(cfg, candidatePath)
	cleanPath := filepath.Clean(remappedPath)
	if cleanPath == "." || cleanPath == "" {
		return false, fmt.Sprintf("%s path missing from ABS metadata", format)
	}
	if !i.pathAllowedForBook(ctx, nil, format, cleanPath) {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, fmt.Sprintf("%s path %q remapped to %q but is outside Bindery storage", format, strings.TrimSpace(candidatePath), cleanPath)
		}
		return false, fmt.Sprintf("%s path %q is outside Bindery storage%s", format, cleanPath, unmappedPathHint(cfg))
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, fmt.Sprintf("%s path %q remapped to %q is not visible to Bindery", format, strings.TrimSpace(candidatePath), cleanPath)
		}
		return false, fmt.Sprintf("%s path %q is not visible to Bindery", format, cleanPath)
	}
	if format == models.MediaTypeEbook && info.IsDir() {
		return false, fmt.Sprintf("%s path %q is a directory", format, cleanPath)
	}
	return true, ""
}

func (i *Importer) remapABSPath(cfg ImportConfig, candidatePath string) string {
	candidatePath = strings.TrimSpace(candidatePath)
	if candidatePath == "" || strings.TrimSpace(cfg.PathRemap) == "" {
		return candidatePath
	}
	return pathmap.Parse(cfg.PathRemap).Apply(candidatePath)
}

func (i *Importer) bookAlreadyTracksPath(ctx context.Context, bookID int64, format, path string) (bool, error) {
	files, err := i.books.ListFiles(ctx, bookID)
	if err != nil {
		return false, err
	}
	cleanPath := filepath.Clean(path)
	for _, file := range files {
		if file.Format == format && filepath.Clean(file.Path) == cleanPath {
			return true, nil
		}
	}
	return false, nil
}

func (i *Importer) pathAllowedForBook(ctx context.Context, author *models.Author, format, path string) bool {
	for _, root := range i.allowedRootsForBook(ctx, author, format) {
		if root == "" {
			continue
		}
		if pathUnderDir(path, root) {
			return true
		}
	}
	return false
}

func (i *Importer) allowedRootsForBook(ctx context.Context, author *models.Author, format string) []string {
	roots := make([]string, 0, 4)
	if format == models.MediaTypeAudiobook {
		// Per-author audiobook root folder (#579), then the global audiobook
		// dir. Both must be accepted so an audiobook stored under an author's
		// override is recognised as inside Bindery storage.
		if root := strings.TrimSpace(i.effectiveAudiobookDir(ctx, author)); root != "" {
			roots = append(roots, filepath.Clean(root))
		}
		if root := strings.TrimSpace(i.audiobookDir); root != "" {
			roots = append(roots, filepath.Clean(root))
		}
	}
	if root := strings.TrimSpace(i.effectiveLibraryDir(ctx, author)); root != "" {
		roots = append(roots, filepath.Clean(root))
	}
	if format == models.MediaTypeAudiobook {
		if root := strings.TrimSpace(i.libraryDir); root != "" {
			roots = append(roots, filepath.Clean(root))
		}
	}
	return dedupeCleanPaths(roots)
}

// effectiveAudiobookDir returns the audiobook root for the given author:
// the per-author AudiobookRootFolderID when set (#579), else the global
// audiobookDir. It deliberately does not consult the ebook RootFolderID so an
// ebook root folder never widens audiobook acceptance into the ebook tree
// (#421). author may be nil (e.g. inspectFormatPath has no author context),
// in which case only the global dir applies.
func (i *Importer) effectiveAudiobookDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.AudiobookRootFolderID != nil && i.rootFolders != nil {
		if root, err := i.rootFolders.GetByID(ctx, *author.AudiobookRootFolderID); err == nil && root != nil {
			return root.Path
		}
	}
	return i.audiobookDir
}

func (i *Importer) effectiveLibraryDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.RootFolderID != nil && i.rootFolders != nil {
		if root, err := i.rootFolders.GetByID(ctx, *author.RootFolderID); err == nil && root != nil {
			return root.Path
		}
	}
	if i.settings != nil && i.rootFolders != nil {
		if setting, err := i.settings.Get(ctx, settingDefaultRootID); err == nil && setting != nil && strings.TrimSpace(setting.Value) != "" {
			if id, err := strconv.ParseInt(strings.TrimSpace(setting.Value), 10, 64); err == nil && id > 0 {
				if root, err := i.rootFolders.GetByID(ctx, id); err == nil && root != nil {
					return root.Path
				}
			}
		}
	}
	return i.libraryDir
}

func pathUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func dedupeCleanPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clean := filepath.Clean(strings.TrimSpace(value))
		if clean == "." || clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}
