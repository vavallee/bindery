package importer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// downloadArtifactExts are download-client receipt and repair files that ride
// along in a completed job folder but have no business in the library: the
// queued .nzb itself, PAR2 repair volumes, and scene checksum/description
// sidecars (#1542). Directory placements (hardlink, copy, move) skip them so
// a usenet job's receipts never land next to the imported media. Deliberately
// NOT included: .nfo, cover art, .cue, booklets — files some users want kept.
var downloadArtifactExts = map[string]bool{
	".nzb": true, ".par2": true, ".sfv": true, ".srr": true, ".srs": true, ".diz": true,
}

// isDownloadArtifact reports whether name has a download-artifact extension
// (case-insensitive). PAR2 volumes like "x.vol01+02.par2" resolve to ".par2"
// via filepath.Ext, so they are covered without special-casing.
func isDownloadArtifact(name string) bool {
	return downloadArtifactExts[strings.ToLower(filepath.Ext(name))]
}

// removeDownloadArtifacts deletes download-artifact files anywhere under dir.
// Used after MoveDir's same-filesystem rename fast path, which moves the job
// folder wholesale (a rename cannot filter); the copy-based paths skip the
// artifacts at placement time instead. Best-effort: a receipt that cannot be
// removed is not worth failing an otherwise-successful import over.
func removeDownloadArtifacts(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() && isDownloadArtifact(d.Name()) {
			if rmErr := os.Remove(path); rmErr != nil {
				slog.Warn("could not remove download artifact from imported folder", "path", path, "error", rmErr)
			}
		}
		return nil
	})
}

// templateGroupRe matches one "{...}" group. The content is parsed by
// renderGroup: either the classic simple form — a bare "{Token}", optionally
// with a default after a colon ("{Genre:Unsorted}") or a zero-pad width
// ("{SeriesNumber:2}") — or a conditional group (#1127) where literal text
// sits alongside the token(s) inside the braces ("{Title}{ - Series}") and
// the whole group collapses to "" when every token in it is empty.
var templateGroupRe = regexp.MustCompile(`\{([^{}]*)\}`)

// simpleGroupRe matches the classic single-token group content: a keyword
// with an optional ":modifier" (default text or zero-pad width).
var simpleGroupRe = regexp.MustCompile(`^(\w+)(?::([^{}]*))?$`)

// groupWordRe finds keyword candidates inside a conditional group: a word
// run with an optional ":N" width. Non-keyword word runs stay literal.
var groupWordRe = regexp.MustCompile(`(\w+)(:\d{1,2})?`)

const defaultNamingTemplate = "{Author}/{Title} ({Year})/{Title} - {Author}.{ext}"
const defaultAudiobookTemplate = "{Author}/{Title} ({Year})"

// Renamer moves and renames imported book files according to a naming template.
// Separate templates for ebook (per-file) and audiobook (per-folder) outputs.
type Renamer struct {
	template          string
	audiobookTemplate string
}

// NewRenamer creates a renamer with the given naming template.
// If template is empty, the default template is used.
func NewRenamer(template string) *Renamer {
	return NewRenamerWithAudiobook(template, "")
}

// NewRenamerWithAudiobook creates a renamer with separate ebook and audiobook
// templates. Empty strings fall back to the defaults.
func NewRenamerWithAudiobook(ebookTemplate, audiobookTemplate string) *Renamer {
	if ebookTemplate == "" {
		ebookTemplate = defaultNamingTemplate
	}
	if audiobookTemplate == "" {
		audiobookTemplate = defaultAudiobookTemplate
	}
	return &Renamer{template: ebookTemplate, audiobookTemplate: audiobookTemplate}
}

// DestPath computes the destination path for an ebook file.
// series and seriesNumber are the book's primary series title and position
// (empty strings for standalone books without a series).
func (r *Renamer) DestPath(rootFolder string, author *models.Author, book *models.Book, series, seriesNumber, srcPath string) (string, error) {
	ext := strings.TrimPrefix(filepath.Ext(srcPath), ".")
	dest := filepath.Join(rootFolder, r.apply(r.template, author, book, series, seriesNumber, ext))
	return ensureContained(dest, rootFolder)
}

