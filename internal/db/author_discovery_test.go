package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func newDiscoveryTestAuthor(t *testing.T, repo *AuthorRepo, foreignID string, mutate func(*models.Author)) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: foreignID, SortName: foreignID, Monitored: true}
	if mutate != nil {
		mutate(a)
	}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("create author %s: %v", foreignID, err)
	}
	return a
}

// TestListDiscoveryDue_FiltersAndOrder pins who scheduled discovery visits and
// in what order (#2236): monitored authors only, never those whose new items
// setting is none, never Calibre shells; never checked authors first, then the
// oldest check; nobody checked more recently than the cutoff.
func TestListDiscoveryDue_FiltersAndOrder(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-168 * time.Hour)

	neverA := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	old := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	older := newDiscoveryTestAuthor(t, repo, "OL3A", nil)
	recent := newDiscoveryTestAuthor(t, repo, "OL4A", nil)
	neverB := newDiscoveryTestAuthor(t, repo, "OL5A", nil)
	newDiscoveryTestAuthor(t, repo, "OL6A", func(a *models.Author) { a.Monitored = false })
	newDiscoveryTestAuthor(t, repo, "OL7A", func(a *models.Author) { a.MonitorNewItems = models.AuthorMonitorNewItemsNone })
	newDiscoveryTestAuthor(t, repo, "calibre:author:9", nil)
	// A hand-made author has no catalogue to discover from (#3840).
	newDiscoveryTestAuthor(t, repo, "manual:author:dan-abnett", nil)

	for id, when := range map[int64]time.Time{
		old.ID:    now.Add(-200 * time.Hour),
		older.ID:  now.Add(-400 * time.Hour),
		recent.ID: now.Add(-2 * time.Hour),
	} {
		if err := repo.StampDiscovery(ctx, id, when); err != nil {
			t.Fatal(err)
		}
	}

	eligible, err := repo.CountDiscoveryEligible(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if eligible != 5 {
		t.Errorf("CountDiscoveryEligible = %d, want 5 (the unmonitored, none, calibre and manual authors are not eligible)", eligible)
	}

	due, err := repo.ListDiscoveryDue(ctx, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, a := range due {
		got = append(got, a.ID)
	}
	want := []int64{neverA.ID, neverB.ID, older.ID, old.ID}
	if len(got) != len(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due = %v, want %v (never checked first, then oldest)", got, want)
		}
	}

	limited, err := repo.ListDiscoveryDue(ctx, cutoff, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 3 {
		t.Errorf("limit 3 returned %d authors", len(limited))
	}
}

// TestStampDiscovery_SurvivesWholeRowUpdate checks the cursor is its own
// column: a whole row author Update, written from a snapshot taken before the
// stamp, must not reset it.
func TestStampDiscovery_SurvivesWholeRowUpdate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	a := newDiscoveryTestAuthor(t, repo, "OL10A", nil)
	snapshot := *a
	when := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	if err := repo.StampDiscovery(ctx, a.ID, when); err != nil {
		t.Fatal(err)
	}
	snapshot.Description = "edited by the user"
	if err := repo.Update(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := repo.LastDiscoveryAt(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Equal(when) {
		t.Fatalf("LastDiscoveryAt = %v, want %v", got, when)
	}
	missing, err := repo.LastDiscoveryAt(ctx, 99999)
	if err != nil || missing != nil {
		t.Fatalf("LastDiscoveryAt(missing) = %v, %v, want nil, nil", missing, err)
	}
}

// TestMigration087_OverPopulatedDatabase applies 087 to a database that
// already holds authors and notifications, the upgrade every existing install
// takes (T4). Existing notifications must come out with on_book_announced = 0
// so nothing new fires until an admin turns it on, and existing authors with
// no discovery cursor, so the first ticks visit them.
func TestMigration087_OverPopulatedDatabase(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	// Roll the schema back to 086: drop what 087 adds and forget it ran.
	for _, stmt := range []string{
		`ALTER TABLE notifications DROP COLUMN on_book_announced`,
		`ALTER TABLE authors DROP COLUMN last_discovery_at`,
		`DELETE FROM schema_migrations WHERE version = 87`,
	} {
		if _, err := database.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// Populate it the way an 086 install would have.
	if _, err := database.ExecContext(ctx, `
		INSERT INTO notifications (name, type, url, method, headers, on_grab, on_import, on_upgrade, on_failure, on_health, enabled)
		VALUES ('everything on', 'webhook', 'https://example.test/hook', 'POST', '{}', 1, 1, 1, 1, 1, 1),
		       ('defaults', 'webhook', 'https://example.test/other', 'POST', '{}', 1, 1, 0, 1, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO authors (foreign_id, name, sort_name, monitored) VALUES ('OL20A', 'Existing Author', 'Author, Existing', 1)`); err != nil {
		t.Fatal(err)
	}

	const name = "087_author_discovery.sql"
	content, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(database, 87, name, string(content)); err != nil {
		t.Fatalf("apply 087: %v", err)
	}

	notifications := NewNotificationRepo(database)
	list, err := notifications.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d notifications, want 2", len(list))
	}
	for _, n := range list {
		if n.OnBookAnnounced {
			t.Errorf("notification %q came out of the migration with on_book_announced on", n.Name)
		}
		if !n.OnGrab || !n.Enabled {
			t.Errorf("notification %q lost its existing toggles: %+v", n.Name, n)
		}
	}

	authors := NewAuthorRepo(database)
	existing, err := authors.GetByForeignID(ctx, "OL20A")
	if err != nil || existing == nil {
		t.Fatalf("existing author lost: %v", err)
	}
	if got, err := authors.LastDiscoveryAt(ctx, existing.ID); err != nil || got != nil {
		t.Errorf("existing author discovery cursor = %v, %v, want nil (never checked)", got, err)
	}

	// Round trip: the new toggle saves and loads through every statement.
	n := list[0]
	n.OnBookAnnounced = true
	if err := notifications.Update(ctx, &n); err != nil {
		t.Fatal(err)
	}
	reloaded, err := notifications.GetByID(ctx, n.ID)
	if err != nil || reloaded == nil || !reloaded.OnBookAnnounced {
		t.Fatalf("OnBookAnnounced did not survive Update: %+v, %v", reloaded, err)
	}
	created := &models.Notification{Name: "new", Type: "webhook", URL: "https://example.test/new", Method: "POST", Headers: "{}", OnBookAnnounced: true, Enabled: true}
	if err := notifications.Create(ctx, created); err != nil {
		t.Fatal(err)
	}
	if got, _ := notifications.GetByID(ctx, created.ID); got == nil || !got.OnBookAnnounced {
		t.Fatalf("OnBookAnnounced did not survive Create: %+v", got)
	}
	plain := &models.Notification{Name: "plain", Type: "webhook", URL: "https://example.test/plain", Method: "POST", Headers: "{}", Enabled: true}
	if err := notifications.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if got, _ := notifications.GetByID(ctx, plain.ID); got == nil || got.OnBookAnnounced {
		t.Fatalf("a new notification defaulted OnBookAnnounced on: %+v", got)
	}
}
