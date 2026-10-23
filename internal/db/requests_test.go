package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func newRequestsFixture(t *testing.T) (*sql.DB, *RequestRepo, *UserRepo) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, NewRequestRepo(database), NewUserRepo(database)
}

func mustUser(t *testing.T, users *UserRepo, name string) *User {
	t.Helper()
	u, err := users.Create(context.Background(), name, "x")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustRequest(t *testing.T, repo *RequestRepo, owner int64, kind, foreignID string) *models.LibraryRequest {
	t.Helper()
	req := &models.LibraryRequest{OwnerUserID: owner, Kind: kind, ForeignID: foreignID, Title: "T " + foreignID, PayloadJSON: `{"kind":"` + kind + `","foreignId":"` + foreignID + `"}`}
	if err := repo.Create(context.Background(), req, 0); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRequestRepo_CreateDedupesPerOwner(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	alice, bob := mustUser(t, users, "alice"), mustUser(t, users, "bob")

	mustRequest(t, repo, alice.ID, models.RequestKindBook, "OL1W")
	dup := &models.LibraryRequest{OwnerUserID: alice.ID, Kind: models.RequestKindBook, ForeignID: "OL1W", PayloadJSON: "{}"}
	if err := repo.Create(ctx, dup, 0); !errors.Is(err, ErrRequestExists) {
		t.Fatalf("second request for the same book: err = %v, want ErrRequestExists", err)
	}
	// Another owner, or another kind, is a separate request.
	mustRequest(t, repo, bob.ID, models.RequestKindBook, "OL1W")
	mustRequest(t, repo, alice.ID, models.RequestKindAuthor, "OL1W")
}

// TestRequestRepo_OwnerIsolation: every owner scoped method sees only the
// owner's rows, whatever the tenancy setting.
func TestRequestRepo_OwnerIsolation(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	alice, bob := mustUser(t, users, "alice"), mustUser(t, users, "bob")
	a := mustRequest(t, repo, alice.ID, models.RequestKindBook, "OL1W")
	mustRequest(t, repo, bob.ID, models.RequestKindBook, "OL2W")
	mustRequest(t, repo, bob.ID, models.RequestKindAuthor, "OL3A")

	items, total, err := repo.ListByOwner(ctx, alice.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != a.ID {
		t.Fatalf("alice sees %d of total %d: %+v, want only her own request", len(items), total, items)
	}
	if got, _ := repo.GetForOwner(ctx, alice.ID, models.RequestKindBook, "OL2W"); got != nil {
		t.Fatalf("GetForOwner returned bob's request to alice: %+v", got)
	}
	if err := repo.DeletePendingForOwner(ctx, a.ID, bob.ID); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("bob withdrawing alice's request: err = %v, want ErrRequestNotPending", err)
	}
	if n, _ := repo.CountPendingByOwner(ctx, bob.ID); n != 2 {
		t.Fatalf("bob pending = %d, want 2", n)
	}
	if n, _ := repo.CountPending(ctx); n != 3 {
		t.Fatalf("all pending = %d, want 3", n)
	}
	if err := repo.DeletePendingForOwner(ctx, a.ID, alice.ID); err != nil {
		t.Fatalf("alice withdrawing her own request: %v", err)
	}
}

// TestRequestRepo_ClaimIsExclusive is plan item T3 for requests: two
// approvals racing for one request, exactly one claim succeeds. Run with
// -race.
func TestRequestRepo_ClaimIsExclusive(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	req := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, losses := 0, 0
	token := ""
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := repo.Claim(ctx, req.ID, admin.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				token = claimed.ClaimToken
			case errors.Is(err, ErrRequestNotPending):
				losses++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || losses != racers-1 {
		t.Fatalf("claims: %d won, %d lost; want exactly one winner", wins, losses)
	}
	if err := repo.Decline(ctx, req.ID, admin.ID, "no"); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("decline of a claimed request: err = %v, want ErrRequestNotPending", err)
	}
	if err := repo.Complete(ctx, req.ID, "not-the-token", nil, nil); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("complete with another claim's token: err = %v, want ErrRequestNotPending", err)
	}
	if err := repo.Complete(ctx, req.ID, token, nil, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := repo.Claim(ctx, req.ID, admin.ID); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("claim after approval: err = %v, want ErrRequestNotPending", err)
	}
}

func TestRequestRepo_ReleaseAndStaleClaim(t *testing.T) {
	database, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	req := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")

	claimed, err := repo.Claim(ctx, req.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Release(ctx, req.ID, claimed.ClaimToken); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, req.ID)
	if got.Status != models.RequestStatusPending || got.DecidedBy != nil {
		t.Fatalf("after release: status %q decided_by %v, want pending and nil", got.Status, got.DecidedBy)
	}

	first, err := repo.Claim(ctx, req.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh claim holds.
	if _, err := repo.Claim(ctx, req.ID, admin.ID); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("second claim inside the TTL: err = %v, want ErrRequestNotPending", err)
	}
	// An abandoned one (the process died mid approval) can be retaken.
	old := time.Now().Add(-2 * RequestClaimTTL).UnixMilli()
	if _, err := database.ExecContext(ctx, "UPDATE requests SET claimed_at = ? WHERE id = ?", old, req.ID); err != nil {
		t.Fatal(err)
	}
	second, err := repo.Claim(ctx, req.ID, admin.ID)
	if err != nil {
		t.Fatalf("claim over a stale claim: %v", err)
	}
	// The retaken claim belongs to the second approval: the first can no
	// longer renew, complete or release it.
	if err := repo.RenewClaim(ctx, req.ID, first.ClaimToken); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("renew with the lost token: err = %v", err)
	}
	if err := repo.Release(ctx, req.ID, first.ClaimToken); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetByID(ctx, req.ID); got.Status != models.RequestStatusApproving {
		t.Fatalf("release with the lost token changed status to %q", got.Status)
	}
	if err := repo.RenewClaim(ctx, req.ID, second.ClaimToken); err != nil {
		t.Fatalf("renew with the live token: %v", err)
	}
}

