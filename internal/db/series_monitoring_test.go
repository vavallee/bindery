package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestSeriesMonitoringLifecycle(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewSeriesRepo(database)
	s := &models.Series{ForeignID: "s1", Title: "One"}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	if due, _ := repo.ListMonitoringDue(ctx, now, 10); len(due) != 0 {
		t.Fatalf("an unmonitored series is due: %v", due)
	}
	if err := repo.SetMonitoring(ctx, s.ID, true, "bogus", 0); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, s.ID)
	if !got.Monitored || got.Monitoring == nil || got.Monitoring.Mode != models.SeriesMonitorUpcoming {
		t.Fatalf("series after monitoring = %+v, want monitored with the upcoming default", got.Monitoring)
	}
	if due, _ := repo.ListMonitoringDue(ctx, now, 10); len(due) != 1 {
		t.Fatalf("a never checked series is not due: %v", due)
	}

	if err := repo.StampCheck(ctx, s.ID, now, now.Add(24*time.Hour), models.SeriesCheckOK, "", 2); err != nil {
		t.Fatal(err)
	}
	if due, _ := repo.ListMonitoringDue(ctx, now.Add(time.Hour), 10); len(due) != 0 {
		t.Error("a checked series was due before its next check")
	}
	if due, _ := repo.ListMonitoringDue(ctx, now.Add(25*time.Hour), 10); len(due) != 1 {
		t.Error("a series was not due after its next check")
	}
	got, _ = repo.GetByID(ctx, s.ID)
	if got.Monitoring.LastStatus != models.SeriesCheckOK || got.Monitoring.LastCreated != 2 || got.Monitoring.LastCheckAt == nil {
		t.Errorf("stamp not stored: %+v", got.Monitoring)
	}

	// A mode change makes the series due at once; the same mode does not.
	if err := repo.SetMonitoring(ctx, s.ID, true, models.SeriesMonitorUpcoming, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.GetByID(ctx, s.ID); got.Monitoring.NextCheckAt == nil {
		t.Error("re-saving the same mode reset the schedule")
	}
	if err := repo.SetMonitoring(ctx, s.ID, true, models.SeriesMonitorAll, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.GetByID(ctx, s.ID); got.Monitoring.NextCheckAt != nil || got.Monitoring.Mode != models.SeriesMonitorAll {
		t.Errorf("changing the mode did not make the series due: %+v", got.Monitoring)
	}
}

func TestSearchHolds(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	author := &models.Author{ForeignID: "a", Name: "A", SortName: "A"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	mk := func(id string) *models.Book {
		b := &models.Book{ForeignID: id, AuthorID: author.ID, Title: id, SortTitle: id, Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{}}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	held, expired := mk("held"), mk("expired")
	now := time.Now()
	if err := books.SetSearchHold(ctx, held.ID, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := books.SetSearchHold(ctx, expired.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	holds, err := books.SearchHolds(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := holds[held.ID]; !ok || len(holds) != 1 {
		t.Errorf("holds = %v, want only the future one", holds)
	}

	page, total, err := books.ListPageFiltered(ctx, BookListFilter{Status: models.BookStatusWanted, Upcoming: UpcomingOnly}, 50, 0)
	if err != nil || total != 1 || page[0].ID != held.ID {
		t.Errorf("upcoming only = %d books (%v), err %v, want just the held one", total, page, err)
	}
	_, total, _ = books.ListPageFiltered(ctx, BookListFilter{Status: models.BookStatusWanted, Upcoming: UpcomingHide}, 50, 0)
	if total != 1 {
		t.Errorf("upcoming hidden = %d books, want 1 (the expired hold counts as released)", total)
	}
	if err := books.ClearSearchHold(ctx, held.ID); err != nil {
		t.Fatal(err)
	}
	if holds, _ = books.SearchHolds(ctx, now); len(holds) != 0 {
		t.Errorf("holds after clearing = %v", holds)
	}
}
