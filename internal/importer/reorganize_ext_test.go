package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func writeBytesAt(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setTemplate(t *testing.T, env reorgEnv, ctx context.Context, tmpl string) {
	t.Helper()
	if err := env.s.settings.Set(ctx, "naming.bookTemplate", tmpl); err != nil {
		t.Fatal(err)
	}
}

// The #3217 repro: a template typo strips the extension, and fixing the
// template afterwards must bring it back, not propose "Title.".
func TestReorganize_RecoversExtensionFromContent(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	lost := filepath.Join(libraryDir, "Jane Doe", "My Bookepub")
	writeBytesAt(t, lost, epubBytes())
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, lost); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, "{Author}/{Title}.{ext}")

	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil || len(moves) != 1 {
		t.Fatalf("preview: %v %v", moves, err)
	}
	want := filepath.Join(libraryDir, "Jane Doe", "My Book.epub")
	if moves[0].Status != ReorgStatusMove || moves[0].Proposed != want {
		t.Fatalf("got %q %q, want move to %q", moves[0].Status, moves[0].Proposed, want)
	}
	res := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if len(res) != 1 || res[0].Status != ReorgStatusMoved {
		t.Fatalf("apply = %+v", res)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("repaired file missing: %v", err)
	}
}

// azw3 and cbz cannot be told from mobi or a bare zip by content, so the
// extension comes from the bookRenamed history of the rename that lost it.
func TestReorganize_RecoversExtensionFromHistory(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	orig := filepath.Join(libraryDir, "old", "My Book.azw3")
	lost := filepath.Join(libraryDir, "Jane Doe", "My Bookazw3")
	writeFileAt(t, lost) // content is not conclusive
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, lost); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, "{Author}/{Title}.{ext}")
	env.s.createHistoryEvent(ctx, models.HistoryEventBookRenamed, book.Title, &book.ID, map[string]string{
		"from": orig, "to": lost, "format": "ebook",
	})

	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil || len(moves) != 1 {
		t.Fatalf("preview: %v %v", moves, err)
	}
	want := filepath.Join(libraryDir, "Jane Doe", "My Book.azw3")
	if moves[0].Status != ReorgStatusMove || moves[0].Proposed != want {
		t.Fatalf("got %q %q (%s), want move to %q", moves[0].Status, moves[0].Proposed, moves[0].Message, want)
	}
}

// With nothing to recover the extension from, no move is proposed or applied.
func TestReorganize_RefusesWhenExtensionCannotBeRecovered(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	lost := filepath.Join(libraryDir, "Jane Doe", "My Bookepub")
	writeFileAt(t, lost)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, lost); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, "{Author}/{Title}.{ext}")

	moves, _ := env.s.PreviewReorganizeBook(ctx, book.ID)
	if len(moves) != 1 || moves[0].Status != ReorgStatusError || moves[0].Proposed != "" {
		t.Fatalf("preview = %+v, want one error with no proposal", moves)
	}
	res := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if res[0].Status != ReorgStatusError {
		t.Fatalf("apply = %+v, want error", res[0])
	}
	if _, err := os.Stat(lost); err != nil {
		t.Fatalf("file must stay put: %v", err)
	}
}

// The typo itself: {Title}{ext} must not be applied again.
func TestReorganize_RefusesTemplateWithoutDotBeforeExt(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	old := filepath.Join(libraryDir, "misc", "x.epub")
	writeFileAt(t, old)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, old); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, "{Author}/{Title}{ext}")

	moves, _ := env.s.PreviewReorganizeBook(ctx, book.ID)
	if len(moves) != 1 || moves[0].Status != ReorgStatusError {
		t.Fatalf("preview = %+v, want error", moves)
	}
	if res := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID}); res[0].Status != ReorgStatusError {
		t.Fatalf("apply = %+v, want error", res[0])
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("file must stay put: %v", err)
	}
}