// AudiobookDestDir computes the destination directory into which an audiobook
// download folder should be moved. The download's internal file structure is
// preserved inside (multi-part m4b/mp3 + cover + cue sheet stay together).
// series and seriesNumber are the book's primary series title and position.
func (r *Renamer) AudiobookDestDir(rootFolder string, author *models.Author, book *models.Book, series, seriesNumber string) (string, error) {
	dest := filepath.Join(rootFolder, r.apply(r.audiobookTemplate, author, book, series, seriesNumber, ""))
	return ensureContained(dest, rootFolder)
}

// DropEbookName returns the flat leaf filename for an ebook placed directly in
// a drop folder (import.drop_layout=flat): "{Title} - {Author}.{ext}" with each
// component sanitized. The result is a bare filename with no path separators,
// safe to filepath.Join under the drop folder. filepath.Base is defensive
// insurance against a sanitizer change ever letting a separator through.
func (r *Renamer) DropEbookName(author *models.Author, book *models.Book, srcPath string) string {
	ext := strings.TrimPrefix(filepath.Ext(srcPath), ".")
	return filepath.Base(r.apply("{Title} - {Author}.{ext}", author, book, "", "", ext))
}

// DropAudiobookName returns the flat leaf directory name for an audiobook
// placed in a drop folder (import.drop_layout=flat): "{Title} - {Author}".
func (r *Renamer) DropAudiobookName(author *models.Author, book *models.Book) string {
	return filepath.Base(r.apply("{Title} - {Author}", author, book, "", "", ""))
}

// ensureContained returns dest unchanged when it resolves inside baseDir; otherwise
// it returns an error. This prevents book-derived path components (e.g. author or
// title strings seeded from remote metadata) from escaping the configured library.
func ensureContained(dest, baseDir string) (string, error) {
	cleanDest := filepath.Clean(dest)
	cleanBase := filepath.Clean(baseDir)
	if !strings.HasPrefix(cleanDest, cleanBase+string(filepath.Separator)) && cleanDest != cleanBase {
		return "", fmt.Errorf("destination path escapes base directory")
	}
	return cleanDest, nil
}

func (r *Renamer) apply(template string, author *models.Author, book *models.Book, series, seriesNumber, ext string) string {
	year := ""
	if book.ReleaseDate != nil {
		year = fmt.Sprintf("%d", book.ReleaseDate.Year())
	}
	authorName := "Unknown Author"
	if author != nil {
		authorName = author.Name
	}
	values := map[string]string{
		"Author":       sanitizePath(authorName),
		"SortAuthor":   sanitizePath(authorSortName(authorName)),
		"Title":        sanitizePath(book.Title),
		"Year":         year,
		"ASIN":         sanitizePath(book.ASIN),
		"Series":       sanitizePath(series),
		"SeriesNumber": sanitizePath(seriesNumber),
		"Genre":        sanitizePath(firstGenre(book.Genres)),
		"Lang":         sanitizePath(book.Language),
		"ext":          ext,
	}

	// Render per path segment. A segment that renders empty (e.g. an empty
	// "{Series}" component) is dropped so the path doesn't gain an empty
	// directory level.
	segments := strings.Split(template, "/")
	kept := segments[:0]
	for _, seg := range segments {
		rendered := renderSegment(seg, values)
		if rendered != "" {
			kept = append(kept, rendered)
		}
	}
	return strings.Join(kept, "/")
}

// renderSegment substitutes "{...}" groups in a single path segment.
// When the leading group(s) of a segment resolve to empty, the separator glue
// that would dangle in front of the first real value is dropped, so a template
// like "{SeriesNumber} - {Title}" with no series number yields "Title" rather
// than " - Title" (issue: Discord report, Jonathan Stroud "The Hollow Boy").
//
// Only leading glue is collapsed. Interior and trailing glue is left intact so
// the established behaviour of "{Title} ({Year})" → "Title ()" and
// "{Title}.{ext}" → "Title." (audiobook folders) is preserved — those are
// pinned by the preview drift guard and mirrored client-side.
//
// Groups that reference no known token are left verbatim (e.g. a "{Titel}"
// typo stays "{Titel}"), matching the previous literal-passthrough behaviour.
func renderSegment(seg string, values map[string]string) string {
	locs := templateGroupRe.FindAllStringSubmatchIndex(seg, -1)
	if len(locs) == 0 {
		return strings.TrimSpace(seg)
	}

	// Split the segment into literals (len n+1) interleaved with group values
	// (len n): lits[0] vals[0] lits[1] vals[1] ... vals[n-1] lits[n].
	lits := make([]string, 0, len(locs)+1)
	vals := make([]string, 0, len(locs))
	prev := 0
	for _, m := range locs {
		start, end := m[0], m[1]
		content := seg[m[2]:m[3]]
		lits = append(lits, seg[prev:start])
		v, known := renderGroup(content, values)
		if !known {
			v = seg[start:end] // no known token in the group: keep it verbatim
		}
		vals = append(vals, v)
		prev = end
	}
	lits = append(lits, seg[prev:])

	// Walk the leading run of empty tokens, dropping the separator that follows
	// each. Stop at the first non-empty value, or at a leading literal that is
	// real text rather than just glue (so "Vol {SeriesNumber}" keeps "Vol ").
	for i := range vals {
		if vals[i] != "" {
			break
		}
		if strings.TrimSpace(lits[i]) != "" {
			break
		}
		lits[i+1] = ""
	}

	var b strings.Builder
	for i, v := range vals {
		b.WriteString(lits[i])
		b.WriteString(v)
	}
	b.WriteString(lits[len(lits)-1])
	return strings.TrimSpace(b.String())
}

