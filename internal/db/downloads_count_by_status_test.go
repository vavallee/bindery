package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestDownloadRepo_CountByStatus checks the bindery_queue_items query counts
// every owner's downloads per status, reports nothing for an empty status,
// and surfaces a database error rather than an empty map.
func TestDownloadRepo_CountByStatus(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	empty, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("count on an empty table: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty table counted %v, want no entries", empty)
	}

	users := NewUserRepo(database)
	a, err := users.Create(ctx, "count-a", "x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := users.Create(ctx, "count-b", "x")
	if err != nil {
		t.Fatal(err)
	}
	userA, userB := a.ID, b.ID
	rows := []struct {
		status models.DownloadState
		owner  int64
	}{
		{models.StateDownloading, userA},
		{models.StateDownloading, userB},
		{models.StateDownloading, 0},
		{models.StateImportFailed, userA},
		{models.StateImported, userB},
	}
	for i, r := range rows {
		d := &models.Download{GUID: fmt.Sprintf("count-%d", i), Title: "t", NZBURL: "fake://x", Status: r.status, OwnerUserID: r.owner}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	got, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"downloading": 3, "importFailed": 1, "imported": 1}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}

	database.Close()
	if _, err := repo.CountByStatus(ctx); err == nil {
		t.Error("expected an error from a closed database")
	}
}
