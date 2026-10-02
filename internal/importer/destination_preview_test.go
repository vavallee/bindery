package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestPreviewImportDestination_EbookMatchesTemplate checks that the preview
// names the same path the import would write, and leaves the source alone.
func TestPreviewImportDestination_EbookMatchesTemplate(t *testing.T) {
	t.Parallel()
	env, libraryDir, _, ctx := reorgFixture(t)
	target := env.seed(t, ctx, "Jane Doe", "Right Book")

	// The file currently sits in the user's own layout under a different book.
	src := filepath.Join(libraryDir, "My Own Layout", "some-file.epub")
	writeFileAt(t, src)

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, "")
	if err != nil {
		t.Fatalf("PreviewImportDestination: %v", err)
	}
	want := filepath.Join(libraryDir, "Jane Doe", "Right Book (2020)", "Right Book - Jane Doe.epub")
	if got.Destination != want {
		t.Errorf("destination = %q, want %q", got.Destination, want)
	}
	if got.Status != ReorgStatusMove {
		t.Errorf("status = %q, want %q", got.Status, ReorgStatusMove)
	}
	if got.Format != models.MediaTypeEbook {
		t.Errorf("format = %q, want ebook", got.Format)
	}
	// Read-only: the source is untouched and the destination is not created.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source file disappeared: %v", err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("preview created the destination: %v", err)
	}
}

// TestPreviewImportDestination_AudiobookUsesFolderTemplate covers the other
// renamer entrypoint: an audiobook folder is placed as a directory.
func TestPreviewImportDestination_AudiobookUsesFolderTemplate(t *testing.T) {
	t.Parallel()
	env, _, audiobookDir, ctx := reorgFixture(t)
	target := env.seed(t, ctx, "Jane Doe", "Right Book")

	src := filepath.Join(audiobookDir, "Some Folder")
	writeFileAt(t, filepath.Join(src, "part1.m4b"))

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, "")
	if err != nil {
		t.Fatalf("PreviewImportDestination: %v", err)
	}
	if got.Format != models.MediaTypeAudiobook {
		t.Fatalf("format = %q, want audiobook", got.Format)
	}
	want := filepath.Join(audiobookDir, "Jane Doe", "Right Book (2020)")
	if got.Destination != want {
		t.Errorf("destination = %q, want %q", got.Destination, want)
	}
}

// TestPreviewImportDestination_FormatHintWins mirrors ImportFromPath: an
// explicit format from the caller overrides extension detection, so the preview
// cannot disagree with the import it is describing.
func TestPreviewImportDestination_FormatHintWins(t *testing.T) {
	t.Parallel()
	env, libraryDir, audiobookDir, ctx := reorgFixture(t)
	target := env.seed(t, ctx, "Jane Doe", "Right Book")

	src := filepath.Join(libraryDir, "mislabelled.epub")
	writeFileAt(t, src)

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, models.MediaTypeAudiobook)
	if err != nil {
		t.Fatalf("PreviewImportDestination: %v", err)
	}
	want := filepath.Join(audiobookDir, "Jane Doe", "Right Book (2020)", "mislabelled.epub")
	if got.Destination != want {
		t.Errorf("destination = %q, want inside the audiobook root %q", got.Destination, want)
	}
}

// TestPreviewImportDestination_AlreadyInPlaceIsNoop means the modal can say
// "nothing moves" rather than warning about a move that will not happen.
func TestPreviewImportDestination_AlreadyInPlaceIsNoop(t *testing.T) {
	t.Parallel()
	env, libraryDir, _, ctx := reorgFixture(t)
	target := env.seed(t, ctx, "Jane Doe", "Right Book")

	src := filepath.Join(libraryDir, "Jane Doe", "Right Book (2020)", "Right Book - Jane Doe.epub")
	writeFileAt(t, src)

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, "")
	if err != nil {
		t.Fatalf("PreviewImportDestination: %v", err)
	}
	if got.Status != ReorgStatusNoop {
		t.Errorf("status = %q, want %q", got.Status, ReorgStatusNoop)
	}
}

func TestPreviewImportDestination_UnknownBook(t *testing.T) {
	t.Parallel()
	env, libraryDir, _, ctx := reorgFixture(t)
	src := filepath.Join(libraryDir, "book.epub")
	writeFileAt(t, src)

	if _, err := env.s.PreviewImportDestination(ctx, 4242, src, ""); err == nil {
		t.Fatal("expected an error for a book that does not exist")
	}
}
