package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A history row keeps its owner after its book is deleted (book_id is ON
// DELETE SET NULL), and a row with neither owner nor book is not shown to a
// user's scoped list. Before rows carried an owner, every bookless row was
// returned to every user, so deleting a book published its history.
func TestHistoryListPage_DeletedBookHistoryStaysWithOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "x")
	bob, _ := users.Create(ctx, "bob", "x")
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	history := NewHistoryRepo(database)

	a := &models.Author{ForeignID: "OL-A", Name: "Alice Author", SortName: "a", MetadataProvider: "ol", OwnerUserID: alice.ID}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-B", AuthorID: a.ID, Title: "Alice Private Book", SortTitle: "x", Status: models.BookStatusImported,
		MetadataProvider: "ol", Genres: []string{}, OwnerUserID: alice.ID}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	grab := &models.HistoryEvent{BookID: &b.ID, EventType: models.HistoryEventGrabbed, SourceTitle: "Alice.Private.Book.EPUB", Data: `{"guid":"g1"}`}
	imported := &models.HistoryEvent{BookID: &b.ID, EventType: models.HistoryEventBookImported, SourceTitle: "Alice.Private.Book.EPUB", Data: `{"path":"/library/Alice Author/Alice Private Book/x.epub"}`}
	// A bookless row nobody can be tied to.
	orphan := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "Unattributed.Grab", Data: `{"guid":"g2"}`}
	// Alice's own free text grab: no book, stamped with her as the grabber.
	freeText := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "Alice.Free.Text", Data: `{"guid":"g3"}`, OwnerUserID: alice.ID}
	for _, e := range []*models.HistoryEvent{grab, imported, orphan, freeText} {
		if err := history.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	list := func(uid int64) map[string]int {
		evs, total, err := history.ListPage(ctx, HistoryListOpts{UserID: uid, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if total != len(evs) {
			t.Errorf("total %d != rows %d", total, len(evs))
		}
		seen := map[string]int{}
		for _, e := range evs {
			seen[e.SourceTitle]++
		}
		return seen
	}

	if got := list(bob.ID); len(got) != 0 {
		t.Errorf("bob sees %v before the delete, want nothing", got)
	}
	if err := books.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := list(bob.ID); len(got) != 0 {
		t.Errorf("bob sees %v after alice deleted her book, want nothing", got)
	}
	if got := list(alice.ID); got["Alice.Private.Book.EPUB"] != 2 || got["Alice.Free.Text"] != 1 || got["Unattributed.Grab"] != 0 {
		t.Errorf("alice sees %v, want her two book rows and her free text grab only", got)
	}
	if got := list(0); len(got) != 3 {
		t.Errorf("unscoped list = %v, want every row", got)
	}

	if owner, ok, err := history.GetOwnerByID(ctx, grab.ID); err != nil || !ok || owner != alice.ID {
		t.Errorf("GetOwnerByID(deleted book's grab) = %d,%v,%v, want alice", owner, ok, err)
	}
	if owner, ok, err := history.GetOwnerByID(ctx, orphan.ID); err != nil || !ok || owner != 0 {
		t.Errorf("GetOwnerByID(orphan) = %d,%v,%v, want 0", owner, ok, err)
	}
	for name, list := range map[string]func() ([]models.HistoryEvent, error){
		"ListForUser": func() ([]models.HistoryEvent, error) { return history.ListForUser(ctx, bob.ID) },
		"ListByTypeAndUser": func() ([]models.HistoryEvent, error) {
			return history.ListByTypeAndUser(ctx, models.HistoryEventGrabbed, bob.ID)
		},
	} {
		if evs, err := list(); err != nil || len(evs) != 0 {
			t.Errorf("%s(bob) = %d rows, %v; want none", name, len(evs), err)
		}
	}
}

// Create takes the book's owner when the book has one, and otherwise the
// owner the caller supplies; an unowned row of a shared book is visible to
// every user who can see that book.
func TestHistoryCreate_OwnerPrecedence(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "x")
	bob, _ := users.Create(ctx, "bob", "x")
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	history := NewHistoryRepo(database)

	a := &models.Author{ForeignID: "OL-A", Name: "A", SortName: "a", MetadataProvider: "ol"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	owned := &models.Book{ForeignID: "OL-1", AuthorID: a.ID, Title: "Owned", SortTitle: "owned", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}, OwnerUserID: alice.ID}
	shared := &models.Book{ForeignID: "OL-2", AuthorID: a.ID, Title: "Shared", SortTitle: "shared", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}}
	for _, b := range []*models.Book{owned, shared} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id = NULL WHERE id = ?", shared.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		bookID   *int64
		fallback int64
		want     int64
	}{
		{"book owner wins over the fallback", &owned.ID, bob.ID, alice.ID},
		{"shared book takes the fallback", &shared.ID, bob.ID, bob.ID},
		{"shared book with no fallback stays unowned", &shared.ID, 0, 0},
		{"no book takes the fallback", nil, bob.ID, bob.ID},
	}
	for _, tc := range cases {
		e := &models.HistoryEvent{BookID: tc.bookID, EventType: models.HistoryEventGrabbed, SourceTitle: tc.name, OwnerUserID: tc.fallback}
		if err := history.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, _ := history.GetByID(ctx, e.ID)
		if got.OwnerUserID != tc.want {
			t.Errorf("%s: owner = %d, want %d", tc.name, got.OwnerUserID, tc.want)
		}
	}

	evs, _, err := history.ListPage(ctx, HistoryListOpts{UserID: alice.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.SourceTitle] = true
	}
	if !seen["book owner wins over the fallback"] || !seen["shared book with no fallback stays unowned"] || seen["shared book takes the fallback"] || seen["no book takes the fallback"] {
		t.Errorf("alice's scoped list = %v", seen)
	}
}

