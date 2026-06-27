package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// writeMoveFile is a small helper for the MoveFileCtx tests.
func writeMoveFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to NOT exist, stat err=%v", path, err)
	}
}

func mustHaveContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("content of %s = %q, want %q", path, string(got), want)
	}
}

// TestMoveFileCtx_Rename covers the fast path: os.Rename succeeds (same
// filesystem), dst lands with the right content and src disappears.
func TestMoveFileCtx_Rename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "book.epub")
	dst := filepath.Join(dir, "library", "Author", "Title", "book.epub")
	writeMoveFile(t, src, "hello world")

	if err := MoveFileCtx(context.Background(), src, dst); err != nil {
		t.Fatalf("MoveFileCtx: %v", err)
	}

	mustHaveContent(t, dst, "hello world")
	mustNotExist(t, src)
}

// TestMoveFileCtx_CrossDeviceCopyFallback forces os.Rename to fail with EXDEV
// (the cross-filesystem error that triggers the copy+verify+delete slow path)
// and asserts the slow path copies the file, the verify passes, and the
// source is removed.
func TestMoveFileCtx_CrossDeviceCopyFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "book.epub")
	dst := filepath.Join(dir, "library", "book.epub")
	writeMoveFile(t, src, "the quick brown fox")

	// Force the cross-device branch: real os.Rename within one tempdir would
	// succeed, so override the seam to return EXDEV exactly as a real
	// cross-filesystem rename would.
	defer restoreMoveSeams()
	moveFileRename = func(_, _ string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }

	if err := MoveFileCtx(context.Background(), src, dst); err != nil {
		t.Fatalf("MoveFileCtx (cross-device fallback): %v", err)
	}

	mustHaveContent(t, dst, "the quick brown fox")
	mustNotExist(t, src)
}

// TestMoveFileCtx_VerifyMismatchKeepsSource is the data-safety invariant: if
// the cross-filesystem copy produces a dst whose size does not match src
// (truncated / partial write), MoveFileCtx must return an error, remove the
// bad dst, and LEAVE THE SOURCE INTACT so the still-seeding/original file is
// never destroyed by a corrupt copy.
func TestMoveFileCtx_VerifyMismatchKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "book.epub")
	dst := filepath.Join(dir, "library", "book.epub")
	const srcContent = "complete original payload"
	writeMoveFile(t, src, srcContent)

	defer restoreMoveSeams()
	// Trigger the slow path...
	moveFileRename = func(_, _ string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	// ...and make the "copy" produce a short/wrong-size dst so verify fails.
	moveFileCopy = func(_ context.Context, _, dst string) error {
		return os.WriteFile(dst, []byte("short"), 0o644)
	}

	err := MoveFileCtx(context.Background(), src, dst)
	if err == nil {
		t.Fatal("expected size-mismatch error, got nil")
	}

	// Source MUST survive untouched: this is the data-loss guard.
	mustHaveContent(t, src, srcContent)
	// Bad dst must be cleaned up.
	mustNotExist(t, dst)
}

// TestMoveFileCtx_ContextCancelKeepsSource cancels the context before the
// copy fallback runs. copyFileCtx returns ctx.Err() and removes any partial
// dst; MoveFileCtx must surface the error and the source must be preserved.
func TestMoveFileCtx_ContextCancelKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "book.epub")
	dst := filepath.Join(dir, "library", "book.epub")
	const srcContent = "still seeding, do not lose me"
	writeMoveFile(t, src, srcContent)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the copy starts

	defer restoreMoveSeams()
	moveFileRename = func(_, _ string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	// The real copyFileCtx races io.Copy of the tiny file against ctx.Done(),
	// so a pre-cancelled ctx is non-deterministic for a small file. To assert
	// MoveFileCtx's contract deterministically we drive the copy through a
	// hook that reports the cancellation the way the real copyFileCtx does on
	// its ctx.Done() branch: remove any partial dst and return ctx.Err(). The
	// point under test is that MoveFileCtx propagates that error and never
	// removes the source.
	moveFileCopy = func(c context.Context, _, dst string) error {
		_ = os.Remove(dst)
		return c.Err()
	}

	err := MoveFileCtx(ctx, src, dst)
	if err == nil {
		t.Fatal("expected ctx-cancel error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got %v", err)
	}

	// Source preserved; no half-written dst left behind.
	mustHaveContent(t, src, srcContent)
	mustNotExist(t, dst)
}

// restoreMoveSeams resets the test-only indirection hooks back to the real
// production functions.
func restoreMoveSeams() {
	moveFileRename = os.Rename
	moveFileCopy = copyFileCtx
}
