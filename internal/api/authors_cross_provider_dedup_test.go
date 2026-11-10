package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestFetchAuthorBooks_CrossProviderWorkDoesNotMintASecondRow reproduces #1705.
//
// The reported sequence: a Hardcover-linked series fills its books, the user
// runs Find better metadata on the author and picks their OpenLibrary entry,
// then refreshes. The refresh fetches OpenLibrary works, whose ids no existing
// row has ever held, and used to create a parallel row for a volume already in
// the library. The reporter was left with two rows for volume 1 of The Mimosa
// Confessions and no way to merge them that did not cost the series link.
//
// The link between the two is HardcoverForeignID: mergeAuthorWorks joins the
// providers' catalogues in memory before any of this, so the OpenLibrary work
// arrives carrying the Hardcover id of the same book.
func TestFetchAuthorBooks_CrossProviderWorkDoesNotMintASecondRow(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	// The author, now relinked to OpenLibrary.
	author := &models.Author{
		ForeignID: "OL500A", Name: "Mimosa Author", SortName: "Author, Mimosa",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// The book as the series fill created it: from Hardcover.
	existing := &models.Book{
		ForeignID: "hc:mimosa-1", AuthorID: author.ID,
		Title: "The Mimosa Confessions", SortTitle: "mimosa confessions",
		Genres: []string{}, Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover",
		Language: "eng",
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// The refresh returns the same volume as an OpenLibrary work. The title
	// differs, so the canonical-title fallback cannot save this: the id link is
	// the only thing connecting them.
	works := []models.Book{{
		ForeignID: "OL500W", Title: "Mimosa Confessions, The", SortTitle: "mimosa confessions the",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", HardcoverForeignID: "hc:mimosa-1",
	}}
	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider),
		settingsRepo, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		for _, b := range got {
			t.Logf("book %d: %q (%s)", b.ID, b.Title, b.ForeignID)
		}
		t.Fatalf("author has %d books, want 1; the same volume was created twice", len(got))
	}
	if got[0].ID != existing.ID {
		t.Errorf("surviving row is %d, want the original %d", got[0].ID, existing.ID)
	}

	// The OpenLibrary id is now on record, so the next refresh matches exactly
	// rather than depending on the ids lining up again.
	resolved, err := bookRepo.GetByAnyForeignID(ctx, "OL500W")
	if err != nil {
		t.Fatalf("lookup by OL id: %v", err)
	}
	if resolved == nil || resolved.ID != existing.ID {
		t.Errorf("the OpenLibrary id was not recorded against the matched row: %+v", resolved)
	}
}

// TestFetchAuthorBooks_UnrelatedWorkStillCreatesARow is the guard on the other
// side: widening the lookup must not start collapsing genuinely distinct books,
// which is the failure #1704 had to fix in the opposite direction.
func TestFetchAuthorBooks_UnrelatedWorkStillCreatesARow(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	author := &models.Author{
		ForeignID: "OL501A", Name: "Series Author", SortName: "Author, Series",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	volume1 := &models.Book{
		ForeignID: "hc:vol-1", AuthorID: author.ID,
		Title: "Volume One", SortTitle: "volume one",
		Genres: []string{}, Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover", Language: "eng",
	}
	if err := bookRepo.Create(ctx, volume1); err != nil {
		t.Fatal(err)
	}

	// A different volume, carrying its own Hardcover link.
	works := []models.Book{{
		ForeignID: "OL502W", Title: "Volume Two", SortTitle: "volume two",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", HardcoverForeignID: "hc:vol-2",
	}}
	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider),
		settingsRepo, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("author has %d books, want 2; a distinct volume was collapsed", len(got))
	}
}

// otherWorldsOpenLibrary is the primary provider for the #3792 tests: Stephen
// King's OpenLibrary works list in its live order, King and Straub's own
// record for the 2026 novel first (undated, because the search page that
// carries dates does not include it) and John Joseph Adams' 2011 anthology of
// the same title second. Edition sampling calls King's record Portuguese: it
// has one Portuguese edition and one with no language.
type otherWorldsOpenLibrary struct {
	stubMetaProvider
}

func (p *otherWorldsOpenLibrary) FillMissingWorkLanguages(_ context.Context, books []models.Book) int {
	filled := 0
	for i := range books {
		if books[i].ForeignID == "OL44907743W" && books[i].Language == "" {
			books[i].Language = "por"
			filled++
		}
	}
	return filled
}

