//go:build !windows

package importer

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestReorganize_ExtensionlessFifoDoesNotHang(t *testing.T) {
	env, _, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	fifo := filepath.Join(t.TempDir(), "Bookepub")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, fifo); err != nil {
		t.Fatal(err)
	}
	setTemplate(t, env, ctx, "{Author}/{Title}.{ext}")
	moves := previewWithin(t, env, ctx, book.ID)
	if len(moves) != 1 || moves[0].Status != ReorgStatusError {
		t.Fatalf("preview = %+v, want one error", moves)
	}
}