// Migration 117 stamps existing history rows: from the book's owner where the
// row has a book, otherwise from the owner of the download its GUID names, and
// leaves the rest unowned. Data that is not JSON must not break the upgrade.
func TestMigrate117BackfillsHistoryOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()
	database := openFileDB(t, path)

	users := NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "x")
	bob, _ := users.Create(ctx, "bob", "x")
	a := &models.Author{ForeignID: "OL-A", Name: "A", SortName: "a", MetadataProvider: "ol"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	aliceBook := &models.Book{ForeignID: "OL-1", AuthorID: a.ID, Title: "Alice", SortTitle: "alice", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}, OwnerUserID: alice.ID}
	sharedBook := &models.Book{ForeignID: "OL-2", AuthorID: a.ID, Title: "Shared", SortTitle: "shared", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}}
	for _, b := range []*models.Book{aliceBook, sharedBook} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id = NULL WHERE id = ?", sharedBook.ID); err != nil {
		t.Fatal(err)
	}
	downloads := NewDownloadRepo(database)
	for _, d := range []*models.Download{
		{GUID: "bob-guid", Title: "Bob dl", Status: models.StateImported, OwnerUserID: bob.ID},
		{GUID: "alice-guid", Title: "Alice dl", Status: models.StateImported, OwnerUserID: alice.ID},
		{GUID: "nobody-guid", Title: "Unowned dl", Status: models.StateImported},
	} {
		if err := downloads.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	// Rewind to the previous release: no owner column, migration 117 not
	// recorded. Rows are then written the way that release wrote them.
	for _, stmt := range []string{
		"DROP INDEX idx_history_owner_user_id",
		"ALTER TABLE history DROP COLUMN owner_user_id",
		"DELETE FROM schema_migrations WHERE version = 117",
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("rewind %q: %v", stmt, err)
		}
	}
	rows := []struct {
		title  string
		bookID any
		data   string
		want   int64
	}{
		{"alice book row", aliceBook.ID, `{"guid":"bob-guid"}`, alice.ID},
		{"deleted book, bob's download", nil, `{"guid":"bob-guid","indexer":"x"}`, bob.ID},
		{"shared book, alice's download", sharedBook.ID, `{"guid":"alice-guid"}`, alice.ID},
		{"shared book, no download", sharedBook.ID, `{"message":"x"}`, 0},
		{"unowned download", nil, `{"guid":"nobody-guid"}`, 0},
		{"unknown guid", nil, `{"guid":"never-seen"}`, 0},
		{"guid not a string", nil, `{"guid":42}`, 0},
		{"not json", nil, `not json at all`, 0},
		{"empty data", nil, ``, 0},
		{"json array", nil, `["bob-guid"]`, 0},
	}
	for _, r := range rows {
		if _, err := database.Exec(
			"INSERT INTO history (book_id, event_type, source_title, data) VALUES (?, 'grabbed', ?, ?)",
			r.bookID, r.title, r.data,
		); err != nil {
			t.Fatalf("seed %q: %v", r.title, err)
		}
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	for _, r := range rows {
		var owner sql.NullInt64
		if err := database.QueryRow("SELECT owner_user_id FROM history WHERE source_title = ?", r.title).Scan(&owner); err != nil {
			t.Fatalf("%s: %v", r.title, err)
		}
		if owner.Int64 != r.want {
			t.Errorf("%s: owner after upgrade = %v, want %d", r.title, owner, r.want)
		}
	}
}

