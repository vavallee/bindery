package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestDeferredHardcoverEditionsDeletion(t *testing.T) {
	for _, mode := range []string{"direct", "bulk", "author cascade", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			database, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx := context.Background()
			settings := NewSettingsRepo(database)
			authors := NewAuthorRepo(database)
			books := NewBookRepo(database)
			author := mkAuthor(t, authors, ctx, "author")
			book := mkBook(t, books, ctx, author.ID, "book", "Book", models.BookStatusWanted)
			if err := settings.SetDeferredHardcoverEditions(ctx, book.ID, "pending"); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "direct":
				err = books.Delete(ctx, book.ID)
			case "bulk":
				_, err = books.DeleteMetadataOnlyWantedByIDs(ctx, author.ID, []int64{book.ID})
			case "author cascade":
				err = authors.Delete(ctx, author.ID)
			case "rollback":
				tx, txErr := database.BeginTx(ctx, nil)
				if txErr != nil {
					t.Fatal(txErr)
				}
				if err := books.WithTx(tx).Delete(ctx, book.ID); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := settings.GetDeferredHardcoverEditions(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (before != nil) != (mode == "rollback") {
				t.Fatalf("wrong deletion/rollback result: %v", before)
			}
			// Simulate an in-flight fetch completing after deletion.
			if err := settings.SetDeferredHardcoverEditions(ctx, book.ID, "late"); err != nil {
				t.Fatal(err)
			}
			got, err := settings.GetDeferredHardcoverEditions(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				if got == nil || *got != "late" {
					t.Fatalf("rollback lost live work: %+v", got)
				}
			} else if got != nil {
				t.Fatalf("deleted book retained work: %+v", got)
			}
		})
	}
}

func TestMigration086PreservesLiveDeferredWork(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	settings := NewSettingsRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "author")
	book := mkBook(t, NewBookRepo(database), ctx, author.ID, "book", "Book", models.BookStatusWanted)
	if _, err := database.ExecContext(ctx, "DROP TABLE deferred_hardcover_editions"); err != nil {
		t.Fatal(err)
	}
	liveKey := "auth.hardcover_deferred_editions." + fmt.Sprint(book.ID)
	for key, value := range map[string]string{liveKey: "live", "auth.hardcover_deferred_editions.999999": "orphan", "other.setting": "keep"} {
		if err := settings.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version=86"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	live, err := settings.GetDeferredHardcoverEditions(ctx, book.ID)
	if err != nil || live == nil || *live != "live" {
		t.Fatalf("lost live work: %v %v", live, err)
	}
	for _, key := range []string{liveKey, "auth.hardcover_deferred_editions.999999"} {
		if got, err := settings.Get(ctx, key); err != nil || got != nil {
			t.Fatalf("legacy marker retained: %+v %v", got, err)
		}
	}
	if got, err := settings.Get(ctx, "other.setting"); err != nil || got == nil || got.Value != "keep" {
		t.Fatalf("unrelated setting changed: %+v %v", got, err)
	}
}
