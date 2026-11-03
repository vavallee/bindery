package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// An automatic grab for a book with no owner (what an admin's Add Book
// stores) is itself stored with no owner. A user's queue list leaves it out,
// and the per id write check the queue's delete and retry routes use must
// refuse it to that user as well, while an admin may still act on it.
func TestAutoGrabForUnownedBook_NotWritableByUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newRegrabFixture(t)
	ctx := context.Background()
	f.book.OwnerUserID = 0
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	rows, err := f.downloads.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("want one grab, got %d (err %v)", len(rows), err)
	}
	owner, _, err := f.downloads.GetOwnerByID(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner != 0 {
		t.Fatalf("auto grab owner = %d, want none", owner)
	}
	userCtx := auth.WithUserRole(auth.WithUserID(ctx, f.alice), "user")
	if listed, _ := f.downloads.ListActiveByUser(ctx, f.alice, time.Now().Add(-time.Hour)); len(listed) != 0 {
		t.Fatalf("user's queue lists %d ownerless rows, want 0", len(listed))
	}
	if auth.CheckWriteOwnership(userCtx, owner) {
		t.Errorf("user %d may act on ownerless auto grab %d", f.alice, rows[0].ID)
	}
	adminCtx := auth.WithUserRole(auth.WithUserID(ctx, f.alice), "admin")
	if !auth.CheckWriteOwnership(adminCtx, owner) {
		t.Error("admin may not act on the ownerless auto grab")
	}
}
