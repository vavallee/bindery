package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// The author-majority language fallback stamps the author's main language on
// works nothing else could resolve. It must not stamp it on a title written in
// another script: that is a translation, and stamping "eng" on it let it past
// even unknown_language_behavior = fail (#3091). A work its provider proved is
// in a non-allowed language is rejected on that evidence, and is not stamped
// either.
func TestFetchAuthorBooks_MajorityLanguageSkipsUnresolvableTranslations(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	profile, err := profileRepo.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || profile == nil {
		t.Fatalf("GetByID(default profile): profile=%+v err=%v", profile, err)
	}
	profile.AllowedLanguages = "eng"
	profile.UnknownLanguageBehavior = models.UnknownLanguageFail
	if err := profileRepo.Update(ctx, profile); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "hc:orson-scott-card", Name: "Orson Scott Card", SortName: "Card, Orson Scott",
		MetadataProvider: "hardcover", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	work := func(id, title, language string) models.Book {
		return models.Book{
			ForeignID: id, Title: title, SortTitle: title, Language: language,
			MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, MetadataProvider: "hardcover",
		}
	}
	provider := &languageEvidenceMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover", works: []models.Book{
			work("hc:enders-game", "Ender's Game", "eng"),
			work("hc:speaker-for-the-dead", "Speaker for the Dead", "eng"),
			work("hc:xenocide", "Xenocide", "eng"),
			work("hc:children-of-the-mind", "Children of the Mind", "eng"),
			work("hc:seventh-son", "Seventh Son", "eng"),
			// The provider found only Spanish editions.
			work("hc:el-septimo-hijo", "El séptimo hijo", ""),
			// No evidence either way, but the title is in another script.
			work("hc:rozhbi-na-saznanieto", "Рожби на съзнанието", ""),
			// No evidence and a Latin title: the fallback still applies.
			work("hc:red-prophet", "Red Prophet", ""),
		}},
		evidence: map[string]metadata.AuthorWorkLanguageEvidence{
			"hc:el-septimo-hijo": {State: metadata.AuthorWorkLanguageNotAllowed, Language: "spa"},
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range books {
		got[b.ForeignID] = b.Language
	}
	for _, translation := range []string{"hc:el-septimo-hijo", "hc:rozhbi-na-saznanieto"} {
		if language, added := got[translation]; added {
			t.Errorf("%s added with language %q; a translation must not inherit the author's language", translation, language)
		}
	}
	if got["hc:red-prophet"] != "eng" {
		t.Errorf("hc:red-prophet language = %q, want eng from the author majority (books: %v)", got["hc:red-prophet"], got)
	}
	if len(books) != 6 {
		t.Errorf("added %d books, want the five English works and Red Prophet: %v", len(books), got)
	}
}
