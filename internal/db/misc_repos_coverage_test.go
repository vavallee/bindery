package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestMiscRepos_Coverage covers smaller repo methods that had no direct test:
// user bootstrap lookups, alias listing, quality profile ownership and
// in-use guards, import list filtering, the Calibre delivery pull page,
// request listing by status, and the author catalogue/DNB upgrade paths.
func TestMiscRepos_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	t.Run("users", func(t *testing.T) {
		users := NewUserRepo(database)
		if id, err := users.FirstAdminID(ctx); err != nil || id != 0 {
			t.Fatalf("FirstAdminID(no users) = %d, %v; want 0", id, err)
		}
		admin, err := users.CreateFirstAdmin(ctx, "misc-admin", "h")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := users.GetOrCreateByUsername(ctx, "misc-proxy")
		if err != nil || plain == nil || plain.Role != "user" || plain.PasswordHash != "" {
			t.Fatalf("GetOrCreateByUsername(new) = %+v, %v", plain, err)
		}
		again, err := users.GetOrCreateByUsername(ctx, "misc-proxy")
		if err != nil || again == nil || again.ID != plain.ID {
			t.Fatalf("GetOrCreateByUsername(existing) = %+v, %v; want id %d", again, err, plain.ID)
		}
		// A later admin does not displace the lowest id.
		if err := users.SetRoleUnguarded(ctx, plain.ID, "admin"); err != nil {
			t.Fatal(err)
		}
		if id, err := users.FirstAdminID(ctx); err != nil || id != admin.ID {
			t.Fatalf("FirstAdminID = %d, %v; want %d", id, err, admin.ID)
		}
	})

	t.Run("aliases", func(t *testing.T) {
		a := mkAuthor(t, authors, ctx, "OL-MISC-AL")
		repo := NewAuthorAliasRepo(database)
		for _, name := range []string{"First Alias", "Second Alias"} {
			if err := repo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.ExecContext(ctx, `UPDATE author_aliases SET created_at = ? WHERE name = 'First Alias'`, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		list, err := repo.List(ctx)
		if err != nil || len(list) < 2 || list[0].Name != "First Alias" {
			t.Fatalf("List = %+v, %v; want newest created_at first", list, err)
		}
	})

	t.Run("quality profiles", func(t *testing.T) {
		repo := NewQualityProfileRepo(database)
		owner := seedUser(t, ctx, database, "misc-qp-owner")
		p := &models.QualityProfile{Name: "Mine", Cutoff: "epub"}
		if err := repo.CreateForUser(ctx, p, owner); err != nil {
			t.Fatal(err)
		}
		if p.ID == 0 || p.OwnerUserID != owner {
			t.Fatalf("CreateForUser = %+v", p)
		}
		var stored sql.NullInt64
		if err := database.QueryRowContext(ctx, `SELECT owner_user_id FROM quality_profiles WHERE id = ?`, p.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored.Int64 != owner {
			t.Fatalf("owner_user_id = %v, want %d", stored, owner)
		}
		shared := &models.QualityProfile{Name: "Shared", Cutoff: "epub"}
		if err := repo.CreateForUser(ctx, shared, 0); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRowContext(ctx, `SELECT owner_user_id FROM quality_profiles WHERE id = ?`, shared.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored.Valid {
			t.Fatalf("owner 0 should be stored as NULL, got %v", stored)
		}

		if err := repo.Update(ctx, &models.QualityProfile{ID: 999999, Name: "x"}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Update(missing) = %v; want sql.ErrNoRows", err)
		}
		if err := repo.Delete(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Delete(missing) = %v; want sql.ErrNoRows", err)
		}
		a := mkAuthor(t, authors, ctx, "OL-MISC-QP")
		a.QualityProfileID = &p.ID
		if err := authors.Update(ctx, a); err != nil {
			t.Fatal(err)
		}
		var inUse *ErrQualityProfileInUse
		if err := repo.Delete(ctx, p.ID); !errors.As(err, &inUse) || inUse.AuthorCount != 1 || inUse.Error() == "" {
			t.Fatalf("Delete(in use) = %v; want ErrQualityProfileInUse with one author", err)
		}
		if err := repo.Delete(ctx, shared.ID); err != nil {
			t.Fatalf("Delete(unused) = %v", err)
		}
	})

	t.Run("import lists", func(t *testing.T) {
		repo := NewImportListRepo(database)
		for _, il := range []*models.ImportList{
			{Name: "B list", Type: "hardcover", Enabled: true},
			{Name: "A list", Type: "hardcover", Enabled: true},
			{Name: "Off", Type: "hardcover", Enabled: false},
			{Name: "Other", Type: "csv", Enabled: true},
		} {
			if err := repo.Create(ctx, il); err != nil {
				t.Fatal(err)
			}
		}
		got, err := repo.ListByType(ctx, "hardcover")
		if err != nil || len(got) != 2 || got[0].Name != "A list" || got[1].Name != "B list" {
			t.Fatalf("ListByType(hardcover) = %+v, %v; want the two enabled lists by name", got, err)
		}
		if got[0].LastSyncAt != nil {
			t.Fatalf("LastSyncAt before sync = %v", got[0].LastSyncAt)
		}
		if err := repo.UpdateLastSyncAt(ctx, got[0].ID); err != nil {
			t.Fatal(err)
		}
		after, err := repo.GetByID(ctx, got[0].ID)
		if err != nil || after == nil || after.LastSyncAt == nil {
			t.Fatalf("after UpdateLastSyncAt = %+v, %v", after, err)
		}
	})

	t.Run("calibre deliveries", func(t *testing.T) {
		repo := NewCalibreDeliveryRepo(database)
		files := NewBookFileRepo(database)
		a := mkAuthor(t, authors, ctx, "OL-MISC-CD")
		var bookIDs []int64
		for _, fid := range []string{"OL-MISC-CD1", "OL-MISC-CD2", "OL-MISC-CD3"} {
			b := mkBook(t, books, ctx, a.ID, fid, fid, models.BookStatusWanted)
			bookIDs = append(bookIDs, b.ID)
			for _, f := range []string{"epub", "pdf"} {
				path := "/cd/" + fid + "." + f
				if err := files.Add(ctx, b.ID, models.MediaTypeEbook, path); err != nil {
					t.Fatal(err)
				}
				var fileID int64
				if err := database.QueryRowContext(ctx, `SELECT id FROM book_files WHERE path = ?`, path).Scan(&fileID); err != nil {
					t.Fatal(err)
				}
				if ok, err := repo.Enqueue(ctx, b.ID, fileID, nil, path, f); err != nil || !ok {
					t.Fatalf("Enqueue %s = %v, %v", path, ok, err)
				}
			}
		}
		later := time.Now().Add(time.Hour)
		if n, err := repo.CountDue(ctx, later); err != nil || n != 6 {
			t.Fatalf("CountDue(later) = %d, %v; want 6", n, err)
		}
		if n, err := repo.CountDue(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
			t.Fatalf("CountDue(earlier) = %d, %v; want 0", n, err)
		}
		page, err := repo.DueBooksAfter(ctx, later, 0, 2)
		if err != nil || len(page) != 4 {
			t.Fatalf("DueBooksAfter(0, 2 books) = %d rows, %v; want both files of the first two books", len(page), err)
		}
		if page[0].BookID != bookIDs[0] || page[1].BookID != bookIDs[0] || page[3].BookID != bookIDs[1] {
			t.Fatalf("DueBooksAfter order = %+v", page)
		}
		next, err := repo.DueBooksAfter(ctx, later, page[3].BookID, 2)
		if err != nil || len(next) != 2 || next[0].BookID != bookIDs[2] {
			t.Fatalf("DueBooksAfter(next page) = %+v, %v", next, err)
		}
	})

	t.Run("requests", func(t *testing.T) {
		users := NewUserRepo(database)
		repo := NewRequestRepo(database)
		if repo.ClaimTTL() != RequestClaimTTL {
			t.Fatalf("default ClaimTTL = %v", repo.ClaimTTL())
		}
		if repo.WithClaimTTL(time.Second).ClaimTTL() != time.Second {
			t.Fatal("WithClaimTTL did not stick")
		}
		owner, err := users.Create(ctx, "misc-req-owner", "h")
		if err != nil {
			t.Fatal(err)
		}
		adminID, err := users.FirstAdminID(ctx)
		if err != nil || adminID == 0 {
			t.Fatalf("FirstAdminID = %d, %v", adminID, err)
		}
		var reqs []*models.LibraryRequest
		for _, fid := range []string{"OL-REQ-1", "OL-REQ-2", "OL-REQ-3"} {
			r := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: fid, Title: fid, PayloadJSON: "{}"}
			if err := repo.Create(ctx, r, 0); err != nil {
				t.Fatal(err)
			}
			reqs = append(reqs, r)
		}
		if _, err := repo.Claim(ctx, reqs[1].ID, adminID); err != nil {
			t.Fatal(err)
		}
		if err := repo.Decline(ctx, reqs[2].ID, adminID, "no"); err != nil {
			t.Fatal(err)
		}
		_, total, err := repo.ListAll(ctx, "", 10, 0)
		if err != nil || total != 3 {
			t.Fatalf("ListAll(all) total = %d, %v", total, err)
		}
		pending, total, err := repo.ListAll(ctx, models.RequestStatusPending, 10, 0)
		if err != nil || total != 2 || len(pending) != 2 {
			t.Fatalf("ListAll(pending) = %d/%d, %v; want the pending and the approving request", len(pending), total, err)
		}
		declined, total, err := repo.ListAll(ctx, models.RequestStatusDeclined, 0, -1)
		if err != nil || total != 1 || len(declined) != 1 || declined[0].ID != reqs[2].ID || declined[0].DeclineReason != "no" {
			t.Fatalf("ListAll(declined) = %+v total=%d, %v", declined, total, err)
		}
	})

	t.Run("authors", func(t *testing.T) {
		a := mkAuthor(t, authors, ctx, "OL-MISC-CAT")
		if when, err := authors.CataloguePopulatedAt(ctx, a.ID); err != nil || when != nil {
			t.Fatalf("CataloguePopulatedAt(no books) = %v, %v; want nil", when, err)
		}
		mkBook(t, books, ctx, a.ID, "OL-MISC-CATB", "Cat Book", models.BookStatusWanted)
		if when, err := authors.CataloguePopulatedAt(ctx, a.ID); err != nil || when == nil {
			t.Fatalf("CataloguePopulatedAt(after a book) = %v, %v; want a time", when, err)
		}
		if when, err := authors.CataloguePopulatedAt(ctx, 999999); err != nil || when != nil {
			t.Fatalf("CataloguePopulatedAt(missing) = %v, %v", when, err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE authors SET catalogue_populated_at = 'garbage' WHERE id = ?`, a.ID); err != nil {
			t.Fatal(err)
		}
		if when, err := authors.CataloguePopulatedAt(ctx, a.ID); err != nil || when != nil {
			t.Fatalf("CataloguePopulatedAt(unparseable) = %v, %v; want nil,nil", when, err)
		}

		// UpgradeSyntheticDNB: validation, a no-op for a missing row, then a
		// real upgrade that keeps the row id and both identifiers.
		if err := authors.UpgradeSyntheticDNB(ctx, "", &models.Author{ForeignID: "x"}); err == nil {
			t.Error("UpgradeSyntheticDNB without a current id should fail")
		}
		if err := authors.UpgradeSyntheticDNB(ctx, "dnb:author:x", nil); err == nil {
			t.Error("UpgradeSyntheticDNB without a target should fail")
		}
		if err := authors.UpgradeSyntheticDNB(ctx, "dnb:author:missing", &models.Author{ForeignID: "OL-NEW"}); err != nil {
			t.Fatalf("UpgradeSyntheticDNB(missing row) = %v; want a silent no-op", err)
		}
		dnb := &models.Author{ForeignID: "dnb:author:tolkien", Name: "Tolkien", SortName: "Tolkien, J. R. R.", MetadataProvider: "dnb", Description: "old"}
		if err := authors.Create(ctx, dnb); err != nil {
			t.Fatal(err)
		}
		if err := authors.UpgradeSyntheticDNB(ctx, dnb.ForeignID, &models.Author{
			ForeignID: "OL26320A", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R.", MetadataProvider: "openlibrary",
		}); err != nil {
			t.Fatal(err)
		}
		up, err := authors.GetByID(ctx, dnb.ID)
		if err != nil || up == nil || up.ForeignID != "OL26320A" || up.Name != "J.R.R. Tolkien" || up.MetadataProvider != "openlibrary" || up.Description != "old" {
			t.Fatalf("after upgrade = %+v, %v; want the canonical id and name with the old description kept", up, err)
		}
		if got, err := authors.GetByAnyForeignID(ctx, "dnb:author:tolkien"); err != nil || got == nil || got.ID != dnb.ID {
			t.Fatalf("old dnb id no longer resolves: %+v, %v", got, err)
		}

		// Update onto a foreign id another author already holds as an
		// identifier is a conflict, and the row is left unchanged.
		other := mkAuthor(t, authors, ctx, "OL-MISC-OTHER")
		before := *other
		other.ForeignID = "dnb:author:tolkien" // held by dnb as an identifier only
		other.Name = "Should Not Stick"
		var conflict *AuthorIdentifierConflictError
		if err := authors.Update(ctx, other); !errors.As(err, &conflict) || conflict.AuthorID != dnb.ID {
			t.Fatalf("Update onto a taken identifier = %v; want AuthorIdentifierConflictError", err)
		}
		if got, _ := authors.GetByID(ctx, other.ID); got == nil || got.Name != before.Name || got.ForeignID != before.ForeignID {
			t.Fatalf("conflicting Update leaked a partial write: %+v", got)
		}
	})

	t.Run("merge copies unset target fields", func(t *testing.T) {
		source := mkAuthor(t, authors, ctx, "OL-MISC-MSRC")
		target := mkAuthor(t, authors, ctx, "OL-MISC-MTGT")
		res, err := database.ExecContext(ctx, `INSERT INTO root_folders (path) VALUES ('/merge-root')`)
		if err != nil {
			t.Fatal(err)
		}
		rootID, _ := res.LastInsertId()
		if _, err := database.ExecContext(ctx,
			`UPDATE authors SET monitored = 0, root_folder_id = ?, metadata_profile_id = 1 WHERE id = ?`, rootID, source.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx,
			`UPDATE authors SET monitored = 1, root_folder_id = NULL, metadata_profile_id = NULL WHERE id = ?`, target.ID); err != nil {
			t.Fatal(err)
		}
		out, err := NewAuthorAliasRepo(database).Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true})
		if err != nil || !out.TargetUpdated || out.AliasesCreated != 1 {
			t.Fatalf("Merge = %+v, %v", out, err)
		}
		got, err := authors.GetByID(ctx, target.ID)
		if err != nil || got == nil {
			t.Fatalf("target after merge = %+v, %v", got, err)
		}
		if got.Monitored || got.RootFolderID == nil || *got.RootFolderID != rootID || got.MetadataProfileID == nil || *got.MetadataProfileID != 1 {
			t.Fatalf("target after merge = monitored %v root %v metadata %v; want source's values copied", got.Monitored, got.RootFolderID, got.MetadataProfileID)
		}
		if gone, _ := authors.GetByID(ctx, source.ID); gone != nil {
			t.Fatalf("source survived the merge: %+v", gone)
		}
	})

	t.Run("library projection", func(t *testing.T) {
		repo := NewRequestRepo(database)
		series := NewSeriesRepo(database)
		files := NewBookFileRepo(database)
		viewer := seedUser(t, ctx, database, "misc-proj-viewer")
		stranger := seedUser(t, ctx, database, "misc-proj-stranger")
		a := &models.Author{ForeignID: "OL-PROJ-A", Name: "Projection Penname", SortName: "Penname, Projection", MetadataProvider: "openlibrary"}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		mk := func(fid, title string, owner int64) *models.Book {
			t.Helper()
			b := &models.Book{ForeignID: fid, AuthorID: a.ID, Title: title, SortTitle: title, Status: models.BookStatusWanted,
				Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner, ImageURL: "http://img/" + fid}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			return b
		}
		mine := mk("OL-PROJ-1", "Zebra Projection", viewer)
		shared := mk("OL-PROJ-2", "Yak Projection", 0)
		mk("OL-PROJ-3", "Xerus Projection", stranger)
		hidden := mk("OL-PROJ-4", "Walrus Projection", viewer)
		if err := books.SetExcluded(ctx, hidden.ID, true); err != nil {
			t.Fatal(err)
		}
		s := &models.Series{ForeignID: "OL-PROJ-S", Title: "Projection Saga"}
		if err := series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := series.LinkBook(ctx, s.ID, mine.ID, "2", true); err != nil {
			t.Fatal(err)
		}
		if err := files.Add(ctx, mine.ID, models.MediaTypeAudiobook, "/proj/mine.m4b"); err != nil {
			t.Fatal(err)
		}

		rows, total, err := repo.ListLibraryProjection(ctx, viewer, "projection penname", 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(rows) != 2 || rows[0].ID != shared.ID || rows[1].ID != mine.ID {
			t.Fatalf("ListLibraryProjection(viewer) = %+v total=%d; want the unowned and own book in title order, not the stranger's or the excluded one", rows, total)
		}
		got := rows[1]
		if got.AuthorName != "Projection Penname" || got.SeriesTitle != "Projection Saga" || got.SeriesPosition != "2" ||
			got.ImageURL != "http://img/OL-PROJ-1" || got.HasEbook || !got.HasAudiobook {
			t.Fatalf("projection row = %+v", got)
		}
		if rows[0].SeriesTitle != "" || rows[0].HasAudiobook {
			t.Fatalf("unlinked row = %+v; want no series and no files", rows[0])
		}

		all, total, err := repo.ListLibraryProjection(ctx, 0, "projection", 1, 1)
		if err != nil || total != 3 || len(all) != 1 || all[0].ID != shared.ID {
			t.Fatalf("ListLibraryProjection(unscoped, page 2) = %+v total=%d, %v; want Yak of Xerus/Yak/Zebra", all, total, err)
		}
		if none, total, err := repo.ListLibraryProjection(ctx, viewer, "no such words", 10, 0); err != nil || total != 0 || len(none) != 0 {
			t.Fatalf("ListLibraryProjection(no match) = %+v total=%d, %v", none, total, err)
		}
	})
}