// Deleting a user hands their history to the reassign target, or removes it
// with a purge, the same way their library goes.
func TestUserDelete_HistoryFollowsStrategy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy UserDeleteStrategy
		reassign bool
	}{
		{"reassign", ReassignOwnedRows, true},
		{"purge", PurgeOwnedRows, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users, _, database, adminID, victimID, _ := newDeleteFixture(t)
			ctx := context.Background()
			history := NewHistoryRepo(database)
			ev := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "Victim.Grab", OwnerUserID: victimID}
			if err := history.Create(ctx, ev); err != nil {
				t.Fatal(err)
			}
			plan := UserDeletePlan{Strategy: tc.strategy}
			if tc.reassign {
				plan.ReassignTo = &adminID
			}
			if err := users.Delete(ctx, victimID, plan); err != nil {
				t.Fatalf("delete: %v", err)
			}
			got, err := history.GetByID(ctx, ev.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.reassign && (got == nil || got.OwnerUserID != adminID):
				t.Errorf("reassign: history row = %+v, want owned by %d", got, adminID)
			case !tc.reassign && got != nil:
				t.Errorf("purge: history row survived: %+v", got)
			}
		})
	}
}

// rewindTo116 opens a migrated file database and puts it back to the schema
// before migration 117, then runs seed with foreign keys off on one
// connection so a test can plant rows a later release could not write.
func rewindTo116(t *testing.T, path string, seed ...string) {
	t.Helper()
	ctx := context.Background()
	database := openFileDB(t, path)
	defer database.Close()
	conn, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmts := append([]string{
		"DROP INDEX idx_history_owner_user_id",
		"ALTER TABLE history DROP COLUMN owner_user_id",
		"DELETE FROM schema_migrations WHERE version = 117",
		"PRAGMA foreign_keys=OFF",
	}, seed...)
	stmts = append(stmts, "PRAGMA foreign_keys=ON")
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			t.Fatalf("rewind %q: %v", s, err)
		}
	}
}

// An owner id on a book or download that names a user who no longer exists
// (deleted while foreign keys were off) must not fail the upgrade: the history
// row is left unowned instead of breaking the users(id) foreign key and
// keeping Bindery from starting.
func TestMigrate117DanglingOwnersLeaveHistoryUnowned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()
	database := openFileDB(t, path)
	a := &models.Author{ForeignID: "OL-A", Name: "A", SortName: "a", MetadataProvider: "ol"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-1", AuthorID: a.ID, Title: "B", SortTitle: "b", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := NewDownloadRepo(database).Create(ctx, &models.Download{GUID: "ghost-guid", Title: "x", Status: models.StateImported}); err != nil {
		t.Fatal(err)
	}
	database.Close()

	rewindTo116(t, path,
		"UPDATE books SET owner_user_id = 999",
		"UPDATE downloads SET owner_user_id = 999 WHERE guid = 'ghost-guid'",
		"INSERT INTO history (book_id, event_type, source_title, data) VALUES (1, 'grabbed', 'ghost book', '{}')",
		"INSERT INTO history (book_id, event_type, source_title, data) VALUES (NULL, 'grabbed', 'ghost download', '{\"guid\":\"ghost-guid\"}')",
	)

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade with owners naming a deleted user: %v", err)
	}
	defer upgraded.Close()
	for _, title := range []string{"ghost book", "ghost download"} {
		var owner sql.NullInt64
		if err := upgraded.QueryRow("SELECT owner_user_id FROM history WHERE source_title = ?", title).Scan(&owner); err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		if owner.Valid {
			t.Errorf("%s: owner = %d, want NULL", title, owner.Int64)
		}
	}
}