// renderGroup renders the content of one "{...}" group. known=false means
// the group references no known token and the caller keeps it verbatim.
//
// Two forms (#1127):
//
//   - Simple: "Token", "Token:default", "Token:N". A ":modifier" of 1–2
//     digits is a zero-pad width applied to an all-digit value
//     ("{SeriesNumber:2}" → "02"); anything else is the classic default text
//     substituted when the token is empty ("{Genre:Unsorted}").
//   - Conditional: literal text alongside the token(s) inside the braces
//     ("{ - Series}", "{Vol SeriesNumber}"). Literals render only when at
//     least one token in the group has a value; when every token is empty
//     the whole group collapses to "". Widths are allowed after a token
//     (":N"); text defaults are not supported in conditional groups.
func renderGroup(content string, values map[string]string) (rendered string, known bool) {
	if m := simpleGroupRe.FindStringSubmatch(content); m != nil {
		v, ok := values[m[1]]
		if !ok {
			return "", false
		}
		if mod := m[2]; mod != "" {
			if w, isWidth := parseWidth(mod); isWidth {
				v = zeroPad(v, w)
			} else if v == "" {
				v = sanitizePath(mod)
			}
		}
		return v, true
	}

	anyKnown, anyValue := false, false
	var b strings.Builder
	prev := 0
	for _, m := range groupWordRe.FindAllStringSubmatchIndex(content, -1) {
		word := content[m[2]:m[3]]
		v, ok := values[word]
		if !ok {
			continue // non-keyword word run: stays part of the literal text
		}
		anyKnown = true
		b.WriteString(sanitizeInline(content[prev:m[0]]))
		if m[4] >= 0 { // ":N" width attached to this token
			if w, isWidth := parseWidth(content[m[4]+1 : m[5]]); isWidth {
				v = zeroPad(v, w)
			}
		}
		if v != "" {
			anyValue = true
		}
		b.WriteString(v)
		prev = m[1]
	}
	if !anyKnown {
		return "", false
	}
	if !anyValue {
		return "", true
	}
	b.WriteString(sanitizeInline(content[prev:]))
	return b.String(), true
}

// parseWidth reports whether a ":modifier" is a zero-pad width: 1–2 digits
// (1–99). Longer all-digit strings (e.g. "{Year:2024}") keep their historical
// meaning as default text.
func parseWidth(mod string) (int, bool) {
	if len(mod) == 0 || len(mod) > 2 {
		return 0, false
	}
	w := 0
	for i := 0; i < len(mod); i++ {
		if mod[i] < '0' || mod[i] > '9' {
			return 0, false
		}
		w = w*10 + int(mod[i]-'0')
	}
	return w, w > 0
}

// zeroPad left-pads an all-digit value with zeros to the given width so
// alphabetic filename sorts order "02" before "10". Non-numeric and empty
// values are returned unchanged — padding "Demo Series" would be nonsense.
func zeroPad(v string, width int) string {
	if v == "" || len(v) >= width {
		return v
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return v
		}
	}
	return strings.Repeat("0", width-len(v)) + v
}

// firstGenre returns the primary genre for the {Genre} token: the first entry
// of the book's genre list, or "" when there are none. The list is populated
// from Hardcover's curated taxonomy when available (see aggregator enrichment);
// the choice of index 0 is deterministic for a given fetch, but note that
// upstream re-categorisation can change it and Bindery does not relocate
// already-imported files when it does.
func firstGenre(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}

