package importer

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

// ZIP extraction for usenet downloads (#4131).
//
// Scene ebook releases are packed twice: a zip that holds a rar that holds the
// book. SABnzbd and NZBGet unpack the RAR and 7z they download but leave a zip
// alone, so the job folder holds only that zip. archive/zip is in the standard
// library, so unlike RAR this needs no external unpacker.
//
// The archive is hostile input, so everything here is a guard:
//
//   - it runs only for a SABnzbd or NZBGet download, never for a torrent or a
//     Soulseek folder, and never for a manual import;
//   - the job folder must resolve strictly inside a folder Bindery was
//     configured with (resolveStrictlyInside), so a path the client reports
//     outside them is never written to;
//   - it runs only when the folder holds no importable book file yet;
//   - every write goes through os.Root on the job folder, with a name that has
//     been cleaned and checked to be local, files are created with O_EXCL (an
//     existing file is never overwritten) and a symlink in the folder cannot
//     lead a write out of it;
//   - the whole archive is validated before the first byte is written: entry
//     count, names, declared sizes and the compression ratio;
//   - sizes are enforced again while copying, so a header that lies about its
//     size cannot unpack more than the caps;
//   - one level only: a zip inside the zip is written as a file, never opened,
//     and a marker file in the folder makes a retry skip extraction;
//   - a failed extraction removes everything it created.
const (
	// settingImportExtractZip is the setting key; api.SettingImportExtractZip
	// is the same string. It defaults to on: only an explicit "false" opts out.
	settingImportExtractZip = "import.extract_zip"

	zipMaxEntries  = 2000
	zipMaxArchives = 20
	// Names and paths: an entry name over zipMaxNameBytes, a path deeper than
	// zipMaxDepth, or more than zipMaxDirs folders to create refuses the archive.
	zipMaxNameBytes = 1024
	zipMaxDepth     = 32
	zipMaxDirs      = 2000
	// zipMaxDirBytes bounds the central directory, which Go reads whole into
	// memory before any entry can be counted: an entry record is 46 bytes plus
	// its name, and a generous allowance for extra fields and comments.
	zipMaxDirBytes int64 = zipMaxEntries * (46 + zipMaxNameBytes + 256)
	// zipMaxTotalBytes caps everything one folder's archives unpack to.
	zipMaxTotalBytes int64 = 4 << 30
	// zipMaxEntryBytes caps one entry.
	zipMaxEntryBytes int64 = 2 << 30
	// zipMaxRatio caps unpacked bytes over compressed bytes. Text compresses
	// 10 to 20 times; a zero filled bomb a thousand times.
	zipMaxRatio = 100
	// The floors keep the ratio from refusing a small, very compressible file.
	zipEntryRatioFloor int64 = 1 << 20
	zipTotalRatioFloor int64 = 16 << 20

	zipFileMode = 0o644
	zipDirMode  = 0o755

	// zipExtractedMarker is written once a folder's ZIP files are extracted.
	// It is a download artifact (see downloadArtifactExts): never imported.
	zipExtractedMarker   = ".bindery-extracted-zip"
	zipExtractedMarkerTx = "Bindery extracted the ZIP files of this folder once. Delete this file to let it try again.\n"
)

var (
	errZipTooLarge  = errors.New("it unpacks to more than the size limits allow")
	errZipBlocked   = errors.New("a file is in the way")
	errZipEntryName = errors.New("the entry name is not safe")
	errZipDirectory = errors.New("its directory is larger than the limits allow")
	errZipShape     = errors.New("it holds names or folders beyond the limits")
	// errZipNoBook is an extraction that yielded nothing that looks like a book.
	// What it wrote was removed again.
	errZipNoBook = errors.New("the ZIP holds nothing that looks like a book")
)

// zipOutcome is what an extraction did, for the message the queue shows.
type zipOutcome struct {
	archives  int
	files     int
	encrypted int
	special   int
	existing  int
	// noBook is set when the extraction yielded no book candidate and was
	// rolled back.
	noBook bool
	// done is set when the folder was extracted by an earlier attempt, so
	// nothing was opened this time.
	done bool
}

