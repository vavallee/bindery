package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Per format import routing (#1632). The reported setup is Calibre-Web-
// Automated for ebooks plus Audiobookshelf for audiobooks: ebooks must be
// handed to CWA's ingest folder (import.mode=external + import.drop_folder)
// while audiobooks land in the audiobook root where ABS scans them. Before
// import.audiobook.mode existed, external mode sent both formats into the one
// drop folder and CWA filed the m4b into the Calibre library.

type routingFixture struct {
	s          *Scanner
	settings   *db.SettingsRepo
	downloads  *db.DownloadRepo
	books      *db.BookRepo
	book       *models.Book
	libraryDir string
	abDir      string
	dropDir    string
	abDropDir  string
	abs        *fakeABSNotifier
	ctx        context.Context
}

// newRoutingFixture builds a scanner with distinct ebook library, audiobook
// library, ebook drop and audiobook drop directories, so every assertion can
// say exactly where a format went, and an ABS notifier that records scans.
func newRoutingFixture(t *testing.T, mediaType string, kv map[string]string) *routingFixture {
	t.Helper()
	s, settings, downloads, books, libraryDir, dropDir, book, ctx := dropFixture(t, mediaType)
	f := &routingFixture{
		s: s, settings: settings, downloads: downloads, books: books, book: book,
		libraryDir: libraryDir, abDir: t.TempDir(), dropDir: dropDir, abDropDir: t.TempDir(),
		abs: &fakeABSNotifier{}, ctx: ctx,
	}
	s.audiobookDir = f.abDir
	s.WithABSNotifier(f.abs, func() []string { return []string{"lib-audio"} })
	for k, v := range kv {
		v = expandRoutingDirs(f, v)
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	return f
}

// expandRoutingDirs lets a test name the fixture's temp dirs symbolically.
func expandRoutingDirs(f *routingFixture, v string) string {
	switch v {
	case "$DROP":
		return f.dropDir
	case "$ABDROP":
		return f.abDropDir
	}
	return v
}

func (f *routingFixture) importFormat(t *testing.T, format string) (*models.Download, string) {
	t.Helper()
	dl := newPairDownload(t, f.downloads, f.book, format, f.ctx)
	var dir string
	if format == models.MediaTypeAudiobook {
		dir = writeAudiobookDownload(t)
	} else {
		dir = writeEbookDownload(t)
	}
	f.s.tryImportInternal(f.ctx, dl, dir, "", "", "", nil, nil)
	return dl, dir
}

// filesUnder lists the regular files under root, relative to it.
func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func assertHasFileNamed(t *testing.T, root, base, what string) {
	t.Helper()
	for _, rel := range filesUnder(t, root) {
		if filepath.Base(rel) == base {
			return
		}
	}
	t.Errorf("%s: no %s under %s; found %v", what, base, root, filesUnder(t, root))
}

func assertNoFiles(t *testing.T, root, what string) {
	t.Helper()
	if got := filesUnder(t, root); len(got) != 0 {
		t.Errorf("%s should be empty, found %v", what, got)
	}
}

// Default unchanged: with no audiobook override, external mode drops both
// formats into the one drop folder exactly as before, and nothing lands in
// either library.
func TestAudiobookImportMode_DefaultFollowsImportMode(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":        "external",
		"import.drop_folder": "$DROP",
	})

	ebookDL, _ := f.importFormat(t, models.MediaTypeEbook)
	abDL, _ := f.importFormat(t, models.MediaTypeAudiobook)

	assertHasFileNamed(t, f.dropDir, "Title T - Author A.epub", "drop folder")
	assertHasFileNamed(t, f.dropDir, "audiobook.m4b", "drop folder")
	assertNoFiles(t, f.abDropDir, "audiobook drop folder")
	assertNoFiles(t, f.libraryDir, "ebook library")
	assertNoFiles(t, f.abDir, "audiobook library")
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImportExternal)
	assertStatus(t, f.downloads, f.ctx, abDL.GUID, models.StateImportExternal)
}