// MoveFile atomically copies a file to the destination and then removes the source.
// This handles cross-filesystem moves (e.g., NFS download dir → NFS library).
func MoveFile(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Try rename first (same filesystem, instant)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Cross-filesystem: copy then delete
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	// Verify copy
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat destination: %w", err)
	}
	if srcInfo.Size() != dstInfo.Size() {
		os.Remove(dst)
		return fmt.Errorf("size mismatch: src=%d dst=%d", srcInfo.Size(), dstInfo.Size())
	}

	// Remove source
	return os.Remove(src)
}

// MoveDir moves a directory (with all its contents) to the destination.
// Cross-filesystem safe: tries rename first, else recursive copy + delete.
// The destination directory must not already exist.
func MoveDir(src, dst string) error {
	return moveDirCtx(context.Background(), src, dst)
}

// moveDirCtx is the context-aware implementation of MoveDir. On the slow
// (cross-filesystem) path the recursive copy honours ctx; if ctx is cancelled
// the partial destination is removed but the source is left intact — never
// remove the still-seeding source after a cancelled or failed copy.
func moveDirCtx(ctx context.Context, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Fast path: same filesystem. Rename moves the folder wholesale, so any
	// download artifacts (.nzb receipts, .par2 volumes) ride along — sweep
	// them out of the destination afterwards to match the filtering the
	// copy-based paths do at placement time.
	if err := os.Rename(src, dst); err == nil {
		removeDownloadArtifacts(dst)
		return nil
	}

	// Slow path: recursive copy, then verify, then remove.
	if err := copyDirContext(ctx, src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("copy dir: %w", err)
	}
	// The copy completed without error (and without cancellation — copyDirContext
	// returns ctx.Err() on cancel). Only now is it safe to remove the source.
	return os.RemoveAll(src)
}

// CopyFile copies src to dst without removing the source. It is the "copy"
// import mode counterpart to MoveFile. The source is left intact so that
// torrent clients continue seeding from the original download location.
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return copyFile(src, dst)
}

// HardlinkFile creates a hard link at dst pointing to the same inode as src.
// When the two paths turn out to be on different mounts (EXDEV — common with
// separate Docker bind mounts or Unraid /mnt/user shares that share a device id
// but not a mount), it falls back to a COPY. A copy is seeding-safe: src is
// left in place, so the download client keeps seeding — unlike a move fallback,
// which would break seeding. The cost is extra disk for the imported copy.
// Non-EXDEV errors (permissions, missing source) are returned as-is.
func HardlinkFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := osLink(src, dst); err != nil {
		if crossDeviceErr(err) {
			return copyFile(src, dst)
		}
		return fmt.Errorf("hardlink %q → %q: %w (download dir and library must be on the same filesystem)", src, dst, err)
	}
	return nil
}

// CopyDir copies a directory tree from src to dst without removing the source.
// Used by "copy" import mode for audiobook folders so the download client can
// continue seeding from the original location.
func CopyDir(src, dst string) error {
	return copyDirPublicCtx(context.Background(), src, dst)
}

// copyDirPublicCtx is the context-aware implementation of CopyDir. On
// cancellation the partial destination is removed; the source is never
// touched (copy mode preserves seeding).
func copyDirPublicCtx(ctx context.Context, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	if err := copyDirContext(ctx, src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("copy dir: %w", err)
	}
	return nil
}

// HardlinkDir mirrors a directory tree from src into dst by hard-linking every
// regular file. Directory entries are created normally. Both trees must be on
// the same filesystem — no fallback is attempted on cross-filesystem failure.
//
// Uses os.Root to scope traversal, preventing symlink-based TOCTOU (gosec G122).
func HardlinkDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer func() { _ = srcRoot.Close() }()

	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open dest root: %w", err)
	}
	defer func() { _ = dstRoot.Close() }()

	if err := hardlinkDirRooted(srcRoot, dstRoot, "."); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return nil
}