// note describes the outcome in a sentence or two, "" when there is nothing to
// add.
func (o zipOutcome) note() string {
	var parts []string
	if o.done {
		parts = append(parts, "Bindery already extracted this folder's ZIP files once (or found no book in them) and does not open a ZIP that came out of another.")
	}
	if o.files > 0 {
		parts = append(parts, fmt.Sprintf("Bindery extracted %d file(s) from %d ZIP archive(s).", o.files, o.archives))
	}
	if o.encrypted > 0 {
		parts = append(parts, fmt.Sprintf("%d password protected entry(ies) were skipped, as Bindery cannot open them.", o.encrypted))
	}
	if o.special > 0 {
		parts = append(parts, fmt.Sprintf("%d link or special entry(ies) were skipped.", o.special))
	}
	if o.existing > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) were skipped because a file of that name was already in the folder.", o.existing))
	}
	return strings.Join(parts, " ")
}

// zipBudget is what the folder's archives may still unpack, shared across them.
type zipBudget struct {
	entries int
	bytes   int64
	// dirs is the folders the archives would create, counted once each.
	dirs map[string]bool
}

// addDirs counts the folders an entry needs (every prefix of its parent path,
// and the path itself for a folder entry) and reports false once the folders
// of all the archives pass zipMaxDirs.
func (b *zipBudget) addDirs(rel string, isDir bool) bool {
	if b.dirs == nil {
		b.dirs = map[string]bool{}
	}
	dir := rel
	if !isDir {
		dir = path.Dir(rel)
	}
	if dir == "." {
		return true
	}
	cur := ""
	for seg := range strings.SplitSeq(dir, "/") {
		cur = path.Join(cur, seg)
		b.dirs[cur] = true
		if len(b.dirs) > zipMaxDirs {
			return false
		}
	}
	return true
}

// zipCreated records what an extraction wrote, so a failure can undo it.
type zipCreated struct {
	files []string
	dirs  []string
}

// cleanZipEntryName turns an entry name into a slash separated path relative to
// the folder, or says why it is not safe. A backslash is a separator (a zip
// made on Windows may use one, and on Linux it would otherwise be a file name
// that hides a "..\.."); an absolute name, a drive letter, a NUL and a ".."
// segment are refused. "" with no error is an entry that names the folder
// itself.
func cleanZipEntryName(name string) (string, error) {
	// A zip written by a tool that does not set the UTF-8 flag (the German scene
	// ones, in CP437) carries raw bytes. A name that is not valid UTF-8 would
	// reach the importer, the database and the API as is, so each run of invalid
	// bytes becomes an underscore. Two names that collapse to one are the
	// duplicate case: the first is kept.
	if len(name) > zipMaxNameBytes || strings.ContainsRune(name, 0) {
		return "", errZipEntryName
	}
	name = strings.ToValidUTF8(name, "_")
	// A control character in a name is a terminal escape in a log line and a
	// trap in any shell the user opens the folder in: sanitised like bad UTF-8.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	if name == "" {
		return "", errZipEntryName
	}
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(n, "/") {
		return "", errZipEntryName
	}
	if len(n) >= 2 && n[1] == ':' && n[0]|0x20 >= 'a' && n[0]|0x20 <= 'z' {
		return "", errZipEntryName
	}
	for seg := range strings.SplitSeq(n, "/") {
		if len(seg) > 255 { // no filesystem takes more, and the open would fail late
			return "", errZipEntryName
		}
		if seg == ".." {
			return "", errZipEntryName
		}
	}
	c := path.Clean(n)
	if c == "." {
		return "", nil
	}
	if !filepath.IsLocal(filepath.FromSlash(c)) || strings.Count(c, "/")+1 > zipMaxDepth {
		return "", errZipEntryName
	}
	return c, nil
}

// zipEntryCap is the most one entry may unpack to: the per entry cap, and a
// ratio of the compressed bytes the archive holds for it. The reader hands the
// decompressor no more than the compressed size from the header, so a header
// that lies cannot buy more than this.
func zipEntryCap(compressed uint64) int64 {
	limit := zipMaxEntryBytes
	if compressed < uint64(zipMaxEntryBytes) {
		if byRatio := int64(compressed) * zipMaxRatio; byRatio < limit {
			limit = byRatio
		}
	}
	return max(limit, zipEntryRatioFloor)
}

// zipExtractTimeout bounds one folder's whole extraction, so a zip near the size
// caps cannot hold a download client's poll loop for the length of the copy.
// A variable so a test can shorten it.
var zipExtractTimeout = 10 * time.Minute

// zipBeforeOpenHook, when set by a test, runs between the caller's containment
// check and the open of the folder.
var zipBeforeOpenHook func(folder string)

