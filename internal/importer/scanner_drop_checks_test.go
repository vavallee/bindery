package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// External mode with a drop folder runs the same language and content checks
// as a managed import, on the files about to be dropped (#3945).

// externalDrop switches a content fixture to external mode with a drop folder
// and returns that folder.
func (f *contentFixture) externalDrop(t *testing.T) string {
	t.Helper()
	drop := t.TempDir()
	f.setMode(t, "import.mode", "external")
	f.setMode(t, "import.drop_folder", drop)
	return drop
}

func dropCheckFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (f *contentFixture) importExternal(t *testing.T) {
	t.Helper()
	f.scanner.tryImportInternal(context.Background(), f.dl, f.dir, "", "", "", nil, nil)
}

func TestExternalDropChecks_Ebook(t *testing.T) {
	const wantTitle, wantAuthor = "Middlemarch", "George Eliot"
	cases := []struct {
		name        string
		mode        string // import.ebook.content_check, "" leaves the default (warn)
		title       string
		creator     string
		wantStatus  models.DownloadState
		wantDropped bool
		wantFlagged bool // contentCheck on the drop event (warn) or the failure event (block)
	}{
		{"matching file is dropped silently", "", wantTitle, wantAuthor, models.StateImportExternal, true, false},
		{"mismatch under warn is dropped and noted", "", "Jane Eyre: An Autobiography", "Currer Bell", models.StateImportExternal, true, true},
		{"mismatch under explicit warn is dropped and noted", "warn", "Jane Eyre: An Autobiography", "Currer Bell", models.StateImportExternal, true, true},
		{"mismatch under block is parked, nothing dropped", "block", "Jane Eyre: An Autobiography", "Currer Bell", models.StateImportBlocked, false, true},
		{"matching file under block is dropped", "block", wantTitle, wantAuthor, models.StateImportExternal, true, false},
		{"mismatch with the check off is dropped unflagged", "off", "Jane Eyre: An Autobiography", "Currer Bell", models.StateImportExternal, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContentFixture(t, wantTitle, wantAuthor)
			drop := f.externalDrop(t)
			if c.mode != "" {
				f.setMode(t, ebookCheckKey, c.mode)
			}
			f.writeEpubTitled(t, "book.epub", c.title, c.creator)
			f.importExternal(t)

			dl, book := f.reload(t)
			if dl.Status != c.wantStatus {
				t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, c.wantStatus)
			}
			if got := len(dropCheckFilesUnder(t, drop)) == 1; got != c.wantDropped {
				t.Errorf("dropped = %v, want %v (%v)", got, c.wantDropped, dropCheckFilesUnder(t, drop))
			}
			if book.Status != models.BookStatusWanted {
				t.Errorf("book status = %q, want wanted", book.Status)
			}
			if c.wantStatus == models.StateImportBlocked {
				if !f.blocklisted(t) || dl.ImportPath == "" {
					t.Errorf("blocklisted=%v importPath=%q, want both set like a managed refusal", f.blocklisted(t), dl.ImportPath)
				}
				if _, ok := f.eventData(t, models.HistoryEventImportFailed)["contentCheck"]; !ok {
					t.Error("importFailed event has no contentCheck")
				}
				return
			}
			_, has := f.eventData(t, models.HistoryEventDownloadFolderImport)["contentCheck"]
			if has != c.wantFlagged {
				t.Errorf("contentCheck on the drop event = %v, want %v", has, c.wantFlagged)
			}
			if f.blocklisted(t) {
				t.Error("a dropped release was blocklisted")
			}
		})
	}
}

// A block in External mode parks the download exactly as a managed import does:
// same state, same message.
func TestExternalDropChecks_BlockMatchesManagedRefusal(t *testing.T) {
	run := func(external bool) *models.Download {
		f := newContentFixture(t, "Middlemarch", "George Eliot")
		f.setMode(t, ebookCheckKey, "block")
		f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")
		if external {
			f.externalDrop(t)
			f.importExternal(t)
		} else {
			f.importDownload(t)
		}
		dl, _ := f.reload(t)
		return dl
	}
	managed, external := run(false), run(true)
	if managed.Status != models.StateImportBlocked || external.Status != managed.Status {
		t.Fatalf("states: managed %q, external %q", managed.Status, external.Status)
	}
	if managed.ErrorMessage != external.ErrorMessage {
		t.Errorf("messages differ:\nmanaged:  %s\nexternal: %s", managed.ErrorMessage, external.ErrorMessage)
	}
}