func TestRequestRepo_DeclineAndReopen(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	a := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")
	b := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL2W")

	if err := repo.Decline(ctx, a.ID, admin.ID, "not this one"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Decline(ctx, a.ID, admin.ID, "again"); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("second decline: err = %v", err)
	}
	got, _ := repo.GetByID(ctx, a.ID)
	if got.Status != models.RequestStatusDeclined || got.DeclineReason != "not this one" || got.DecidedAt == nil {
		t.Fatalf("declined row = %+v", got)
	}
	if err := repo.Reopen(ctx, a.ID, owner.ID, "", "{}", "", nil, 0); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("reopen of a declined request: err = %v, want refusal", err)
	}

	claimedB, err := repo.Claim(ctx, b.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, b.ID, claimedB.ClaimToken, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reopen(ctx, b.ID, admin.ID, "", "{}", "", nil, 0); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("reopen by someone other than the owner: err = %v", err)
	}
	if err := repo.Reopen(ctx, b.ID, owner.ID, "audiobook", `{"x":1}`, "", nil, 0); err != nil {
		t.Fatalf("owner reopen of an approved request: %v", err)
	}
	got, _ = repo.GetByID(ctx, b.ID)
	if got.Status != models.RequestStatusPending || got.MediaType != "audiobook" || got.DecidedAt != nil {
		t.Fatalf("reopened row = %+v", got)
	}
}

