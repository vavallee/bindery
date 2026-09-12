package importer

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/models"
)

// opfSidecarFileName is the Calibre convention this mirrors: one
// metadata.opf per book folder. Unambiguous because Bindery gives every
// book its own folder — multiple files of the SAME book (formats of one
// edition, or a merged ebook+audiobook pair) can share a folder, but two
// different books never do (see DestPath/AudiobookDestDir).
const opfSidecarFileName = "metadata.opf"

// opfSidecarEnabled reports whether the opt-in "write a metadata.opf
// sidecar" feature is on (import.write_opf_sidecar). Off by default: unlike
// renaming, this is the first time Bindery writes a *new* file into a
// library folder rather than just moving/renaming the download, so it opts
// in rather than opting out. Read as a string literal to avoid an import
// cycle with the api package; keep in sync with
// api.SettingImportWriteOPFSidecar.
func (s *Scanner) opfSidecarEnabled(ctx context.Context) bool {
	if s.settings == nil {
		return false
	}
	setting, err := s.settings.Get(ctx, "import.write_opf_sidecar")
	if err != nil || setting == nil {
		return false
	}
	return setting.Value == "true"
}

// writeOPFSidecar writes (or overwrites) metadata.opf in dir when the
// setting is on. Best-effort, mirroring pushToCWA/pushToCalibre: a sidecar
// failure is logged and swallowed rather than failing an otherwise-good
// import or reorganize move. dir is the book's folder — the caller passes
// filepath.Dir(destPath) for a single ebook file or the audiobook
// destination directory directly. roots is the set of configured library
// roots dir must resolve inside (see dirWithinRoots) — every caller already
// derives dir from Renamer.DestPath/AudiobookDestDir, which sanitize and
// contain it before this is ever reached, but this function does not trust
// that: it re-verifies independently rather than assuming every future
// caller gets that right.
func (s *Scanner) writeOPFSidecar(ctx context.Context, dir string, roots []string, book *models.Book, author *models.Author, edition *models.Edition, seriesTitle, seriesNum string) {
	if !s.opfSidecarEnabled(ctx) || book == nil || dir == "" {
		return
	}
	if err := WriteOPFSidecarFile(dir, roots, book, author, edition, seriesTitle, seriesNum); err != nil {
		slog.Warn("opf sidecar: write failed, continuing", "bookID", book.ID, "dir", dir, "error", err)
		return
	}
	slog.Info("opf sidecar: metadata.opf written", "bookID", book.ID, "dir", dir)
}

// dirWithinRoots reports whether dir resolves inside at least one of roots,
// reusing ensureContained's same prefix-after-Clean check that
// Renamer.DestPath/AudiobookDestDir already apply. WriteOPFSidecarFile calls
// this itself, independent of any sanitization its caller already did, so a
// book/author string sourced from remote provider metadata can never steer
// a library-folder-derived path outside the configured roots — this is the
// direct guard for what a path-injection static analysis (rightly) cannot
// verify by tracing through the renamer's own containment check several
// calls upstream.
func dirWithinRoots(dir string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, err := ensureContained(dir, root); err == nil {
			return true
		}
	}
	return false
}