func TestExternalDropChecks_Language(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	ctx := context.Background()
	drop := t.TempDir()
	for k, v := range map[string]string{"import.mode": "external", "import.drop_folder": drop} {
		if err := f.settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	f.writeEpub(t, "sv")
	f.scanner.tryImportInternal(ctx, f.dl, f.dir, "", "", "", nil, nil)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want blocked", dl.Status, dl.ErrorMessage)
	}
	for _, want := range []string{"Swedish", "English"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("message %q does not name %s", dl.ErrorMessage, want)
		}
	}
	if !f.blocked(t) || dl.ImportPath == "" {
		t.Error("release not blocklisted or path not recorded")
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q", book.Status)
	}
	if n := len(dropCheckFilesUnder(t, drop)); n != 0 {
		t.Errorf("%d file(s) dropped for a refused language", n)
	}

	// An allowed language drops as before.
	f2 := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	drop2 := t.TempDir()
	for k, v := range map[string]string{"import.mode": "external", "import.drop_folder": drop2} {
		if err := f2.settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	f2.writeEpub(t, "en")
	f2.scanner.tryImportInternal(ctx, f2.dl, f2.dir, "", "", "", nil, nil)
	if dl, _ := f2.reload(t); dl.Status != models.StateImportExternal {
		t.Errorf("allowed language: status = %q (%s)", dl.Status, dl.ErrorMessage)
	}
	if n := len(dropCheckFilesUnder(t, drop2)); n != 1 {
		t.Errorf("allowed language: %d file(s) dropped, want 1", n)
	}
}

func TestExternalDropChecks_Audiobook(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		mismatch   bool
		wantStatus models.DownloadState
	}{
		{"matching album is dropped", "block", false, models.StateImportExternal},
		{"mismatch under warn is dropped and noted", "warn", true, models.StateImportExternal},
		{"mismatch under block is parked", "block", true, models.StateImportBlocked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAudioFixture(t, "The Firm", "Author Name")
			drop := f.externalDrop(t)
			f.setMode(t, audioCheckKey, c.mode)
			f.writeAlbum(t, "The Firm", 4)
			if c.mismatch {
				f.writeAlbum(t, "The Runaway Jury", 20)
			}
			f.importExternal(t)

			dl, _ := f.reload(t)
			if dl.Status != c.wantStatus {
				t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, c.wantStatus)
			}
			dropped := len(dropCheckFilesUnder(t, drop)) > 0
			if dropped != (c.wantStatus == models.StateImportExternal) {
				t.Errorf("dropped = %v with status %q", dropped, dl.Status)
			}
			switch c.wantStatus {
			case models.StateImportBlocked:
				if !f.blocklisted(t) || dl.ImportPath == "" {
					t.Error("not blocklisted or path not recorded")
				}
				if _, ok := f.eventData(t, models.HistoryEventImportFailed)["contentCheck"]; !ok {
					t.Error("importFailed has no contentCheck")
				}
			default:
				_, has := f.eventData(t, models.HistoryEventDownloadFolderImport)["contentCheck"]
				if has != c.mismatch {
					t.Errorf("contentCheck on the drop event = %v, want %v", has, c.mismatch)
				}
			}
		})
	}
}

// With no drop folder the file stays in the download folder and nothing is
// placed, so there is nothing to take back: the plain hand off is unchanged and
// does not run the checks.
func TestExternalDropChecks_NoDropFolderHandsOffInPlace(t *testing.T) {
	f := newContentFixture(t, "Middlemarch", "George Eliot")
	f.setMode(t, "import.mode", "external")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")
	f.importExternal(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportExternal {
		t.Errorf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportExternal)
	}
	if len(f.checks) != 0 {
		t.Errorf("content check ran %v for an in place hand off", f.checks)
	}
}

// Pair gating is unchanged for a clean file (held until the sibling arrives),
// and a refused file is blocked instead of held.
func TestExternalDropChecks_PairGating(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		creator    string
		wantStatus models.DownloadState
	}{
		{"clean ebook is held", "Middlemarch", "George Eliot", models.StateImportHeld},
		{"refused ebook is blocked, not held", "Jane Eyre: An Autobiography", "Currer Bell", models.StateImportBlocked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContentFixtureFor(t, "Middlemarch", "George Eliot", models.MediaTypeBoth)
			drop := f.externalDrop(t)
			f.setMode(t, "import.drop_pair_gating", "true")
			f.setMode(t, ebookCheckKey, "block")
			f.writeEpubTitled(t, "book.epub", c.title, c.creator)
			f.importExternal(t)

			dl, _ := f.reload(t)
			if dl.Status != c.wantStatus {
				t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, c.wantStatus)
			}
			if n := len(dropCheckFilesUnder(t, drop)); n != 0 {
				t.Errorf("%d file(s) in the drop folder, want 0", n)
			}
		})
	}
}

