package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestBookRepoFillMissingAudiobookDuration(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-DURATION-A")
	book := mkBook(t, books, ctx, author.ID, "hc:duration-book", "Duration Book", models.BookStatusWanted)
	book.MetadataProvider = "hardcover"
	book.MediaType = models.MediaTypeAudiobook
	book.ASIN = "B000AUDIO1"
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}

	fetched := *book
	fetched.DurationSeconds = 36000
	updated, err := books.FillMissingAudiobookDuration(ctx, &fetched)
	if err != nil || !updated || fetched.DurationSeconds != 36000 {
		t.Fatalf("initial duration fill: updated=%v err=%v", updated, err)
	}

	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("load stored book: %v", err)
	}
	if stored.DurationSeconds != 36000 {
		t.Fatalf("stored duration = %d, want 36000", stored.DurationSeconds)
	}
	stored.Monitored = false
	stored.Status = models.BookStatusSkipped
	stored.Narrator = "Concurrent narrator"
	if err := books.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}

	fetched.DurationSeconds = 72000
	updated, err = books.FillMissingAudiobookDuration(ctx, &fetched)
	if err != nil || updated || fetched.DurationSeconds != 36000 {
		t.Fatalf("existing duration should win: updated=%v err=%v", updated, err)
	}
	stored, err = books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload stored book: %v", err)
	}
	if stored.DurationSeconds != 36000 || stored.Monitored || stored.Status != models.BookStatusSkipped || stored.Narrator != "Concurrent narrator" || fetched.Monitored || fetched.Status != stored.Status || fetched.Narrator != stored.Narrator {
		t.Fatalf("guarded write changed concurrent fields: %+v", stored)
	}

	stored.ASIN = "B000OTHER1"
	stored.DurationSeconds = 42000
	if err := books.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err = books.FillMissingAudiobookDuration(ctx, &fetched)
	if err != nil || updated {
		t.Fatalf("changed ASIN must reload the current identity and duration: updated=%v err=%v", updated, err)
	}
	stored, err = books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload changed book: %v", err)
	}
	if stored.ASIN != "B000OTHER1" || stored.DurationSeconds != 42000 || fetched.ASIN != stored.ASIN || fetched.DurationSeconds != stored.DurationSeconds {
		t.Fatalf("changed book was overwritten: %+v", stored)
	}

	fetched.MediaType = models.MediaTypeEbook
	updated, err = books.FillMissingAudiobookDuration(ctx, &fetched)
	if err != nil || updated {
		t.Fatalf("ebook must not receive audio duration: updated=%v err=%v", updated, err)
	}
	fetched.MediaType = models.MediaTypeAudiobook
	fetched.DurationSeconds = 0
	if _, err := books.FillMissingAudiobookDuration(ctx, &fetched); err == nil {
		t.Fatal("zero candidate duration should be rejected")
	}
	if _, err := books.FillMissingAudiobookDuration(ctx, nil); err == nil {
		t.Fatal("nil book should be rejected")
	}
	fetched.DurationSeconds = 72000
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := books.FillMissingAudiobookDuration(cancelled, &fetched); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write error = %v, want context.Canceled", err)
	}
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	if updated, err := books.FillMissingAudiobookDuration(ctx, &fetched); updated || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted book: updated=%v err=%v", updated, err)
	}
}

func TestBookRepoFillMissingAudiobookDurationForBoth(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-DURATION-B")
	book := mkBook(t, books, ctx, author.ID, "hc:both-book", "Both Book", models.BookStatusWanted)
	book.MetadataProvider = "hardcover"
	book.MediaType = models.MediaTypeBoth
	book.DurationSeconds = 5400
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	book.DurationSeconds = 6300
	updated, err := books.FillMissingAudiobookDuration(ctx, book)
	if err != nil || updated || book.DurationSeconds != 5400 {
		t.Fatalf("known dual-format duration should win: updated=%v err=%v", updated, err)
	}
}

