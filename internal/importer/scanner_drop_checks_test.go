package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