// Pair gating stores only the held download's path, so the release and the
// timeout sweep re-read the files. They must run the same checks again (#3945).

func addSiblingAudiobook(t *testing.T, f *contentFixture) *models.Download {
	t.Helper()
	adl := &models.Download{BookID: &f.book.ID, GUID: "guid-sibling-audio", Title: "Sibling", Status: models.StateCompleted, Protocol: "torrent"}
	if err := f.downloads.Create(context.Background(), adl); err != nil {
		t.Fatal(err)
	}
	return adl
}

func releaseWithAudiobook(t *testing.T, f *contentFixture) {
	t.Helper()
	audioDir := writeAudiobookDownload(t)
	adl := addSiblingAudiobook(t, f)
	f.scanner.tryImportInternal(context.Background(), adl, audioDir, "", "", "", nil, nil)
}

func droppedEpubLanguages(t *testing.T, drop string) []string {
	t.Helper()
	var langs []string
	for _, p := range dropCheckFilesUnder(t, drop) {
		if !strings.HasSuffix(p, ".epub") {
			continue
		}
		meta, err := ReadEbookMetadata(p)
		if err != nil {
			t.Fatal(err)
		}
		langs = append(langs, meta.Languages...)
	}
	return langs
}

// A release holding a Swedish and an English EPUB is held, then released when
// the sibling arrives: only the allowed file is dropped.
func TestExternalDropChecks_HeldMixedLanguageReleasesOnlyAllowed(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "whole download"
		if shared {
			name = "explicit files on a shared root"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.book.MediaType = models.MediaTypeBoth
			if err := f.books.Update(ctx, f.book); err != nil {
				t.Fatal(err)
			}
			drop := t.TempDir()
			for k, v := range map[string]string{"import.mode": "external", "import.drop_folder": drop, "import.drop_pair_gating": "true"} {
				if err := f.settings.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			writeEpubWithLanguage(t, filepath.Join(f.dir, "The Racketeer.epub"), "sv")
			writeEpubWithLanguage(t, filepath.Join(f.dir, "Other.epub"), "en")
			var explicit []string
			if shared {
				explicit = []string{filepath.Join(f.dir, "Other.epub")}
			}
			f.scanner.tryImportInternal(ctx, f.dl, f.dir, "", "", "", nil, explicit)
			if dl, _ := f.reload(t); dl.Status != models.StateImportHeld {
				t.Fatalf("status = %q (%s), want held", dl.Status, dl.ErrorMessage)
			}

			audioDir := writeAudiobookDownload(t)
			adl := &models.Download{BookID: &f.book.ID, GUID: "guid-sibling-audio", Title: "Sibling", Status: models.StateCompleted, Protocol: "torrent"}
			if err := f.downloads.Create(ctx, adl); err != nil {
				t.Fatal(err)
			}
			f.scanner.tryImportInternal(ctx, adl, audioDir, "", "", "", nil, nil)

			if dl, _ := f.reload(t); dl.Status != models.StateImportExternal {
				t.Fatalf("held ebook status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportExternal)
			}
			langs := droppedEpubLanguages(t, drop)
			if len(langs) != 1 || langs[0] != "eng" {
				t.Errorf("dropped epub languages = %v, want only [eng]", langs)
			}
		})
	}
}

// A held ebook that now fails a block policy is parked like a managed refusal
// when the sibling arrives, and nothing of it is placed.
func TestExternalDropChecks_HeldReleaseNowBlocked(t *testing.T) {
	f := newContentFixtureFor(t, "Middlemarch", "George Eliot", models.MediaTypeBoth)
	drop := f.externalDrop(t)
	f.setMode(t, "import.drop_pair_gating", "true")
	f.writeEpubTitled(t, "book.epub", "Middlemarch", "George Eliot")
	f.importExternal(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportHeld {
		t.Fatalf("status = %q, want held", dl.Status)
	}

	// While held, the policy tightens and the file turns out to be another book.
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")
	releaseWithAudiobook(t, f)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("held ebook status = %q (%s), want blocked", dl.Status, dl.ErrorMessage)
	}
	if !f.blocklisted(t) || dl.ImportPath == "" {
		t.Error("held release not blocklisted or path not recorded")
	}
	for _, p := range dropCheckFilesUnder(t, drop) {
		if strings.HasSuffix(p, ".epub") {
			t.Errorf("a refused held ebook was placed: %s", p)
		}
	}
}

