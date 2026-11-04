package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func openUnmatchedRepo(t testing.TB) (*sql.DB, *UnmatchedUnitRepo) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, NewUnmatchedUnitRepo(database)
}

func scanUnit(path string) UnmatchedUnitScan {
	return UnmatchedUnitScan{
		UnitPath: path, UnitKind: UnmatchedKindFile, Format: "ebook", FileCount: 1, SizeBytes: 10,
		RootPath: "/lib", RelPath: strings.TrimPrefix(path, "/lib/"), AuthorFolder: "A",
		ParsedTitle: "T " + path, ParsedAuthor: "A", Reason: "no_title_match",
		MemberPaths: []string{path},
	}
}

func unitByPath(t *testing.T, database *sql.DB, repo *UnmatchedUnitRepo, path string) *UnmatchedUnit {
	t.Helper()
	var id int64
	err := database.QueryRow(`SELECT id FROM unmatched_units WHERE unit_path = ?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	u, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestApplyAdoptionSeriesHintCreatesAndReversesExactMembership(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	link, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, 0, "Saga", "2")
	if err != nil {
		t.Fatalf("apply hint: %v", err)
	}
	if link == nil || link.SeriesID == 0 || link.CreatedSeriesID != link.SeriesID || !link.Primary {
		t.Fatalf("link = %+v, want created primary membership", link)
	}
	stored, err := repo.Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SeriesLink == nil || stored.SeriesLink.SeriesID != link.SeriesID {
		t.Fatalf("stored link = %+v, want %+v", stored.SeriesLink, link)
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, stored.SeriesLink); err != nil {
		t.Fatalf("reverse hint: %v", err)
	}
	var memberships, series int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series_books WHERE series_id = ?`, link.SeriesID).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM series WHERE id = ?`, link.SeriesID).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 || series != 0 {
		t.Fatalf("after reverse memberships=%d series=%d, want both zero", memberships, series)
	}
}

func TestApplyAdoptionSeriesHintKeepsChangedMembership(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	link, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, 0, "Saga", "2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE series_books SET position_in_series = '2.5' WHERE series_id = ? AND book_id = ?`, link.SeriesID, book.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	var position string
	if err := database.QueryRow(`SELECT position_in_series FROM series_books WHERE series_id = ? AND book_id = ?`, link.SeriesID, book.ID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != "2.5" {
		t.Fatalf("position after reverse = %q, want preserved edit", position)
	}
}

func TestApplyAdoptionSeriesHintExistingAndRejectedTargets(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	seriesRepo := NewSeriesRepo(database)
	existing, err := seriesRepo.CreateManual(ctx, "Existing Saga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	if _, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, 99999, "Saga", "2"); !errors.Is(err, ErrAdoptionSeriesNotFound) {
		t.Fatalf("missing series error = %v, want ErrAdoptionSeriesNotFound", err)
	}
	link, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, existing.ID, "ignored", "2")
	if err != nil || link == nil || link.SeriesID != existing.ID {
		t.Fatalf("existing series link = %+v, %v", link, err)
	}
	if again, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, existing.ID, "ignored", "2"); err != nil || again == nil || again.SeriesID != 0 {
		t.Fatalf("existing membership = %+v, %v; want no-op marker", again, err)
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series_books WHERE series_id = ? AND book_id = ?`, existing.ID, book.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("existing series membership after reverse = %d, want zero", count)
	}
	if missing, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, "not-the-token", book.ID, existing.ID, "Saga", "2"); err != nil || missing != nil {
		t.Fatalf("lost claim = %+v, %v; want nil, nil", missing, err)
	}
}

func TestApplyAdoptionSeriesHintHonorsSeriesExclusion(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	series, err := NewSeriesRepo(database).CreateManual(ctx, "Excluded Saga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO book_series_exclusions (book_id, series_foreign_id, position_in_series) VALUES (?, ?, '')`, book.ID, series.ForeignID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	if _, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, series.ID, "Saga", "2"); !errors.Is(err, ErrAdoptionSeriesExcluded) {
		t.Fatalf("excluded series error = %v, want ErrAdoptionSeriesExcluded", err)
	}
}