func TestDestPath_ExtensionGuard(t *testing.T) {
	author := &models.Author{Name: "Jane Doe"}
	book := &models.Book{Title: "Dr. Book"}
	dir := t.TempDir()
	epub := filepath.Join(dir, "a")
	writeBytesAt(t, epub, epubBytes())

	// Content supplies the extension for an extensionless source.
	got, err := NewRenamer("{Title}.{ext}").DestPath("/lib", author, book, "", "", epub)
	if err != nil || got != "/lib/Dr. Book.epub" {
		t.Fatalf("got %q %v", got, err)
	}
	// A dotted title does not hide a missing dot before {ext}.
	if _, err := NewRenamer("{Title}{ext}").DestPath("/lib", author, book, "", "", "x.epub"); !errors.Is(err, ErrEbookNoExtension) {
		t.Fatalf("err = %v, want ErrEbookNoExtension", err)
	}
	// No extension anywhere.
	if _, err := NewRenamer("{Title}.{ext}").DestPath("/lib", author, book, "", "", filepath.Join(dir, "none")); !errors.Is(err, ErrEbookNoExtension) {
		t.Fatalf("err = %v, want ErrEbookNoExtension", err)
	}
}

func TestTemplateExtChecks(t *testing.T) {
	for tmpl, want := range map[string]bool{
		"{Title}.{ext}": false, "{Title}{ext}": true, "{A}/{Title} {ext}": true,
		"{Title}{.ext}": false, "{Title}": false,
	} {
		if got := EbookTemplateExtNeedsDot(tmpl); got != want {
			t.Errorf("EbookTemplateExtNeedsDot(%q) = %v, want %v", tmpl, got, want)
		}
	}
	for tmpl, want := range map[string]bool{
		"{Title}.{ext}": true, "{Title}{.ext}": true, "{Title}": false, "{Author}/{Title} ({Year})": false,
	} {
		if got := AudiobookTemplateUsesExt(tmpl); got != want {
			t.Errorf("AudiobookTemplateUsesExt(%q) = %v, want %v", tmpl, got, want)
		}
	}
}

// previewWithin fails the test when the preview does not return in time, which
// is how a blocking open on a special file shows up.
func previewWithin(t *testing.T, env reorgEnv, ctx context.Context, bookID int64) []ReorganizeMove {
	t.Helper()
	type result struct {
		moves []ReorganizeMove
		err   error
	}
	done := make(chan result, 1)
	go func() {
		m, err := env.s.PreviewReorganizeBook(ctx, bookID)
		done <- result{m, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.moves
	case <-time.After(5 * time.Second):
		t.Fatal("preview blocked: an extensionless special file was opened for sniffing")
		return nil
	}
}

func trackExtensionless(t *testing.T, env reorgEnv, ctx context.Context, name string, content []byte, tmpl string) (*models.Book, string) {
	t.Helper()
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	lost := filepath.Join(t.TempDir(), name)
	if content != nil {
		writeBytesAt(t, lost, content)
	}
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, lost); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, tmpl)
	return book, lost
}

func TestReorganize_MissingExtensionlessFileReadsMissing(t *testing.T) {
	env, _, _, ctx := reorgFixture(t)
	book, _ := trackExtensionless(t, env, ctx, "gone", nil, "{Author}/{Title}.{ext}")
	moves := previewWithin(t, env, ctx, book.ID)
	if len(moves) != 1 || moves[0].Status != ReorgStatusMissing {
		t.Fatalf("preview = %+v, want missing", moves)
	}
}

