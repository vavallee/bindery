package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// These tests cover the persistence choke point for #3086. The startup backfill
// rewrites a stored "nob" to "nor", but a provider that reports "nob" again on
// the next refresh would write it straight back unless the repositories
// themselves canonicalise. Each test hands the repo a raw provider code and
// reads back what was stored.

func languageRepoFixture(t *testing.T) (*BookRepo, *EditionRepo, *models.Author, context.Context) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-LW-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return NewBookRepo(database), NewEditionRepo(database), author, ctx
}

func newLanguageBook(authorID int64, fid, lang string) *models.Book {
	return &models.Book{
		ForeignID: fid, AuthorID: authorID, Title: "T " + fid, SortTitle: "T " + fid,
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "nb",
		Monitored: true, Language: lang,
	}
}

func TestBookRepoCreate_NormalizesLanguage(t *testing.T) {
	books, _, author, ctx := languageRepoFixture(t)
	for i, tc := range []struct{ in, want string }{
		{"nob", "nor"}, {"nno", "nor"}, {"deu", "ger"}, {"en", "eng"}, {"Klingon", "klingon"}, {"", ""},
	} {
		b := newLanguageBook(author.ID, "LW-C"+string(rune('a'+i)), tc.in)
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		got, err := books.GetByID(ctx, b.ID)
		if err != nil || got == nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Language != tc.want {
			t.Errorf("Create(%q) stored %q, want %q", tc.in, got.Language, tc.want)
		}
	}
}

func TestBookRepoUpdate_NormalizesLanguage(t *testing.T) {
	books, _, author, ctx := languageRepoFixture(t)
	b := newLanguageBook(author.ID, "LW-U", "nor")
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	// A refresh hands back what the provider said, not what is stored.
	b.Language = "nob"
	if err := books.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, _ := books.GetByID(ctx, b.ID)
	if got.Language != "nor" {
		t.Errorf("Update(nob) stored %q, want nor", got.Language)
	}

	// The guarded variant and the hydration write share the rule.
	got.Language = "nno"
	if ok, err := books.UpdateIfUnchanged(ctx, got, got.UpdatedAtRaw); err != nil || !ok {
		t.Fatalf("UpdateIfUnchanged ok=%v err=%v", ok, err)
	}
	got, _ = books.GetByID(ctx, b.ID)
	if got.Language != "nor" {
		t.Errorf("UpdateIfUnchanged(nno) stored %q, want nor", got.Language)
	}
	got.Language = "deu"
	if ok, err := books.UpdateHydratedMetadata(ctx, got, got.UpdatedAtRaw); err != nil || !ok {
		t.Fatalf("UpdateHydratedMetadata ok=%v err=%v", ok, err)
	}
	got, _ = books.GetByID(ctx, b.ID)
	if got.Language != "ger" {
		t.Errorf("UpdateHydratedMetadata(deu) stored %q, want ger", got.Language)
	}
	if err := books.SetLanguage(ctx, b.ID, "fra"); err != nil {
		t.Fatal(err)
	}
	got, _ = books.GetByID(ctx, b.ID)
	if got.Language != "fre" {
		t.Errorf("SetLanguage(fra) stored %q, want fre", got.Language)
	}
}

func TestEditionRepoUpsert_NormalizesLanguage(t *testing.T) {
	books, editions, author, ctx := languageRepoFixture(t)
	b := newLanguageBook(author.ID, "LW-E", "")
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	e := &models.Edition{ForeignID: "nb:ed1", BookID: b.ID, Title: "Ed", Language: "nob", Monitored: true}
	if err := editions.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := editions.GetByForeignID(ctx, "nb:ed1")
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Language != "nor" {
		t.Errorf("Upsert insert stored %q, want nor", got.Language)
	}
	// The conflict branch rewrites the column too.
	e.Language = "deu"
	if err := editions.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ = editions.GetByForeignID(ctx, "nb:ed1")
	if got.Language != "ger" {
		t.Errorf("Upsert conflict stored %q, want ger", got.Language)
	}
	// And UpsertMetadata, the provider edition path.
	m := &models.Edition{ForeignID: "nb:ed2", BookID: b.ID, Title: "Ed2", Language: "nno", Monitored: true}
	if ok, err := editions.UpsertMetadata(ctx, m, b.ForeignID, b.MetadataProvider); err != nil || !ok {
		t.Fatalf("UpsertMetadata ok=%v err=%v", ok, err)
	}
	got, _ = editions.GetByForeignID(ctx, "nb:ed2")
	if got.Language != "nor" {
		t.Errorf("UpsertMetadata stored %q, want nor", got.Language)
	}
}