func hardlinkDirRooted(srcRoot, dstRoot *os.Root, rel string) error {
	f, err := srcRoot.Open(rel)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		if !e.Type().IsRegular() && !e.IsDir() {
			continue // skip symlinks
		}
		if e.Type().IsRegular() && isDownloadArtifact(e.Name()) {
			continue // receipts/repair files never enter the library (#1542)
		}
		if e.IsDir() {
			if err := dstRoot.Mkdir(child, 0o750); err != nil && !os.IsExist(err) {
				return err
			}
			if err := hardlinkDirRooted(srcRoot, dstRoot, child); err != nil {
				return err
			}
			continue
		}
		srcPath := filepath.Join(srcRoot.Name(), child)
		dstPath := filepath.Join(dstRoot.Name(), child)
		if err := osLink(srcPath, dstPath); err != nil {
			// Cross-mount (EXDEV): fall back to a seeding-safe rooted copy for
			// this entry instead of failing the whole audiobook folder. See
			// HardlinkFile for the copy-vs-move rationale.
			if crossDeviceErr(err) {
				if cerr := copyFileRooted(srcRoot, dstRoot, child); cerr != nil {
					return fmt.Errorf("hardlink cross-device copy fallback %q → %q: %w", srcPath, dstPath, cerr)
				}
				continue
			}
			return fmt.Errorf("hardlink %q → %q: %w (download dir and library must be on the same filesystem)", srcPath, dstPath, err)
		}
	}
	return nil
}

// copyDirContext recursively copies srcDir contents into dstDir, preserving the
// internal layout. dstDir will be created (including parents).
//
// Uses os.Root to scope all filesystem operations, preventing symlink-based
// TOCTOU traversal (gosec G122). A symlink inside the source tree that
// points outside the root is rejected by the kernel, not by user-space
// checks that can race.
//
// ctx is checked before every directory entry and before every file copy, so a
// cancelled import (30-min cap or shutdown) stops the copy promptly and returns
// ctx.Err() instead of running to completion in a detached goroutine. This is
// what lets callers safely skip os.RemoveAll(src) on cancellation — the copy
// never finishes, so the seeding source is never deleted.
func copyDirContext(ctx context.Context, srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return err
	}
	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer func() { _ = srcRoot.Close() }()

	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		return fmt.Errorf("open dest root: %w", err)
	}
	defer func() { _ = dstRoot.Close() }()

	return copyDirRooted(ctx, srcRoot, dstRoot, ".")
}

func copyDirRooted(ctx context.Context, srcRoot, dstRoot *os.Root, rel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := srcRoot.Open(rel)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := filepath.Join(rel, e.Name())
		if !e.Type().IsRegular() && !e.IsDir() {
			continue // skip symlinks and other non-regular entries
		}
		if e.Type().IsRegular() && isDownloadArtifact(e.Name()) {
			continue // receipts/repair files never enter the library (#1542)
		}
		if e.IsDir() {
			if err := dstRoot.Mkdir(child, 0o750); err != nil && !os.IsExist(err) {
				return err
			}
			if err := copyDirRooted(ctx, srcRoot, dstRoot, child); err != nil {
				return err
			}
			continue
		}
		if err := copyFileRooted(srcRoot, dstRoot, child); err != nil {
			return err
		}
	}
	return nil
}

func copyFileRooted(srcRoot, dstRoot *os.Root, rel string) error {
	in, err := srcRoot.Open(rel)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := dstRoot.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	// closeOnce: explicit Close in the happy path surfaces deferred write
	// errors; the defer runs as a no-op safety net on early returns. Without
	// this, NFS write errors that the kernel does not surface until Close
	// would be swallowed and the importer would record a corrupt file as
	// successfully copied (Wave 4 / finding 24).
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	closed = true
	if err := out.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

// copyFileCtx copies src to dst, returning ctx.Err() if the context is
// cancelled before the copy completes. Closing the file descriptors on
// cancellation encourages the blocking io.Copy goroutine to unblock on
// Linux (including NFS); if it does not, the goroutine exits when the
// process eventually closes those fds. Any partial dst is removed.
//
// out.Close() is invoked explicitly after Sync (Wave 4 / finding 24) so
// deferred write errors are surfaced. The kernel page cache can hold dirty
// pages from a successful Write call until close on NFS, where the actual
// server-side write happens; a swallowed Close error would record a
// successful copy of a corrupt file. The deferred Close runs only on the
// cancellation path, where the caller is already returning ctx.Err() and
// the Close error is meaningless.
func copyFileCtx(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// closeOnce flag so the cancellation defer and the happy-path Close do
	// not double-close the same fd.
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		_, copyErr := io.Copy(out, in)
		if copyErr == nil {
			copyErr = out.Sync()
		}
		ch <- result{copyErr}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			_ = os.Remove(dst)
			return r.err
		}
		// Close explicitly so NFS / FUSE filesystems that defer write
		// errors until Close surface them as a copy failure, not as a
		// silent partial file (finding 24). If Close fails, treat the
		// dst as corrupt and remove it so a retry sees a clean slate.
		closed = true
		if err := out.Close(); err != nil {
			_ = os.Remove(dst)
			return fmt.Errorf("close: %w", err)
		}
		return nil
	case <-ctx.Done():
		_ = in.Close()
		_ = out.Close()
		closed = true
		_ = os.Remove(dst)
		return ctx.Err()
	}
}