// otherWorldsHardcover is the Hardcover supplement: King's catalogue holds the
// novel, and it has English editions. Like the real client it answers language
// evidence only for the hc: ids it is asked about.
type otherWorldsHardcover struct {
	stubMetaProvider
}

func (p *otherWorldsHardcover) GetAuthorWorkLanguageEvidence(_ context.Context, books []models.Book, _ []string) (map[string]metadata.AuthorWorkLanguageEvidence, error) {
	out := map[string]metadata.AuthorWorkLanguageEvidence{}
	for _, b := range books {
		if b.ForeignID == otherWorldsNovelHC {
			out[b.ForeignID] = metadata.AuthorWorkLanguageEvidence{State: metadata.AuthorWorkLanguageAllowed, Language: "eng"}
		}
	}
	return out, nil
}

const otherWorldsNovelHC = "hc:other-worlds-than-these-a-talisman-novel-2026"

var (
	otherWorldsAnthologyDate = time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)
	otherWorldsNovelDate     = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
)

func otherWorldsAggregator() *metadata.Aggregator {
	primary := &otherWorldsOpenLibrary{stubMetaProvider{name: "openlibrary", works: []models.Book{
		{
			ForeignID: "OL44907743W", Title: "Other Worlds Than These", SortTitle: "other worlds than these",
			MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", CreditedAuthorForeignIDs: []string{"OL19981A", "OL19807A"},
		},
		{
			ForeignID: "OL17400760W", Title: "Other Worlds Than These", SortTitle: "other worlds than these",
			MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", Language: "eng", ReleaseDate: &otherWorldsAnthologyDate,
			CreditedAuthorForeignIDs: []string{"OL6811804A", "OL19981A"},
		},
	}}}
	hardcover := &otherWorldsHardcover{stubMetaProvider{name: "hardcover", authorWorksByName: []models.Book{{
		ForeignID: otherWorldsNovelHC, Title: "Other Worlds Than These", SortTitle: "other worlds than these",
		MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Genres: []string{},
		MetadataProvider: "hardcover", ReleaseDate: &otherWorldsNovelDate,
		SeriesRefs: []models.SeriesRef{{ForeignID: "hc-series:4521", Title: "The Talisman", Position: "3", Primary: true}},
	}}}}
	return metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
}

// otherWorldsLibrary opens a library with Stephen King in it and the 2011
// anthology already on his shelf, the reporter's starting point. allowed sets
// the default metadata profile's languages ("any" for no filter).
func otherWorldsLibrary(t *testing.T, allowed string) (*AuthorHandler, *db.BookRepo, *models.Author, *models.Book) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profile, err := profileRepo.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || profile == nil {
		t.Fatalf("default profile: %+v %v", profile, err)
	}
	profile.AllowedLanguages = allowed
	if err := profileRepo.Update(ctx, profile); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "OL19981A", Name: "Stephen King", SortName: "King, Stephen",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	anthology := &models.Book{
		ForeignID: "OL17400760W", AuthorID: author.ID,
		Title: "Other Worlds Than These", SortTitle: "other worlds than these",
		Genres: []string{}, Status: models.BookStatusWanted, ReleaseDate: &otherWorldsAnthologyDate,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", Language: "eng",
	}
	if err := bookRepo.Create(ctx, anthology); err != nil {
		t.Fatal(err)
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, otherWorldsAggregator(),
		settingsRepo, profileRepo, nil)
	return h, bookRepo, author, anthology
}

