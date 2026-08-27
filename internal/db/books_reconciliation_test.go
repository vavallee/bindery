package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestDeleteMetadataOnlyWantedByIDs_RechecksEverySafetyGuard(t *testing.T) {
	database, author, plain := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	create := func(foreignID, title, status string) *models.Book {
		t.Helper()
		book := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID, Title: title, SortTitle: title,
			Monitored: true, Status: status, MediaType: models.MediaTypeEbook,
		}
		if err := repo.Create(ctx, book); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return book
	}

	imported := create("OL-imported", "Imported", models.BookStatusImported)
	legacyFile := create("OL-legacy-file", "Legacy file", models.BookStatusWanted)
	legacyFile.FilePath = "/library/legacy.epub"
	if err := repo.Update(ctx, legacyFile); err != nil {
		t.Fatal(err)
	}
	legacyEbook := create("OL-legacy-ebook", "Legacy ebook", models.BookStatusWanted)
	legacyEbook.EbookFilePath = "/library/legacy-ebook.epub"
	if err := repo.Update(ctx, legacyEbook); err != nil {
		t.Fatal(err)
	}
	legacyAudiobook := create("OL-legacy-audiobook", "Legacy audiobook", models.BookStatusWanted)
	legacyAudiobook.AudiobookFilePath = "/library/legacy-audiobook.m4b"
	if err := repo.Update(ctx, legacyAudiobook); err != nil {
		t.Fatal(err)
	}
	trackedFile := create("OL-tracked-file", "Tracked file", models.BookStatusWanted)
	if err := repo.AddBookFile(ctx, trackedFile.ID, models.MediaTypeEbook, "/library/tracked.epub"); err != nil {
		t.Fatal(err)
	}
	excluded := create("OL-excluded", "Excluded", models.BookStatusWanted)
	if err := repo.SetExcluded(ctx, excluded.ID, true); err != nil {
		t.Fatal(err)
	}
	downloading := create("OL-downloading", "Downloading", models.BookStatusDownloading)

	ids := []int64{
		plain.ID, imported.ID, legacyFile.ID, legacyEbook.ID, legacyAudiobook.ID,
		trackedFile.ID, excluded.ID, downloading.ID,
	}
	deleted, err := repo.DeleteMetadataOnlyWantedByIDs(ctx, author.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if got, err := repo.GetByID(ctx, plain.ID); err != nil || got != nil {
		t.Fatalf("plain metadata-only Wanted row survived: book=%+v err=%v", got, err)
	}
	for _, protected := range []*models.Book{
		imported, legacyFile, legacyEbook, legacyAudiobook, trackedFile, excluded, downloading,
	} {
		if got, err := repo.GetByID(ctx, protected.ID); err != nil || got == nil {
			t.Errorf("protected book %q was deleted: book=%+v err=%v", protected.Title, got, err)
		}
	}
}

func TestDeleteMetadataOnlyWantedByIDs_IsAuthorScoped(t *testing.T) {
	database, author, book := openTestDB(t)
	repo := NewBookRepo(database)
	deleted, err := repo.DeleteMetadataOnlyWantedByIDs(context.Background(), author.ID+999, []int64{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if got, err := repo.GetByID(context.Background(), book.ID); err != nil || got == nil {
		t.Fatalf("cross-author delete removed book: book=%+v err=%v", got, err)
	}
}

func TestDeleteMetadataOnlyWantedByIDs_IgnoresInvalidInputs(t *testing.T) {
	database, author, _ := openTestDB(t)
	repo := NewBookRepo(database)
	for _, tt := range []struct {
		name     string
		authorID int64
		ids      []int64
	}{
		{name: "invalid author", ids: []int64{1}},
		{name: "empty ids", authorID: author.ID},
		{name: "only invalid ids", authorID: author.ID, ids: []int64{0, -1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deleted, err := repo.DeleteMetadataOnlyWantedByIDs(context.Background(), tt.authorID, tt.ids)
			if err != nil || deleted != 0 {
				t.Fatalf("deleted=%d err=%v, want 0/nil", deleted, err)
			}
		})
	}
}

func TestDeleteMetadataOnlyWantedByIDs_ReportsDatabaseErrors(t *testing.T) {
	database, author, _ := openTestDB(t)
	execErr := errors.New("exec failed")
	rowsErr := errors.New("rows affected failed")
	for _, tt := range []struct {
		name string
		exec dbExecutor
		want error
	}{
		{
			name: "delete",
			exec: reconciliationExecutor{err: execErr},
			want: execErr,
		},
		{
			name: "rows affected",
			exec: reconciliationExecutor{result: reconciliationResult{rowsErr: rowsErr}},
			want: rowsErr,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewBookRepo(database)
			repo.exec = tt.exec
			deleted, err := repo.DeleteMetadataOnlyWantedByIDs(context.Background(), author.ID, []int64{-1, 1})
			if deleted != 0 || !errors.Is(err, tt.want) {
				t.Fatalf("deleted=%d err=%v, want wrapped %v", deleted, err, tt.want)
			}
		})
	}
}

type reconciliationExecutor struct {
	result sql.Result
	err    error
}

func (e reconciliationExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return e.result, e.err
}

func (reconciliationExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("unexpected query")
}

func (reconciliationExecutor) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("unexpected query row")
}

type reconciliationResult struct {
	rowsErr error
}

func (reconciliationResult) LastInsertId() (int64, error) { return 0, nil }

func (r reconciliationResult) RowsAffected() (int64, error) { return 0, r.rowsErr }