func copyFile(src, dst string) error {
	return copyFileCtx(context.Background(), src, dst)
}

// moveFileRename and moveFileCopy are indirection seams over os.Rename and
// copyFileCtx so tests can exercise MoveFileCtx's cross-filesystem fallback
// without an actual second filesystem (and inject a short copy to verify the
// size-mismatch data-safety branch). Production always uses the real
// functions; tests override these in-process and restore them.
var (
	moveFileRename = os.Rename
	moveFileCopy   = copyFileCtx
)

// osLink is an indirection seam over os.Link so tests can simulate a
// cross-device (EXDEV) link failure and exercise the seeding-safe copy
// fallback without a second real filesystem. Production always uses os.Link.
var osLink = os.Link

// MoveFileCtx is like MoveFile but returns ctx.Err() if the context is
// cancelled during the cross-filesystem copy phase.
func MoveFileCtx(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	if err := moveFileRename(src, dst); err == nil {
		return nil
	}

	if err := moveFileCopy(ctx, src, dst); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat destination: %w", err)
	}
	if srcInfo.Size() != dstInfo.Size() {
		_ = os.Remove(dst)
		return fmt.Errorf("size mismatch: src=%d dst=%d", srcInfo.Size(), dstInfo.Size())
	}

	return os.Remove(src)
}

// CopyFileCtx is like CopyFile but returns ctx.Err() if the context is
// cancelled during the copy.
func CopyFileCtx(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return copyFileCtx(ctx, src, dst)
}