// assertOtherWorldsSplit checks the end state #3792 asks for: the anthology row
// is still the anthology, and the novel has a row of its own that its
// OpenLibrary and Hardcover ids both resolve to.
func assertOtherWorldsSplit(t *testing.T, bookRepo *db.BookRepo, author *models.Author, anthology *models.Book) {
	t.Helper()
	ctx := context.Background()
	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		for _, b := range got {
			t.Logf("book %d: %q (%s) %v", b.ID, b.Title, b.ForeignID, b.ReleaseDate)
		}
		t.Fatalf("author has %d books, want 2: the anthology and the novel", len(got))
	}
	novel, err := bookRepo.GetByForeignID(ctx, "OL44907743W")
	if err != nil {
		t.Fatal(err)
	}
	if novel == nil || novel.ID == anthology.ID {
		t.Fatalf("King's own OpenLibrary record has no row of its own: %+v", novel)
	}
	if novel.ReleaseDate == nil || novel.ReleaseDate.Year() != 2026 {
		t.Errorf("the novel row should carry the 2026 date, got %v", novel.ReleaseDate)
	}
	byHC, err := bookRepo.GetByAnyForeignID(ctx, otherWorldsNovelHC)
	if err != nil {
		t.Fatal(err)
	}
	if byHC == nil || byHC.ID != novel.ID {
		t.Errorf("the novel's Hardcover id should resolve to the novel row %d, got %+v", novel.ID, byHC)
	}
	still, err := bookRepo.GetByID(ctx, anthology.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.ForeignID != "OL17400760W" || still.ReleaseDate == nil || still.ReleaseDate.Year() != 2011 {
		t.Errorf("the anthology row changed: %+v", still)
	}
}

// TestFetchAuthorBooks_OtherWorldsThanTheseGetsItsOwnRow is the end-to-end
// regression for #3792, through the real aggregator and the catalogue sync,
// with the works in the order OpenLibrary actually serves them. The novel used
// to fold onto the anthology row through the sync's title fallback, which
// recorded OL44907743W and the hc: id on the anthology and never created the
// novel. With an English-only profile it was dropped before that: edition
// sampling called King's record Portuguese, and the Hardcover evidence that
// the novel has English editions was never asked for.
func TestFetchAuthorBooks_OtherWorldsThanTheseGetsItsOwnRow(t *testing.T) {
	for _, allowed := range []string{"any", "eng"} {
		t.Run("languages="+allowed, func(t *testing.T) {
			h, bookRepo, author, anthology := otherWorldsLibrary(t, allowed)
			h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
			assertOtherWorldsSplit(t, bookRepo, author, anthology)
			if allowed == "eng" {
				// Hardcover's evidence decided the filter, so its language
				// is what the row stores, not OpenLibrary's Portuguese
				// edition sample.
				novel, err := bookRepo.GetByForeignID(context.Background(), "OL44907743W")
				if err != nil {
					t.Fatal(err)
				}
				if novel.Language != "eng" {
					t.Errorf("the novel row stores language %q, want eng", novel.Language)
				}
			}
		})
	}
}

// TestFetchAuthorBooks_OtherWorldsThanTheseHealsAnEarlierWrongBind covers the
// libraries an earlier release already got wrong: the anthology row holds the
// novel's OpenLibrary and Hardcover ids as aliases, so the id lookup resolved
// the novel to it on every refresh. The next sync gives the novel its own row
// and moves both aliases to it, so its OpenLibrary and hc: ids resolve to the
// novel from then on, and a second sync changes nothing.
func TestFetchAuthorBooks_OtherWorldsThanTheseHealsAnEarlierWrongBind(t *testing.T) {
	h, bookRepo, author, anthology := otherWorldsLibrary(t, "any")
	ctx := context.Background()
	for _, id := range []string{"OL44907743W", otherWorldsNovelHC} {
		if err := bookRepo.UpsertBookIdentifier(ctx, anthology.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
	assertOtherWorldsSplit(t, bookRepo, author, anthology)

	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
	assertOtherWorldsSplit(t, bookRepo, author, anthology)
}

// TestFetchAuthorBooks_SameTitleCrossProviderCloseDatesStillMatch keeps the
// ordinary cross-provider title match: the same book from Hardcover, dated a
// couple of years after OpenLibrary's first edition, is still one row.
func TestFetchAuthorBooks_SameTitleCrossProviderCloseDatesStillMatch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	author := &models.Author{
		ForeignID: "OL19981A", Name: "Stephen King", SortName: "King, Stephen",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	olDate := time.Date(1984, 1, 1, 0, 0, 0, 0, time.UTC)
	hcDate := time.Date(1986, 1, 1, 0, 0, 0, 0, time.UTC)
	existing := &models.Book{
		ForeignID: "OL81613W", AuthorID: author.ID,
		Title: "The Talisman", SortTitle: "talisman",
		Genres: []string{}, Status: models.BookStatusWanted, ReleaseDate: &olDate,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", Language: "eng",
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	works := []models.Book{{
		ForeignID: "hc:the-talisman", Title: "The Talisman", SortTitle: "talisman",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		MetadataProvider: "hardcover", ReleaseDate: &hcDate,
	}}
	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider),
		settingsRepo, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("author has %d books, want 1; the same book was created twice", len(got))
	}
}

// hobbitAggregator serves The Hobbit the way OpenLibrary does: three work
// records for one book, dated 1937, 1990 and 2026 by the search page, plus
// Hardcover's record of it dated hcYear.
func hobbitAggregator(hcYear int) *metadata.Aggregator {
	d := func(y int) *time.Time { t := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC); return &t }
	work := func(id string, year int) models.Book {
		return models.Book{
			ForeignID: id, Title: "The Hobbit", SortTitle: "hobbit", MediaType: models.MediaTypeEbook,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
			Language: "eng", ReleaseDate: d(year),
		}
	}
	primary := &stubMetaProvider{name: "openlibrary", works: []models.Book{
		work("OL27482W", 1937), work("OL219602W", 1990), work("OL45346371W", 2026),
	}}
	hardcover := &stubMetaProvider{name: "hardcover", authorWorksByName: []models.Book{{
		ForeignID: "hc:the-hobbit", Title: "The Hobbit", SortTitle: "hobbit", MediaType: models.MediaTypeEbook,
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover", ReleaseDate: d(hcYear),
	}}}
	return metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
}