// The timeout sweep runs the checks too, and a refusal is recorded once, not
// on every tick.
func TestExternalDropChecks_SweepRunsChecksOnce(t *testing.T) {
	f := newContentFixtureFor(t, "Middlemarch", "George Eliot", models.MediaTypeBoth)
	drop := f.externalDrop(t)
	f.setMode(t, "import.drop_pair_gating", "true")
	f.writeEpubTitled(t, "book.epub", "Middlemarch", "George Eliot")
	f.importExternal(t)
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")

	ctx := context.Background()
	old := time.Now().Add(-100 * time.Hour).UTC()
	if _, err := f.sqlDB.ExecContext(ctx, "UPDATE downloads SET completed_at=? WHERE id=?", old, f.dl.ID); err != nil {
		t.Fatal(err)
	}
	f.scanner.sweepHeldPairGating(ctx)
	f.scanner.sweepHeldPairGating(ctx)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want blocked", dl.Status, dl.ErrorMessage)
	}
	if !f.blocklisted(t) {
		t.Error("not blocklisted")
	}
	if n := len(dropCheckFilesUnder(t, drop)); n != 0 {
		t.Errorf("%d file(s) placed for a refused held ebook", n)
	}
	events, err := f.history.ListByType(ctx, models.HistoryEventImportFailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("%d importFailed events, want 1", len(events))
	}
}

// Whether a held download is checked when it is released follows the held
// download, not the call that releases it (#3945): a manual import is never
// refused, an automatic one always is.
func TestExternalDropChecks_HeldReleaseFollowsTheHeldDownloadNotTheCaller(t *testing.T) {
	cases := []struct {
		name        string
		heldManual  bool
		viaSweep    bool
		wantBlocked bool
	}{
		{"manual held, released by an automatic sibling", true, false, false},
		{"automatic held, released by a manual sibling", false, false, true},
		{"manual held, swept on an automatic tick", true, true, false},
		{"automatic held, swept on a manual tick", false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContentFixtureFor(t, "Middlemarch", "George Eliot", models.MediaTypeBoth)
			drop := f.externalDrop(t)
			f.setMode(t, "import.drop_pair_gating", "true")
			f.setMode(t, ebookCheckKey, "block")
			// A file that is a different book. A manual import is held with it
			// (never refused); an automatic one is first held clean, then the
			// file turns out to be another book while it waits.
			if c.heldManual {
				f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")
			} else {
				f.writeEpubTitled(t, "book.epub", "Middlemarch", "George Eliot")
			}
			heldCtx := context.Background()
			if c.heldManual {
				heldCtx = withManualImport(heldCtx)
			}
			f.scanner.tryImportInternal(heldCtx, f.dl, f.dir, "", "", "", nil, nil)
			if dl, _ := f.reload(t); dl.Status != models.StateImportHeld {
				t.Fatalf("status = %q (%s), want held", dl.Status, dl.ErrorMessage)
			}
			if !c.heldManual {
				f.writeEpubTitled(t, "book.epub", "Jane Eyre: An Autobiography", "Currer Bell")
			}

			// The releasing call carries the opposite mark.
			relCtx := context.Background()
			if !c.heldManual {
				relCtx = withManualImport(relCtx)
			}
			if c.viaSweep {
				old := time.Now().Add(-100 * time.Hour).UTC()
				if _, err := f.sqlDB.ExecContext(relCtx, "UPDATE downloads SET completed_at=? WHERE id=?", old, f.dl.ID); err != nil {
					t.Fatal(err)
				}
				f.scanner.sweepHeldPairGating(relCtx)
			} else {
				adl := addSiblingAudiobook(t, f)
				f.scanner.tryImportInternal(relCtx, adl, writeAudiobookDownload(t), "", "", "", nil, nil)
			}

			dl, _ := f.reload(t)
			dropped := false
			for _, p := range dropCheckFilesUnder(t, drop) {
				dropped = dropped || strings.HasSuffix(p, ".epub")
			}
			if c.wantBlocked {
				if dl.Status != models.StateImportBlocked || dropped {
					t.Errorf("status = %q (%s), dropped = %v, want blocked and not dropped", dl.Status, dl.ErrorMessage, dropped)
				}
				return
			}
			if dl.Status != models.StateImportExternal || !dropped {
				t.Errorf("status = %q (%s), dropped = %v, want a manual import dropped, not refused", dl.Status, dl.ErrorMessage, dropped)
			}
		})
	}
}
