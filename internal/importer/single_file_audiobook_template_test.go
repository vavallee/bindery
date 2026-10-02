package importer

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Tests for #2900: a single-file audiobook is named from
// naming.audiobook_file_template, the same as each track of a multi-file one.

// conditionalPartTemplate puts {Part} in a #1127 conditional group, so the
// group, dash included, collapses when there is no part to number. "Pt." is
// used rather than "Part" because inside a group every word that is a token
// name is read as that token.
const conditionalPartTemplate = "{Author} - {Title}{ - Pt. Part:3}.{ext}"

func TestAudiobookSingleFileName(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Stephen King"}
	book := &models.Book{Title: "The Shining"}

	cases := []struct {
		name     string
		template string
		want     string
	}{
		{"conditional part group collapses", conditionalPartTemplate, "Stephen King - The Shining.m4b"},
		{"conditional group without literal word", "{Title}{ - Part:3}.{ext}", "The Shining.m4b"},
		{"leading width then literal collapses", "{Part:3 - }{Title}.{ext}", "The Shining.m4b"},
		{"template without part tokens renders as written", "{Author} - {Title}.{ext}", "Stephen King - The Shining.m4b"},
		// A bare {Part} keeps its glue outside the braces. Rendering it empty
		// would give "The Shining - Part .m4b", so the lone file is part 1.
		{"unconditional part numbers the file as part one", "{Title} - Part {Part:3}.{ext}", "The Shining - Part 001.m4b"},
		{"empty template uses the default", "", "The Shining - Part 001.m4b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.AudiobookSingleFileName(tc.template, author, book, "", "", "m4b"); got != tc.want {
				t.Errorf("AudiobookSingleFileName(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}

	// The multi-file render of the same template differs only in the part.
	if got, want := r.AudiobookFileName(conditionalPartTemplate, author, book, "", "", "m4b", 2), "Stephen King - The Shining - Pt. 002.m4b"; got != want {
		t.Errorf("AudiobookFileName part 2 = %q, want %q", got, want)
	}
}

func TestAudiobookTemplateHasPart(t *testing.T) {
	for tmpl, want := range map[string]bool{
		"{Title} - Part {Part:3}.{ext}":  true,
		"{Part}.{ext}":                   true,
		conditionalPartTemplate:          true,
		"{Title}{ - Part:3}.{ext}":       true,
		"{Part:3 - }{Title}.{ext}":       true,
		"{Title}.{ext}":                  false,
		"{Title} - Part.{ext}":           false, // literal text outside a group is not a token
		"{Title}{ - Parts Series}.{ext}": false,
	} {
		if got := AudiobookTemplateHasPart(tmpl); got != want {
			t.Errorf("AudiobookTemplateHasPart(%q) = %v, want %v", tmpl, got, want)
		}
	}
}

// importLoneAudiobook imports one audiobook whose source is the FILE itself
// (what a manual import of a lone .m4b resolves to) and returns the names in
// the folder the book records. A template of "" leaves the setting unset.
func importLoneAudiobook(t *testing.T, mode, template, srcName string) (folder string, names []string) {
	t.Helper()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, t.TempDir())
	if err := settingsRepo.Set(ctx, "import.mode", mode); err != nil {
		t.Fatal(err)
	}
	if template != "" {
		if err := settingsRepo.Set(ctx, "naming.audiobook_file_template", template); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), srcName)
	if err := os.WriteFile(src, []byte("m4b-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{GUID: "guid-2900-" + t.Name(), Title: "Jordan B. Peterson - We Who Wrestle with God [M4B]", BookID: &book.ID, Status: models.StateCompleted}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	s.ImportFromPath(ctx, dl, src, models.MediaTypeAudiobook)
	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("import status = %q, want imported (error: %s)", got.Status, got.ErrorMessage)
	}
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			folder = f.Path
		}
	}
	return folder, dirNames(t, folder)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".opf" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func TestImport_SingleFileAudiobookUsesFileTemplate(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			_, names := importLoneAudiobook(t, mode, conditionalPartTemplate, "wwwwg_cd-rip.M4B")
			want := "Jordan B. Peterson - We Who Wrestle with God.m4b"
			if len(names) != 1 || names[0] != want {
				t.Errorf("folder holds %v, want [%s]", names, want)
			}
		})
	}
}

func TestImport_SingleFileAudiobookUnconditionalPartIsPartOne(t *testing.T) {
	_, names := importLoneAudiobook(t, "copy", "{Title} - Part {Part:3}.{ext}", "source.m4b")
	if want := "We Who Wrestle with God - Part 001.m4b"; len(names) != 1 || names[0] != want {
		t.Errorf("folder holds %v, want [%s]", names, want)
	}
}

func TestImport_SingleFileAudiobookWithoutTemplateKeepsSourceName(t *testing.T) {
	_, names := importLoneAudiobook(t, "copy", "", "wwwwg_cd-rip.M4B")
	if len(names) != 1 || names[0] != "wwwwg_cd-rip.M4B" {
		t.Errorf("folder holds %v, want the source name kept", names)
	}
}

// importAudiobookFolder imports a download folder holding the given tracks.
func importAudiobookFolder(t *testing.T, template string, tracks ...string) []string {
	t.Helper()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, t.TempDir())
	if err := settingsRepo.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, "naming.audiobook_file_template", template); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	for _, name := range tracks {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dl := &models.Download{GUID: "guid-2900-dir-" + t.Name(), Title: "Jordan B. Peterson - We Who Wrestle with God", BookID: &book.ID, Status: models.StateCompleted}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	s.ImportFromPath(ctx, dl, src, models.MediaTypeAudiobook)
	if got, _ := dlRepo.GetByGUID(ctx, dl.GUID); got == nil || got.Status != models.StateImported {
		t.Fatalf("folder import did not complete: %+v", got)
	}
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			return dirNames(t, f.Path)
		}
	}
	t.Fatal("no audiobook recorded")
	return nil
}