func TestBookRepoUpdateHydratedMetadata(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-HYDRATED-A")
	book := mkBook(t, books, ctx, author.ID, "hc:hydrated", "Original title", models.BookStatusWanted)
	before := *book
	book.Language, book.ImageURL = "eng", "https://example.com/cover.jpg"
	book.MediaType, book.ASIN = models.MediaTypeAudiobook, "B000AUDIO1"
	book.Narrator, book.DurationSeconds, book.Description = "Narrator", 36000, "Summary"
	// A hydration update must not rewrite unrelated snapshot fields or locks.
	book.Title = "Stale title"
	book.LockField(models.BookFieldGenres)
	updated, err := books.UpdateHydratedMetadata(ctx, book, before.UpdatedAtRaw)
	if err != nil || !updated {
		t.Fatalf("initial write: updated=%v err=%v", updated, err)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Language != "eng" || stored.ImageURL != book.ImageURL || stored.MediaType != models.MediaTypeAudiobook || stored.ASIN != book.ASIN || stored.Narrator != "Narrator" || stored.DurationSeconds != 36000 || stored.Description != "Summary" || stored.Title != before.Title || len(stored.LockedFields) != 0 || !stored.UpdatedAt.Equal(book.UpdatedAt) || stored.UpdatedAtRaw != book.UpdatedAtRaw {
		t.Fatalf("incorrect metadata write: %+v", stored)
	}
	// A second attempt with the old timestamp loses, and reloads the winner.
	book.Language = "ger"
	updated, err = books.UpdateHydratedMetadata(ctx, book, before.UpdatedAtRaw)
	if err != nil || updated || book.Language != "eng" || book.Title != before.Title || len(book.LockedFields) != 0 {
		t.Fatalf("stale write: updated=%v err=%v book=%+v", updated, err, book)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := books.UpdateHydratedMetadata(cancelled, book, book.UpdatedAtRaw); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if _, err := books.UpdateHydratedMetadata(ctx, nil, book.UpdatedAtRaw); err == nil {
		t.Fatal("nil snapshot should be rejected")
	}
	if _, err := books.UpdateHydratedMetadata(ctx, book, ""); err == nil {
		t.Fatal("snapshot without a stored updated_at should be rejected")
	}
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	if updated, err := books.UpdateHydratedMetadata(ctx, book, book.UpdatedAtRaw); updated || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted book: updated=%v err=%v", updated, err)
	}
}

func TestBookRepoFillMissingAudiobookDurationRequiresMatchingIdentity(t *testing.T) {
	for _, field := range []struct {
		column, value string
	}{
		{"foreign_id", "hc:rebound-book"},
		{"metadata_provider", "openlibrary"},
		{"media_type", models.MediaTypeBoth},
		{"asin", "B000OTHER1"},
	} {
		// A zero stored duration pins the UPDATE clause; a known one pins the
		// reload, which must report the row's own duration, not the fetched one.
		for _, storedDuration := range []int{0, 42000} {
			t.Run(fmt.Sprintf("%s/stored=%d", field.column, storedDuration), func(t *testing.T) {
				database, err := OpenMemory()
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				ctx := context.Background()
				books := NewBookRepo(database)
				author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-DURATION-ID")
				book := mkBook(t, books, ctx, author.ID, "hc:identity-book", "Identity Book", models.BookStatusWanted)
				book.MetadataProvider = "hardcover"
				book.MediaType = models.MediaTypeAudiobook
				book.ASIN = "B000AUDIO1"
				if err := books.Update(ctx, book); err != nil {
					t.Fatal(err)
				}
				fetched := *book
				fetched.DurationSeconds = 36000

				// Change only one identity column, as a concurrent rebind or re-match would.
				query := fmt.Sprintf("UPDATE books SET %s = ?, duration_seconds = ? WHERE id = ?", field.column)
				if _, err := database.ExecContext(ctx, query, field.value, storedDuration, book.ID); err != nil {
					t.Fatal(err)
				}

				updated, err := books.FillMissingAudiobookDuration(ctx, &fetched)
				if err != nil || updated || fetched.DurationSeconds != storedDuration {
					t.Fatalf("changed %s: updated=%v duration=%d err=%v, want no write and the stored duration %d", field.column, updated, fetched.DurationSeconds, err, storedDuration)
				}
				// The duration must arrive with the identity it belongs to.
				reloaded := map[string]string{"foreign_id": fetched.ForeignID, "metadata_provider": fetched.MetadataProvider, "media_type": fetched.MediaType, "asin": fetched.ASIN}
				if got := reloaded[field.column]; got != field.value {
					t.Fatalf("changed %s: reloaded %q, want %q", field.column, got, field.value)
				}
				var duration int
				if err := database.QueryRowContext(ctx, "SELECT duration_seconds FROM books WHERE id = ?", book.ID).Scan(&duration); err != nil {
					t.Fatal(err)
				}
				if duration != storedDuration {
					t.Fatalf("changed %s: stored duration = %d, want %d", field.column, duration, storedDuration)
				}
			})
		}
	}
}

// books.updated_at holds every shape Bindery has ever written there. The
// hydration guard must match each one as stored, or every refresh of a legacy
// row silently discards its metadata (#2758).
func TestBookRepoUpdateHydratedMetadataLegacyUpdatedAtShapes(t *testing.T) {
	for _, tc := range []struct{ name, stored string }{
		{"current_timestamp", "2026-09-01 10:00:00"},
		{"go_time_string_914", "2026-09-01 10:00:00.123456789 +0000 UTC"},
		{"rfc3339nano", "2026-09-01T10:00:00.123456789Z"},
		{"unparseable", "not a time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx := context.Background()
			books := NewBookRepo(database)
			author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-LEGACY-A")
			book := mkBook(t, books, ctx, author.ID, "hc:legacy", "Legacy", models.BookStatusWanted)
			load := func() *models.Book {
				t.Helper()
				if _, err := database.ExecContext(ctx, "UPDATE books SET updated_at=? WHERE id=?", tc.stored, book.ID); err != nil {
					t.Fatal(err)
				}
				loaded, err := books.GetByID(ctx, book.ID)
				if err != nil || loaded == nil {
					t.Fatalf("load legacy book: %v", err)
				}
				if loaded.UpdatedAtRaw != tc.stored {
					t.Fatalf("UpdatedAtRaw = %q, want stored text %q", loaded.UpdatedAtRaw, tc.stored)
				}
				return loaded
			}

			loaded := load()
			before := *loaded
			loaded.Language = "eng"
			updated, err := books.UpdateHydratedMetadata(ctx, loaded, before.UpdatedAtRaw)
			if err != nil || !updated {
				t.Fatalf("unchanged legacy row: updated=%v err=%v", updated, err)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Language != "eng" || stored.UpdatedAtRaw != loaded.UpdatedAtRaw || stored.UpdatedAtRaw == tc.stored {
				t.Fatalf("hydrated write not persisted: language=%q raw=%q caller raw=%q", stored.Language, stored.UpdatedAtRaw, loaded.UpdatedAtRaw)
			}

			// A genuine edit after the snapshot still wins, whichever writer made
			// it: each one must advance updated_at or hydration overwrites it.
			target := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-LEGACY-B")
			for _, w := range []struct {
				name    string
				prepare string
				write   func() error
				check   func(*models.Book) bool
			}{
				{
					name:  "SetLanguage",
					write: func() error { return books.SetLanguage(ctx, book.ID, "fre") },
					check: func(b *models.Book) bool { return b.Language == "fre" },
				},
				{
					name:  "SetImageURL",
					write: func() error { return books.SetImageURL(ctx, book.ID, "/covers/local.jpg") },
					check: func(b *models.Book) bool { return b.ImageURL == "/covers/local.jpg" },
				},
				{
					name:    "MarkWantedMonitored",
					prepare: "UPDATE books SET status='skipped', monitored=0 WHERE id=?",
					write:   func() error { return books.MarkWantedMonitored(ctx, book.ID) },
					check:   func(b *models.Book) bool { return b.Status == models.BookStatusWanted && b.Monitored },
				},
				{
					name:  "SetExcluded",
					write: func() error { return books.SetExcluded(ctx, book.ID, true) },
					check: func(b *models.Book) bool { return b.Excluded },
				},
				{
					name: "AuthorMerge",
					write: func() error {
						_, err := NewAuthorAliasRepo(database).Merge(ctx, author.ID, target.ID, MergeOptions{})
						return err
					},
					check: func(b *models.Book) bool { return b.AuthorID == target.ID },
				},
			} {
				t.Run(w.name, func(t *testing.T) {
					// Undo any write a previous case wrongly let through.
					if _, err := database.ExecContext(ctx, "UPDATE books SET language='eng' WHERE id=?", book.ID); err != nil {
						t.Fatal(err)
					}
					if w.prepare != "" {
						if _, err := database.ExecContext(ctx, w.prepare, book.ID); err != nil {
							t.Fatal(err)
						}
					}
					loaded := load()
					before := *loaded
					if err := w.write(); err != nil {
						t.Fatal(err)
					}
					loaded.Language = "ger"
					updated, err := books.UpdateHydratedMetadata(ctx, loaded, before.UpdatedAtRaw)
					if err != nil || updated || loaded.Language == "ger" || !w.check(loaded) {
						t.Fatalf("concurrent edit: updated=%v err=%v book=%+v", updated, err, loaded)
					}
					stored, err := books.GetByID(ctx, book.ID)
					if err != nil {
						t.Fatal(err)
					}
					if stored.Language == "ger" || !w.check(stored) {
						t.Fatalf("concurrent edit overwritten: %+v", stored)
					}
				})
			}
		})
	}
}