func TestApplyAdoptionSeriesHintKeepsExistingPrimaryMembership(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	seriesRepo := NewSeriesRepo(database)
	primary, err := seriesRepo.CreateManual(ctx, "Primary Saga")
	if err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, primary.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	target, err := seriesRepo.CreateManual(ctx, "Other Saga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	link, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID, target.ID, "Other Saga", "2")
	if err != nil {
		t.Fatal(err)
	}
	if link.Primary {
		t.Fatalf("link = %+v, want non-primary membership", link)
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	var primaryCount, targetCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series_books WHERE series_id = ? AND book_id = ? AND primary_series = 1`, primary.ID, book.ID).Scan(&primaryCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM series_books WHERE series_id = ? AND book_id = ?`, target.ID, book.ID).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if primaryCount != 1 || targetCount != 0 {
		t.Fatalf("membership after reverse primary=%d target=%d, want 1 and 0", primaryCount, targetCount)
	}
}

func TestAdoptionSeriesLinkRecordRoundTripAndReset(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	want := &AdoptionSeriesLink{SeriesID: 7, BookID: book.ID, Position: "2", Primary: true}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: want.BookID, SeriesLink: want}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	stored, err := repo.Get(ctx, u.ID)
	if err != nil || stored.SeriesLink == nil || *stored.SeriesLink != *want {
		t.Fatalf("stored series link = %+v, %v; want %+v", stored.SeriesLink, err, want)
	}
	if ok, err := repo.ResetToPending(ctx, u.ID, UnmatchedStateAdopting, token); err != nil || !ok {
		t.Fatalf("reset = %v, %v", ok, err)
	}
	stored, err = repo.Get(ctx, u.ID)
	if err != nil || stored.SeriesLink != nil {
		t.Fatalf("link after reset = %+v, %v; want nil", stored.SeriesLink, err)
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, nil); err != nil {
		t.Fatalf("reverse nil link: %v", err)
	}
}

func TestUnmatchedUnitRejectsMalformedAdoptionSeriesLink(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	if _, err := database.Exec(`UPDATE unmatched_units SET series_link_json = '{not json' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, u.ID); err == nil || !strings.Contains(err.Error(), "series link") {
		t.Fatalf("malformed series link error = %v, want descriptive error", err)
	}
}

func TestApplyAdoptionSeriesHintRejectsChangedClaimedBook(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/Saga #2 - Two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/Saga #2 - Two.epub")
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim = %q, %v", token, err)
	}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("record adoption = %v, %v", ok, err)
	}
	if _, err := repo.ApplyAdoptionSeriesHint(ctx, u.ID, token, book.ID+1, 0, "Saga", "2"); err == nil || !strings.Contains(err.Error(), "claimed book changed") {
		t.Fatalf("changed claimed book error = %v, want validation error", err)
	}
}

func TestApplyAdoptionSeriesHintRefusesIncompleteHint(t *testing.T) {
	_, repo := openUnmatchedRepo(t)
	for _, args := range []struct {
		bookID          int64
		title, position string
	}{
		{title: "Saga", position: "2"},
		{bookID: 1, position: "2"},
		{bookID: 1, title: "Saga"},
	} {
		if _, err := repo.ApplyAdoptionSeriesHint(context.Background(), 1, "token", args.bookID, 0, args.title, args.position); err == nil {
			t.Fatalf("ApplyAdoptionSeriesHint(%+v) succeeded, want validation error", args)
		}
	}
}

func TestReverseAdoptionSeriesLinkPreservesChangedManualSeries(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	series, err := NewSeriesRepo(database).CreateManual(ctx, "Changed Saga")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSeriesRepo(database).LinkBook(ctx, series.ID, book.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if err := NewSeriesRepo(database).SetMonitored(ctx, series.ID, true); err != nil {
		t.Fatal(err)
	}
	link := &AdoptionSeriesLink{
		SeriesID: series.ID, BookID: book.ID, Position: "2", Primary: true,
		CreatedSeriesID: series.ID, CreatedSeriesForeignID: series.ForeignID, CreatedSeriesTitle: series.Title,
		CreatedSeriesMonitored: false,
	}
	if err := repo.ReverseAdoptionSeriesLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series WHERE id = ?`, series.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("changed series count = %d, want preserved series", remaining)
	}
}