// A folder holding one track is the same audiobook as the lone file, so it
// must get the same name rather than depending on how the release was packed.
func TestImport_OneTrackFolderMatchesSingleFileName(t *testing.T) {
	names := importAudiobookFolder(t, conditionalPartTemplate, "only.m4b")
	if want := "Jordan B. Peterson - We Who Wrestle with God.m4b"; len(names) != 1 || names[0] != want {
		t.Errorf("folder holds %v, want [%s]", names, want)
	}
}

// Multi-file naming is unchanged: every track is numbered, conditional or not.
func TestImport_MultiTrackFolderStillNumbersEveryTrack(t *testing.T) {
	for tmpl, want := range map[string][]string{
		conditionalPartTemplate: {
			"Jordan B. Peterson - We Who Wrestle with God - Pt. 001.m4b",
			"Jordan B. Peterson - We Who Wrestle with God - Pt. 002.m4b",
		},
		"{Title} - Part {Part:3}.{ext}": {
			"We Who Wrestle with God - Part 001.m4b",
			"We Who Wrestle with God - Part 002.m4b",
		},
	} {
		t.Run(tmpl, func(t *testing.T) {
			names := importAudiobookFolder(t, tmpl, "01.m4b", "02.m4b")
			if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
				t.Errorf("folder holds %v, want %v", names, want)
			}
		})
	}
}

// A merge into the book's shared folder never overwrites a file already there
// (#2686). With a template that check is made against the templated name.
func TestImport_SingleFileAudiobookTemplatedNameCollisionIsSkipped(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			s, book, dlRepo, _, ctx, ebookDir := importSharedEbook(t, mode)
			if err := s.settings.Set(ctx, "naming.audiobook_file_template", conditionalPartTemplate); err != nil {
				t.Fatal(err)
			}
			existing := []byte("an m4b that was already here")
			seeded := filepath.Join(ebookDir, "Jordan B. Peterson - We Who Wrestle with God.m4b")
			if err := os.WriteFile(seeded, existing, 0o644); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(t.TempDir(), "source.m4b")
			if err := os.WriteFile(src, []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			dl := &models.Download{GUID: "guid-2900-collide-" + t.Name(), Title: "We Who Wrestle with God [M4B]", BookID: &book.ID, Status: models.StateCompleted}
			if err := dlRepo.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}
			s.ImportFromPath(ctx, dl, src, models.MediaTypeAudiobook)
			if got, _ := dlRepo.GetByGUID(ctx, dl.GUID); got == nil || got.Status != models.StateImported {
				t.Fatalf("import did not complete: %+v", got)
			}
			if got, err := os.ReadFile(seeded); err != nil || string(got) != string(existing) {
				t.Errorf("the file already at the templated name was overwritten or removed (err %v)", err)
			}
			if _, err := os.Stat(filepath.Join(ebookDir, "source.m4b")); !os.IsNotExist(err) {
				t.Errorf("the skipped file was placed under its source name instead: %v", err)
			}
			if _, err := os.Stat(src); err != nil {
				t.Errorf("mode=%s: the source of a skipped file is gone: %v", mode, err)
			}
		})
	}
}

// Rename files proposes the name an import would give, and apply puts the file
// exactly where the preview said.
func TestReorganize_SingleFileAudiobookUsesFileTemplate(t *testing.T) {
	env, _, audiobookDir, ctx := reorgFixture(t)
	if err := env.s.settings.Set(ctx, "naming.audiobook_file_template", conditionalPartTemplate); err != nil {
		t.Fatal(err)
	}
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	oldPath := filepath.Join(audiobookDir, "Jane Doe", "my_book_rip.m4b")
	writeFileAt(t, oldPath)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, oldPath); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(audiobookDir, "Jane Doe", "My Book (2020)", "Jane Doe - My Book.m4b")
	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].Status != ReorgStatusMove || moves[0].Proposed != want {
		t.Fatalf("preview = %+v, want move to %q", moves, want)
	}
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if results[0].Status != ReorgStatusMoved || results[0].Proposed != moves[0].Proposed {
		t.Fatalf("apply = %+v, want moved to the previewed %q", results[0], moves[0].Proposed)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("file not at the templated name: %v", err)
	}

	// Once there it stays there.
	again, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Status != ReorgStatusNoop {
		t.Errorf("second preview = %+v, want noop", again)
	}
}

// Fix Match's destination preview routes through the same code, so it names
// the templated file too.
func TestPreviewImportDestination_SingleFileAudiobookUsesFileTemplate(t *testing.T) {
	env, _, audiobookDir, ctx := reorgFixture(t)
	if err := env.s.settings.Set(ctx, "naming.audiobook_file_template", conditionalPartTemplate); err != nil {
		t.Fatal(err)
	}
	target := env.seed(t, ctx, "Jane Doe", "Right Book")
	src := filepath.Join(t.TempDir(), "rip.m4b")
	writeFileAt(t, src)

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(audiobookDir, "Jane Doe", "Right Book (2020)", "Jane Doe - Right Book.m4b"); got.Destination != want {
		t.Errorf("destination = %q, want %q", got.Destination, want)
	}
}