func hobbitLibrary(t *testing.T, hcYear int) (*AuthorHandler, *db.BookRepo, *models.Author) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	author := &models.Author{
		ForeignID: "OL26320A", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R.",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, hobbitAggregator(hcYear),
		settingsRepo, profileRepo, nil)
	return h, bookRepo, author
}

func assertOneHobbit(t *testing.T, bookRepo *db.BookRepo, author *models.Author) []models.Book {
	t.Helper()
	got, err := bookRepo.ListByAuthor(context.Background(), author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		for _, b := range got {
			t.Logf("book %d: %q (%s) %v monitored=%v", b.ID, b.Title, b.ForeignID, b.ReleaseDate, b.Monitored)
		}
		t.Fatalf("author has %d Hobbits, want 1", len(got))
	}
	return got
}

// TestFetchAuthorBooks_DuplicateOpenLibraryWorksStayOneBook guards the other
// side of #3792. OpenLibrary holds The Hobbit as three work records decades
// apart, none of them a different book. A fresh library must get one row, as
// before, not three monitored, wanted Hobbits.
func TestFetchAuthorBooks_DuplicateOpenLibraryWorksStayOneBook(t *testing.T) {
	h, bookRepo, author := hobbitLibrary(t, 1937)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
	assertOneHobbit(t, bookRepo, author)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
	assertOneHobbit(t, bookRepo, author)
}

// TestFetchAuthorBooks_OwnedBookIsNeverSplit covers an upgraded library: an
// owned Hobbit row (OL27482W, 1937) holds OL219602W as an alias the old title
// dedup recorded. Even with Hardcover's record dated so that the merge binds
// it to OL219602W, the sync must not mint a wanted second Hobbit for a book
// already on disk.
func TestFetchAuthorBooks_OwnedBookIsNeverSplit(t *testing.T) {
	h, bookRepo, author := hobbitLibrary(t, 1992)
	ctx := context.Background()
	d := time.Date(1937, 1, 1, 0, 0, 0, 0, time.UTC)
	owned := &models.Book{
		ForeignID: "OL27482W", AuthorID: author.ID, Title: "The Hobbit", SortTitle: "hobbit",
		Genres: []string{}, Status: models.BookStatusImported, ReleaseDate: &d,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", Language: "eng",
	}
	if err := bookRepo.Create(ctx, owned); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.UpsertBookIdentifier(ctx, owned.ID, "OL219602W"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "The Hobbit.epub")
	if err := os.WriteFile(file, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, owned.ID, models.MediaTypeEbook, file); err != nil {
		t.Fatal(err)
	}
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)
	got := assertOneHobbit(t, bookRepo, author)
	if got[0].ID != owned.ID {
		t.Errorf("the surviving Hobbit is %d, want the owned row %d", got[0].ID, owned.ID)
	}
}