// Migration 117 clears the stored recommendations of users who are not
// admins, which were built from every user's library, so their next refresh
// rebuilds them scoped. Admin batches, dismissals and author exclusions stay.
func TestMigrate117ClearsNonAdminRecommendations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()
	database := openFileDB(t, path)
	users := NewUserRepo(database)
	admin, err := users.CreateFirstAdmin(ctx, "admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := users.Create(ctx, "bob", "x")
	recs := NewRecommendationRepo(database)
	for _, uid := range []int64{admin.ID, bob.ID} {
		if err := recs.ReplaceBatch(ctx, uid, []models.RecommendationCandidate{
			{ForeignID: "OL-rec-1", Title: "One", RecType: models.RecTypeAuthorNew},
			{ForeignID: "OL-rec-2", Title: "Two", RecType: models.RecTypeAuthorNew},
		}); err != nil {
			t.Fatal(err)
		}
		if err := recs.AddAuthorExclusion(ctx, uid, "Excluded Author"); err != nil {
			t.Fatal(err)
		}
	}
	bobRecs, err := recs.List(ctx, bob.ID, "", 10, 0)
	if err != nil || len(bobRecs) != 2 {
		t.Fatalf("seed bob recs = %d, %v", len(bobRecs), err)
	}
	if err := recs.Dismiss(ctx, bob.ID, bobRecs[0].ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	rewindTo116(t, path)
	upgraded := openFileDB(t, path)
	defer upgraded.Close()
	recs = NewRecommendationRepo(upgraded)

	count := func(uid int64) int {
		var n int
		if err := upgraded.QueryRow("SELECT COUNT(*) FROM recommendations WHERE user_id = ?", uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(bob.ID); n != 0 {
		t.Errorf("bob keeps %d stored recommendations after the upgrade, want 0", n)
	}
	if n := count(admin.ID); n != 2 {
		t.Errorf("admin has %d stored recommendations after the upgrade, want 2", n)
	}
	if dismissed, err := recs.ListDismissedIDs(ctx, bob.ID); err != nil || !dismissed[bobRecs[0].ForeignID] {
		t.Errorf("bob's dismissal lost: %v, %v", dismissed, err)
	}
	if excl, err := recs.ListAuthorExclusions(ctx, bob.ID); err != nil || len(excl) != 1 {
		t.Errorf("bob's author exclusions = %v, %v; want kept", excl, err)
	}
}

// Deleting a user clears their own Refresh all status row.
func TestUserDelete_ClearsRefreshStatus(t *testing.T) {
	users, _, database, adminID, victimID, _ := newDeleteFixture(t)
	ctx := context.Background()
	settings := NewSettingsRepo(database)
	for _, uid := range []int64{adminID, victimID} {
		if err := settings.Set(ctx, AuthorBulkRefreshUserKey(uid), `{"status":"completed"}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := users.Delete(ctx, victimID, UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &adminID}); err != nil {
		t.Fatal(err)
	}
	if s, _ := settings.Get(ctx, AuthorBulkRefreshUserKey(victimID)); s != nil {
		t.Errorf("deleted user's refresh status survives: %q", s.Value)
	}
	if s, _ := settings.Get(ctx, AuthorBulkRefreshUserKey(adminID)); s == nil {
		t.Error("another user's refresh status was cleared")
	}
}

// SeriesRepo.GetByIDForUser carries each book's owner, which the recommender's
// in memory visibility check reads.
func TestSeriesGetByIDForUser_CarriesBookOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	alice, _ := NewUserRepo(database).Create(ctx, "alice", "x")
	a := &models.Author{ForeignID: "OL-A", Name: "A", SortName: "a", MetadataProvider: "ol"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-1", AuthorID: a.ID, Title: "B", SortTitle: "b", Status: models.BookStatusWanted, MetadataProvider: "ol", Genres: []string{}, OwnerUserID: alice.ID}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	series := NewSeriesRepo(database)
	s := &models.Series{ForeignID: "OL-S", Title: "S"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, b.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	full, err := series.GetByIDForUser(ctx, s.ID, 0)
	if err != nil || full == nil || len(full.Books) != 1 {
		t.Fatalf("series = %+v, %v", full, err)
	}
	if got := full.Books[0].Book.OwnerUserID; got != alice.ID {
		t.Errorf("series book owner = %d, want alice (%d)", got, alice.ID)
	}
}