// TestReconcileScan_KeepsDecisions covers the ReconcileScan contract: a rescan
// never moves an ignored row, a unit the scan stops seeing is removed while
// pending and kept once decided, and the counts come back.
func TestReconcileScan_KeepsDecisions(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)

	first := []UnmatchedUnitScan{scanUnit("/lib/A/one.epub"), scanUnit("/lib/A/two.epub"), scanUnit("/lib/A/three.epub")}
	res, err := repo.ReconcileScan(ctx, first, ReconcileScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 3 || res.Generation != 1 {
		t.Fatalf("first reconcile = %+v, want 3 pending in generation 1", res)
	}

	two := unitByPath(t, database, repo, "/lib/A/two.epub")
	if ok, err := repo.ClaimState(ctx, two.ID, UnmatchedStatePending, UnmatchedStateIgnored); err != nil || !ok {
		t.Fatalf("ignore two: ok=%v err=%v", ok, err)
	}

	// The second scan still sees two (ignored) and one, and no longer sees three.
	updated := scanUnit("/lib/A/one.epub")
	updated.ParsedTitle = "Renamed"
	res, err = repo.ReconcileScan(ctx, []UnmatchedUnitScan{updated, scanUnit("/lib/A/two.epub")}, ReconcileScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedPending != 1 || res.Pending != 1 || res.Ignored != 1 {
		t.Fatalf("second reconcile = %+v, want 1 removed, 1 pending, 1 ignored", res)
	}
	if got := unitByPath(t, database, repo, "/lib/A/two.epub"); got == nil || got.State != UnmatchedStateIgnored {
		t.Fatalf("ignored row after rescan = %+v, want still ignored", got)
	}
	if got := unitByPath(t, database, repo, "/lib/A/three.epub"); got != nil {
		t.Fatalf("vanished pending row survived: %+v", got)
	}
	if got := unitByPath(t, database, repo, "/lib/A/one.epub"); got.ParsedTitle != "Renamed" || got.ID == two.ID {
		t.Fatalf("pending row not refreshed in place: %+v", got)
	}
}

// TestReconcileScan_ZeroFileScanDeletesNothing is the unmounted volume case: a
// scan that found nothing must not erase a single row or decision.
func TestReconcileScan_ZeroFileScanDeletesNothing(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/one.epub"), scanUnit("/lib/A/two.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	two := unitByPath(t, database, repo, "/lib/A/two.epub")
	if _, err := repo.ClaimState(ctx, two.ID, UnmatchedStatePending, UnmatchedStateIgnored); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if _, err := database.Exec(`UPDATE unmatched_units SET last_seen_at = ?`, unitTime(old)); err != nil {
		t.Fatal(err)
	}
	res, err := repo.ReconcileScan(ctx, nil, ReconcileScanOptions{SkipDeletion: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedPending != 0 || res.Purged != 0 || res.Pending != 1 || res.Ignored != 1 {
		t.Fatalf("zero file reconcile = %+v, want nothing removed", res)
	}
}

// TestReconcileScan_PurgesOldDecisions: ignored rows unseen for 30 days go
// when their root produced files, a truncated scan removes and purges
// nothing, a root that produced no files keeps its ignores, and a recent one
// stays.
func TestReconcileScan_PurgesOldDecisions(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	units := []UnmatchedUnitScan{scanUnit("/lib/A/old.epub"), scanUnit("/lib/A/recent.epub")}
	if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET state = 'ignored'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET last_seen_at = ? WHERE unit_path = '/lib/A/old.epub'`,
		unitTime(time.Now().Add(-31*24*time.Hour))); err != nil {
		t.Fatal(err)
	}

	res, err := repo.ReconcileScan(ctx, nil, ReconcileScanOptions{SkipDeletion: true, RootsWithFiles: []string{"/lib"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 0 || res.Ignored != 2 {
		t.Fatalf("truncated reconcile = %+v, want no purge", res)
	}
	res, err = repo.ReconcileScan(ctx, nil, ReconcileScanOptions{RootsWithFiles: []string{"/audiobooks"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 0 || res.Ignored != 2 {
		t.Fatalf("reconcile with the row's root producing no files = %+v, want no purge", res)
	}
	res, err = repo.ReconcileScan(ctx, nil, ReconcileScanOptions{RootsWithFiles: []string{"/lib"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 1 || res.Ignored != 1 {
		t.Fatalf("reconcile = %+v, want the 31 day old row purged", res)
	}
	if unitByPath(t, database, repo, "/lib/A/recent.epub") == nil {
		t.Fatal("recent ignored row purged")
	}
}

func seedAdoptionBook(t *testing.T, database *sql.DB) *models.Book {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "ol:seed", Name: "Seed Author", SortName: "Author, Seed", MetadataProvider: "openlibrary"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:seed-book", AuthorID: author.ID, Title: "Seed",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return book
}

// TestReconcileScan_AdoptedRowSeenAgain: an adoption finished before the scan
// started whose unit is unmatched again was undone elsewhere, so it returns to
// pending; one finished while the scan ran is left alone.
func TestReconcileScan_AdoptedRowSeenAgain(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	units := []UnmatchedUnitScan{scanUnit("/lib/A/before.epub"), scanUnit("/lib/A/during.epub")}
	if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	book := seedAdoptionBook(t, database)
	for _, p := range []string{"/lib/A/before.epub", "/lib/A/during.epub"} {
		u := unitByPath(t, database, repo, p)
		token, _ := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
		if token == "" {
			t.Fatal("claim failed")
		}
		if ok, err := repo.CompleteAdoption(ctx, u.ID, token, AdoptionRecord{BookID: book.ID, PriorBookStatus: "skipped", Registered: []RegisteredFile{{Path: p, BookID: book.ID}}}); err != nil || !ok {
			t.Fatalf("complete: ok=%v err=%v", ok, err)
		}
	}
	scanStart := time.Now()
	if _, err := database.Exec(`UPDATE unmatched_units SET resolved_at = ? WHERE unit_path = '/lib/A/before.epub'`,
		unitTime(scanStart.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET resolved_at = ? WHERE unit_path = '/lib/A/during.epub'`,
		unitTime(scanStart.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{StartedAt: scanStart}); err != nil {
		t.Fatal(err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/before.epub"); got.State != UnmatchedStatePending || len(got.Registered) != 0 || got.PriorBookStatus != "" {
		t.Fatalf("stale adoption = %+v, want pending with nothing registered and empty prior_book_status", got)
	}
	if got := unitByPath(t, database, repo, "/lib/A/during.epub"); got.State != UnmatchedStateAdopted || got.PriorBookStatus != "skipped" {
		t.Fatalf("adoption during the scan = %+v, want still adopted with prior_book_status 'skipped'", got)
	}
}

// TestReconcileScan_ChunksAndLeavesClaims: more than one chunk of units
// lands, a truncated scan removes no pending row, and a scan never touches a
// row an adopt holds (its side effects are the adoption handler's to reverse).
func TestReconcileScan_ChunksAndLeavesClaims(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	units := make([]UnmatchedUnitScan, reconcileChunkSize*2+7)
	for i := range units {
		units[i] = scanUnit(fmt.Sprintf("/lib/A/%04d.epub", i))
	}
	res, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Upserted != len(units) || res.Pending != len(units) {
		t.Fatalf("reconcile = %+v, want %d rows", res, len(units))
	}
	claimed := unitByPath(t, database, repo, "/lib/A/0000.epub")
	if ok, _ := repo.ClaimState(ctx, claimed.ID, UnmatchedStatePending, UnmatchedStateAdopting); !ok {
		t.Fatal("claim failed")
	}
	if res, err = repo.ReconcileScan(ctx, units[1:2], ReconcileScanOptions{SkipDeletion: true}); err != nil || res.RemovedPending != 0 {
		t.Fatalf("truncated reconcile = %+v %v, want no pending row removed", res, err)
	}
	if _, err := repo.ReconcileScan(ctx, units[1:], ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/0000.epub"); got == nil || got.State != UnmatchedStateAdopting {
		t.Fatalf("claimed row after scans = %+v, want untouched", got)
	}
}

// TestStaleClaims_DatedByClaimedAt: a scan refreshes updated_at on every row
// it sees, so a claim is dated by claimed_at. A long held claim is stale even
// when a scan just touched the row; a fresh one is not.
func TestStaleClaims_DatedByClaimedAt(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	units := []UnmatchedUnitScan{scanUnit("/lib/A/old.epub"), scanUnit("/lib/A/new.epub")}
	if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/lib/A/old.epub", "/lib/A/new.epub"} {
		if ok, _ := repo.ClaimState(ctx, unitByPath(t, database, repo, p).ID, UnmatchedStatePending, UnmatchedStateAdopting); !ok {
			t.Fatal("claim failed")
		}
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET claimed_at = ? WHERE unit_path = '/lib/A/old.epub'`,
		unitTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.StaleClaims(ctx, time.Now().Add(-UnmatchedClaimTimeout))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].UnitPath != "/lib/A/old.epub" {
		t.Fatalf("stale claims = %+v, want only the hour old claim", stale)
	}
	if ok, err := repo.ResetToPending(ctx, stale[0].ID, UnmatchedStateAdopting, stale[0].ClaimToken); err != nil || !ok {
		t.Fatalf("reset: %v %v", ok, err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/old.epub"); got.State != UnmatchedStatePending || got.ClaimedAt != nil {
		t.Fatalf("reset row = %+v", got)
	}
}

// TestClaimState_ExactlyOneWinner is T3 at the repository: many goroutines
// racing to claim one row, one wins.
func TestClaimState_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/x.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/x.epub")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.ClaimState(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("claims won = %d, want 1", wins)
	}
}

// TestUnmatchedList_FiltersSortsAndEscapes covers the list parameters S9 names:
// an unknown sort falls back, search is folded and bound, and the folder
// filter is bound.
func TestUnmatchedList_FiltersSortsAndEscapes(t *testing.T) {
	ctx := context.Background()
	_, repo := openUnmatchedRepo(t)
	a := scanUnit("/lib/Weir/Hail Mary.epub")
	a.ParsedTitle, a.AuthorFolder, a.Candidates = "Project Hail Mary", "Weir", []UnmatchedCandidate{{BookID: 1, Score: 0.7}}
	b := scanUnit("/lib/Other/100% Done.epub")
	b.ParsedTitle, b.AuthorFolder, b.FileCount = "100% Done", "Other", 3
	c := scanUnit("/lib/Other/100 Done.epub")
	c.ParsedTitle, c.AuthorFolder, c.Reason = "100 Done", "Other", "author_not_in_library"
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{a, b, c}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}

	items, total, err := repo.List(ctx, UnmatchedListQuery{Sort: "score); DROP TABLE unmatched_units; --"})
	if err != nil || total != 3 || items[0].ParsedTitle != "Project Hail Mary" {
		t.Fatalf("default list = %d items, total %d, err %v", len(items), total, err)
	}
	items, _, err = repo.List(ctx, UnmatchedListQuery{Search: "hail MARY"})
	if err != nil || len(items) != 1 || items[0].ParsedTitle != "Project Hail Mary" {
		t.Fatalf("search = %+v err %v", items, err)
	}
	// A bare wildcard folds away to no term at all: every row, not an error
	// and not a pattern.
	if _, total, err = repo.List(ctx, UnmatchedListQuery{Search: "%_"}); err != nil || total != 3 {
		t.Fatalf("wildcard search total = %d err %v, want 3", total, err)
	}
	items, _, err = repo.List(ctx, UnmatchedListQuery{AuthorFolder: "Other' OR '1'='1", Sort: "files"})
	if err != nil || len(items) != 0 {
		t.Fatalf("injected folder matched %d rows", len(items))
	}
	items, _, err = repo.List(ctx, UnmatchedListQuery{AuthorFolder: "Other", Sort: "files"})
	if err != nil || len(items) != 2 || items[0].FileCount != 3 {
		t.Fatalf("folder list sorted by files = %+v err %v", items, err)
	}

	f, err := repo.Facets(ctx, UnmatchedListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Folders) != 2 || f.Folders[0].Folder != "Other" || f.Folders[0].Units != 2 || f.Folders[0].Files != 4 || f.Folders[0].NotInLibrary != 1 {
		t.Fatalf("folder facets = %+v", f.Folders)
	}
	if len(f.Reasons) != 2 || len(f.Formats) != 1 {
		t.Fatalf("facets = %+v", f)
	}
}

func queryPlan(t *testing.T, database *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := database.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, " | ")
}

// TestUnmatchedList_QueryPlansUseIndexes is P4: the default list and every sort
// search an index for the state filter instead of scanning the table.
func TestUnmatchedList_QueryPlansUseIndexes(t *testing.T) {
	database, _ := openUnmatchedRepo(t)
	for _, sort := range []string{"", "score", "title", "folder", "files", "size", "seen"} {
		q := UnmatchedListQuery{Sort: sort}
		where, args := unmatchedWhere(q, true)
		joined := queryPlan(t, database, `SELECT id FROM unmatched_units WHERE `+where+
			` ORDER BY `+unmatchedOrderBy(q.Sort, q.Dir)+` LIMIT 50`, args...)
		if !strings.Contains(joined, "INDEX idx_unmatched_units_state") || strings.Contains(joined, "SCAN unmatched_units") {
			t.Errorf("sort %q plan does not search a state index: %s", sort, joined)
		}
		if (sort == "" || sort == "score" || sort == "folder") && strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("sort %q should be served in index order: %s", sort, joined)
		}
	}
}

// TestBookRefs_OneQueryPerPage hydrates many ids through a single statement.
func TestBookRefs_OneQueryPerPage(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "ol:a", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := range 5 {
		b := &models.Book{ForeignID: fmt.Sprintf("ol:b%d", i), AuthorID: author.ID, Title: fmt.Sprintf("Book %d", i),
			Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	refs, err := repo.BookRefs(ctx, append(ids, 99999))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 5 || refs[ids[2]].AuthorName != "Ann Leckie" || refs[ids[2]].Title != "Book 2" {
		t.Fatalf("refs = %+v", refs)
	}
}

// TestMigrate088_OverPopulatedDatabase is T4: 088 applied to a database that
// already holds authors, books, notifications and users adds an empty table,
// leaves every existing row alone, and clears a book reference on delete
// instead of blocking it.
func TestMigrate088_OverPopulatedDatabase(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, stmt := range []string{
		`DROP INDEX idx_unmatched_units_book`, `DROP INDEX idx_unmatched_units_state_reason`,
		`DROP INDEX idx_unmatched_units_state_folder`, `DROP INDEX idx_unmatched_units_state_score`,
		`DROP TABLE unmatched_units`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "ol:a", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:b", AuthorID: author.ID, Title: "Ancillary Justice",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := NewNotificationRepo(database).Create(ctx, &models.Notification{Name: "hook", Type: "webhook", URL: "http://example.invalid", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewUserRepo(database).Create(ctx, "reader", "hash"); err != nil {
		t.Fatal(err)
	}

	// 098 and 100 add columns to the table 088 creates, so they rerun with it.
	for _, file := range []string{"088_unmatched_units.sql", "098_unmatched_units_prior_status.sql", "100_unmatched_units_files_author.sql", "118_unmatched_units_series_link.sql"} {
		v := migrationVersionForTest(t, file)
		if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version = ?`, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migrations 088, 098, and 100: %v", err)
	}

	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM unmatched_units`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unmatched_units after migration: n=%d err=%v", n, err)
	}
	for table, want := range map[string]int{"authors": 1, "books": 1, "notifications": 1, "users": 1} {
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Errorf("%s rows after migration = %d (err %v), want %d", table, n, err, want)
		}
	}

	repo := NewUnmatchedUnitRepo(database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/x.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/x.epub")
	if u.State != UnmatchedStatePending {
		t.Fatalf("new row state = %q, want pending by default", u.State)
	}
	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteAdoption(ctx, u.ID, token, AdoptionRecord{BookID: book.ID, CreatedBookID: book.ID, CreatedAuthorID: author.ID}); err != nil {
		t.Fatal(err)
	}
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatalf("delete a book an adoption points at: %v", err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/x.epub"); got.BookID != 0 || got.CreatedBookID != 0 {
		t.Fatalf("book references after delete = %+v, want cleared", got)
	}
}

func TestUnmatchedUnits_PriorBookStatusForBook(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	books := NewBookRepo(database)
	authors := NewAuthorRepo(database)
	author := &models.Author{ForeignID: "ol:a", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:b", AuthorID: author.ID, Title: "Ancillary Justice",
		Status: models.BookStatusSkipped, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	u1 := scanUnit("/lib/A/part1.epub")
	u2 := scanUnit("/lib/A/part2.epub")
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{u1, u2}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	unit1 := unitByPath(t, database, repo, "/lib/A/part1.epub")
	unit2 := unitByPath(t, database, repo, "/lib/A/part2.epub")

	// Initially no prior status
	status, err := repo.PriorBookStatusForBook(ctx, book.ID, unit2.ID)
	if err != nil || status != "" {
		t.Fatalf("before adoption: status=%q err=%v, want empty", status, err)
	}

	// Adopt unit1 with prior status skipped
	token1, _ := repo.Claim(ctx, unit1.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	rec1 := AdoptionRecord{BookID: book.ID, PriorBookStatus: "skipped", Registered: []RegisteredFile{{Path: "/lib/A/part1.epub", BookID: book.ID}}}
	if ok, err := repo.CompleteAdoption(ctx, unit1.ID, token1, rec1); err != nil || !ok {
		t.Fatalf("complete unit1: %v", err)
	}

	// Now querying for unit2 finds unit1's recorded prior status "skipped"
	status, err = repo.PriorBookStatusForBook(ctx, book.ID, unit2.ID)
	if err != nil || status != "skipped" {
		t.Fatalf("after unit1 adoption: status=%q err=%v, want 'skipped'", status, err)
	}

	// Querying for unit1 (exceptID=unit1.ID) excludes unit1 itself
	status, err = repo.PriorBookStatusForBook(ctx, book.ID, unit1.ID)
	if err != nil || status != "" {
		t.Fatalf("for unit1 itself: status=%q err=%v, want empty", status, err)
	}
}

// BenchmarkReconcileScan is T6: one full reconcile at the scan's unit cap.
func BenchmarkReconcileScan(b *testing.B) {
	ctx := context.Background()
	units := make([]UnmatchedUnitScan, 20000)
	for i := range units {
		units[i] = scanUnit(fmt.Sprintf("/lib/Author %03d/Book %05d/book.epub", i%400, i))
		units[i].Candidates = []UnmatchedCandidate{{BookID: int64(i), Score: 0.7}}
	}
	_, repo := openUnmatchedRepo(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := repo.ReconcileScan(ctx, units, ReconcileScanOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
