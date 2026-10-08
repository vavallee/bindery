package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestReposSurfaceDatabaseErrors is a contract test over every exported repo
// method that returns an error: run against a closed *sql.DB, each one must
// hand the failure back to its caller rather than swallow it and report
// success. A few methods legitimately answer without touching the database
// for some inputs; the arguments here are chosen so none take that shortcut.
//
// The handle is closed before any query, so this needs no migrations and
// adds next to nothing to the package's runtime.
func TestReposSurfaceDatabaseErrors(t *testing.T) {
	t.Parallel()
	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	ptrInt64 := func(v int64) *int64 { return &v }

	calls := []struct {
		name string
		call func() error
	}{
		{"ABSImportRunEntityRepo.ListByRun", func() error { _, err := NewABSImportRunEntityRepo(closed).ListByRun(ctx, 1); return err }},
		{"ABSImportRunEntityRepo.Record", func() error {
			return NewABSImportRunEntityRepo(closed).Record(ctx, &models.ABSImportRunEntity{RunID: 1})
		}},
		{"ABSImportRunRepo.Create", func() error { return NewABSImportRunRepo(closed).Create(ctx, &models.ABSImportRun{}) }},
		{"ABSImportRunRepo.Finish", func() error { return NewABSImportRunRepo(closed).Finish(ctx, 1, "x", nil) }},
		{"ABSImportRunRepo.GetByID", func() error { _, err := NewABSImportRunRepo(closed).GetByID(ctx, 1); return err }},
		{"ABSImportRunRepo.LatestRunningWithCheckpoint", func() error { _, err := NewABSImportRunRepo(closed).LatestRunningWithCheckpoint(ctx); return err }},
		{"ABSImportRunRepo.ListRecent", func() error { _, err := NewABSImportRunRepo(closed).ListRecent(ctx, 1); return err }},
		{"ABSImportRunRepo.UpdateCheckpoint", func() error { return NewABSImportRunRepo(closed).UpdateCheckpoint(ctx, 1, nil) }},
		{"ABSImportRunRepo.UpdateStatus", func() error { return NewABSImportRunRepo(closed).UpdateStatus(ctx, 1, "x") }},
		{"ABSMetadataConflictRepo.Claim", func() error { _, err := NewABSMetadataConflictRepo(closed).Claim(ctx, 1); return err }},
		{"ABSMetadataConflictRepo.GetByEntityField", func() error {
			_, err := NewABSMetadataConflictRepo(closed).GetByEntityField(ctx, "x", 1, "x")
			return err
		}},
		{"ABSMetadataConflictRepo.GetByID", func() error { _, err := NewABSMetadataConflictRepo(closed).GetByID(ctx, 1); return err }},
		{"ABSMetadataConflictRepo.List", func() error { _, err := NewABSMetadataConflictRepo(closed).List(ctx); return err }},
		{"ABSMetadataConflictRepo.ListPaginated", func() error { _, _, err := NewABSMetadataConflictRepo(closed).ListPaginated(ctx, 1, 1); return err }},
		{"ABSMetadataConflictRepo.Unclaim", func() error { return NewABSMetadataConflictRepo(closed).Unclaim(ctx, 1) }},
		{"ABSMetadataConflictRepo.Upsert", func() error { return NewABSMetadataConflictRepo(closed).Upsert(ctx, &models.ABSMetadataConflict{}) }},
		{"ABSProvenanceRepo.DeleteByExternal", func() error { return NewABSProvenanceRepo(closed).DeleteByExternal(ctx, "x", "x", "x", "x") }},
		{"ABSProvenanceRepo.DeleteByLocal", func() error { _, err := NewABSProvenanceRepo(closed).DeleteByLocal(ctx, "x", 1); return err }},
		{"ABSProvenanceRepo.GetByExternal", func() error {
			_, err := NewABSProvenanceRepo(closed).GetByExternal(ctx, "x", "x", "x", "x")
			return err
		}},
		{"ABSProvenanceRepo.ListByLocal", func() error { _, err := NewABSProvenanceRepo(closed).ListByLocal(ctx, "x", 1); return err }},
		{"ABSProvenanceRepo.Upsert", func() error { return NewABSProvenanceRepo(closed).Upsert(ctx, &models.ABSProvenance{}) }},
		{"ABSReviewItemRepo.DismissByRunID", func() error { _, err := NewABSReviewItemRepo(closed).DismissByRunID(ctx, 1); return err }},
		{"ABSReviewItemRepo.GetByID", func() error { _, err := NewABSReviewItemRepo(closed).GetByID(ctx, 1); return err }},
		{"ABSReviewItemRepo.ListByStatus", func() error { _, err := NewABSReviewItemRepo(closed).ListByStatus(ctx, "x"); return err }},
		{"ABSReviewItemRepo.ListByStatusPaginated", func() error {
			_, _, err := NewABSReviewItemRepo(closed).ListByStatusPaginated(ctx, "x", 1, 1)
			return err
		}},
		{"ABSReviewItemRepo.MarkResolvedByItemIDs", func() error {
			_, err := NewABSReviewItemRepo(closed).MarkResolvedByItemIDs(ctx, "x", "x", []string{"x"})
			return err
		}},
		{"ABSReviewItemRepo.ResolveAuthorForPrimary", func() error {
			_, err := NewABSReviewItemRepo(closed).ResolveAuthorForPrimary(ctx, "x", "x", "x", "x", "x")
			return err
		}},
		{"ABSReviewItemRepo.ResolveBook", func() error { return NewABSReviewItemRepo(closed).ResolveBook(ctx, 1, "x", "x", "x") }},
		{"ABSReviewItemRepo.UpdateStatus", func() error { return NewABSReviewItemRepo(closed).UpdateStatus(ctx, 1, "x") }},
		{"ABSReviewItemRepo.UpsertPending", func() error { return NewABSReviewItemRepo(closed).UpsertPending(ctx, &models.ABSReviewItem{}) }},
		{"AuthorAliasRepo.Create", func() error { return NewAuthorAliasRepo(closed).Create(ctx, &models.AuthorAlias{}) }},
		{"AuthorAliasRepo.Delete", func() error { return NewAuthorAliasRepo(closed).Delete(ctx, 1) }},
		{"AuthorAliasRepo.DeleteForAuthor", func() error { _, err := NewAuthorAliasRepo(closed).DeleteForAuthor(ctx, 1, 1); return err }},
		{"AuthorAliasRepo.GetByName", func() error { _, err := NewAuthorAliasRepo(closed).GetByName(ctx, "x"); return err }},
		{"AuthorAliasRepo.List", func() error { _, err := NewAuthorAliasRepo(closed).List(ctx); return err }},
		{"AuthorAliasRepo.ListByAuthor", func() error { _, err := NewAuthorAliasRepo(closed).ListByAuthor(ctx, 1); return err }},
		{"AuthorAliasRepo.LookupByName", func() error { _, err := NewAuthorAliasRepo(closed).LookupByName(ctx, "x"); return err }},
		{"AuthorAliasRepo.Merge", func() error { _, err := NewAuthorAliasRepo(closed).Merge(ctx, 1, 2, MergeOptions{}); return err }},
		{"AuthorRepo.CataloguePopulatedAt", func() error { _, err := NewAuthorRepo(closed).CataloguePopulatedAt(ctx, 1); return err }},
		{"AuthorRepo.CountDiscoveryEligible", func() error { _, err := NewAuthorRepo(closed).CountDiscoveryEligible(ctx); return err }},
		{"AuthorRepo.Create", func() error { return NewAuthorRepo(closed).Create(ctx, &models.Author{}) }},
		{"AuthorRepo.CreateForUser", func() error { return NewAuthorRepo(closed).CreateForUser(ctx, &models.Author{}, 1) }},
		{"AuthorRepo.Delete", func() error { return NewAuthorRepo(closed).Delete(ctx, 1) }},
		{"AuthorRepo.DeleteAuthorIdentifier", func() error { return NewAuthorRepo(closed).DeleteAuthorIdentifier(ctx, 1, "x") }},
		{"AuthorRepo.GetAuthorIdentifier", func() error { _, err := NewAuthorRepo(closed).GetAuthorIdentifier(ctx, "x"); return err }},
		{"AuthorRepo.GetByAnyForeignID", func() error { _, err := NewAuthorRepo(closed).GetByAnyForeignID(ctx, "x"); return err }},
		{"AuthorRepo.GetByAnyForeignIDForUser", func() error { _, err := NewAuthorRepo(closed).GetByAnyForeignIDForUser(ctx, "x", 1); return err }},
		{"AuthorRepo.GetByDNBSyntheticName", func() error { _, err := NewAuthorRepo(closed).GetByDNBSyntheticName(ctx, "x", 1); return err }},
		{"AuthorRepo.GetByForeignID", func() error { _, err := NewAuthorRepo(closed).GetByForeignID(ctx, "x"); return err }},
		{"AuthorRepo.GetByForeignIDForUser", func() error { _, err := NewAuthorRepo(closed).GetByForeignIDForUser(ctx, "x", 1); return err }},
		{"AuthorRepo.GetByID", func() error { _, err := NewAuthorRepo(closed).GetByID(ctx, 1); return err }},
		{"AuthorRepo.GetByIDForUser", func() error { _, err := NewAuthorRepo(closed).GetByIDForUser(ctx, 1, 1); return err }},
		{"AuthorRepo.LastDiscoveryAt", func() error { _, err := NewAuthorRepo(closed).LastDiscoveryAt(ctx, 1); return err }},
		{"AuthorRepo.LibraryIDsByAnyForeignIDsForUser", func() error {
			_, err := NewAuthorRepo(closed).LibraryIDsByAnyForeignIDsForUser(ctx, []string{"x"}, 1)
			return err
		}},
		{"AuthorRepo.List", func() error { _, err := NewAuthorRepo(closed).List(ctx); return err }},
		{"AuthorRepo.ListAuthorIdentifiers", func() error { _, err := NewAuthorRepo(closed).ListAuthorIdentifiers(ctx, 1); return err }},
		{"AuthorRepo.ListByUser", func() error { _, err := NewAuthorRepo(closed).ListByUser(ctx, 1); return err }},
		{"AuthorRepo.ListDiscoveryDue", func() error { _, err := NewAuthorRepo(closed).ListDiscoveryDue(ctx, now, 1); return err }},
		{"AuthorRepo.ListMonitoredSeriesIDs", func() error { _, err := NewAuthorRepo(closed).ListMonitoredSeriesIDs(ctx, 1); return err }},
		{"AuthorRepo.ListPage", func() error { _, _, err := NewAuthorRepo(closed).ListPage(ctx, 1, 1, 1); return err }},
		{"AuthorRepo.ListPageFiltered", func() error {
			_, _, err := NewAuthorRepo(closed).ListPageFiltered(ctx, AuthorListFilter{}, 1, 1)
			return err
		}},
		{"AuthorRepo.MarkCataloguePopulated", func() error { return NewAuthorRepo(closed).MarkCataloguePopulated(ctx, 1, now) }},
		{"AuthorRepo.SetMonitoredSeriesIDs", func() error { return NewAuthorRepo(closed).SetMonitoredSeriesIDs(ctx, 1, []int64{1}) }},
		{"AuthorRepo.StampDiscovery", func() error { return NewAuthorRepo(closed).StampDiscovery(ctx, 1, now) }},
		{"AuthorRepo.UnmonitoredAuthorIDs", func() error { _, err := NewAuthorRepo(closed).UnmonitoredAuthorIDs(ctx); return err }},
		{"AuthorRepo.Update", func() error { return NewAuthorRepo(closed).Update(ctx, &models.Author{}) }},
		{"AuthorRepo.UpgradeSyntheticDNB", func() error { return NewAuthorRepo(closed).UpgradeSyntheticDNB(ctx, "x", &models.Author{}) }},
		{"AuthorRepo.UpsertAuthorIdentifier", func() error { return NewAuthorRepo(closed).UpsertAuthorIdentifier(ctx, 1, "x") }},
		{"BlocklistRepo.Create", func() error { return NewBlocklistRepo(closed).Create(ctx, &models.BlocklistEntry{}) }},
		{"BlocklistRepo.CreateByUser", func() error { return NewBlocklistRepo(closed).CreateByUser(ctx, &models.BlocklistEntry{}, 1) }},
		{"BlocklistRepo.DeleteByBookID", func() error { return NewBlocklistRepo(closed).DeleteByBookID(ctx, 1) }},
		{"BlocklistRepo.DeleteByID", func() error { return NewBlocklistRepo(closed).DeleteByID(ctx, 1) }},
		{"BlocklistRepo.IsBlocked", func() error { _, err := NewBlocklistRepo(closed).IsBlocked(ctx, "x"); return err }},
		{"BlocklistRepo.List", func() error { _, err := NewBlocklistRepo(closed).List(ctx); return err }},
		{"BookFileRepo.Add", func() error { return NewBookFileRepo(closed).Add(ctx, 1, "x", "x") }},
		{"BookFileRepo.AddIfMissing", func() error { _, err := NewBookFileRepo(closed).AddIfMissing(ctx, 1, "x", "x"); return err }},
		{"BookFileRepo.BookIDForFile", func() error { _, err := NewBookFileRepo(closed).BookIDForFile(ctx, 1); return err }},
		{"BookFileRepo.DeleteByBook", func() error { return NewBookFileRepo(closed).DeleteByBook(ctx, 1) }},
		{"BookFileRepo.DeleteByPath", func() error { _, err := NewBookFileRepo(closed).DeleteByPath(ctx, "x"); return err }},
		{"BookFileRepo.Fingerprint", func() error { _, _, err := NewBookFileRepo(closed).Fingerprint(ctx); return err }},
		{"BookFileRepo.ListAllPaths", func() error { _, err := NewBookFileRepo(closed).ListAllPaths(ctx); return err }},
		{"BookFileRepo.ListByBook", func() error { _, err := NewBookFileRepo(closed).ListByBook(ctx, 1); return err }},
		{"BookFileRepo.ListByBooks", func() error { _, err := NewBookFileRepo(closed).ListByBooks(ctx, []int64{1}); return err }},
		{"BookFileRepo.MoveToBook", func() error { _, err := NewBookFileRepo(closed).MoveToBook(ctx, 1, "x", "x"); return err }},
		{"BookFileRepo.PathOwnedByOtherBook", func() error { _, err := NewBookFileRepo(closed).PathOwnedByOtherBook(ctx, "x", 1); return err }},
		{"BookFileRepo.RecentEbookPaths", func() error { _, err := NewBookFileRepo(closed).RecentEbookPaths(ctx, 1); return err }},
		{"BookFileRepo.Track", func() error { _, err := NewBookFileRepo(closed).Track(ctx, 1, "x", "x"); return err }},
		{"BookFileRepo.UpdatePath", func() error { return NewBookFileRepo(closed).UpdatePath(ctx, 1, "x") }},
		{"BookRepo.AddBookFile", func() error { return NewBookRepo(closed).AddBookFile(ctx, 1, "x", "x") }},
		{"BookRepo.AddBookFileIfMissing", func() error { _, err := NewBookRepo(closed).AddBookFileIfMissing(ctx, 1, "x", "x"); return err }},
		{"BookRepo.BookFilesFingerprint", func() error { _, _, err := NewBookRepo(closed).BookFilesFingerprint(ctx); return err }},
		{"BookRepo.BookIDForFile", func() error { _, err := NewBookRepo(closed).BookIDForFile(ctx, 1); return err }},
		{"BookRepo.Create", func() error { return NewBookRepo(closed).Create(ctx, &models.Book{}) }},
		{"BookRepo.Delete", func() error { return NewBookRepo(closed).Delete(ctx, 1) }},
		{"BookRepo.DeleteBookIdentifier", func() error { return NewBookRepo(closed).DeleteBookIdentifier(ctx, 1, "x") }},
		{"BookRepo.DeleteMetadataOnlyWantedByIDs", func() error {
			_, err := NewBookRepo(closed).DeleteMetadataOnlyWantedByIDs(ctx, 1, []int64{1})
			return err
		}},
		{"BookRepo.FillMissingAudiobookDuration", func() error {
			_, err := NewBookRepo(closed).FillMissingAudiobookDuration(ctx, &models.Book{})
			return err
		}},
		{"BookRepo.FindAllByAuthorAndDedupKey", func() error { _, err := NewBookRepo(closed).FindAllByAuthorAndDedupKey(ctx, 1, "x"); return err }},
		{"BookRepo.FindByAuthorAndDedupKey", func() error { _, err := NewBookRepo(closed).FindByAuthorAndDedupKey(ctx, 1, "x"); return err }},
		{"BookRepo.FindByAuthorAndDedupKeyVisibleTo", func() error {
			_, err := NewBookRepo(closed).FindByAuthorAndDedupKeyVisibleTo(ctx, 1, "x", 1)
			return err
		}},
		{"BookRepo.FindByAuthorAndTitle", func() error { _, err := NewBookRepo(closed).FindByAuthorAndTitle(ctx, 1, "x"); return err }},
		{"BookRepo.GetBookIdentifier", func() error { _, err := NewBookRepo(closed).GetBookIdentifier(ctx, "x"); return err }},
		{"BookRepo.GetByAnyForeignID", func() error { _, err := NewBookRepo(closed).GetByAnyForeignID(ctx, "x"); return err }},
		{"BookRepo.GetByCalibreID", func() error { _, err := NewBookRepo(closed).GetByCalibreID(ctx, 1); return err }},
		{"BookRepo.GetByForeignID", func() error { _, err := NewBookRepo(closed).GetByForeignID(ctx, "x"); return err }},
		{"BookRepo.GetByForeignIDForUser", func() error { _, err := NewBookRepo(closed).GetByForeignIDForUser(ctx, "x", 1); return err }},
		{"BookRepo.GetByForeignIDVisibleTo", func() error { _, err := NewBookRepo(closed).GetByForeignIDVisibleTo(ctx, "x", 1); return err }},
		{"BookRepo.GetByID", func() error { _, err := NewBookRepo(closed).GetByID(ctx, 1); return err }},
		{"BookRepo.GetByIDs", func() error { _, err := NewBookRepo(closed).GetByIDs(ctx, []int64{1}); return err }},
		{"BookRepo.LibraryIDsByForeignIDsForUser", func() error {
			_, err := NewBookRepo(closed).LibraryIDsByForeignIDsForUser(ctx, []string{"x"}, 1)
			return err
		}},
		{"BookRepo.List", func() error { _, err := NewBookRepo(closed).List(ctx); return err }},
		{"BookRepo.ListAllBookFilePaths", func() error { _, err := NewBookRepo(closed).ListAllBookFilePaths(ctx); return err }},
		{"BookRepo.ListBookFiles", func() error { _, err := NewBookRepo(closed).ListBookFiles(ctx, 1); return err }},
		{"BookRepo.ListBookIdentifiers", func() error { _, err := NewBookRepo(closed).ListBookIdentifiers(ctx, 1); return err }},
		{"BookRepo.ListBookIdentifiersByAuthor", func() error { _, err := NewBookRepo(closed).ListBookIdentifiersByAuthor(ctx, 1); return err }},
		{"BookRepo.ListByAuthor", func() error { _, err := NewBookRepo(closed).ListByAuthor(ctx, 1); return err }},
		{"BookRepo.ListByAuthorAndUser", func() error { _, err := NewBookRepo(closed).ListByAuthorAndUser(ctx, 1, 1); return err }},
		{"BookRepo.ListByAuthorIncludingExcluded", func() error { _, err := NewBookRepo(closed).ListByAuthorIncludingExcluded(ctx, 1); return err }},
		{"BookRepo.ListByStatus", func() error { _, err := NewBookRepo(closed).ListByStatus(ctx, "x"); return err }},
		{"BookRepo.ListByStatusAndUser", func() error { _, err := NewBookRepo(closed).ListByStatusAndUser(ctx, "x", 1); return err }},
		{"BookRepo.ListByStatusIncludingExcluded", func() error { _, err := NewBookRepo(closed).ListByStatusIncludingExcluded(ctx, "x"); return err }},
		{"BookRepo.ListByStatusIncludingExcludedAndUser", func() error {
			_, err := NewBookRepo(closed).ListByStatusIncludingExcludedAndUser(ctx, "x", 1)
			return err
		}},
		{"BookRepo.ListByUser", func() error { _, err := NewBookRepo(closed).ListByUser(ctx, 1); return err }},
		{"BookRepo.ListFiles", func() error { _, err := NewBookRepo(closed).ListFiles(ctx, 1); return err }},
		{"BookRepo.ListFilesForBooks", func() error { _, err := NewBookRepo(closed).ListFilesForBooks(ctx, []int64{1}); return err }},
		{"BookRepo.ListIncludingExcluded", func() error { _, err := NewBookRepo(closed).ListIncludingExcluded(ctx); return err }},
		{"BookRepo.ListPage", func() error { _, _, err := NewBookRepo(closed).ListPage(ctx, 1, 1, 1); return err }},
		{"BookRepo.ListPageFiltered", func() error {
			_, _, err := NewBookRepo(closed).ListPageFiltered(ctx, BookListFilter{}, 1, 1)
			return err
		}},
		{"BookRepo.ListWithLocalImagePath", func() error { _, err := NewBookRepo(closed).ListWithLocalImagePath(ctx); return err }},
		{"BookRepo.MarkWantedMonitored", func() error { return NewBookRepo(closed).MarkWantedMonitored(ctx, 1) }},
		{"BookRepo.MoveBookFile", func() error { _, err := NewBookRepo(closed).MoveBookFile(ctx, "x", 1, "x"); return err }},
		{"BookRepo.MultiFileBookPaths", func() error { _, err := NewBookRepo(closed).MultiFileBookPaths(ctx); return err }},
		{"BookRepo.PathOwnedByOtherBook", func() error { _, err := NewBookRepo(closed).PathOwnedByOtherBook(ctx, "x", 1); return err }},
		{"BookRepo.RefreshBookStatus", func() error { return NewBookRepo(closed).RefreshBookStatus(ctx, 1) }},
		{"BookRepo.ReloadHydratedBook", func() error { return NewBookRepo(closed).ReloadHydratedBook(ctx, &models.Book{}) }},
		{"BookRepo.RemoveBookFile", func() error { _, err := NewBookRepo(closed).RemoveBookFile(ctx, "x"); return err }},
		{"BookRepo.SetCalibreID", func() error { return NewBookRepo(closed).SetCalibreID(ctx, 1, 1) }},
		{"BookRepo.SetCalibreIDIfUnset", func() error { _, err := NewBookRepo(closed).SetCalibreIDIfUnset(ctx, 1, 1); return err }},
		{"BookRepo.SetExcluded", func() error { return NewBookRepo(closed).SetExcluded(ctx, 1, true) }},
		{"BookRepo.SetFilePath", func() error { return NewBookRepo(closed).SetFilePath(ctx, 1, "x") }},
		{"BookRepo.SetFormatFilePath", func() error { return NewBookRepo(closed).SetFormatFilePath(ctx, 1, "x", "x") }},
		{"BookRepo.SetImageURL", func() error { return NewBookRepo(closed).SetImageURL(ctx, 1, "x") }},
		{"BookRepo.SetLanguage", func() error { return NewBookRepo(closed).SetLanguage(ctx, 1, "x") }},
		{"BookRepo.UntrackFilePath", func() error { _, err := NewBookRepo(closed).UntrackFilePath(ctx, "x"); return err }},
		{"BookRepo.UntrackFilePathForBook", func() error { _, err := NewBookRepo(closed).UntrackFilePathForBook(ctx, "x", 1); return err }},
		{"BookRepo.Update", func() error { return NewBookRepo(closed).Update(ctx, &models.Book{}) }},
		{"BookRepo.UpdateBookFilePath", func() error { return NewBookRepo(closed).UpdateBookFilePath(ctx, 1, 1, "x") }},
		{"BookRepo.UpdateHydratedMetadata", func() error {
			_, err := NewBookRepo(closed).UpdateHydratedMetadata(ctx, &models.Book{}, "x")
			return err
		}},
		{"BookRepo.UpdateIfUnchanged", func() error { _, err := NewBookRepo(closed).UpdateIfUnchanged(ctx, &models.Book{}, "x"); return err }},
		{"BookRepo.UpsertBookIdentifier", func() error { return NewBookRepo(closed).UpsertBookIdentifier(ctx, 1, "x") }},
		{"CalibreDeliveryRepo.ClearPending", func() error { _, err := NewCalibreDeliveryRepo(closed).ClearPending(ctx); return err }},
		{"CalibreDeliveryRepo.CountDue", func() error { _, err := NewCalibreDeliveryRepo(closed).CountDue(ctx, now); return err }},
		{"CalibreDeliveryRepo.DeliveredByCalibreID", func() error { _, err := NewCalibreDeliveryRepo(closed).DeliveredByCalibreID(ctx, 1); return err }},
		{"CalibreDeliveryRepo.DueBatch", func() error { _, err := NewCalibreDeliveryRepo(closed).DueBatch(ctx, now, 1); return err }},
		{"CalibreDeliveryRepo.DueBooksAfter", func() error { _, err := NewCalibreDeliveryRepo(closed).DueBooksAfter(ctx, now, 1, 1); return err }},
		{"CalibreDeliveryRepo.Enqueue", func() error {
			_, err := NewCalibreDeliveryRepo(closed).Enqueue(ctx, 1, 1, ptrInt64(1), "x", "x")
			return err
		}},
		{"CalibreDeliveryRepo.Get", func() error { _, err := NewCalibreDeliveryRepo(closed).Get(ctx, 1); return err }},
		{"CalibreDeliveryRepo.GetByBookFile", func() error { _, err := NewCalibreDeliveryRepo(closed).GetByBookFile(ctx, 1); return err }},
		{"CalibreDeliveryRepo.HasSkipped", func() error { _, err := NewCalibreDeliveryRepo(closed).HasSkipped(ctx, "x"); return err }},
		{"CalibreDeliveryRepo.List", func() error {
			_, err := NewCalibreDeliveryRepo(closed).List(ctx, models.CalibreDeliveryState("pending"), 1, 1)
			return err
		}},
		{"CalibreDeliveryRepo.ListAll", func() error { _, err := NewCalibreDeliveryRepo(closed).ListAll(ctx); return err }},
		{"CalibreDeliveryRepo.ListByBook", func() error { _, err := NewCalibreDeliveryRepo(closed).ListByBook(ctx, 1); return err }},
		{"CalibreDeliveryRepo.ListWithBooks", func() error {
			_, err := NewCalibreDeliveryRepo(closed).ListWithBooks(ctx, models.CalibreDeliveryState("pending"), 1, 1)
			return err
		}},
		{"CalibreDeliveryRepo.MarkDelivered", func() error { return NewCalibreDeliveryRepo(closed).MarkDelivered(ctx, 1, 1, "x", "x") }},
		{"CalibreDeliveryRepo.MarkFailed", func() error { return NewCalibreDeliveryRepo(closed).MarkFailed(ctx, 1, "x", "x", now, true) }},
		{"CalibreDeliveryRepo.MarkSkipped", func() error { return NewCalibreDeliveryRepo(closed).MarkSkipped(ctx, 1, "x") }},
		{"CalibreDeliveryRepo.RearmSkipped", func() error { _, err := NewCalibreDeliveryRepo(closed).RearmSkipped(ctx, "x"); return err }},
		{"CalibreDeliveryRepo.ResetAll", func() error { _, err := NewCalibreDeliveryRepo(closed).ResetAll(ctx); return err }},
		{"CalibreDeliveryRepo.Retry", func() error {
			_, err := NewCalibreDeliveryRepo(closed).Retry(ctx, models.CalibreDeliveryState("pending"))
			return err
		}},
		{"CalibreDeliveryRepo.Summary", func() error { _, err := NewCalibreDeliveryRepo(closed).Summary(ctx); return err }},
		{"CalibreEntitySnapshotRepo.ListByRun", func() error { _, err := NewCalibreEntitySnapshotRepo(closed).ListByRun(ctx, 1); return err }},
		{"CalibreEntitySnapshotRepo.Record", func() error {
			return NewCalibreEntitySnapshotRepo(closed).Record(ctx, &models.CalibreEntitySnapshot{RunID: 1})
		}},
		{"CalibreImportRunRepo.Create", func() error { return NewCalibreImportRunRepo(closed).Create(ctx, &models.CalibreImportRun{}) }},
		{"CalibreImportRunRepo.Finish", func() error { return NewCalibreImportRunRepo(closed).Finish(ctx, 1, "x", nil) }},
		{"CalibreImportRunRepo.GetByID", func() error { _, err := NewCalibreImportRunRepo(closed).GetByID(ctx, 1); return err }},
		{"CalibreImportRunRepo.ListRecent", func() error { _, err := NewCalibreImportRunRepo(closed).ListRecent(ctx, 1); return err }},
		{"CalibreImportRunRepo.UpdateStatus", func() error { return NewCalibreImportRunRepo(closed).UpdateStatus(ctx, 1, "x") }},
		{"CalibreProvenanceRepo.DeleteByExternal", func() error { return NewCalibreProvenanceRepo(closed).DeleteByExternal(ctx, "x", "x", "x") }},
		{"CalibreProvenanceRepo.DeleteByLocal", func() error { _, err := NewCalibreProvenanceRepo(closed).DeleteByLocal(ctx, "x", 1); return err }},
		{"CalibreProvenanceRepo.GetByExternal", func() error { _, err := NewCalibreProvenanceRepo(closed).GetByExternal(ctx, "x", "x", "x"); return err }},
		{"CalibreProvenanceRepo.ListByLocal", func() error { _, err := NewCalibreProvenanceRepo(closed).ListByLocal(ctx, "x", 1); return err }},
		{"CalibreProvenanceRepo.Upsert", func() error { return NewCalibreProvenanceRepo(closed).Upsert(ctx, &models.CalibreProvenance{}) }},
		{"CustomFormatRepo.Create", func() error { return NewCustomFormatRepo(closed).Create(ctx, &models.CustomFormat{}) }},
		{"CustomFormatRepo.Delete", func() error { return NewCustomFormatRepo(closed).Delete(ctx, 1) }},
		{"CustomFormatRepo.GetByID", func() error { _, err := NewCustomFormatRepo(closed).GetByID(ctx, 1); return err }},
		{"CustomFormatRepo.List", func() error { _, err := NewCustomFormatRepo(closed).List(ctx); return err }},
		{"CustomFormatRepo.Update", func() error { return NewCustomFormatRepo(closed).Update(ctx, &models.CustomFormat{}) }},
		{"DelayProfileRepo.Create", func() error { return NewDelayProfileRepo(closed).Create(ctx, &models.DelayProfile{}) }},
		{"DelayProfileRepo.Delete", func() error { return NewDelayProfileRepo(closed).Delete(ctx, 1) }},
		{"DelayProfileRepo.GetByID", func() error { _, err := NewDelayProfileRepo(closed).GetByID(ctx, 1); return err }},
		{"DelayProfileRepo.List", func() error { _, err := NewDelayProfileRepo(closed).List(ctx); return err }},
		{"DelayProfileRepo.Update", func() error { return NewDelayProfileRepo(closed).Update(ctx, &models.DelayProfile{}) }},
		{"DownloadClientRepo.Create", func() error { return NewDownloadClientRepo(closed).Create(ctx, &models.DownloadClient{}) }},
		{"DownloadClientRepo.Delete", func() error { return NewDownloadClientRepo(closed).Delete(ctx, 1) }},
		{"DownloadClientRepo.GetByID", func() error { _, err := NewDownloadClientRepo(closed).GetByID(ctx, 1); return err }},
		{"DownloadClientRepo.GetEnabledByProtocol", func() error { _, err := NewDownloadClientRepo(closed).GetEnabledByProtocol(ctx, "x"); return err }},
		{"DownloadClientRepo.GetFirstEnabled", func() error { _, err := NewDownloadClientRepo(closed).GetFirstEnabled(ctx); return err }},
		{"DownloadClientRepo.GetFirstEnabledByProtocol", func() error { _, err := NewDownloadClientRepo(closed).GetFirstEnabledByProtocol(ctx, "x"); return err }},
		{"DownloadClientRepo.List", func() error { _, err := NewDownloadClientRepo(closed).List(ctx); return err }},
		{"DownloadClientRepo.ListEnabled", func() error { _, err := NewDownloadClientRepo(closed).ListEnabled(ctx); return err }},
		{"DownloadClientRepo.Update", func() error { return NewDownloadClientRepo(closed).Update(ctx, &models.DownloadClient{}) }},
		{"DownloadRepo.CountWedgedCompletedOverRetryLimit", func() error { _, err := NewDownloadRepo(closed).CountWedgedCompletedOverRetryLimit(ctx, 1); return err }},
		{"DownloadRepo.Create", func() error { return NewDownloadRepo(closed).Create(ctx, &models.Download{}) }},
		{"DownloadRepo.Delete", func() error { return NewDownloadRepo(closed).Delete(ctx, 1) }},
		{"DownloadRepo.DeleteByBook", func() error { return NewDownloadRepo(closed).DeleteByBook(ctx, 1) }},
		{"DownloadRepo.GetByGUID", func() error { _, err := NewDownloadRepo(closed).GetByGUID(ctx, "x"); return err }},
		{"DownloadRepo.GetByID", func() error { _, err := NewDownloadRepo(closed).GetByID(ctx, 1); return err }},
		{"DownloadRepo.GetByNzoID", func() error { _, err := NewDownloadRepo(closed).GetByNzoID(ctx, "x"); return err }},
		{"DownloadRepo.GetByTorrentID", func() error { _, err := NewDownloadRepo(closed).GetByTorrentID(ctx, "x"); return err }},
		{"DownloadRepo.GetOwnerByID", func() error { _, _, err := NewDownloadRepo(closed).GetOwnerByID(ctx, 1); return err }},
		{"DownloadRepo.IncrementImportRetryCount", func() error { return NewDownloadRepo(closed).IncrementImportRetryCount(ctx, 1) }},
		{"DownloadRepo.List", func() error { _, err := NewDownloadRepo(closed).List(ctx); return err }},
		{"DownloadRepo.ListByStatus", func() error { _, err := NewDownloadRepo(closed).ListByStatus(ctx, models.StateGrabbed); return err }},
		{"DownloadRepo.ListByStatusAndUser", func() error {
			_, err := NewDownloadRepo(closed).ListByStatusAndUser(ctx, models.StateGrabbed, 1)
			return err
		}},
		{"DownloadRepo.ListByStatuses", func() error { _, err := NewDownloadRepo(closed).ListByStatuses(ctx, models.StateGrabbed); return err }},
		{"DownloadRepo.ListByUser", func() error { _, err := NewDownloadRepo(closed).ListByUser(ctx, 1); return err }},
		{"DownloadRepo.RecoverInterruptedImports", func() error { _, err := NewDownloadRepo(closed).RecoverInterruptedImports(ctx); return err }},
		{"DownloadRepo.RecoverWedgedCompleted", func() error { _, err := NewDownloadRepo(closed).RecoverWedgedCompleted(ctx, 1, 1); return err }},
		{"DownloadRepo.ResetImportRetry", func() error { _, _, err := NewDownloadRepo(closed).ResetImportRetry(ctx, 1); return err }},
		{"DownloadRepo.RetryDeadForAutoGrab", func() error {
			_, err := NewDownloadRepo(closed).RetryDeadForAutoGrab(ctx, &models.Download{}, now)
			return err
		}},
		{"DownloadRepo.RetryFailed", func() error { _, err := NewDownloadRepo(closed).RetryFailed(ctx, &models.Download{}); return err }},
		{"DownloadRepo.SetBookID", func() error { return NewDownloadRepo(closed).SetBookID(ctx, 1, 1) }},
		{"DownloadRepo.SetError", func() error { return NewDownloadRepo(closed).SetError(ctx, 1, "x") }},
		{"DownloadRepo.SetErrorWithStatus", func() error { return NewDownloadRepo(closed).SetErrorWithStatus(ctx, 1, models.StateGrabbed, "x") }},
		{"DownloadRepo.SetGrabbedAt", func() error { return NewDownloadRepo(closed).SetGrabbedAt(ctx, 1, now) }},
		{"DownloadRepo.SetImportPath", func() error { return NewDownloadRepo(closed).SetImportPath(ctx, 1, "x") }},
		{"DownloadRepo.SetNzoID", func() error { return NewDownloadRepo(closed).SetNzoID(ctx, 1, "x") }},
		{"DownloadRepo.SetTorrentID", func() error { return NewDownloadRepo(closed).SetTorrentID(ctx, 1, "x") }},
		{"DownloadRepo.UpdateStatus", func() error { return NewDownloadRepo(closed).UpdateStatus(ctx, 1, models.StateGrabbed) }},
		{"EditionRepo.Delete", func() error { return NewEditionRepo(closed).Delete(ctx, 1) }},
		{"EditionRepo.GetByForeignID", func() error { _, err := NewEditionRepo(closed).GetByForeignID(ctx, "x"); return err }},
		{"EditionRepo.GetByID", func() error { _, err := NewEditionRepo(closed).GetByID(ctx, 1); return err }},
		{"EditionRepo.ListByBook", func() error { _, err := NewEditionRepo(closed).ListByBook(ctx, 1); return err }},
		{"EditionRepo.ListWithLocalImagePath", func() error { _, err := NewEditionRepo(closed).ListWithLocalImagePath(ctx); return err }},
		{"EditionRepo.SetImageURL", func() error { return NewEditionRepo(closed).SetImageURL(ctx, 1, "x") }},
		{"EditionRepo.Upsert", func() error { return NewEditionRepo(closed).Upsert(ctx, &models.Edition{}) }},
		{"EditionRepo.UpsertMetadata", func() error {
			_, err := NewEditionRepo(closed).UpsertMetadata(ctx, &models.Edition{ForeignID: "x", BookID: 1}, "x", "x")
			return err
		}},
		{"GrimmoryPushRepo.Has", func() error { _, err := NewGrimmoryPushRepo(closed).Has(ctx, "x"); return err }},
		{"GrimmoryPushRepo.LastPush", func() error { _, _, err := NewGrimmoryPushRepo(closed).LastPush(ctx); return err }},
		{"GrimmoryPushRepo.Record", func() error { return NewGrimmoryPushRepo(closed).Record(ctx, 1, "x", 1) }},
		{"HistoryRepo.Create", func() error { return NewHistoryRepo(closed).Create(ctx, &models.HistoryEvent{}) }},
		{"HistoryRepo.Delete", func() error { return NewHistoryRepo(closed).Delete(ctx, 1) }},
		{"HistoryRepo.FirstEventAt", func() error { _, err := NewHistoryRepo(closed).FirstEventAt(ctx, "x"); return err }},
		{"HistoryRepo.GetByID", func() error { _, err := NewHistoryRepo(closed).GetByID(ctx, 1); return err }},
		{"HistoryRepo.GetOwnerByID", func() error { _, _, err := NewHistoryRepo(closed).GetOwnerByID(ctx, 1); return err }},
		{"HistoryRepo.List", func() error { _, err := NewHistoryRepo(closed).List(ctx); return err }},
		{"HistoryRepo.ListByBook", func() error { _, err := NewHistoryRepo(closed).ListByBook(ctx, 1); return err }},
		{"HistoryRepo.ListByBookAndUser", func() error { _, err := NewHistoryRepo(closed).ListByBookAndUser(ctx, 1, 1); return err }},
		{"HistoryRepo.ListByType", func() error { _, err := NewHistoryRepo(closed).ListByType(ctx, "x"); return err }},
		{"HistoryRepo.ListByTypeAndUser", func() error { _, err := NewHistoryRepo(closed).ListByTypeAndUser(ctx, "x", 1); return err }},
		{"HistoryRepo.ListForUser", func() error { _, err := NewHistoryRepo(closed).ListForUser(ctx, 1); return err }},
		{"HistoryRepo.ListPage", func() error { _, _, err := NewHistoryRepo(closed).ListPage(ctx, HistoryListOpts{}); return err }},
		{"ImportListRepo.Create", func() error { return NewImportListRepo(closed).Create(ctx, &models.ImportList{}) }},
		{"ImportListRepo.CreateExclusion", func() error { return NewImportListRepo(closed).CreateExclusion(ctx, &models.ImportListExclusion{}) }},
		{"ImportListRepo.Delete", func() error { return NewImportListRepo(closed).Delete(ctx, 1) }},
		{"ImportListRepo.DeleteExclusion", func() error { return NewImportListRepo(closed).DeleteExclusion(ctx, 1) }},
		{"ImportListRepo.GetByID", func() error { _, err := NewImportListRepo(closed).GetByID(ctx, 1); return err }},
		{"ImportListRepo.List", func() error { _, err := NewImportListRepo(closed).List(ctx); return err }},
		{"ImportListRepo.ListByType", func() error { _, err := NewImportListRepo(closed).ListByType(ctx, "x"); return err }},
		{"ImportListRepo.ListExclusions", func() error { _, err := NewImportListRepo(closed).ListExclusions(ctx); return err }},
		{"ImportListRepo.Update", func() error { return NewImportListRepo(closed).Update(ctx, &models.ImportList{}) }},
		{"ImportListRepo.UpdateLastSyncAt", func() error { return NewImportListRepo(closed).UpdateLastSyncAt(ctx, 1) }},
		{"IndexerRepo.AddQueryCount", func() error { return NewIndexerRepo(closed).AddQueryCount(ctx, 1, now, 1) }},
		{"IndexerRepo.Create", func() error { return NewIndexerRepo(closed).Create(ctx, &models.Indexer{}) }},
		{"IndexerRepo.Delete", func() error { return NewIndexerRepo(closed).Delete(ctx, 1) }},
		{"IndexerRepo.DeleteByProwlarrInstance", func() error { return NewIndexerRepo(closed).DeleteByProwlarrInstance(ctx, 1) }},
		{"IndexerRepo.GetByID", func() error { _, err := NewIndexerRepo(closed).GetByID(ctx, 1); return err }},
		{"IndexerRepo.List", func() error { _, err := NewIndexerRepo(closed).List(ctx); return err }},
		{"IndexerRepo.ListByProwlarrInstance", func() error { _, err := NewIndexerRepo(closed).ListByProwlarrInstance(ctx, 1); return err }},
		{"IndexerRepo.LoadQueryCounts", func() error { _, err := NewIndexerRepo(closed).LoadQueryCounts(ctx, now); return err }},
		{"IndexerRepo.PruneQueryCounts", func() error { return NewIndexerRepo(closed).PruneQueryCounts(ctx, now) }},
		{"IndexerRepo.QueryUsage", func() error { _, err := NewIndexerRepo(closed).QueryUsage(ctx, now); return err }},
		{"IndexerRepo.RecordSearchFailure", func() error { return NewIndexerRepo(closed).RecordSearchFailure(ctx, 1, 1, "x", now) }},
		{"IndexerRepo.RecordSearchSuccess", func() error { return NewIndexerRepo(closed).RecordSearchSuccess(ctx, 1, now) }},
		{"IndexerRepo.Update", func() error { return NewIndexerRepo(closed).Update(ctx, &models.Indexer{}) }},
		{"IndexerRepo.UpdateAPIKeyByProwlarrInstance", func() error { _, err := NewIndexerRepo(closed).UpdateAPIKeyByProwlarrInstance(ctx, 1, "x"); return err }},
		{"LogRepo.ErrorSummary", func() error { _, _, _, err := NewLogRepo(closed).ErrorSummary(ctx, now, 1); return err }},
		{"LogRepo.Insert", func() error { return NewLogRepo(closed).Insert(ctx, LogEntry{}) }},
		{"LogRepo.Query", func() error { _, err := NewLogRepo(closed).Query(ctx, LogFilter{}); return err }},
		{"LogRepo.Trim", func() error { return NewLogRepo(closed).Trim(ctx, now) }},
		{"MetadataProfileRepo.Create", func() error { return NewMetadataProfileRepo(closed).Create(ctx, &models.MetadataProfile{}) }},
		{"MetadataProfileRepo.CreateForUser", func() error { return NewMetadataProfileRepo(closed).CreateForUser(ctx, &models.MetadataProfile{}, 1) }},
		{"MetadataProfileRepo.Delete", func() error { return NewMetadataProfileRepo(closed).Delete(ctx, 1) }},
		{"MetadataProfileRepo.GetByID", func() error { _, err := NewMetadataProfileRepo(closed).GetByID(ctx, 1); return err }},
		{"MetadataProfileRepo.List", func() error { _, err := NewMetadataProfileRepo(closed).List(ctx); return err }},
		{"MetadataProfileRepo.Update", func() error { return NewMetadataProfileRepo(closed).Update(ctx, &models.MetadataProfile{}) }},
		{"NotificationRepo.Create", func() error { return NewNotificationRepo(closed).Create(ctx, &models.Notification{}) }},
		{"NotificationRepo.Delete", func() error { return NewNotificationRepo(closed).Delete(ctx, 1) }},
		{"NotificationRepo.GetByID", func() error { _, err := NewNotificationRepo(closed).GetByID(ctx, 1); return err }},
		{"NotificationRepo.List", func() error { _, err := NewNotificationRepo(closed).List(ctx); return err }},
		{"NotificationRepo.Update", func() error { return NewNotificationRepo(closed).Update(ctx, &models.Notification{}) }},
		{"PendingReleaseRepo.DeleteByBook", func() error { return NewPendingReleaseRepo(closed).DeleteByBook(ctx, 1) }},
		{"PendingReleaseRepo.DeleteByBookAndMediaType", func() error { return NewPendingReleaseRepo(closed).DeleteByBookAndMediaType(ctx, 1, "x") }},
		{"PendingReleaseRepo.DeleteByGUID", func() error { return NewPendingReleaseRepo(closed).DeleteByGUID(ctx, "x") }},
		{"PendingReleaseRepo.DeleteByID", func() error { return NewPendingReleaseRepo(closed).DeleteByID(ctx, 1) }},
		{"PendingReleaseRepo.GetByID", func() error { _, err := NewPendingReleaseRepo(closed).GetByID(ctx, 1); return err }},
		{"PendingReleaseRepo.GetOwnerByID", func() error { _, _, err := NewPendingReleaseRepo(closed).GetOwnerByID(ctx, 1); return err }},
		{"PendingReleaseRepo.List", func() error { _, err := NewPendingReleaseRepo(closed).List(ctx); return err }},
		{"PendingReleaseRepo.ListByBook", func() error { _, err := NewPendingReleaseRepo(closed).ListByBook(ctx, 1); return err }},
		{"PendingReleaseRepo.ListByBookAndMediaType", func() error { _, err := NewPendingReleaseRepo(closed).ListByBookAndMediaType(ctx, 1, "x"); return err }},
		{"PendingReleaseRepo.ListForUser", func() error { _, err := NewPendingReleaseRepo(closed).ListForUser(ctx, 1); return err }},
		{"PendingReleaseRepo.Upsert", func() error { return NewPendingReleaseRepo(closed).Upsert(ctx, &models.PendingRelease{}) }},
		{"ProwlarrRepo.Create", func() error { return NewProwlarrRepo(closed).Create(ctx, &models.ProwlarrInstance{}) }},
		{"ProwlarrRepo.Delete", func() error { return NewProwlarrRepo(closed).Delete(ctx, 1) }},
		{"ProwlarrRepo.GetByID", func() error { _, err := NewProwlarrRepo(closed).GetByID(ctx, 1); return err }},
		{"ProwlarrRepo.List", func() error { _, err := NewProwlarrRepo(closed).List(ctx); return err }},
		{"ProwlarrRepo.SetLastSyncAt", func() error { return NewProwlarrRepo(closed).SetLastSyncAt(ctx, 1, now) }},
		{"ProwlarrRepo.Update", func() error { return NewProwlarrRepo(closed).Update(ctx, &models.ProwlarrInstance{}) }},
		{"QualityProfileRepo.AuthorNamesUsing", func() error { _, err := NewQualityProfileRepo(closed).AuthorNamesUsing(ctx, 1, 1); return err }},
		{"QualityProfileRepo.CountAuthorsUsing", func() error { _, err := NewQualityProfileRepo(closed).CountAuthorsUsing(ctx, 1); return err }},
		{"QualityProfileRepo.Create", func() error { return NewQualityProfileRepo(closed).Create(ctx, &models.QualityProfile{}) }},
		{"QualityProfileRepo.CreateForUser", func() error { return NewQualityProfileRepo(closed).CreateForUser(ctx, &models.QualityProfile{}, 1) }},
		{"QualityProfileRepo.Delete", func() error { return NewQualityProfileRepo(closed).Delete(ctx, 1) }},
		{"QualityProfileRepo.GetByID", func() error { _, err := NewQualityProfileRepo(closed).GetByID(ctx, 1); return err }},
		{"QualityProfileRepo.List", func() error { _, err := NewQualityProfileRepo(closed).List(ctx); return err }},
		{"QualityProfileRepo.NameExists", func() error { _, err := NewQualityProfileRepo(closed).NameExists(ctx, "x", 1); return err }},
		{"QualityProfileRepo.Update", func() error { return NewQualityProfileRepo(closed).Update(ctx, &models.QualityProfile{}) }},
		{"RecommendationRepo.AddAuthorExclusion", func() error { return NewRecommendationRepo(closed).AddAuthorExclusion(ctx, 1, "x") }},
		{"RecommendationRepo.ClearDismissals", func() error { return NewRecommendationRepo(closed).ClearDismissals(ctx, 1) }},
		{"RecommendationRepo.Dismiss", func() error { return NewRecommendationRepo(closed).Dismiss(ctx, 1, 1) }},
		{"RecommendationRepo.GetByID", func() error { _, err := NewRecommendationRepo(closed).GetByID(ctx, 1); return err }},
		{"RecommendationRepo.IsDismissed", func() error { _, err := NewRecommendationRepo(closed).IsDismissed(ctx, 1, "x"); return err }},
		{"RecommendationRepo.List", func() error { _, err := NewRecommendationRepo(closed).List(ctx, 1, "x", 1, 1); return err }},
		{"RecommendationRepo.ListAuthorExclusions", func() error { _, err := NewRecommendationRepo(closed).ListAuthorExclusions(ctx, 1); return err }},
		{"RecommendationRepo.ListDismissedIDs", func() error { _, err := NewRecommendationRepo(closed).ListDismissedIDs(ctx, 1); return err }},
		{"RecommendationRepo.RemoveAuthorExclusion", func() error { return NewRecommendationRepo(closed).RemoveAuthorExclusion(ctx, 1, "x") }},
		{"RecommendationRepo.ReplaceBatch", func() error {
			return NewRecommendationRepo(closed).ReplaceBatch(ctx, 1, []models.RecommendationCandidate{{}})
		}},
		{"RequestRepo.Claim", func() error { _, err := NewRequestRepo(closed).Claim(ctx, 1, 1); return err }},
		{"RequestRepo.ClaimAutoApprove", func() error { _, err := NewRequestRepo(closed).ClaimAutoApprove(ctx, 1, 1, now, 1); return err }},
		{"RequestRepo.Complete", func() error { return NewRequestRepo(closed).Complete(ctx, 1, "x", ptrInt64(1), ptrInt64(1)) }},
		{"RequestRepo.CountPending", func() error { _, err := NewRequestRepo(closed).CountPending(ctx); return err }},
		{"RequestRepo.CountPendingByOwner", func() error { _, err := NewRequestRepo(closed).CountPendingByOwner(ctx, 1); return err }},
		{"RequestRepo.Create", func() error { return NewRequestRepo(closed).Create(ctx, &models.LibraryRequest{}, 1) }},
		{"RequestRepo.Decline", func() error { return NewRequestRepo(closed).Decline(ctx, 1, 1, "x") }},
		{"RequestRepo.DeletePendingForOwner", func() error { return NewRequestRepo(closed).DeletePendingForOwner(ctx, 1, 1) }},
		{"RequestRepo.GetByID", func() error { _, err := NewRequestRepo(closed).GetByID(ctx, 1); return err }},
		{"RequestRepo.GetForOwner", func() error { _, err := NewRequestRepo(closed).GetForOwner(ctx, 1, "x", "x"); return err }},
		{"RequestRepo.ListAll", func() error { _, _, err := NewRequestRepo(closed).ListAll(ctx, "x", 1, 1); return err }},
		{"RequestRepo.ListByOwner", func() error { _, _, err := NewRequestRepo(closed).ListByOwner(ctx, 1, 1, 1); return err }},
		{"RequestRepo.ListLibraryProjection", func() error { _, _, err := NewRequestRepo(closed).ListLibraryProjection(ctx, 1, "x", 1, 1); return err }},
		{"RequestRepo.Release", func() error { return NewRequestRepo(closed).Release(ctx, 1, "x") }},
		{"RequestRepo.RenewClaim", func() error { return NewRequestRepo(closed).RenewClaim(ctx, 1, "x") }},
		{"RequestRepo.Reopen", func() error { return NewRequestRepo(closed).Reopen(ctx, 1, 1, "x", "x", 1) }},
		{"RootFolderRepo.Create", func() error { _, err := NewRootFolderRepo(closed).Create(ctx, "x"); return err }},
		{"RootFolderRepo.Delete", func() error { return NewRootFolderRepo(closed).Delete(ctx, 1) }},
		{"RootFolderRepo.GetByID", func() error { _, err := NewRootFolderRepo(closed).GetByID(ctx, 1); return err }},
		{"RootFolderRepo.List", func() error { _, err := NewRootFolderRepo(closed).List(ctx); return err }},
		{"RootFolderRepo.UpdateFreeSpace", func() error { return NewRootFolderRepo(closed).UpdateFreeSpace(ctx, 1, 1) }},
		{"SeriesRepo.ClearGenreOverride", func() error { return NewSeriesRepo(closed).ClearGenreOverride(ctx, 1) }},
		{"SeriesRepo.Create", func() error { return NewSeriesRepo(closed).Create(ctx, &models.Series{}) }},
		{"SeriesRepo.CreateManual", func() error { _, err := NewSeriesRepo(closed).CreateManual(ctx, "x"); return err }},
		{"SeriesRepo.CreateOrGet", func() error { return NewSeriesRepo(closed).CreateOrGet(ctx, &models.Series{}) }},
		{"SeriesRepo.Delete", func() error { return NewSeriesRepo(closed).Delete(ctx, 1) }},
		{"SeriesRepo.DeleteHardcoverLink", func() error { return NewSeriesRepo(closed).DeleteHardcoverLink(ctx, 1) }},
		{"SeriesRepo.EnsureHardcoverLinkFromForeignID", func() error {
			_, err := NewSeriesRepo(closed).EnsureHardcoverLinkFromForeignID(ctx, 1, "hc-series:1", "x")
			return err
		}},
		{"SeriesRepo.GetBookBySeriesPosition", func() error { _, err := NewSeriesRepo(closed).GetBookBySeriesPosition(ctx, "x", "x"); return err }},
		{"SeriesRepo.GetByForeignID", func() error { _, err := NewSeriesRepo(closed).GetByForeignID(ctx, "x"); return err }},
		{"SeriesRepo.GetByID", func() error { _, err := NewSeriesRepo(closed).GetByID(ctx, 1); return err }},
		{"SeriesRepo.GetByIDForUser", func() error { _, err := NewSeriesRepo(closed).GetByIDForUser(ctx, 1, 1); return err }},
		{"SeriesRepo.GetHardcoverLink", func() error { _, err := NewSeriesRepo(closed).GetHardcoverLink(ctx, 1); return err }},
		{"SeriesRepo.GetPrimarySeriesForBook", func() error { _, _, err := NewSeriesRepo(closed).GetPrimarySeriesForBook(ctx, 1); return err }},
		{"SeriesRepo.GetSeriesIDsForBook", func() error { _, err := NewSeriesRepo(closed).GetSeriesIDsForBook(ctx, 1); return err }},
		{"SeriesRepo.HasPrimarySeries", func() error { _, err := NewSeriesRepo(closed).HasPrimarySeries(ctx, 1); return err }},
		{"SeriesRepo.LinkBook", func() error { return NewSeriesRepo(closed).LinkBook(ctx, 1, 1, "x", true) }},
		{"SeriesRepo.LinkBookIfMissing", func() error { _, err := NewSeriesRepo(closed).LinkBookIfMissing(ctx, 1, 1, "x", true); return err }},
		{"SeriesRepo.LinkBookPreservingPrimary", func() error { _, err := NewSeriesRepo(closed).LinkBookPreservingPrimary(ctx, 1, 1, "x"); return err }},
		{"SeriesRepo.List", func() error { _, err := NewSeriesRepo(closed).List(ctx); return err }},
		{"SeriesRepo.ListBookSeriesByAuthor", func() error { _, err := NewSeriesRepo(closed).ListBookSeriesByAuthor(ctx, 1); return err }},
		{"SeriesRepo.ListBookSeriesMembershipsByAuthor", func() error { _, err := NewSeriesRepo(closed).ListBookSeriesMembershipsByAuthor(ctx, 1); return err }},
		{"SeriesRepo.ListBookSeriesMembershipsForBook", func() error { _, err := NewSeriesRepo(closed).ListBookSeriesMembershipsForBook(ctx, 1); return err }},
		{"SeriesRepo.ListBooksInSeries", func() error { _, err := NewSeriesRepo(closed).ListBooksInSeries(ctx, 1); return err }},
		{"SeriesRepo.ListBooksInSeriesIncludingExcluded", func() error { _, err := NewSeriesRepo(closed).ListBooksInSeriesIncludingExcluded(ctx, 1); return err }},
		{"SeriesRepo.ListByAuthor", func() error { _, err := NewSeriesRepo(closed).ListByAuthor(ctx, 1); return err }},
		{"SeriesRepo.ListPageWithBooksForUser", func() error { _, _, err := NewSeriesRepo(closed).ListPageWithBooksForUser(ctx, 1, 1, 1); return err }},
		{"SeriesRepo.ListWithBooks", func() error { _, err := NewSeriesRepo(closed).ListWithBooks(ctx); return err }},
		{"SeriesRepo.ListWithBooksForUser", func() error { _, err := NewSeriesRepo(closed).ListWithBooksForUser(ctx, 1); return err }},
		{"SeriesRepo.SearchTitles", func() error { _, err := NewSeriesRepo(closed).SearchTitles(ctx, "x", 1, 1); return err }},
		{"SeriesRepo.SetGenreOverride", func() error { return NewSeriesRepo(closed).SetGenreOverride(ctx, 1, []string{"x"}) }},
		{"SeriesRepo.SetMonitored", func() error { return NewSeriesRepo(closed).SetMonitored(ctx, 1, true) }},
		{"SeriesRepo.SetPrimarySeries", func() error { return NewSeriesRepo(closed).SetPrimarySeries(ctx, 1, 1) }},
		{"SeriesRepo.UnlinkBook", func() error { return NewSeriesRepo(closed).UnlinkBook(ctx, 1, 1) }},
		{"SeriesRepo.UpdateBookLinkPosition", func() error { return NewSeriesRepo(closed).UpdateBookLinkPosition(ctx, 1, 1, "x") }},
		{"SeriesRepo.UpdateForeignID", func() error { return NewSeriesRepo(closed).UpdateForeignID(ctx, 1, "x") }},
		{"SeriesRepo.UpdateTitle", func() error { return NewSeriesRepo(closed).UpdateTitle(ctx, 1, "x") }},
		{"SeriesRepo.UpsertBookLink", func() error { return NewSeriesRepo(closed).UpsertBookLink(ctx, 1, 1, "x", true) }},
		{"SeriesRepo.UpsertHardcoverLink", func() error { return NewSeriesRepo(closed).UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{}) }},
		{"SettingsRepo.Delete", func() error { return NewSettingsRepo(closed).Delete(ctx, "x") }},
		{"SettingsRepo.Get", func() error { _, err := NewSettingsRepo(closed).Get(ctx, "x"); return err }},
		{"SettingsRepo.List", func() error { _, err := NewSettingsRepo(closed).List(ctx); return err }},
		{"SettingsRepo.Set", func() error { return NewSettingsRepo(closed).Set(ctx, "x", "x") }},
		{"SettingsRepo.SetIfAbsent", func() error { _, err := NewSettingsRepo(closed).SetIfAbsent(ctx, "x", "x"); return err }},
		{"SettingsRepo.SetMany", func() error { return NewSettingsRepo(closed).SetMany(ctx, []SettingKV{{}}) }},
		{"UnmatchedUnitRepo.AuthorReferencedElsewhere", func() error { _, err := NewUnmatchedUnitRepo(closed).AuthorReferencedElsewhere(ctx, 1, 1); return err }},
		{"UnmatchedUnitRepo.BookFingerprint", func() error { _, err := NewUnmatchedUnitRepo(closed).BookFingerprint(ctx, 1); return err }},
		{"UnmatchedUnitRepo.BookReferencedElsewhere", func() error { _, err := NewUnmatchedUnitRepo(closed).BookReferencedElsewhere(ctx, 1, 1); return err }},
		{"UnmatchedUnitRepo.BookRefs", func() error { _, err := NewUnmatchedUnitRepo(closed).BookRefs(ctx, []int64{1}); return err }},
		{"UnmatchedUnitRepo.Claim", func() error { _, err := NewUnmatchedUnitRepo(closed).Claim(ctx, 1, "x", "x"); return err }},
		{"UnmatchedUnitRepo.ClaimState", func() error { _, err := NewUnmatchedUnitRepo(closed).ClaimState(ctx, 1, "x", "x"); return err }},
		{"UnmatchedUnitRepo.CompleteAdoption", func() error {
			_, err := NewUnmatchedUnitRepo(closed).CompleteAdoption(ctx, 1, "x", AdoptionRecord{})
			return err
		}},
		{"UnmatchedUnitRepo.CompleteUndo", func() error { _, err := NewUnmatchedUnitRepo(closed).CompleteUndo(ctx, 1, "x"); return err }},
		{"UnmatchedUnitRepo.Facets", func() error { _, err := NewUnmatchedUnitRepo(closed).Facets(ctx, UnmatchedListQuery{}); return err }},
		{"UnmatchedUnitRepo.Get", func() error { _, err := NewUnmatchedUnitRepo(closed).Get(ctx, 1); return err }},
		{"UnmatchedUnitRepo.IgnorePending", func() error { _, err := NewUnmatchedUnitRepo(closed).IgnorePending(ctx, []int64{1}, "x"); return err }},
		{"UnmatchedUnitRepo.List", func() error { _, _, err := NewUnmatchedUnitRepo(closed).List(ctx, UnmatchedListQuery{}); return err }},
		{"UnmatchedUnitRepo.ReconcileScan", func() error {
			_, err := NewUnmatchedUnitRepo(closed).ReconcileScan(ctx, []UnmatchedUnitScan{{}}, ReconcileScanOptions{})
			return err
		}},
		{"UnmatchedUnitRepo.RecordAdoptionProgress", func() error {
			_, err := NewUnmatchedUnitRepo(closed).RecordAdoptionProgress(ctx, 1, "x", AdoptionRecord{})
			return err
		}},
		{"UnmatchedUnitRepo.ReleaseClaim", func() error { _, err := NewUnmatchedUnitRepo(closed).ReleaseClaim(ctx, 1, "x", "x", "x"); return err }},
		{"UnmatchedUnitRepo.ResetToPending", func() error { _, err := NewUnmatchedUnitRepo(closed).ResetToPending(ctx, 1, "x", "x"); return err }},
		{"UnmatchedUnitRepo.StaleClaims", func() error { _, err := NewUnmatchedUnitRepo(closed).StaleClaims(ctx, now); return err }},
		{"UnmatchedUnitRepo.Summary", func() error { _, err := NewUnmatchedUnitRepo(closed).Summary(ctx); return err }},
		{"UserRepo.BumpSessionEpoch", func() error { return NewUserRepo(closed).BumpSessionEpoch(ctx, 1) }},
		{"UserRepo.Count", func() error { _, err := NewUserRepo(closed).Count(ctx); return err }},
		{"UserRepo.CountAdmins", func() error { _, err := NewUserRepo(closed).CountAdmins(ctx); return err }},
		{"UserRepo.Create", func() error { _, err := NewUserRepo(closed).Create(ctx, "x", "x"); return err }},
		{"UserRepo.CreateFirstAdmin", func() error { _, err := NewUserRepo(closed).CreateFirstAdmin(ctx, "x", "x"); return err }},
		{"UserRepo.Delete", func() error { return NewUserRepo(closed).Delete(ctx, 1, UserDeletePlan{}) }},
		{"UserRepo.FirstAdminID", func() error { _, err := NewUserRepo(closed).FirstAdminID(ctx); return err }},
		{"UserRepo.GetByEmail", func() error { _, err := NewUserRepo(closed).GetByEmail(ctx, "x"); return err }},
		{"UserRepo.GetByID", func() error { _, err := NewUserRepo(closed).GetByID(ctx, 1); return err }},
		{"UserRepo.GetByOIDC", func() error { _, err := NewUserRepo(closed).GetByOIDC(ctx, "x", "x"); return err }},
		{"UserRepo.GetByUsername", func() error { _, err := NewUserRepo(closed).GetByUsername(ctx, "x"); return err }},
		{"UserRepo.GetOrCreateByOIDC", func() error {
			_, err := NewUserRepo(closed).GetOrCreateByOIDC(ctx, "x", "x", "x", "x", "x", "x")
			return err
		}},
		{"UserRepo.GetOrCreateByUsername", func() error { _, err := NewUserRepo(closed).GetOrCreateByUsername(ctx, "x"); return err }},
		{"UserRepo.GetSessionEpoch", func() error { _, err := NewUserRepo(closed).GetSessionEpoch(ctx, 1); return err }},
		{"UserRepo.LinkOIDCSubject", func() error { return NewUserRepo(closed).LinkOIDCSubject(ctx, 1, "x", "x") }},
		{"UserRepo.List", func() error { _, err := NewUserRepo(closed).List(ctx); return err }},
		{"UserRepo.OwnedRows", func() error { _, err := NewUserRepo(closed).OwnedRows(ctx, 1); return err }},
		{"UserRepo.PromoteFirstUser", func() error { return NewUserRepo(closed).PromoteFirstUser(ctx) }},
		{"UserRepo.SetRequestsAutoApprove", func() error { return NewUserRepo(closed).SetRequestsAutoApprove(ctx, 1, true) }},
		{"UserRepo.SetRole", func() error { return NewUserRepo(closed).SetRole(ctx, 1, "x") }},
		{"UserRepo.SetRoleUnguarded", func() error { return NewUserRepo(closed).SetRoleUnguarded(ctx, 1, "x") }},
		{"UserRepo.UpdatePassword", func() error { return NewUserRepo(closed).UpdatePassword(ctx, 1, "x") }},
		{"UserRepo.UpdateUsername", func() error { return NewUserRepo(closed).UpdateUsername(ctx, 1, "x") }},
	}
	for _, c := range calls {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s panicked on a closed database: %v", c.name, r)
				}
			}()
			if err := c.call(); err == nil {
				t.Errorf("%s returned nil on a closed database; the failure was swallowed", c.name)
			}
		}()
	}
}
