package metadata

import (
	"maps"
	"slices"

	"github.com/vavallee/bindery/internal/models"
)

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

func cloneEditions(editions []models.Edition) []models.Edition {
	out := slices.Clone(editions)
	for i := range out {
		e := &out[i]
		e.ISBN13 = clonePointer(e.ISBN13)
		e.ISBN10 = clonePointer(e.ISBN10)
		e.ASIN = clonePointer(e.ASIN)
		e.PublishDate = clonePointer(e.PublishDate)
		e.NumPages = clonePointer(e.NumPages)
	}
	return out
}

func cloneBooks(books []models.Book) []models.Book {
	out := slices.Clone(books)
	for i := range out {
		b := &out[i]
		b.ReleaseDate = clonePointer(b.ReleaseDate)
		b.SelectedEditionID = clonePointer(b.SelectedEditionID)
		b.CalibreID = clonePointer(b.CalibreID)
		b.LastMetadataRefreshAt = clonePointer(b.LastMetadataRefreshAt)
		b.Genres = slices.Clone(b.Genres)
		b.LockedFields = slices.Clone(b.LockedFields)
		b.Editions = cloneEditions(b.Editions)
		b.BookFiles = slices.Clone(b.BookFiles)
		b.Identifiers = slices.Clone(b.Identifiers)
		b.SeriesRefs = slices.Clone(b.SeriesRefs)
		b.ProviderISBNs = slices.Clone(b.ProviderISBNs)
		b.CreditedAuthorForeignIDs = slices.Clone(b.CreditedAuthorForeignIDs)
		if b.Author != nil {
			b.Author = &cloneAuthors([]models.Author{*b.Author})[0]
		}
	}
	return out
}

func cloneAuthors(authors []models.Author) []models.Author {
	out := slices.Clone(authors)
	for i := range out {
		a := &out[i]
		a.QualityProfileID = clonePointer(a.QualityProfileID)
		a.MetadataProfileID = clonePointer(a.MetadataProfileID)
		a.RootFolderID = clonePointer(a.RootFolderID)
		a.AudiobookRootFolderID = clonePointer(a.AudiobookRootFolderID)
		a.LastMetadataRefreshAt = clonePointer(a.LastMetadataRefreshAt)
		a.ProviderIdentifiers = maps.Clone(a.ProviderIdentifiers)
		a.Books = cloneBooks(a.Books)
		a.Statistics = clonePointer(a.Statistics)
		a.Aliases = slices.Clone(a.Aliases)
		a.MonitoredSeriesIDs = slices.Clone(a.MonitoredSeriesIDs)
		a.AlternateNames = slices.Clone(a.AlternateNames)
		a.ProviderMismatch = clonePointer(a.ProviderMismatch)
		a.LastSync = clonePointer(a.LastSync)
		if s := a.LastSync; s != nil {
			s.AllowedLanguages = slices.Clone(s.AllowedLanguages)
			s.SkippedLanguageSample = slices.Clone(s.SkippedLanguageSample)
			s.SkippedPartBooksSample = slices.Clone(s.SkippedPartBooksSample)
			s.SkippedMissingDateSample = slices.Clone(s.SkippedMissingDateSample)
			s.SkippedMinPagesSample = slices.Clone(s.SkippedMinPagesSample)
			s.SkippedMissingISBNSample = slices.Clone(s.SkippedMissingISBNSample)
		}
	}
	return out
}

func cloneAuthor(author *models.Author) *models.Author {
	if author == nil {
		return nil
	}
	return &cloneAuthors([]models.Author{*author})[0]
}

func cloneBook(book *models.Book) *models.Book {
	if book == nil {
		return nil
	}
	return &cloneBooks([]models.Book{*book})[0]
}
