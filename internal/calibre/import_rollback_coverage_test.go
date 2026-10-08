package calibre

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// covRollback is a rollback fixture whose runs, snapshots and provenance are
// written by hand, so each branch of the planner can be reached directly
// rather than through whatever an import happens to record.
type covRollback struct {
	t        *testing.T
	ctx      context.Context
	imp      *Importer
	database *sql.DB
	authors  *db.AuthorRepo
	books    *db.BookRepo
	editions *db.EditionRepo
	runs     *db.CalibreImportRunRepo
	snaps    *db.CalibreEntitySnapshotRepo
	prov     *db.CalibreProvenanceRepo
	seq      int
}

func newCovRollback(t *testing.T) *covRollback {
	t.Helper()
	imp, _, authors, books, editions, runs, snaps, prov, database := newRollbackFixtureWithDB(t)
	return &covRollback{t: t, ctx: context.Background(), imp: imp, database: database,
		authors: authors, books: books, editions: editions, runs: runs, snaps: snaps, prov: prov}
}

func (c *covRollback) run(dryRun bool) int64 {
	c.t.Helper()
	r := &models.CalibreImportRun{LibraryPath: "/lib", Status: runStatusCompleted, DryRun: dryRun}
	if err := c.runs.Create(c.ctx, r); err != nil {
		c.t.Fatal(err)
	}
	return r.ID
}

func (c *covRollback) author(name string) *models.Author {
	c.t.Helper()
	c.seq++
	a := &models.Author{ForeignID: fmt.Sprintf("covA%d", c.seq), Name: name, SortName: name, Monitored: true}
	if err := c.authors.Create(c.ctx, a); err != nil {
		c.t.Fatal(err)
	}
	return a
}

func (c *covRollback) book(authorID int64, title string) *models.Book {
	c.t.Helper()
	c.seq++
	b := &models.Book{ForeignID: fmt.Sprintf("covB%d", c.seq), AuthorID: authorID, Title: title, SortTitle: title,
		Status: models.BookStatusImported, Monitored: true, AnyEditionOK: true, MetadataProvider: "calibre"}
	if err := c.books.Create(c.ctx, b); err != nil {
		c.t.Fatal(err)
	}
	return b
}

// entity records a snapshot row and, unless owner is 0, a provenance row
// owned by run owner pointing at provLocal.
func (c *covRollback) entity(runID int64, entityType, externalID string, localID int64, outcome, metadata string, owner, provLocal int64) {
	c.t.Helper()
	if err := c.snaps.Record(c.ctx, &models.CalibreEntitySnapshot{RunID: runID, EntityType: entityType,
		ExternalID: externalID, LocalID: localID, Outcome: outcome, MetadataJSON: metadata}); err != nil {
		c.t.Fatal(err)
	}
	if owner != 0 {
		c.link(entityType, externalID, provLocal, owner)
	}
}