// History and content disagree: conclusive bytes win, history only fills in
// where the sniff is blind, and only ebook extensions are ever taken from it.
func TestReorganize_HistoryVersusContent(t *testing.T) {
	pdf := []byte("%PDF-1.4\n" + strings.Repeat(" ", 600))
	palm := make([]byte, 600)
	copy(palm[0x3C:], "BOOKMOBI")
	cases := []struct {
		name     string
		content  []byte
		histFrom string
		want     string // proposed extension, "" means error
	}{
		{"pdf bytes beat epub history", pdf, "My Book.epub", ".pdf"},
		{"epub bytes beat azw3 history", epubBytes(), "My Book.azw3", ".epub"},
		{"audio history is never used", []byte("blank"), "My Book.m4b", ""},
		{"mobi bytes keep azw3 history", palm, "My Book.azw3", ".azw3"},
		{"mobi bytes beat epub history", palm, "My Book.epub", ".mobi"},
		{"blind bytes take cbz history", []byte("blank"), "My Book.cbz", ".cbz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, _, _, ctx := reorgFixture(t)
			book, lost := trackExtensionless(t, env, ctx, "Bookx", c.content, "{Author}/{Title}.{ext}")
			env.s.createHistoryEvent(ctx, models.HistoryEventBookRenamed, book.Title, &book.ID, map[string]string{
				"from": filepath.Join(filepath.Dir(lost), c.histFrom), "to": lost, "format": "ebook",
			})
			moves := previewWithin(t, env, ctx, book.ID)
			if len(moves) != 1 {
				t.Fatalf("moves = %+v", moves)
			}
			if c.want == "" {
				if moves[0].Status != ReorgStatusError {
					t.Fatalf("got %q %q, want error", moves[0].Status, moves[0].Proposed)
				}
				return
			}
			if moves[0].Status != ReorgStatusMove || filepath.Ext(moves[0].Proposed) != c.want {
				t.Fatalf("got %q %q, want move to %s", moves[0].Status, moves[0].Proposed, c.want)
			}
		})
	}
}

func TestDestPath_GuardEdges(t *testing.T) {
	author := &models.Author{Name: "Jane Doe"}
	// A trailing dot with an extension available still has no extension.
	if _, err := NewRenamer("{Title}.").DestPath("/lib", author, &models.Book{Title: "Book"}, "", "", "x.epub"); !errors.Is(err, ErrEbookNoExtension) {
		t.Errorf("trailing dot: err = %v", err)
	}
	// A template with no {ext} at all must still end in an extension; a dot
	// inside the title is not one.
	for _, title := range []string{"Book", "Dr. Book"} {
		if _, err := NewRenamer("{Author}/{Title}").DestPath("/lib", author, &models.Book{Title: title}, "", "", "x.epub"); !errors.Is(err, ErrEbookNoExtension) {
			t.Errorf("no {ext} template, title %q: err = %v", title, err)
		}
	}
	if got, err := NewRenamer("{Title}.kepub").DestPath("/lib", author, &models.Book{Title: "Book"}, "", "", "x.epub"); err != nil || got != "/lib/Book.kepub" {
		t.Errorf("fixed extension template: %q %v", got, err)
	}
}

func TestDropEbookName_UsesRecoveredExtension(t *testing.T) {
	src := filepath.Join(t.TempDir(), "noext")
	writeBytesAt(t, src, epubBytes())
	got := NewRenamer("").DropEbookName(&models.Author{Name: "Jane Doe"}, &models.Book{Title: "Book"}, src)
	if got != "Book - Jane Doe.epub" {
		t.Errorf("got %q", got)
	}
}

// A template that cannot name an ebook with an extension blocks the import with
// a message that says why, rather than writing an extensionless file.
func TestImport_BlockedByTemplateWithoutExtension(t *testing.T) {
	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "book.epub"), bookSized("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "copy")
	if err := s.settings.Set(ctx, "naming.bookTemplate", "{Author}/{Title}{ext}"); err != nil {
		t.Fatal(err)
	}
	s.tryImportInternal(ctx, dl, downloadPath, "", "", "", nil, nil)
	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked || !strings.Contains(got.ErrorMessage, "dot before {ext}") {
		t.Errorf("status %q message %q, want blocked naming the missing dot", got.Status, got.ErrorMessage)
	}
	assertEmptyDir(t, libraryDir, "library dir")
}

func TestDrop_TemplatedBlockedByTemplateWithoutExtension(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir, "import.drop_layout": "templated"})
	if err := settings.Set(ctx, "naming.bookTemplate", "{Author}/{Title}{ext}"); err != nil {
		t.Fatal(err)
	}
	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "x.epub"), bookSized("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)
	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportBlocked)
	got, _ := downloads.GetByGUID(ctx, dl.GUID)
	if !strings.Contains(got.ErrorMessage, "dot before {ext}") {
		t.Errorf("message %q does not explain the missing dot", got.ErrorMessage)
	}
	assertEmptyDir(t, dropDir, "drop folder")
}