// TestRequestRepo_ListDerivesFulfilment: a book request reports its book's
// status by join, an author request its books' import progress, and the
// progress for a page comes from one grouped query.
func TestRequestRepo_ListDerivesFulfilment(t *testing.T) {
	database, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	for _, s := range []string{
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (10, 'OL10A', 'Writer', 'Writer')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title, status) VALUES (100, 'OL100W', 10, 'One', 'One', 'imported')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title, status) VALUES (101, 'OL101W', 10, 'Two', 'Two', 'wanted')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title, status) VALUES (102, 'OL102W', 10, 'Three', 'Three', 'imported')`,
	} {
		if _, err := database.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	bookReq := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL101W")
	authorReq := mustRequest(t, repo, owner.ID, models.RequestKindAuthor, "OL10A")
	tokens := map[int64]string{}
	for _, id := range []int64{bookReq.ID, authorReq.ID} {
		c, err := repo.Claim(ctx, id, admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		tokens[id] = c.ClaimToken
	}
	bookID, authorID := int64(101), int64(10)
	if err := repo.Complete(ctx, bookReq.ID, tokens[bookReq.ID], &bookID, &authorID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, authorReq.ID, tokens[authorReq.ID], nil, &authorID); err != nil {
		t.Fatal(err)
	}

	items, _, err := repo.ListByOwner(ctx, owner.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]models.LibraryRequest{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got := byID[bookReq.ID]; got.BookStatus != models.BookStatusWanted {
		t.Errorf("book request BookStatus = %q, want wanted", got.BookStatus)
	}
	if got := byID[authorReq.ID]; got.AuthorBooks != 3 || got.AuthorBooksImported != 2 {
		t.Errorf("author request progress = %d of %d, want 2 of 3", got.AuthorBooksImported, got.AuthorBooks)
	}

	queue, total, err := repo.ListAll(ctx, models.RequestStatusApproved, 50, 0)
	if err != nil || total != 2 || len(queue) != 2 || queue[0].OwnerUsername != "reader" {
		t.Fatalf("admin queue = %+v total %d err %v", queue, total, err)
	}
}

// TestRequestRepo_DeletingTheUserRemovesTheirRequests: owner_user_id
// cascades, so deleting a requester leaves no orphan requests and does not
// block the delete.
func TestRequestRepo_DeletingTheUserRemovesTheirRequests(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	owner := mustUser(t, users, "reader")
	req := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")
	if err := repo.Decline(ctx, req.ID, admin.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, owner.ID, UserDeletePlan{Strategy: ReassignOwnedRows}); err != nil {
		t.Fatalf("delete requester: %v", err)
	}
	if got, _ := repo.GetByID(ctx, req.ID); got != nil {
		t.Fatalf("request survived its owner: %+v", got)
	}
	// The deciding admin can be deleted too; decided_by goes NULL.
	other := mustUser(t, users, "reader2")
	req2 := mustRequest(t, repo, other.ID, models.RequestKindBook, "OL2W")
	second := mustUser(t, users, "admin2")
	if err := users.SetRole(ctx, second.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Decline(ctx, req2.ID, second.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, second.ID, UserDeletePlan{Strategy: ReassignOwnedRows}); err != nil {
		t.Fatalf("delete deciding admin: %v", err)
	}
	if got, _ := repo.GetByID(ctx, req2.ID); got == nil || got.DecidedBy != nil {
		t.Fatalf("after deleting the deciding admin: %+v, want the request kept with decided_by NULL", got)
	}
}

// TestMigration089_OnPopulatedDatabase is plan item T4: 089 applied over a
// database that already has users, authors and notifications adds the table
// empty and leaves every existing notification with on_request_created = 0,
// so no new alert fires until an admin turns it on.
func TestMigration089_OnPopulatedDatabase(t *testing.T) {
	database, _, users := newRequestsFixture(t)
	ctx := context.Background()
	for _, s := range []string{
		`DROP TABLE requests`,
		`ALTER TABLE notifications DROP COLUMN on_request_created`,
		`DELETE FROM schema_migrations WHERE version = 89`,
	} {
		if _, err := database.ExecContext(ctx, s); err != nil {
			t.Fatalf("roll back 089 (%s): %v", s, err)
		}
	}
	mustUser(t, users, "existing")
	for _, s := range []string{
		`INSERT INTO authors (foreign_id, name, sort_name) VALUES ('OL1A', 'A', 'A')`,
		`INSERT INTO notifications (name, type, url, method, headers, on_grab, on_import, on_upgrade, on_failure, on_health, enabled)
		 VALUES ('hook', 'webhook', 'https://example.test/h', 'POST', '', 1, 1, 1, 1, 1, 1)`,
	} {
		if _, err := database.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed (%s): %v", s, err)
		}
	}

	content, err := migrationsFS.ReadFile("migrations/089_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(database, 89, "089_requests.sql", string(content)); err != nil {
		t.Fatalf("apply 089 over populated data: %v", err)
	}

	var onRequest, n int
	if err := database.QueryRowContext(ctx, `SELECT on_request_created FROM notifications WHERE name = 'hook'`).Scan(&onRequest); err != nil {
		t.Fatal(err)
	}
	if onRequest != 0 {
		t.Errorf("existing notification on_request_created = %d, want 0", onRequest)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&n); err != nil || n != 0 {
		t.Errorf("requests rows = %d err %v, want an empty table", n, err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = 'existing'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("existing user lost: %d %v", n, err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO requests (owner_user_id, kind, foreign_id, status) VALUES (1, 'movie', 'x', 'pending')`); err == nil {
		t.Error("kind CHECK accepted 'movie'")
	}
}