// zipStopped is the error that ends an extraction whose context is done, in
// words the Queue can show.
func zipStopped(ctx context.Context) error {
	switch err := ctx.Err(); {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("extraction took longer than %s and was stopped", zipExtractTimeout)
	default:
		return fmt.Errorf("extraction was cancelled: %w", err)
	}
}

// ctxReader fails a read once its context is done, so a copy checks for
// cancellation between chunks.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := zipStopped(c.ctx); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// confirmOpenedFolder re-resolves folder after it was opened as root and
// refuses unless it is still a real path (no link has appeared in it), still
// strictly inside one of roots, and the same directory root holds open.
func confirmOpenedFolder(root *os.Root, folder string, roots []string) error {
	again, ok := resolveStrictlyInside(folder, roots)
	if !ok || again != folder {
		return errors.New("the folder changed while it was being opened, so nothing was written")
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	held, err := dir.Stat()
	if err != nil {
		return err
	}
	now, err := os.Stat(again)
	if err != nil || !os.SameFile(held, now) {
		return errors.New("the folder changed while it was being opened, so nothing was written")
	}
	return nil
}

// copyBounded copies src to dst and fails when src holds more than limit bytes.
// The limit is enforced while copying, not read from a header.
func copyBounded(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(ctxReader{ctx: ctx, r: src}, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errZipTooLarge
	}
	return n, nil
}

type zipEntryKind int

const (
	zipEntryFile zipEntryKind = iota
	zipEntryDir
)

// zipPlanEntry is one validated entry that will be written.
type zipPlanEntry struct {
	file *zip.File
	rel  string
	kind zipEntryKind
}

// shortZipName keeps a hostile entry name from filling a log line or a Queue
// message.
func shortZipName(name string) string {
	if len(name) > 80 {
		return strings.ToValidUTF8(name[:80], "_") + "..."
	}
	return name
}

// planZip validates every entry of an open archive without writing anything:
// the count, each name, the declared sizes and the compression ratio. Entries
// that are not extracted (encrypted, links, special files) are counted in out
// and left out of the plan.
func planZip(zr *zip.Reader, archiveSize int64, b *zipBudget, out *zipOutcome) ([]zipPlanEntry, error) {
	if len(zr.File) > b.entries {
		return nil, fmt.Errorf("it holds more than %d entries", zipMaxEntries)
	}
	b.entries -= len(zr.File)
	plan := make([]zipPlanEntry, 0, len(zr.File))
	var declared int64
	for _, f := range zr.File {
		rel, err := cleanZipEntryName(f.Name)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", shortZipName(f.Name), err)
		}
		mode := f.Mode()
		switch {
		case f.Flags&0x1 != 0:
			out.encrypted++
			continue
		case mode&(fs.ModeSymlink|fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0:
			out.special++
			continue
		case rel == "":
			continue
		case mode.IsDir() || strings.HasSuffix(strings.ReplaceAll(f.Name, `\`, "/"), "/"):
			if !b.addDirs(rel, true) {
				return nil, fmt.Errorf("it would create more than %d folders: %w", zipMaxDirs, errZipShape)
			}
			plan = append(plan, zipPlanEntry{file: f, rel: rel, kind: zipEntryDir})
			continue
		case rel == zipExtractedMarker:
			out.special++
			continue
		}
		if f.UncompressedSize64 > uint64(zipEntryCap(f.CompressedSize64)) { //nolint:gosec // the cap is positive (at least the 1 MiB floor)
			return nil, fmt.Errorf("entry %q: %w", shortZipName(f.Name), errZipTooLarge)
		}
		if !b.addDirs(rel, false) {
			return nil, fmt.Errorf("it would create more than %d folders: %w", zipMaxDirs, errZipShape)
		}
		declared += int64(f.UncompressedSize64) //nolint:gosec // just checked against the entry cap, which is an int64
		if declared > b.bytes {
			return nil, errZipTooLarge
		}
		plan = append(plan, zipPlanEntry{file: f, rel: rel, kind: zipEntryFile})
	}
	if budget := max(archiveSize*zipMaxRatio, zipTotalRatioFloor); declared > budget {
		return nil, errZipTooLarge
	}
	return plan, nil
}

// mkdirTracked creates dir and the folders above it that do not exist, inside
// root, and records the ones it made. A file or link where a folder is needed
// is errZipBlocked.
func mkdirTracked(ctx context.Context, root *os.Root, dir string, created *zipCreated) error {
	cur := ""
	for seg := range strings.SplitSeq(dir, "/") {
		if err := zipStopped(ctx); err != nil {
			return err
		}
		cur = path.Join(cur, seg)
		local := filepath.FromSlash(cur)
		fi, err := root.Lstat(local)
		switch {
		case err == nil:
			if !fi.IsDir() {
				return errZipBlocked
			}
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(local, zipDirMode); err != nil {
				return err
			}
			created.dirs = append(created.dirs, cur)
		default:
			return err
		}
	}
	return nil
}

// writeZipEntry writes one entry below root with O_EXCL, so a file that is
// already there is skipped, never overwritten. It reports whether it wrote.
func writeZipEntry(ctx context.Context, root *os.Root, e zipPlanEntry, b *zipBudget, created *zipCreated) (bool, error) {
	if dir := path.Dir(e.rel); dir != "." {
		if err := mkdirTracked(ctx, root, dir, created); err != nil {
			if errors.Is(err, errZipBlocked) {
				return false, nil
			}
			return false, err
		}
	}
	dst, err := root.OpenFile(filepath.FromSlash(e.rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, zipFileMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	created.files = append(created.files, e.rel)
	src, err := e.file.Open()
	if err != nil {
		_ = dst.Close()
		return false, err
	}
	limit := min(zipEntryCap(e.file.CompressedSize64), b.bytes)
	n, err := copyBounded(ctx, dst, src, limit)
	_ = src.Close()
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	b.bytes -= n
	if err != nil {
		return false, err
	}
	return true, nil
}

// rollbackZip removes what an extraction created: files first, then the
// folders it made, deepest first, each with a plain remove through root.
func rollbackZip(root *os.Root, created *zipCreated) {
	for _, f := range created.files {
		_ = root.Remove(filepath.FromSlash(f))
	}
	dirs := append([]string(nil), created.dirs...)
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = root.Remove(filepath.FromSlash(d))
	}
}

// checkZipDirectory refuses an archive whose central directory is larger than
// zipMaxDirBytes, before archive/zip reads it. The reader keeps a record for
// every entry in memory before any count can be checked, and the entry count in
// the end of central directory record cannot be trusted (it is 16 bits, and the
// reader compares it modulo 65536), so the directory's size in bytes is what is
// bounded. It reads the end of central directory record, and the ZIP64 one when
// the sizes are the "look elsewhere" markers. A file with no such record is left
// for the reader to refuse.
func checkZipDirectory(f io.ReaderAt, size int64) error {
	const eocdLen = 22
	tailLen := min(size, 65535+eocdLen)
	tail := make([]byte, tailLen)
	if _, err := f.ReadAt(tail, size-tailLen); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	pos := -1
	for i := len(tail) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 {
			pos = i
			break
		}
	}
	if pos < 0 {
		return nil
	}
	rec := tail[pos:]
	dirSize := int64(binary.LittleEndian.Uint32(rec[12:]))
	entries := int64(binary.LittleEndian.Uint16(rec[10:]))
	if dirSize == 0xFFFFFFFF || entries == 0xFFFF || binary.LittleEndian.Uint32(rec[16:]) == 0xFFFFFFFF {
		// ZIP64: the locator sits just before the end record.
		locAt := size - tailLen + int64(pos) - 20
		if locAt < 0 {
			return errZipDirectory
		}
		loc := make([]byte, 20)
		if _, err := f.ReadAt(loc, locAt); err != nil || binary.LittleEndian.Uint32(loc) != 0x07064b50 {
			return errZipDirectory
		}
		off := binary.LittleEndian.Uint64(loc[8:])
		if off > uint64(size) { //nolint:gosec // size is a file size, never negative
			return errZipDirectory
		}
		z64 := make([]byte, 56)
		if _, err := f.ReadAt(z64, int64(off)); err != nil || binary.LittleEndian.Uint32(z64) != 0x06064b50 { //nolint:gosec // checked above
			return errZipDirectory
		}
		n := binary.LittleEndian.Uint64(z64[32:])
		sz := binary.LittleEndian.Uint64(z64[40:])
		if n > zipMaxEntries || sz > uint64(zipMaxDirBytes) {
			return errZipDirectory
		}
		return nil
	}
	if dirSize > zipMaxDirBytes {
		return errZipDirectory
	}
	return nil
}

// zipReadSlack is what the reader may read beyond the directory limit: the end
// of central directory search reads up to 64 KiB twice, and the ZIP64 records
// are small.
const zipReadSlack = 256 << 10

// boundedReaderAt serves at most left bytes in all, then reports EOF and
// remembers that it did.
type boundedReaderAt struct {
	r       io.ReaderAt
	left    int64
	clipped bool
}

func (b *boundedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if b.left <= 0 {
		b.clipped = true
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		n, err := b.r.ReadAt(p[:b.left], off)
		b.left -= int64(n)
		b.clipped = true
		if err == nil {
			err = io.EOF
		}
		return n, err
	}
	n, err := b.r.ReadAt(p, off)
	b.left -= int64(n)
	return n, err
}

// topLevelZips lists the regular ".zip" files directly inside the folder, by
// name. Links are not followed.
func topLevelZips(folder string) []string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// zipArchive is one opened zip with its validated plan.
type zipArchive struct {
	name string
	f    *os.File
	plan []zipPlanEntry
}

// extractZipsInFolder unpacks the ".zip" files at the top of folder, which the
// caller has already confirmed is inside a download folder. It is all or
// nothing: on an error everything it wrote is removed. The zips themselves are
// kept. A folder that was extracted once is not extracted again, which is what
// stops a zip that came out of a zip from being opened on the next attempt.
func extractZipsInFolder(ctx context.Context, folder string, roots []string) (zipOutcome, error) {
	return extractZips(ctx, folder, roots, nil)
}

// extractZips is extractZipsInFolder with a check run once everything is
// written: when hasCandidate says the folder holds nothing that looks like a
// book, the extraction is rolled back (the marker is still written, so a retry
// opens nothing) and the outcome says so.
func extractZips(ctx context.Context, folder string, roots []string, hasCandidate func() bool) (zipOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, zipExtractTimeout)
	defer cancel()
	var out zipOutcome
	names := topLevelZips(folder)
	if len(names) == 0 {
		return out, nil
	}
	if len(names) > zipMaxArchives {
		return out, fmt.Errorf("it holds more than %d ZIP files", zipMaxArchives)
	}
	if zipBeforeOpenHook != nil {
		zipBeforeOpenHook(folder)
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return out, err
	}
	defer func() { _ = root.Close() }()
	// The folder was resolved and checked by the caller, but a path can be
	// swapped for a link between that check and this open. Resolve it again and
	// require it to be the same real folder, still inside a root, and the very
	// directory the handle points at; otherwise write nothing.
	if err := confirmOpenedFolder(root, folder, roots); err != nil {
		return out, err
	}
	if _, err := root.Lstat(zipExtractedMarker); err == nil {
		out.done = true
		return out, nil
	}

	b := &zipBudget{entries: zipMaxEntries, bytes: zipMaxTotalBytes}
	var archives []*zipArchive
	defer func() {
		for _, a := range archives {
			_ = a.f.Close()
		}
	}()
	for _, name := range names {
		if err := zipStopped(ctx); err != nil {
			return out, err
		}
		f, err := root.Open(name)
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		a := &zipArchive{name: name, f: f}
		archives = append(archives, a)
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return out, fmt.Errorf("%s is not a regular file", name)
		}
		if err := checkZipDirectory(f, fi.Size()); err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		// archive/zip reads the central directory from its offset until the
		// records stop, whatever size the end record declares, so the size check
		// above is only an early refusal. The reader below is the bound: it serves
		// no more than the directory limit plus the end records, and a read it had
		// to cut short refuses the archive.
		bound := &boundedReaderAt{r: f, left: zipMaxDirBytes + zipReadSlack}
		zr, err := zip.NewReader(bound, fi.Size())
		if bound.clipped {
			return out, fmt.Errorf("%s: %w", name, errZipDirectory)
		}
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		bound.left = math.MaxInt64 // entry data is read through it later
		if a.plan, err = planZip(zr, fi.Size(), b, &out); err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
	}

	var created zipCreated
	for _, a := range archives {
		for _, e := range a.plan {
			if err := zipStopped(ctx); err != nil {
				rollbackZip(root, &created)
				return zipOutcome{}, err
			}
			switch e.kind {
			case zipEntryDir:
				if err := mkdirTracked(ctx, root, e.rel, &created); err != nil && !errors.Is(err, errZipBlocked) {
					rollbackZip(root, &created)
					return zipOutcome{}, fmt.Errorf("%s: %w", a.name, err)
				}
			case zipEntryFile:
				wrote, err := writeZipEntry(ctx, root, e, b, &created)
				if err != nil {
					rollbackZip(root, &created)
					return zipOutcome{}, fmt.Errorf("%s: entry %q: %w", a.name, shortZipName(e.file.Name), err)
				}
				if wrote {
					out.files++
				} else {
					out.existing++
				}
			}
		}
		out.archives++
	}
	if hasCandidate != nil && out.files > 0 && !hasCandidate() {
		rollbackZip(root, &created)
		out.noBook = true
		out.files = 0
	}
	if m, err := root.OpenFile(zipExtractedMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, zipFileMode); err == nil {
		_, _ = m.WriteString(zipExtractedMarkerTx)
		_ = m.Close()
	}
	return out, nil
}

// zipExtractionEnabled reads import.extract_zip. Unset means on.
func (s *Scanner) zipExtractionEnabled(ctx context.Context) bool {
	if s.settings == nil {
		return true
	}
	v, err := s.settings.Get(ctx, settingImportExtractZip)
	if err != nil || v == nil {
		return true
	}
	return strings.TrimSpace(v.Value) != "false"
}

type zipRootsKey struct{}

// withZipExtractRoots attaches the folders an import may extract inside to its
// context. Only the SABnzbd and NZBGet pollers set it, so a download that came
// any other way has no roots and is never extracted.
func withZipExtractRoots(ctx context.Context, roots []string) context.Context {
	return context.WithValue(ctx, zipRootsKey{}, roots)
}

func zipExtractRootsFrom(ctx context.Context) []string {
	r, _ := ctx.Value(zipRootsKey{}).([]string)
	return r
}

// usenetExtractRoots is every folder Bindery was told its usenet jobs finish
// in: BINDERY_DOWNLOAD_DIR, BINDERY_AUDIOBOOK_DOWNLOAD_DIR, and the Bindery
// side of the client's PathRemap and of BINDERY_DOWNLOAD_PATH_REMAP. The path
// a client reports is deliberately not a root: it is the very thing being
// checked.
func (s *Scanner) usenetExtractRoots(client *models.DownloadClient) []string {
	roots := []string{s.downloadDir, s.audiobookDownloadDir}
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		roots = append(roots, pathmap.Parse(client.PathRemap).Targets()...)
	}
	return append(roots, s.remapper.Targets()...)
}

// resolveStrictlyInside returns the real location of folder when it is a
// directory below (never equal to) the real location of one of roots. Both
// sides are resolved through symlinks first. A root that is a filesystem root
// is not accepted, as everything is inside it.
func resolveStrictlyInside(folder string, roots []string) (string, bool) {
	realFolder, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", false
	}
	// The containment check is filepath.Rel plus filepath.IsLocal on the value
	// that is then joined onto the root and returned, the guard shape CodeQL's
	// go/path-injection query credits (see WriteOPFSidecarFile). Nothing reads
	// or stats the folder before it has passed.
	for _, r := range roots {
		if strings.TrimSpace(r) == "" || !filepath.IsAbs(r) {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(r)
		if err != nil || realRoot == filepath.Dir(realRoot) {
			continue
		}
		rel, err := filepath.Rel(realRoot, realFolder)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			continue
		}
		target := filepath.Join(realRoot, rel)
		if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
			return "", false
		}
		return target, true
	}
	return "", false
}

// acceptableZipRoots drops every root that is too broad for "inside the
// download folder" to mean anything, the way movePruneRoot's guard refuses a
// folder: a filesystem root, the user's home, and any folder that equals or
// contains a library folder or a root folder. A remap target such as "/" or
// "/data" would otherwise let any path through. Roots that cannot be resolved
// are dropped too.
func (s *Scanner) acceptableZipRoots(ctx context.Context, roots []string) []string {
	protected := []string{s.libraryDir, s.audiobookDir}
	if home, err := os.UserHomeDir(); err == nil {
		protected = append(protected, home)
	}
	var homeReal string
	if home, err := os.UserHomeDir(); err == nil {
		homeReal, _ = filepath.EvalSymlinks(home)
	}
	if s.rootFolders != nil {
		rfs, err := s.rootFolders.List(ctx)
		if err != nil {
			return nil // unknown library folders: refuse rather than guess
		}
		for _, rf := range rfs {
			protected = append(protected, rf.Path)
		}
	}
	var realProtected []string
	for _, p := range protected {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			realProtected = append(realProtected, rp)
		} else {
			realProtected = append(realProtected, filepath.Clean(p))
		}
	}
	var out []string
	for _, r := range roots {
		if strings.TrimSpace(r) == "" || !filepath.IsAbs(r) {
			continue
		}
		real, err := filepath.EvalSymlinks(r)
		if err != nil || real == filepath.Dir(real) || (homeReal != "" && real == homeReal) {
			continue
		}
		broad := false
		for _, p := range realProtected {
			// p is the root, or inside it.
			if rel, err := filepath.Rel(real, p); err == nil && relInside(rel) {
				broad = true
				break
			}
		}
		if !broad {
			out = append(out, r)
		}
	}
	return out
}

// countArchiveParts counts the files under folder that are archive volumes.
func countArchiveParts(folder string) int {
	n := 0
	_ = filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && isArchivePart(d.Name()) {
			n++
		}
		return nil
	})
	return n
}

// unpackZipForImport runs the ZIP extraction for a download that found no book
// file, when every guard allows it. extracted reports that files were written.
// note explains, for the "waiting" message, why a ZIP is still there when
// nothing was extracted; guard is set when the note is such a refusal, which
// only means something for a ZIP. A non nil err is a failed extraction that
// left the folder as it was. The folder is not listed until it has passed the
// containment check.
func (s *Scanner) unpackZipForImport(ctx context.Context, dl *models.Download, downloadPath, clientType string, explicitFiles []string) (extracted bool, note string, guard bool, err error) {
	switch {
	case !s.zipExtractionEnabled(ctx):
		return false, "Bindery's ZIP extraction is switched off (Settings, File Naming, Extract ZIP archives of usenet downloads).", true, nil
	case (clientType != "sabnzbd" && clientType != "nzbget") || dl == nil || dl.Protocol == "torrent" || dl.Protocol == "soulseek" || isManualImport(ctx) || len(explicitFiles) > 0:
		return false, "Bindery extracts ZIP files only for usenet downloads.", true, nil
	}
	// The pollers attach the client's own folders; any other caller gets the
	// scanner's configured ones. Roots too broad to mean anything are dropped.
	roots := zipExtractRootsFrom(ctx)
	if len(roots) == 0 {
		roots = s.usenetExtractRoots(nil)
	}
	roots = s.acceptableZipRoots(ctx, roots)
	if len(roots) == 0 {
		return false, "Bindery extracts ZIP files only inside its download folders, and it has none it can use: set BINDERY_DOWNLOAD_DIR (or a path remap that is not a library folder, your home or /), then use Retry import once the client has been polled.", true, nil
	}
	folder, ok := resolveStrictlyInside(downloadPath, roots)
	if !ok {
		return false, "Bindery extracts ZIP files only inside its download folders (BINDERY_DOWNLOAD_DIR, BINDERY_AUDIOBOOK_DOWNLOAD_DIR and the download client's path remap), and this one is outside them, so it was not touched.", true, nil
	}
	if len(topLevelZips(folder)) == 0 {
		return false, "", false, nil
	}
	// What the folder held before: a notes file or a tiny file is not a book, and
	// a zip that yields nothing more than that is rolled back.
	// An archive that came out of the zip (a rar, a nested zip) is a candidate
	// too: it keeps waiting for an unpacker, as before.
	_, smallBefore := discoverBookFilesSized(folder, nil, true)
	archivesBefore := countArchiveParts(folder)
	hasCandidate := func() bool {
		books, small := discoverBookFilesSized(folder, nil, true)
		return len(books) > 0 || len(small) > len(smallBefore) || countArchiveParts(folder) > archivesBefore
	}
	out, err := extractZips(ctx, folder, roots, hasCandidate)
	if err != nil {
		return false, "", false, err
	}
	if out.noBook {
		slog.Info("a ZIP of a usenet download held nothing that looks like a book; extraction undone", "path", folder)
		return false, "", false, errZipNoBook
	}
	slog.Info("extracted ZIP files of a usenet download", "path", folder, "files", out.files, "archives", out.archives,
		"encrypted", out.encrypted, "special", out.special, "existing", out.existing)
	return out.files > 0, out.note(), false, nil
}