// WriteOPFSidecarFile renders book/author/edition metadata as a
// Calibre-style OPF package document and writes it to
// filepath.Join(dir, "metadata.opf"), creating dir if needed. roots is the
// set of acceptable library roots dir must resolve inside (see
// dirWithinRoots); a dir outside every root is refused rather than written.
func WriteOPFSidecarFile(dir string, roots []string, book *models.Book, author *models.Author, edition *models.Edition, seriesTitle, seriesNum string) error {
	if !dirWithinRoots(dir, roots) {
		return fmt.Errorf("opfsidecar: refusing to write outside configured library roots: %q", dir)
	}
	xmlBytes, err := BuildOPFXML(book, author, edition, seriesTitle, seriesNum)
	if err != nil {
		return fmt.Errorf("opfsidecar: build xml: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("opfsidecar: create dir %q: %w", dir, err)
	}
	dest := filepath.Join(dir, opfSidecarFileName)
	// Staged write, not os.WriteFile: WriteFile truncates first, so a crash
	// (or an unlucky reader) mid-write leaves a zero-length or half-written
	// metadata.opf, which is strictly worse than no sidecar — a library app
	// parses it as a corrupt book instead of falling back to the filename.
	// This is also the overwrite path (Reorganize rewrites an existing
	// sidecar), so the truncate window is not a one-off. Same temp+rename
	// shape as calibre.MaterializeCover; the rename is atomic because the temp
	// file is created in the destination directory.
	tmp, err := os.CreateTemp(dir, ".metadata.opf-*")
	if err != nil {
		return fmt.Errorf("opfsidecar: create temp in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(xmlBytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("opfsidecar: write %q: %w", tmpName, err)
	}
	// CreateTemp makes the file 0600; the sidecar wants the same 0644 the
	// library files it sits beside get, so it is readable by whatever reads
	// the book itself.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("opfsidecar: chmod %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("opfsidecar: close %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("opfsidecar: rename into %q: %w", dest, err)
	}
	return nil
}

// removeOrphanedSidecar deletes a metadata.opf left alone in oldDir after the
// book file it described moved out (Reorganize). Without it a reorganize that
// renames a book's folder strands the old folder permanently: the sidecar
// keeps the folder non-empty, so pruneEmptyParents' os.Remove fails with
// ENOTEMPTY, and the library accumulates one orphan folder per rename, each
// holding an out-of-date copy of a book's metadata that a sidecar-reading
// library app will happily index as a real book.
//
// Deliberately NOT gated on import.write_opf_sidecar: turning the setting off
// does not retract the sidecars already on disk, and those strand folders just
// the same. The guard is instead that the sidecar must be the SOLE remaining
// entry. A folder still holding another format of the book — or anything else
// at all — is left completely alone, sidecar included; a folder holding
// nothing but a metadata.opf is one pruneEmptyParents was going to reclaim
// anyway. Best-effort: a failure only leaves the folder behind, which is
// exactly the prior behaviour.
func removeOrphanedSidecar(oldDir, newDir string) {
	if oldDir == "" || filepath.Clean(oldDir) == filepath.Clean(newDir) {
		return
	}
	RemoveOrphanedSidecar(oldDir)
}

// RemoveOrphanedSidecar deletes metadata.opf from dir when it is the SOLE
// remaining entry there, so a caller that just removed a book's last file
// (a plain delete, not only Reorganize's move) can reclaim the folder
// afterward instead of stranding it holding nothing but a stale sidecar — the
// same failure shape as removeOrphanedSidecar's move case, reached from
// internal/api/books.go's delete paths instead. A folder still holding
// another format of the book, or anything else at all, is left completely
// alone, sidecar included. Best-effort: a failure only leaves the folder
// behind, which is no worse than before this existed.
func RemoveOrphanedSidecar(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		return
	}
	if entries[0].IsDir() || entries[0].Name() != opfSidecarFileName {
		return
	}
	if err := os.Remove(filepath.Join(dir, opfSidecarFileName)); err != nil {
		slog.Warn("opf sidecar: failed to remove the orphaned sidecar", "dir", dir, "error", err)
	}
}