// StagedImport places src at a sibling of dst (the "staging" path) so the
// caller can write a book_files row pointing at dst before any user-visible
// file appears at dst, then atomically promote it via the returned commit
// function. This is the importer-atomicity primitive used by Wave 4 finding
// 23 to close the move-then-DB-update gap: previously the importer landed
// the file at its final destination first and wrote the DB row second, which
// meant a transient SQLite error left the file orphaned on disk with no
// book_files row — the user saw "not imported" in the queue and re-grabbed,
// silently duplicating the file under " (2)".
//
// The contract:
//
//   - On success Stage produces a file at a path inside filepath.Dir(dst)
//     whose name is dst's basename plus a per-call random suffix. The caller
//     can now perform any DB work that should be reversible without leaving
//     a half-imported file in the library.
//   - commit performs an os.Rename(staged, dst). On POSIX same-filesystem
//     this is atomic; both sides of StagedImport require the staged path to
//     be on the same filesystem as dst (which it is by construction since
//     they share a directory).
//   - rollback removes the staged file and returns. It is safe to call
//     after a successful commit (it becomes a no-op because the staged path
//     no longer exists).
//
// Hardlink mode is supported: a hard link is created at the staging path,
// then renamed onto dst. The inode count of the source ends up at 2 (source
// + dst), matching the non-staged hardlink mode exactly.
//
// Copy mode invokes the existing copyFileCtx, which (Wave 4 / finding 24)
// surfaces NFS-deferred write errors via an explicit Close.
//
// Move mode tries os.Rename(src, staging) first (same-filesystem fast path)
// and falls back to copy + Remove(src) on cross-filesystem. The source is
// only removed after the staged copy has been Sync+Close'd successfully, so
// a failure in the slow-path copy never destroys the still-seeding source.
//
// The mode strings match those used by scanner.go: "hardlink", "copy",
// and the default (anything else) is move.
func StagedImport(ctx context.Context, mode, src, dst string) (stagedPath string, commit func() error, rollback func(), err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", nil, nil, fmt.Errorf("create dir: %w", err)
	}
	staged := stagingPath(dst)

	// movedSrcViaRename and movedSrcInPlace control rollback / post-commit
	// cleanup for move mode:
	//
	//   - movedSrcViaRename: the same-fs fast path used os.Rename(src,
	//     staged). On rollback we reverse it (atomic) so the still-seeding
	//     source survives a commit failure (Wave 4 / finding 23 regression
	//     guard against issue #705 finding 1).
	//
	//   - movedSrcInPlace: the cross-fs slow path COPIED src to staged and
	//     left src intact. The source is only removed AFTER commit succeeds;
	//     this preserves the "never destroy still-seeding source after a
	//     failed import" invariant in the slow path too.
	movedSrcViaRename := false
	movedSrcInPlace := false

	switch mode {
	case "hardlink":
		if err := osLink(src, staged); err != nil {
			// Non-EXDEV errors (permissions, missing source) fail loudly.
			if !crossDeviceErr(err) {
				return "", nil, nil, fmt.Errorf("stage hardlink: %w", err)
			}
			// src and staged turned out to be on different mounts despite
			// hardlink mode being selected (e.g. two bind mounts sharing a
			// device id, or an operator forcing import.mode=hardlink). Fall
			// back to COPY: unlike a move it preserves seeding (src is left in
			// place), at the cost of extra disk.
			if cerr := copyFileCtx(ctx, src, staged); cerr != nil {
				return "", nil, nil, fmt.Errorf("stage hardlink cross-device copy fallback: %w", cerr)
			}
		}
	case "copy":
		if err := copyFileCtx(ctx, src, staged); err != nil {
			return "", nil, nil, fmt.Errorf("stage copy: %w", err)
		}
	default: // move
		moved, err := stageMove(ctx, src, staged)
		if err != nil {
			return "", nil, nil, err
		}
		movedSrcViaRename = moved
		movedSrcInPlace = !moved
	}

	committed := false
	commit = func() error {
		if err := os.Rename(staged, dst); err != nil {
			return fmt.Errorf("commit staged file: %w", err)
		}
		committed = true
		// Slow-path move: src was copied + left in place; the post-commit
		// removal happens here so a commit failure never destroys the
		// source. A failure to remove src after a successful commit is
		// non-fatal (move-mode cleanup elsewhere prunes leftover sources);
		// log + ignore so the imported file is still recorded as success.
		if movedSrcInPlace {
			if err := os.Remove(src); err != nil {
				// Returning nil because the import has succeeded; the
				// orphan source is a cosmetic leak, not a failure.
				return nil
			}
		}
		return nil
	}
	rollback = func() {
		if committed {
			return
		}
		if movedSrcViaRename {
			// Same-fs path: put the source back. Both endpoints are on
			// the same filesystem (we used os.Rename in stageMove), so
			// this is atomic on POSIX. If it fails, fall through and
			// remove the staged file anyway — losing the source is
			// preferable to leaving an orphan stage that re-runs of the
			// importer keep tripping over.
			if err := os.Rename(staged, src); err == nil {
				return
			}
		}
		// Cross-fs path: src is still in place; just delete the staged
		// copy. The src survives untouched and a retry can re-import it.
		_ = os.Remove(staged)
	}
	return staged, commit, rollback, nil
}

// stagingPath returns a sibling of dst with a random suffix appended. The
// staged file lives in the same directory as dst so the final os.Rename is
// guaranteed to be on the same filesystem (atomic on POSIX). The "{dst}"
// basename prefix means a directory listing makes the staged file obviously
// related to the final import, which helps an operator debugging a crashed
// import (Wave 4 / finding 23). The ".bindery-stage-" infix is recognisable
// enough to write a manual cleanup script against if needed.
func stagingPath(dst string) string {
	base := filepath.Base(dst)
	// time.Now().UnixNano() is sufficient uniqueness for a serial importer;
	// the importer holds a per-download lock so two stage calls for the same
	// dst cannot race.
	return filepath.Join(filepath.Dir(dst), fmt.Sprintf(".bindery-stage-%d-%s", time.Now().UnixNano(), base))
}