func (c *covRollback) link(entityType, externalID string, localID, owner int64) {
	c.t.Helper()
	rid := owner
	if err := c.prov.Upsert(c.ctx, &models.CalibreProvenance{EntityType: entityType, ExternalID: externalID,
		LocalID: localID, ImportRunID: &rid}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *covRollback) exec(sql string) {
	c.t.Helper()
	if _, err := c.database.ExecContext(c.ctx, sql); err != nil {
		c.t.Fatalf("%s: %v", sql, err)
	}
}

func covBookMeta(t *testing.T, before, after *bookRollbackSnapshot) string {
	t.Helper()
	env, err := bookSnapshotMetadata(nil, before, after)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func covAuthorMeta(t *testing.T, before, after *authorRollbackSnapshot) string {
	t.Helper()
	env, err := authorSnapshotMetadata(nil, before, after)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func covFindAction(t *testing.T, res *RollbackResult, externalID string) RollbackAction {
	t.Helper()
	for _, a := range res.Actions {
		if a.ExternalID == externalID {
			return a
		}
	}
	t.Fatalf("no action for %s in %+v", externalID, res.Actions)
	return RollbackAction{}
}

func TestRollback_UnavailableAndDryRun(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bare := NewImporter(db.NewAuthorRepo(database), db.NewAuthorAliasRepo(database), db.NewBookRepo(database),
		db.NewEditionRepo(database), db.NewSettingsRepo(database))
	if _, err := bare.PreviewRollback(ctx, 1); !errors.Is(err, ErrRollbackUnavailable) {
		t.Errorf("no run tracking: %v, want ErrRollbackUnavailable", err)
	}
	if runs, err := bare.RecentRuns(ctx, 5); runs != nil || err != nil {
		t.Errorf("RecentRuns without tracking = %v, %v; want nil", runs, err)
	}
	if run, err := bare.GetRun(ctx, 1); run != nil || err != nil {
		t.Errorf("GetRun without tracking = %v, %v; want nil", run, err)
	}
	tracked := NewImporter(nil, nil, nil, nil, nil).WithRunTracking(db.NewCalibreImportRunRepo(database),
		db.NewCalibreEntitySnapshotRepo(database), db.NewCalibreProvenanceRepo(database))
	if _, err := tracked.Rollback(ctx, 1); !errors.Is(err, ErrRollbackUnavailable) {
		t.Errorf("tracking without entity repos: %v, want ErrRollbackUnavailable", err)
	}

	c := newCovRollback(t)
	runID := c.run(true)
	a := c.author("Dry Author")
	c.entity(runID, entityTypeAuthor, "author:1", a.ID, outcomeCreated, "", runID, a.ID)
	res, err := c.imp.Rollback(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Actions) != 0 || res.Applied {
		t.Fatalf("dry-run rollback = %+v, want a no-op", res)
	}
	if got, _ := c.authors.GetByID(ctx, a.ID); got == nil {
		t.Fatal("dry-run rollback deleted an author")
	}
	got, err := c.imp.GetRun(ctx, runID)
	if err != nil || got == nil || got.ID != runID {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
}

// One run exercising every skip and retain branch of the planner, previewed
// and then executed.
func TestRollback_PlannerBranches(t *testing.T) {
	c := newCovRollback(t)
	ctx := c.ctx
	runID := c.run(false)
	otherRun := c.run(false)

	kept := c.author("Kept Author")               // still has a book after rollback
	shared := c.author("Shared Author")           // another run links it too
	restored := c.author("New Name")              // updated in place
	notOwned := c.author("Taken Over")            // a later run owns it
	keptBook := c.book(kept.ID, "Imported Title") // updated in place
	sharedBook := c.book(kept.ID, "Shared Book")  // another run links it too
	lostBook := c.book(kept.ID, "Lost Book")      // deleted before rollback
	otherBook := c.book(kept.ID, "Elsewhere")

	// Authors.
	c.entity(runID, entityTypeAuthor, "author:kept", kept.ID, outcomeCreated, "", runID, kept.ID)
	c.entity(runID, entityTypeAuthor, "author:shared", shared.ID, outcomeCreated, "", runID, shared.ID)
	c.link(entityTypeAuthor, "author:shared-elsewhere", shared.ID, otherRun)
	c.entity(runID, entityTypeAuthor, "author:restored", restored.ID, outcomeUpdated,
		covAuthorMeta(t, &authorRollbackSnapshot{Name: "Old Name", SortName: "New Name", Monitored: true},
			&authorRollbackSnapshot{Name: "New Name", SortName: "New Name", Monitored: true}), runID, restored.ID)
	c.entity(runID, entityTypeAuthor, "author:nosnap", restored.ID, outcomeUpdated, "{}", runID, restored.ID)
	c.entity(runID, entityTypeAuthor, "author:notowned", notOwned.ID, outcomeCreated, "", otherRun, notOwned.ID)
	// Books.
	c.entity(runID, entityTypeBook, "book:shared", sharedBook.ID, outcomeCreated, "", runID, sharedBook.ID)
	c.link(entityTypeBook, "book:shared-elsewhere", sharedBook.ID, otherRun)
	c.entity(runID, entityTypeBook, "book:notowned", otherBook.ID, outcomeCreated, "", otherRun, otherBook.ID)
	c.entity(runID, entityTypeBook, "book:nosnap", otherBook.ID, outcomeUpdated, "not json", runID, otherBook.ID)
	c.entity(runID, entityTypeBook, "book:restored", keptBook.ID, outcomeUpdated,
		covBookMeta(t, &bookRollbackSnapshot{Title: "Original Title", AuthorID: kept.ID},
			&bookRollbackSnapshot{Title: "Imported Title", AuthorID: kept.ID}), runID, keptBook.ID)
	c.entity(runID, entityTypeBook, "book:lost", lostBook.ID, outcomeUpdated,
		covBookMeta(t, &bookRollbackSnapshot{Title: "A"}, &bookRollbackSnapshot{Title: "B"}), runID, lostBook.ID)
	// Editions.
	c.entity(runID, entityTypeEdition, "edition:updated", 900, outcomeUpdated, "", runID, 900)
	c.entity(runID, entityTypeEdition, "edition:notowned", 901, outcomeCreated, "", otherRun, 901)
	// Book files.
	c.entity(runID, entityTypeBookFile, "book_file:updated", keptBook.ID, outcomeUpdated, "", runID, keptBook.ID)
	c.entity(runID, entityTypeBookFile, "book_file:notowned", keptBook.ID, outcomeCreated, "", otherRun, keptBook.ID)
	c.entity(runID, entityTypeBookFile, "garbage-key", keptBook.ID, outcomeCreated, "", runID, keptBook.ID)
	// Series links: the fixture has no series repo.
	c.entity(runID, entityTypeSeriesLink, "calibre:series-link:1:2", keptBook.ID, outcomeUpdated, "", runID, keptBook.ID)
	c.entity(runID, entityTypeSeriesLink, "calibre:series-link:3:4", keptBook.ID, outcomeCreated, "", otherRun, keptBook.ID)
	c.entity(runID, entityTypeSeriesLink, "calibre:series-link:5:6", keptBook.ID, outcomeCreated, "", runID, keptBook.ID)
	// Provenance trouble and unknown types.
	c.entity(runID, entityTypeBook, "book:gone", otherBook.ID, outcomeCreated, "", 0, 0)
	c.entity(runID, entityTypeBook, "book:moved", otherBook.ID, outcomeCreated, "", runID, keptBook.ID)

	if err := c.books.Delete(ctx, lostBook.ID); err != nil {
		t.Fatal(err)
	}

	want := map[string]struct{ action, reason string }{
		"author:kept":             {"unlink_provenance", "still has linked books"},
		"author:shared":           {"unlink_provenance", "another Calibre import link"},
		"author:restored":         {"restore_author", ""},
		"author:nosnap":           {"skip", "no usable snapshot"},
		"author:notowned":         {"skip", "no longer the current provenance owner for this author"},
		"book:shared":             {"unlink_provenance", "another Calibre import link"},
		"book:notowned":           {"skip", "no longer the current provenance owner for this book"},
		"book:nosnap":             {"skip", "no usable snapshot"},
		"book:restored":           {"restore_book", ""},
		"edition:updated":         {"skip", "edition was not created by this run"},
		"edition:notowned":        {"skip", "no longer the current provenance owner for this edition"},
		"book_file:updated":       {"skip", "not created by this run"},
		"book_file:notowned":      {"skip", "no longer the current provenance owner for this book file"},
		"garbage-key":             {"skip", "unparseable book file provenance key"},
		"calibre:series-link:1:2": {"skip", "not created by this run"},
		"calibre:series-link:3:4": {"skip", "no longer the current provenance owner for this series link"},
		"calibre:series-link:5:6": {"skip", "series repository not configured"},
		"book:gone":               {"skip", "already rolled back"},
		"book:moved":              {"skip", "points to a different local entity"},
	}

	preview, err := c.imp.PreviewRollback(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Preview || preview.Applied {
		t.Fatalf("preview flags = %+v", preview)
	}
	for ext, w := range want {
		a := covFindAction(t, preview, ext)
		if a.Action != w.action || !strings.Contains(a.Reason, w.reason) {
			t.Errorf("%s: action %q reason %q, want %q containing %q", ext, a.Action, a.Reason, w.action, w.reason)
		}
	}
	if a := covFindAction(t, preview, "book:lost"); a.Action != "restore_book" {
		t.Errorf("book:lost preview action = %q; preview cannot know the row is gone", a.Action)
	}
	if a := covFindAction(t, preview, "author:restored"); a.DisplayName != "New Name" {
		t.Errorf("display name = %q, want the author's current name", a.DisplayName)
	}
	if a := covFindAction(t, preview, "garbage-key"); a.DisplayName != "Imported Title" {
		t.Errorf("book file display name = %q, want the book title", a.DisplayName)
	}
	// Preview writes nothing.
	if got, _ := c.authors.GetByID(ctx, restored.ID); got.Name != "New Name" {
		t.Fatalf("preview restored the author to %q", got.Name)
	}

	res, err := c.imp.Rollback(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Status != runStatusRolledBack {
		t.Fatalf("execute = %+v, want applied and rolled back", res)
	}
	if a := covFindAction(t, res, "book:lost"); a.Action != "skip" || a.Reason != "book no longer exists" {
		t.Errorf("book:lost = %+v, want skipped as gone", a)
	}
	if got, _ := c.authors.GetByID(ctx, restored.ID); got == nil || got.Name != "Old Name" {
		t.Errorf("restored author = %+v, want name Old Name", got)
	}
	if got, _ := c.books.GetByID(ctx, keptBook.ID); got == nil || got.Title != "Original Title" {
		t.Errorf("restored book = %+v, want title Original Title", got)
	}
	for _, id := range []int64{kept.ID, shared.ID, notOwned.ID} {
		if got, _ := c.authors.GetByID(ctx, id); got == nil {
			t.Errorf("author %d was deleted; every author here is retained", id)
		}
	}
	if got, _ := c.books.GetByID(ctx, sharedBook.ID); got == nil {
		t.Error("shared book deleted; another run still links it")
	}
	for _, ext := range []string{"author:kept", "author:shared", "author:restored", "book:shared", "book:restored"} {
		et := entityTypeAuthor
		if strings.HasPrefix(ext, "book:") {
			et = entityTypeBook
		}
		if p, _ := c.prov.GetByExternal(ctx, defaultSourceID, et, ext); p != nil {
			t.Errorf("provenance %s survived execute", ext)
		}
	}
	if p, _ := c.prov.GetByExternal(ctx, defaultSourceID, entityTypeAuthor, "author:shared-elsewhere"); p == nil {
		t.Error("another run's provenance was removed")
	}
	if _, err := c.imp.Rollback(ctx, runID); !errors.Is(err, ErrAlreadyRolledBack) {
		t.Errorf("second rollback: %v, want ErrAlreadyRolledBack", err)
	}
}

// A provenance table that cannot be read makes preview report every entity
// as failed, and execute refuse outright, leaving the run as it was.
func TestRollback_ProvenanceLookupFailure(t *testing.T) {
	c := newCovRollback(t)
	runID := c.run(false)
	a := c.author("Lookup Author")
	c.entity(runID, entityTypeAuthor, "author:lookup", a.ID, outcomeCreated, "", runID, a.ID)

	c.exec(`ALTER TABLE calibre_provenance RENAME TO calibre_provenance_hidden`)
	preview, err := c.imp.PreviewRollback(c.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Stats.Failed != 1 || preview.Actions[0].Action != "inspect" || preview.Actions[0].DisplayName != "Lookup Author" {
		t.Fatalf("preview = %+v, want one inspect action", preview)
	}
	if _, err := c.imp.Rollback(c.ctx, runID); err == nil || !strings.Contains(err.Error(), "provenance lookup") {
		t.Fatalf("execute err = %v, want a provenance lookup error", err)
	}
	c.exec(`ALTER TABLE calibre_provenance_hidden RENAME TO calibre_provenance`)
	if run, _ := c.runs.GetByID(c.ctx, runID); run.Status != runStatusCompleted {
		t.Fatalf("run status = %q, want completed after a failed rollback", run.Status)
	}
	if got, _ := c.authors.GetByID(c.ctx, a.ID); got == nil {
		t.Fatal("a failed rollback deleted the author")
	}
}

// Each write the execute path makes aborts the whole rollback when it fails.
// A trigger refuses one write at a time; the run must stay completed and
// nothing must change.
func TestRollback_WriteFailuresAbort(t *testing.T) {
	c := newCovRollback(t)
	ctx := c.ctx

	type scenario struct {
		name    string
		trigger string
		seed    func(runID int64)
		wantErr string
	}
	block := func(when, table string) string {
		return fmt.Sprintf(`CREATE TRIGGER cov_block %s ON %s BEGIN SELECT RAISE(FAIL, 'cov blocked'); END`, when, table)
	}
	scenarios := []scenario{
		{"restore book", block("BEFORE UPDATE", "books"), func(runID int64) {
			a := c.author("RB Author")
			b := c.book(a.ID, "After")
			c.entity(runID, entityTypeBook, fmt.Sprintf("book:rb%d", runID), b.ID, outcomeUpdated,
				covBookMeta(t, &bookRollbackSnapshot{Title: "Before", AuthorID: a.ID}, &bookRollbackSnapshot{Title: "After", AuthorID: a.ID}), runID, b.ID)
		}, "restore book"},
		{"restore author", block("BEFORE UPDATE", "authors"), func(runID int64) {
			a := c.author("After Name")
			c.entity(runID, entityTypeAuthor, fmt.Sprintf("author:ra%d", runID), a.ID, outcomeUpdated,
				covAuthorMeta(t, &authorRollbackSnapshot{Name: "Before Name", SortName: "After Name", Monitored: true},
					&authorRollbackSnapshot{Name: "After Name", SortName: "After Name", Monitored: true}), runID, a.ID)
		}, "restore author"},
		{"delete author", block("BEFORE DELETE", "authors"), func(runID int64) {
			a := c.author("Doomed")
			c.entity(runID, entityTypeAuthor, fmt.Sprintf("author:da%d", runID), a.ID, outcomeCreated, "", runID, a.ID)
		}, "delete author"},
		{"delete edition", block("BEFORE DELETE", "editions"), func(runID int64) {
			a := c.author("Ed Author")
			b := c.book(a.ID, "Ed Book")
			e := &models.Edition{ForeignID: fmt.Sprintf("covE%d", runID), BookID: b.ID, Title: "Ed", Format: "EPUB"}
			if err := c.editions.Upsert(ctx, e); err != nil {
				t.Fatal(err)
			}
			c.entity(runID, entityTypeEdition, fmt.Sprintf("edition:%d:EPUB", runID), e.ID, outcomeCreated, "", runID, e.ID)
		}, "delete edition"},
		{"unlink retained book", block("BEFORE DELETE", "calibre_provenance"), func(runID int64) {
			a := c.author("Ret Author")
			b := c.book(a.ID, "Ret Book")
			c.entity(runID, entityTypeBook, fmt.Sprintf("book:ret%d", runID), b.ID, outcomeCreated, "", runID, b.ID)
			c.link(entityTypeBook, fmt.Sprintf("book:ret-else%d", runID), b.ID, c.run(false))
		}, "unlink book"},
		{"unlink retained author", block("BEFORE DELETE", "calibre_provenance"), func(runID int64) {
			a := c.author("Ret Author 2")
			c.book(a.ID, "Keeps The Author")
			c.entity(runID, entityTypeAuthor, fmt.Sprintf("author:ret%d", runID), a.ID, outcomeCreated, "", runID, a.ID)
		}, "unlink author"},
		{"untrack file provenance", block("BEFORE DELETE", "calibre_provenance"), func(runID int64) {
			a := c.author("File Author")
			b := c.book(a.ID, "File Book")
			c.entity(runID, entityTypeBookFile, fmt.Sprintf("%s/lib/f%d.epub", bookFileExternalIDPrefix, runID), b.ID, outcomeCreated, "", runID, b.ID)
		}, "untrack book file"},
		{"mark run", block("BEFORE UPDATE", "calibre_import_runs"), func(runID int64) {}, "mark run rolled back"},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			runID := c.run(false)
			sc.seed(runID)
			c.exec(sc.trigger)
			defer c.exec(`DROP TRIGGER cov_block`)
			_, err := c.imp.Rollback(ctx, runID)
			if err == nil || !strings.Contains(err.Error(), sc.wantErr) {
				t.Fatalf("err = %v, want %q", err, sc.wantErr)
			}
			if run, _ := c.runs.GetByID(ctx, runID); run.Status != runStatusCompleted {
				t.Fatalf("run status = %q, want completed", run.Status)
			}
		})
	}
}

func TestRollbackDisplayName_Fallbacks(t *testing.T) {
	ctx := context.Background()
	for _, et := range []string{entityTypeBook, entityTypeAuthor, entityTypeEdition, "widget"} {
		if got := rollbackDisplayName(ctx, nil, nil, nil, models.CalibreEntitySnapshot{EntityType: et, LocalID: 4}); got != "" {
			t.Errorf("%s with no repos = %q, want empty", et, got)
		}
	}
	if got := rollbackDisplayName(ctx, nil, nil, nil, models.CalibreEntitySnapshot{EntityType: entityTypeBook}); got != "" {
		t.Errorf("zero local id = %q, want empty", got)
	}

	c := newCovRollback(t)
	a := c.author("  Spaced Author  ")
	b := c.book(a.ID, "Book")
	e := &models.Edition{ForeignID: "covEd", BookID: b.ID, Title: "  Edition Title ", Format: "EPUB"}
	if err := c.editions.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		entity models.CalibreEntitySnapshot
		want   string
	}{
		{models.CalibreEntitySnapshot{EntityType: entityTypeAuthor, LocalID: a.ID}, "Spaced Author"},
		{models.CalibreEntitySnapshot{EntityType: entityTypeEdition, LocalID: e.ID}, "Edition Title"},
		{models.CalibreEntitySnapshot{EntityType: entityTypeSeriesLink, LocalID: b.ID}, "Book"},
		{models.CalibreEntitySnapshot{EntityType: entityTypeAuthor, LocalID: 99999}, ""},
		{models.CalibreEntitySnapshot{EntityType: entityTypeEdition, LocalID: 99999}, ""},
		{models.CalibreEntitySnapshot{EntityType: entityTypeBook, LocalID: 99999}, ""},
	}
	for _, tc := range cases {
		if got := rollbackDisplayName(ctx, c.books, c.authors, c.editions, tc.entity); got != tc.want {
			t.Errorf("%s/%d = %q, want %q", tc.entity.EntityType, tc.entity.LocalID, got, tc.want)
		}
	}
	if rollbackEntityRank(models.CalibreEntitySnapshot{EntityType: "widget"}) != 5 {
		t.Error("unknown entity types sort last")
	}
}

func TestSnapshotRecordersIgnoreMissingInputs(t *testing.T) {
	c := newCovRollback(t)
	ctx := c.ctx
	runID := c.run(false)
	a := c.author("Snap Author")
	b := c.book(a.ID, "Snap Book")

	// Every recorder is a no-op without a run, a repo or a subject.
	c.imp.recordBookBeforeSnapshot(ctx, 0, "book:x", b, outcomeUpdated, nil)
	c.imp.recordBookBeforeSnapshot(ctx, runID, "book:x", nil, outcomeUpdated, nil)
	c.imp.recordBookAfterSnapshot(ctx, runID, "book:x", 0, outcomeUpdated, nil)
	c.imp.recordBookAfterSnapshot(ctx, runID, "book:x", 99999, outcomeUpdated, nil)
	c.imp.recordAuthorBeforeSnapshot(ctx, runID, "author:x", nil, outcomeUpdated, nil)
	c.imp.recordAuthorAfterSnapshot(ctx, runID, "author:x", 0, outcomeUpdated, nil)
	c.imp.recordAuthorAfterSnapshot(ctx, runID, "author:x", 99999, outcomeUpdated, nil)
	c.imp.recordEditionBeforeSnapshot(ctx, runID, "edition:x", nil, outcomeUpdated, nil)
	c.imp.upsertProvenance(ctx, runID, entityTypeBook, "", b.ID)
	if snaps, _ := c.snaps.ListByRun(ctx, runID); len(snaps) != 0 {
		t.Fatalf("recorders wrote %d snapshots from missing inputs", len(snaps))
	}

	// Real inputs are recorded, before and after merging into one row.
	c.imp.recordAuthorBeforeSnapshot(ctx, runID, "author:x", a, outcomeUpdated, nil)
	c.imp.recordAuthorAfterSnapshot(ctx, runID, "author:x", a.ID, outcomeUpdated, nil)
	snaps, _ := c.snaps.ListByRun(ctx, runID)
	if len(snaps) != 1 {
		t.Fatalf("author snapshots = %d, want 1", len(snaps))
	}
	before, after, ok := authorRollbackSnapshotFromMetadata(snaps[0].MetadataJSON)
	if !ok || before.Name != "Snap Author" || after.Name != "Snap Author" || before.IdentifierForeignIDs == nil {
		t.Fatalf("author snapshot = %+v / %+v (%v)", before, after, ok)
	}

	// With the snapshot table unreadable the recorders log and carry on.
	c.exec(`ALTER TABLE calibre_entity_snapshots RENAME TO cov_snaps_hidden`)
	c.imp.recordBookBeforeSnapshot(ctx, runID, "book:y", b, outcomeUpdated, nil)
	c.exec(`ALTER TABLE cov_snaps_hidden RENAME TO calibre_entity_snapshots`)
	c.exec(`ALTER TABLE calibre_provenance RENAME TO cov_prov_hidden`)
	c.imp.upsertProvenance(ctx, runID, entityTypeBook, "book:y", b.ID)
	c.exec(`ALTER TABLE cov_prov_hidden RENAME TO calibre_provenance`)
	if snaps, _ := c.snaps.ListByRun(ctx, runID); len(snaps) != 1 {
		t.Fatalf("snapshots after a failed write = %d, want still 1", len(snaps))
	}

	// Snapshot decoders reject envelopes of the wrong kind or shape.
	for _, raw := range []string{"", "{", `{"kind":"other","version":1}`, covAuthorMeta(t, nil, &authorRollbackSnapshot{})} {
		if _, _, ok := bookRollbackSnapshotFromMetadata(raw); ok {
			t.Errorf("book decoder accepted %q", raw)
		}
		if _, _, ok := authorRollbackSnapshotFromMetadata(raw); ok {
			t.Errorf("author decoder accepted %q", raw)
		}
	}
}