// The issue's CWA + Audiobookshelf setup: ebooks external into the ingest
// folder, audiobooks copied into the audiobook root with an ABS scan after.
func TestAudiobookImportMode_EbookExternalAudiobookCopy(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":           "external",
		"import.drop_folder":    "$DROP",
		"import.audiobook.mode": "copy",
	})

	ebookDL, _ := f.importFormat(t, models.MediaTypeEbook)
	abDL, abSrc := f.importFormat(t, models.MediaTypeAudiobook)

	// Ebook: handed to the ingest folder, never placed in the library.
	assertHasFileNamed(t, f.dropDir, "Title T - Author A.epub", "drop folder")
	assertNoFiles(t, f.libraryDir, "ebook library")
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImportExternal)

	// Audiobook: copied into the audiobook root, not into the ingest folder.
	for _, rel := range filesUnder(t, f.dropDir) {
		if filepath.Ext(rel) == ".m4b" {
			t.Errorf("audiobook was dropped into the ebook ingest folder at %s", rel)
		}
	}
	assertHasFileNamed(t, f.abDir, "audiobook.m4b", "audiobook library")
	assertStatus(t, f.downloads, f.ctx, abDL.GUID, models.StateImported)
	if _, err := os.Stat(filepath.Join(abSrc, "audiobook.m4b")); err != nil {
		t.Errorf("copy mode must leave the audiobook source in place: %v", err)
	}
	reloaded, _ := f.books.GetByID(f.ctx, f.book.ID)
	if reloaded.AudiobookFilePath == "" {
		t.Error("AudiobookFilePath is empty; the copied audiobook was not recorded")
	}
	if len(f.abs.scanCalls) != 1 || f.abs.scanCalls[0] != "lib-audio" {
		t.Errorf("ABS scan calls = %v, want one scan of lib-audio after the audiobook import", f.abs.scanCalls)
	}
}

// The reverse split with a dedicated audiobook drop folder: ebooks copy into
// the library, audiobooks are handed off into their own folder.
func TestAudiobookImportMode_AudiobookExternalOwnDropFolder(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":                  "copy",
		"import.drop_folder":           "$DROP",
		"import.audiobook.mode":        "external",
		"import.audiobook.drop_folder": "$ABDROP",
	})

	ebookDL, _ := f.importFormat(t, models.MediaTypeEbook)
	abDL, _ := f.importFormat(t, models.MediaTypeAudiobook)

	assertHasFileNamed(t, f.abDropDir, "audiobook.m4b", "audiobook drop folder")
	assertNoFiles(t, f.abDir, "audiobook library")
	assertNoFiles(t, f.dropDir, "ebook drop folder")
	assertStatus(t, f.downloads, f.ctx, abDL.GUID, models.StateImportExternal)

	assertHasFileNamed(t, f.libraryDir, "Title T - Author A.epub", "ebook library")
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImported)
	if len(f.abs.scanCalls) != 0 {
		t.Errorf("ABS scan calls = %v, want none: the audiobook was handed off, not imported", f.abs.scanCalls)
	}
}

// Both formats external, each into its own folder (the smaller proposal in the
// issue): the audiobook folder applies even when the mode is inherited.
func TestAudiobookImportMode_SharedExternalSeparateFolders(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":                  "external",
		"import.drop_folder":           "$DROP",
		"import.audiobook.drop_folder": "$ABDROP",
	})

	f.importFormat(t, models.MediaTypeEbook)
	f.importFormat(t, models.MediaTypeAudiobook)

	assertHasFileNamed(t, f.dropDir, "Title T - Author A.epub", "ebook drop folder")
	assertHasFileNamed(t, f.abDropDir, "audiobook.m4b", "audiobook drop folder")
	for _, rel := range filesUnder(t, f.dropDir) {
		if filepath.Ext(rel) == ".m4b" {
			t.Errorf("audiobook landed in the ebook drop folder at %s", rel)
		}
	}
	for _, rel := range filesUnder(t, f.abDropDir) {
		if filepath.Ext(rel) == ".epub" {
			t.Errorf("ebook landed in the audiobook drop folder at %s", rel)
		}
	}
}

// An explicit "auto" override means auto for audiobooks even though
// import.mode is external: the audiobook is placed in the library.
func TestAudiobookImportMode_AutoOverridesExternal(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{
		"import.mode":           "external",
		"import.drop_folder":    "$DROP",
		"import.audiobook.mode": "auto",
	})

	abDL, _ := f.importFormat(t, models.MediaTypeAudiobook)

	assertNoFiles(t, f.dropDir, "drop folder")
	assertHasFileNamed(t, f.abDir, "audiobook.m4b", "audiobook library")
	assertStatus(t, f.downloads, f.ctx, abDL.GUID, models.StateImported)
}

// Pair gating with a mixed config: the audiobook is imported, never dropped,
// so holding the ebook for it could only end at the 72h timeout. The ebook
// drops immediately instead.
func TestAudiobookImportMode_PairGatingSkippedWhenSiblingDoesNotDrop(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":             "external",
		"import.drop_folder":      "$DROP",
		"import.audiobook.mode":   "copy",
		"import.drop_pair_gating": "true",
	})

	ebookDL, _ := f.importFormat(t, models.MediaTypeEbook)

	assertHasFileNamed(t, f.dropDir, "Title T - Author A.epub", "drop folder")
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImportExternal)
}