// BuildOPFXML renders book/author/edition metadata as a Calibre-style OPF
// package document (the same "content.opf" schema an EPUB carries inside
// its own zip, minus the file-manifest/spine sections that only make sense
// for an actual archive — see ReadEpubMetadata's read-side counterpart).
//
// Deliberately built by hand rather than via encoding/xml struct tags:
// namespaced attributes (opf:role, opf:file-as, opf:scheme) are awkward to
// get byte-stable through Go's XML marshaller, and parseOPFMetadata already
// established that this codebase treats OPF as "walk/write the tokens
// directly" rather than "bind a fixed struct" for exactly that reason.
//
// Reuses the same field-resolution helpers as the Calibre push integration
// (calibre.IdentifiersForBook, calibre.NormalizeLanguageForCalibre,
// calibre.FormatPublishedDate) so a book's metadata.opf and its
// calibredb-pushed metadata never disagree. One deliberate divergence:
// Book.AverageRating is left out of calibre:rating here, unlike
// calibreMetadata. Calibre's rating field is a personal 1-5 star rating;
// AverageRating is a public/aggregate score pulled from a provider, and
// writing it under a tag readers expect to mean "your rating" would be
// misleading. The Calibre push integration made the opposite call for its
// own (pre-existing, out of scope here) reasons — this does not change that.
//
// No cover reference is written: Bindery never places a cover image file in
// the library folder (covers are proxied/cached separately), so there is
// nothing on disk for a <meta name="cover"> to point at.
func BuildOPFXML(book *models.Book, author *models.Author, edition *models.Edition, seriesTitle, seriesNum string) ([]byte, error) {
	if book == nil {
		return nil, fmt.Errorf("opfsidecar: nil book")
	}

	var uniqueID string
	var meta bytes.Buffer

	fmt.Fprintf(&meta, "    <dc:title>%s</dc:title>\n", opfEscape(book.Title))
	if strings.TrimSpace(book.SortTitle) != "" {
		fmt.Fprintf(&meta, "    <meta name=\"calibre:title_sort\" content=%s/>\n", opfAttr(book.SortTitle))
	}

	if author != nil && strings.TrimSpace(author.Name) != "" {
		if fileAs := strings.TrimSpace(author.SortName); fileAs != "" {
			fmt.Fprintf(&meta, "    <dc:creator opf:role=\"aut\" opf:file-as=%s>%s</dc:creator>\n", opfAttr(fileAs), opfEscape(author.Name))
		} else {
			fmt.Fprintf(&meta, "    <dc:creator opf:role=\"aut\">%s</dc:creator>\n", opfEscape(author.Name))
		}
	}

	if narrator := strings.TrimSpace(book.Narrator); narrator != "" {
		fmt.Fprintf(&meta, "    <dc:contributor opf:role=\"nrt\">%s</dc:contributor>\n", opfEscape(narrator))
	}

	identifiers := calibre.IdentifiersForBook(book, edition)
	keys := make([]string, 0, len(identifiers))
	for k := range identifiers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		scheme := opfIdentifierScheme(k)
		if k == "bindery" {
			uniqueID = "bindery-id"
			fmt.Fprintf(&meta, "    <dc:identifier id=%s opf:scheme=%s>%s</dc:identifier>\n", opfAttr(uniqueID), opfAttr(scheme), opfEscape(identifiers[k]))
			continue
		}
		fmt.Fprintf(&meta, "    <dc:identifier opf:scheme=%s>%s</dc:identifier>\n", opfAttr(scheme), opfEscape(identifiers[k]))
	}

	language := book.Language
	if edition != nil && strings.TrimSpace(edition.Language) != "" {
		language = edition.Language
	}
	if language = calibre.NormalizeLanguageForCalibre(language); language != "" {
		fmt.Fprintf(&meta, "    <dc:language>%s</dc:language>\n", opfEscape(language))
	}

	if edition != nil && strings.TrimSpace(edition.Publisher) != "" {
		fmt.Fprintf(&meta, "    <dc:publisher>%s</dc:publisher>\n", opfEscape(edition.Publisher))
	}

	date := ""
	if edition != nil && edition.PublishDate != nil {
		date = calibre.FormatPublishedDate(edition.PublishDate)
	} else if book.ReleaseDate != nil {
		date = calibre.FormatPublishedDate(book.ReleaseDate)
	}
	if date != "" {
		fmt.Fprintf(&meta, "    <dc:date>%s</dc:date>\n", opfEscape(date))
	}

	if strings.TrimSpace(book.Description) != "" {
		fmt.Fprintf(&meta, "    <dc:description>%s</dc:description>\n", opfEscape(book.Description))
	}

	for _, genre := range book.Genres {
		if strings.TrimSpace(genre) == "" {
			continue
		}
		fmt.Fprintf(&meta, "    <dc:subject>%s</dc:subject>\n", opfEscape(genre))
	}

	if strings.TrimSpace(seriesTitle) != "" {
		fmt.Fprintf(&meta, "    <meta name=\"calibre:series\" content=%s/>\n", opfAttr(seriesTitle))
		if strings.TrimSpace(seriesNum) != "" {
			fmt.Fprintf(&meta, "    <meta name=\"calibre:series_index\" content=%s/>\n", opfAttr(seriesNum))
		}
	}

	var doc bytes.Buffer
	doc.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	if uniqueID != "" {
		fmt.Fprintf(&doc, "<package xmlns=\"http://www.idpf.org/2007/opf\" unique-identifier=%s version=\"2.0\">\n", opfAttr(uniqueID))
	} else {
		doc.WriteString("<package xmlns=\"http://www.idpf.org/2007/opf\" version=\"2.0\">\n")
	}
	doc.WriteString("  <metadata xmlns:dc=\"http://purl.org/dc/elements/1.1/\" xmlns:opf=\"http://www.idpf.org/2007/opf\">\n")
	doc.Write(meta.Bytes())
	doc.WriteString("  </metadata>\n")
	doc.WriteString("</package>\n")
	return doc.Bytes(), nil
}

// opfIdentifierScheme maps a calibre.IdentifiersForBook key to the
// opf:scheme value real Calibre metadata.opf files use for it, falling back
// to an uppercased form of the key (underscores to hyphens, matching
// Calibre's own multi-word scheme style like "MOBI-ASIN") for anything not
// specifically known.
func opfIdentifierScheme(key string) string {
	switch key {
	case "bindery":
		return "BINDERY"
	case "isbn":
		return "ISBN"
	case "asin":
		return "MOBI-ASIN"
	default:
		return strings.ToUpper(strings.ReplaceAll(key, "_", "-"))
	}
}

// opfEscape XML-escapes text content (dc:title, dc:description, …).
func opfEscape(s string) string {
	var buf bytes.Buffer
	// xml.EscapeText cannot fail on a bytes.Buffer target.
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// opfAttr renders s as a double-quoted, escaped XML attribute value
// (including the quotes), so callers can drop it straight after "=" without
// juggling quote characters themselves.
func opfAttr(s string) string {
	return "\"" + opfEscape(s) + "\""
}
