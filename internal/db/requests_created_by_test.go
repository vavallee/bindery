package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRequestRepo_RecordsWhoAskedForTheOwner covers migration 113: a request
// made on its owner's behalf keeps who made it, an ordinary one records
// nobody, a reopen records whoever asked this time, and deleting the admin
// who asked leaves the request with its owner.
func TestRequestRepo_RecordsWhoAskedForTheOwner(t *testing.T) {
	database, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")

	own := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")
	got, err := repo.GetByID(ctx, own.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.CreatedVia != "" || got.CreatedByUserID != nil || got.CreatedByUsername != "" {
		t.Fatalf("own request created via %q by %v (%q), want nobody", got.CreatedVia, got.CreatedByUserID, got.CreatedByUsername)
	}

	adminID := admin.ID
	forOwner := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: "OL2W", PayloadJSON: "{}",
		CreatedVia: models.RequestCreatedViaAdmin, CreatedByUserID: &adminID}
	if err := repo.Create(ctx, forOwner, 0); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, forOwner.ID)
	if got.OwnerUserID != owner.ID || got.CreatedVia != models.RequestCreatedViaAdmin ||
		got.CreatedByUserID == nil || *got.CreatedByUserID != admin.ID || got.CreatedByUsername != "admin" {
		t.Fatalf("request for owner = %+v", got)
	}
	items, _, err := repo.ListAll(ctx, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == forOwner.ID && it.CreatedByUsername != "admin" {
			t.Fatalf("queue row created by %q, want admin", it.CreatedByUsername)
		}
	}

	// Approve it, then reopen it as the API key: the reopen is a new ask.
	claimed, err := repo.Claim(ctx, forOwner.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, forOwner.ID, claimed.ClaimToken, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reopen(ctx, forOwner.ID, owner.ID, "", "{}", models.RequestCreatedViaAPIKey, nil, 0); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, forOwner.ID)
	if got.CreatedVia != models.RequestCreatedViaAPIKey || got.CreatedByUserID != nil {
		t.Fatalf("reopened by the key: via %q by %v", got.CreatedVia, got.CreatedByUserID)
	}

	// A request whose asking admin is deleted stays with its owner.
	byAdmin := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: "OL3W", PayloadJSON: "{}",
		CreatedVia: models.RequestCreatedViaAdmin, CreatedByUserID: &adminID}
	if err := repo.Create(ctx, byAdmin, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM users WHERE id = ?", admin.ID); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, byAdmin.ID)
	if err != nil || got == nil {
		t.Fatalf("request after its admin was deleted: %v", err)
	}
	if got.OwnerUserID != owner.ID || got.CreatedByUserID != nil || got.CreatedVia != models.RequestCreatedViaAdmin {
		t.Fatalf("after deleting the admin: %+v", got)
	}

	// The column refuses anything but the three values.
	bad := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: "OL4W", PayloadJSON: "{}", CreatedVia: "header"}
	if err := repo.Create(ctx, bad, 0); err == nil {
		t.Fatal("created_via \"header\" was stored")
	}
}