// stageMove relocates src to staged. It tries os.Rename first (same-fs fast
// path: O(1) and src disappears in the same syscall); on cross-fs it falls
// back to a Sync+Close'd copy and leaves src intact. The caller's commit
// closure is responsible for removing src after the staged file has been
// promoted to dst — keeping src around until commit means a failed import
// can always restore "src still on disk, nothing in library".
//
// The returned bool is true when the fast path was used. The caller's
// rollback logic uses it to decide whether to restore src by reversing the
// rename (fast path) or to do nothing about src because it was never
// touched (slow path).
func stageMove(ctx context.Context, src, staged string) (movedViaRename bool, err error) {
	if err := os.Rename(src, staged); err == nil {
		return true, nil
	}
	if err := copyFileCtx(ctx, src, staged); err != nil {
		return false, fmt.Errorf("stage move (slow path): %w", err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, fmt.Errorf("stat source: %w", err)
	}
	stagedInfo, err := os.Stat(staged)
	if err != nil {
		return false, fmt.Errorf("stat staged: %w", err)
	}
	if srcInfo.Size() != stagedInfo.Size() {
		_ = os.Remove(staged)
		return false, fmt.Errorf("size mismatch: src=%d staged=%d", srcInfo.Size(), stagedInfo.Size())
	}
	return false, nil
}

// MoveDirCtx is like MoveDir but returns ctx.Err() if the context is
// cancelled. Unlike the previous detached-goroutine implementation, the copy
// is performed inline with per-entry cancellation checks: when ctx fires the
// copy stops promptly and the source is NOT removed. A cancelled or shut-down
// import therefore can never delete the still-seeding source after the fact.
func MoveDirCtx(ctx context.Context, src, dst string) error {
	return moveDirCtx(ctx, src, dst)
}

// CopyDirCtx is like CopyDir but returns ctx.Err() if the context is
// cancelled. The copy honours ctx per-entry; on cancellation the partial
// destination is removed and the source is left untouched.
func CopyDirCtx(ctx context.Context, src, dst string) error {
	return copyDirPublicCtx(ctx, src, dst)
}

// maxPathComponentLen caps each sanitized path segment. Most filesystems limit
// a single name to 255 bytes (NAME_MAX); 200 leaves room for the extension and
// any uniqueness suffix the importer appends.
const maxPathComponentLen = 200

// pathCharReplacer neutralises characters that are problematic in file
// paths. Shared by sanitizePath (full field sanitisation) and sanitizeInline
// (conditional-group literals, #1127).
var pathCharReplacer = strings.NewReplacer(
	"/", "-", "\\", "-", ":", "-", "*", "", "?", "",
	"\"", "", "<", "", ">", "", "|", "",
)

// sanitizeInline neutralises path separators and control characters in a
// conditional-group literal without trimming or segment-splitting — the
// literal's surrounding whitespace is meaningful glue ("{ - Series}").
// Replacing the separators is what keeps a group literal from injecting an
// extra path level or a traversal segment after the template has been split
// on "/".
func sanitizeInline(s string) string {
	cleaned := pathCharReplacer.Replace(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, cleaned)
}

func sanitizePath(s string) string {
	// Remove characters that are problematic in file paths
	replacer := pathCharReplacer
	cleaned := strings.TrimSpace(replacer.Replace(s))
	// Drop control characters, including the NUL byte: a NUL makes the os.*
	// calls fail with EINVAL (a hard import failure driven by attacker-
	// controlled metadata), and other control bytes are never valid in a name.
	cleaned = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, cleaned)
	// Strip traversal components (".", "..", empties) so a single field can't
	// contribute a segment that walks out of the library root even after
	// character replacement.
	parts := strings.Split(cleaned, string(filepath.Separator))
	kept := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		// Cap each component (rune-safe) so an overlong metadata field can't
		// produce an ENAMETOOLONG that fails the whole import.
		if r := []rune(p); len(r) > maxPathComponentLen {
			p = strings.TrimSpace(string(r[:maxPathComponentLen]))
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, string(filepath.Separator))
}

func authorSortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// DefaultNamingTemplate returns the default naming template for reference.
func DefaultNamingTemplate() string {
	return defaultNamingTemplate
}

// UniqueDir returns dst if nothing exists there; otherwise appends
// " (2)", " (3)", ... until a free path is found. MoveDir refuses an
// existing destination, so callers that import the same title twice
// (duplicate grab, reprocessed history, second edition) resolve the
// collision here before the move rather than failing silently.
func UniqueDir(dst string) string {
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		return dst
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", dst, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return dst
}

// NowYear returns the current year as a string, used as fallback.
func NowYear() string {
	return fmt.Sprintf("%d", time.Now().Year())
}
