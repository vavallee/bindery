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
	)

	if ebookPath := strings.TrimSpace(item.EbookPath); ebookPath != "" {
		ok, changed, message := i.reconcileFormatPath(ctx, cfg, author, book, models.MediaTypeEbook, ebookPath)
		if ok {
			if changed {
				ownedMarked++
			}
			reconcileMessages = append(reconcileMessages, "ebook verified")
		} else {
			pendingManual++
			reconcileMessages = append(reconcileMessages, message)
		}
	}

	if audiobookPath := strings.TrimSpace(item.Path); audiobookPath != "" && len(item.AudioFiles) > 0 {
		ok, changed, message := i.reconcileFormatPath(ctx, cfg, author, book, models.MediaTypeAudiobook, audiobookPath)
		if ok {
			if changed {
				ownedMarked++
			}
			reconcileMessages = append(reconcileMessages, "audiobook verified")
		} else {
			pendingManual++
			reconcileMessages = append(reconcileMessages, message)
		}
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

func (i *Importer) reconcileFormatPath(ctx context.Context, cfg ImportConfig, author *models.Author, book *models.Book, format, candidatePath string) (bool, bool, string) {
	remappedPath := i.remapABSPath(cfg, candidatePath)
	cleanPath := filepath.Clean(remappedPath)
	if cleanPath == "." || cleanPath == "" {
		return false, false, fmt.Sprintf("%s path missing from ABS metadata; imported metadata only", format)
	}
	if !i.pathAllowedForBook(ctx, author, format, cleanPath) {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, false, fmt.Sprintf("%s path %q remapped to %q but is still outside Bindery storage; imported metadata only", format, strings.TrimSpace(candidatePath), cleanPath)
		}
		return false, false, fmt.Sprintf("%s path %q is outside Bindery storage; imported metadata only", format, cleanPath)
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		if remappedPath != strings.TrimSpace(candidatePath) {
			return false, false, fmt.Sprintf("%s path %q remapped to %q is not visible to Bindery; imported metadata only", format, strings.TrimSpace(candidatePath), cleanPath)
		}
		return false, false, fmt.Sprintf("%s path %q is not visible to Bindery; imported metadata only", format, cleanPath)
	}
	if format == models.MediaTypeEbook && info.IsDir() {
		return false, false, fmt.Sprintf("%s path %q is a directory; imported metadata only", format, cleanPath)
	}

	alreadyTracked, err := i.bookAlreadyTracksPath(ctx, book.ID, format, cleanPath)
	if err != nil {
		slog.Warn("abs import: file reconciliation lookup failed", "bookID", book.ID, "format", format, "path", cleanPath, "error", err)
		return false, false, fmt.Sprintf("%s verification could not inspect existing Bindery files; imported metadata only", format)
	}
	if cfg.DryRun {
		return true, !alreadyTracked, ""
	}
	if err := i.books.SetFormatFilePath(ctx, book.ID, format, cleanPath); err != nil {
		slog.Warn("abs import: file reconciliation failed", "bookID", book.ID, "format", format, "path", cleanPath, "error", err)
		return false, false, fmt.Sprintf("%s path %q could not be registered in Bindery; imported metadata only", format, cleanPath)
	}
	return true, !alreadyTracked, ""
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
		return false, fmt.Sprintf("%s path %q is outside Bindery storage", format, cleanPath)
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
	roots := make([]string, 0, 3)
	if format == models.MediaTypeAudiobook {
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