// Pair gating with both formats external but separate folders: the held
// ebook is released into the EBOOK folder when the audiobook arrives, not
// into the folder of the format that triggered the release.
func TestAudiobookImportMode_PairGatingReleasesSiblingIntoItsOwnFolder(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":                  "external",
		"import.drop_folder":           "$DROP",
		"import.audiobook.drop_folder": "$ABDROP",
		"import.drop_pair_gating":      "true",
	})

	ebookDL, _ := f.importFormat(t, models.MediaTypeEbook)
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImportHeld)
	assertNoFiles(t, f.dropDir, "ebook drop folder while held")

	abDL, _ := f.importFormat(t, models.MediaTypeAudiobook)

	assertHasFileNamed(t, f.abDropDir, "audiobook.m4b", "audiobook drop folder")
	assertHasFileNamed(t, f.dropDir, "Title T - Author A.epub", "ebook drop folder")
	for _, rel := range filesUnder(t, f.abDropDir) {
		if filepath.Ext(rel) == ".epub" {
			t.Errorf("held ebook was released into the audiobook drop folder at %s", rel)
		}
	}
	assertStatus(t, f.downloads, f.ctx, ebookDL.GUID, models.StateImportExternal)
	assertStatus(t, f.downloads, f.ctx, abDL.GUID, models.StateImportExternal)
}

// Manual import passes an explicit format hint; the mode follows the hint,
// the same way placement does, even when the extensions would say otherwise.
func TestDownloadImportMode_FollowsFormatHint(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeBoth, map[string]string{
		"import.mode":           "external",
		"import.audiobook.mode": "hardlink",
	})
	ebookDir := writeEbookDownload(t)
	abDir := writeAudiobookDownload(t)

	cases := []struct {
		name, path, hint, want string
	}{
		{"ebook by extension", ebookDir, "", "external"},
		{"audiobook by extension", abDir, "", "hardlink"},
		{"hint audiobook wins", ebookDir, models.MediaTypeAudiobook, "hardlink"},
		{"hint ebook wins", abDir, models.MediaTypeEbook, "external"},
	}
	for _, tc := range cases {
		if got := f.s.downloadImportMode(f.ctx, tc.path, tc.hint, nil); got != tc.want {
			t.Errorf("%s: downloadImportMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An unrecognised or empty override falls back to import.mode rather than to
// auto, so a stray value cannot silently change where audiobooks go.
func TestConfiguredImportModeFor_Fallbacks(t *testing.T) {
	cases := []struct {
		base, override, want string
	}{
		{"external", "", "external"},
		{"external", "bogus", "external"},
		{"copy", "external", "external"},
		{"external", "auto", ""},
		{"", "move", "move"},
	}
	for _, tc := range cases {
		f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{
			"import.mode":           tc.base,
			"import.audiobook.mode": tc.override,
		})
		if got := f.s.configuredImportModeFor(f.ctx, models.MediaTypeAudiobook); got != tc.want {
			t.Errorf("import.mode=%q audiobook=%q: got %q, want %q", tc.base, tc.override, got, tc.want)
		}
		if got := f.s.configuredImportModeFor(f.ctx, models.MediaTypeEbook); got != f.s.configuredImportMode(f.ctx) {
			t.Errorf("ebook mode %q must always equal import.mode %q", got, f.s.configuredImportMode(f.ctx))
		}
	}
}

// The pair gating timeout sweep releases a held audiobook into the audiobook
// drop folder, not the ebook one: the sweep reads the folder per format too.
func TestAudiobookImportMode_TimeoutSweepUsesAudiobookFolder(t *testing.T) {
	s, settings, downloads, _, database, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	abDropDir := t.TempDir()
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":           dropDir,
		"import.audiobook.drop_folder": abDropDir,
		"import.drop_pair_gating":      "true",
	})

	abSrc := writeAudiobookDownload(t)
	dl := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, dl, abSrc, "", "", "", nil, nil)
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)

	old := time.Now().Add(-100 * time.Hour).UTC()
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET completed_at=? WHERE id=?", old, dl.ID); err != nil {
		t.Fatalf("backdate completed_at: %v", err)
	}
	s.sweepHeldPairGating(ctx)

	assertHasFileNamed(t, abDropDir, "audiobook.m4b", "audiobook drop folder")
	assertNoFiles(t, dropDir, "ebook drop folder")
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}
