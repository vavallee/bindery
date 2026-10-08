package abs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func covImpCreateAuthor(t *testing.T, env *covImpEnv, foreignID, name string) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: name, SortName: name, Monitored: true}
	if err := env.authors.Create(context.Background(), a); err != nil {
		t.Fatalf("Create author %q: %v", name, err)
	}
	return a
}

func covImpCreateAlias(t *testing.T, env *covImpEnv, authorID int64, name, source string) *models.AuthorAlias {
	t.Helper()
	a := &models.AuthorAlias{AuthorID: authorID, Name: name, SourceOLID: source}
	if err := env.aliases.Create(context.Background(), a); err != nil {
		t.Fatalf("Create alias %q: %v", name, err)
	}
	return a
}

func TestCovImpAuthorMatcher_GetAuthorAndAliasBookkeeping(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()

	var nilMatcher *authorMatcher
	if a, err := nilMatcher.getAuthor(ctx, 1); a != nil || err != nil {
		t.Fatalf("nil matcher getAuthor = %v %v", a, err)
	}
	nilMatcher.addAlias(models.AuthorAlias{AuthorID: 1, Name: "x"}) // must not panic
	nilMatcher.addAuthor(&models.Author{ID: 1})
	if ok, err := nilMatcher.authorMatchesABSName(ctx, &models.Author{ID: 1, Name: "Andy Weir"}, "Someone Else"); ok || err != nil {
		t.Fatalf("nil matcher authorMatchesABSName = %v %v", ok, err)
	}

	matcher, err := env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("newAuthorMatcher: %v", err)
	}
	// An author created after the matcher was built is fetched lazily, then cached.
	late := covImpCreateAuthor(t, env, "OL-LATE", "Late Author")
	got, err := matcher.getAuthor(ctx, late.ID)
	if err != nil || got == nil || got.Name != "Late Author" {
		t.Fatalf("getAuthor(late) = %+v %v", got, err)
	}
	got.Name = "mutated"
	again, _ := matcher.getAuthor(ctx, late.ID)
	if again.Name != "Late Author" {
		t.Fatalf("getAuthor returned shared state: %q", again.Name)
	}
	if missing, err := matcher.getAuthor(ctx, 999999); missing != nil || err != nil {
		t.Fatalf("getAuthor(missing) = %v %v, want nil nil", missing, err)
	}

	matcher.addAlias(models.AuthorAlias{AuthorID: 0, Name: "nobody"})
	matcher.addAlias(models.AuthorAlias{AuthorID: late.ID, Name: "   "})
	matcher.addAlias(models.AuthorAlias{AuthorID: late.ID, Name: "L. Author"})
	matcher.addAlias(models.AuthorAlias{AuthorID: late.ID, Name: "l. author"})
	if len(matcher.aliases) != 1 {
		t.Fatalf("aliases = %+v, want one deduplicated alias", matcher.aliases)
	}

	env.exec(t, "ALTER TABLE authors RENAME TO covimp_authors_gone")
	if _, err := matcher.getAuthor(ctx, 424242); err == nil {
		t.Fatal("getAuthor with broken table returned nil error")
	}
	if _, err := env.importer.newAuthorMatcher(ctx); err == nil {
		t.Fatal("newAuthorMatcher with broken table returned nil error")
	}
	if _, _, _, err := env.importer.findAuthorByName(ctx, "anyone"); err == nil {
		t.Fatal("findAuthorByName with broken table returned nil error")
	}
}

func TestCovImpAuthorMatcher_AliasListErrorFailsMatcher(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	env.exec(t, "ALTER TABLE author_aliases RENAME TO covimp_aliases_gone")
	if _, err := env.importer.newAuthorMatcher(context.Background()); err == nil {
		t.Fatal("newAuthorMatcher with broken alias table returned nil error")
	}
	if _, err := env.importer.cleanupABSSourcedAliases(context.Background()); err == nil {
		t.Fatal("cleanupABSSourcedAliases with broken alias table returned nil error")
	}
}

func TestCovImpAuthorMatcher_FindAuthorByNameTiers(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	tolkien := covImpCreateAuthor(t, env, "OL-TOLKIEN", "J.R.R. Tolkien")
	covImpCreateAlias(t, env, tolkien.ID, "Ronald Tolkien Pen", "OL-SOURCE")
	pratchett := covImpCreateAuthor(t, env, "OL-PRATCHETT", "Terry Pratchett")
	covImpCreateAlias(t, env, pratchett.ID, "Sir Terry", "OL-SOURCE")
	// Two distinct authors share the alias-free name below so the exact tier
	// is ambiguous.
	covImpCreateAuthor(t, env, "OL-SMITH-1", "John Smith")
	covImpCreateAuthor(t, env, "OL-SMITH-2", "john smith")

	matcher, err := env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("newAuthorMatcher: %v", err)
	}
	cases := []struct {
		name      string
		wantID    int64
		wantBy    string
		ambiguous bool
	}{
		{name: "Sir Terry", wantID: pratchett.ID, wantBy: "alias"},
		{name: "terry pratchett", wantID: pratchett.ID, wantBy: "name"},
		{name: "John Smith", ambiguous: true},
		{name: "Completely Unknown Person"},
	}
	for _, tc := range cases {
		got, by, ambiguous, err := matcher.findAuthorByName(ctx, tc.name)
		if err != nil {
			t.Fatalf("findAuthorByName(%q): %v", tc.name, err)
		}
		if ambiguous != tc.ambiguous {
			t.Fatalf("findAuthorByName(%q) ambiguous = %v, want %v", tc.name, ambiguous, tc.ambiguous)
		}
		if tc.wantID == 0 {
			if got != nil {
				t.Fatalf("findAuthorByName(%q) = %+v, want nil", tc.name, got)
			}
			continue
		}
		if got == nil || got.ID != tc.wantID || by != tc.wantBy {
			t.Fatalf("findAuthorByName(%q) = %+v by %q, want id %d by %q", tc.name, got, by, tc.wantID, tc.wantBy)
		}
	}

	// An alias whose author row vanished is ignored rather than matched.
	ghost := covImpCreateAuthor(t, env, "OL-GHOST", "Ghost Writer")
	matcher.addAlias(models.AuthorAlias{AuthorID: ghost.ID + 1000, Name: "Phantom Pen", SourceOLID: "OL-X"})
	if got, _, ambiguous, err := matcher.findAuthorByName(ctx, "Phantom Pen"); got != nil || ambiguous || err != nil {
		t.Fatalf("dangling alias match = %+v %v %v, want nothing", got, ambiguous, err)
	}
}

func TestCovImpAuthorMatcher_NormalizedAndFuzzyAliasTiers(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	author := covImpCreateAuthor(t, env, "OL-CANON", "Canonical Person")
	// A trusted alias (non-ABS source) under a different real-name form.
	covImpCreateAlias(t, env, author.ID, "Ursula K. Le Guin", "OL-SRC")

	matcher, err := env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("newAuthorMatcher: %v", err)
	}
	got, by, ambiguous, err := matcher.findAuthorByName(ctx, "Le Guin, Ursula K.")
	if err != nil || ambiguous || got == nil || got.ID != author.ID {
		t.Fatalf("normalized alias = %+v by %q ambiguous=%v err=%v", got, by, ambiguous, err)
	}
	// "Le Guin, Ursula K." only equals the alias after name normalisation,
	// so the match comes from the normalized alias tier, not the exact one.
	if by != "normalized_alias" {
		t.Fatalf("matchedBy = %q, want normalized_alias", by)
	}
	if !shouldRecordAuthorVariantAlias("normalized_alias") || shouldRecordAuthorVariantAlias("alias") {
		t.Fatal("shouldRecordAuthorVariantAlias disagrees with the tier contract")
	}

	// authorMatchesABSName consults aliases owned by the author.
	ok, err := matcher.authorMatchesABSName(ctx, author, "Ursula K. Le Guin")
	if err != nil || !ok {
		t.Fatalf("authorMatchesABSName via alias = %v %v, want true", ok, err)
	}
	ok, err = matcher.authorMatchesABSName(ctx, author, "Someone Unrelated")
	if err != nil || ok {
		t.Fatalf("authorMatchesABSName unrelated = %v %v, want false", ok, err)
	}
	if ok, _ := matcher.authorMatchesABSName(ctx, author, "Canonical Person"); !ok {
		t.Fatal("authorMatchesABSName must accept the author's own name")
	}
	if ok, _ := matcher.authorMatchesABSName(ctx, nil, "x"); ok {
		t.Fatal("nil author matched")
	}
	if ok, _ := matcher.authorMatchesABSName(ctx, author, "  "); ok {
		t.Fatal("blank name matched")
	}
}

func TestCovImpTrustedAuthorAliasAndInitials(t *testing.T) {
	t.Parallel()
	author := &models.Author{ID: 1, Name: "Andy Weir"}
	if trustedAuthorAlias(models.AuthorAlias{Name: "x"}, nil) {
		t.Fatal("nil author trusted")
	}
	if !trustedAuthorAlias(models.AuthorAlias{Name: "Pen Name", SourceOLID: "OL123A"}, author) {
		t.Fatal("provider-sourced alias must be trusted regardless of name")
	}
	if trustedAuthorAlias(models.AuthorAlias{Name: "Pen Name", SourceOLID: "abs"}, author) {
		t.Fatal("abs-sourced alias with an unrelated name must not be trusted")
	}
	if !trustedAuthorAlias(models.AuthorAlias{Name: "A. Weir", SourceOLID: "ABS"}, author) {
		t.Fatal("abs-sourced initial variant must be trusted")
	}
	if trustedAuthorAlias(models.AuthorAlias{Name: "Pen Name"}, author) {
		t.Fatal("unsourced unrelated alias trusted")
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"a weir", "andy weir", true},
		{"andy weir", "a weir", true},
		{"andy weir", "andy weir", false},
		{"andy weir", "bob weir", false},
		{"a b weir", "andy weir", false},
		{"", "", false},
	} {
		if got := normalizedAuthorInitialVariantMatch(tc.a, tc.b); got != tc.want {
			t.Fatalf("normalizedAuthorInitialVariantMatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCovImpCleanupABSSourcedAliases(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	author := covImpCreateAuthor(t, env, "OL-WEIR", "Andy Weir")
	keep := covImpCreateAlias(t, env, author.ID, "A. Weir", "abs")
	stale := covImpCreateAlias(t, env, author.ID, "Totally Different", "abs")
	same := covImpCreateAlias(t, env, author.ID, "ANDY WEIR", "abs")
	provider := covImpCreateAlias(t, env, author.ID, "Provider Pen", "OL-SRC")
	orphanAuthor := covImpCreateAuthor(t, env, "OL-ORPHAN", "Orphan Author")
	orphan := covImpCreateAlias(t, env, orphanAuthor.ID, "Orphan Pen", "abs")
	env.exec(t, "PRAGMA foreign_keys = OFF")
	env.exec(t, "DELETE FROM authors WHERE id = ?", orphanAuthor.ID)
	env.exec(t, "PRAGMA foreign_keys = ON")

	removed, err := env.importer.cleanupABSSourcedAliases(ctx)
	if err != nil {
		t.Fatalf("cleanupABSSourcedAliases: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (unrelated and identical abs aliases)", removed)
	}
	remaining := map[int64]bool{}
	list, err := env.aliases.List(ctx)
	if err != nil {
		t.Fatalf("List aliases: %v", err)
	}
	for _, a := range list {
		remaining[a.ID] = true
	}
	if !remaining[keep.ID] || !remaining[provider.ID] || !remaining[orphan.ID] {
		t.Fatalf("remaining = %v, want variant, provider and orphan aliases kept", remaining)
	}
	if remaining[stale.ID] || remaining[same.ID] {
		t.Fatalf("remaining = %v, want stale and identical aliases removed", remaining)
	}

	bare := &Importer{}
	if n, err := bare.cleanupABSSourcedAliases(ctx); n != 0 || err != nil {
		t.Fatalf("cleanup without repos = %d %v", n, err)
	}

	env.failOn(t, "covimp_alias_delete", "DELETE", "author_aliases", "")
	covImpCreateAlias(t, env, author.ID, "Unrelated Again", "abs")
	if _, err := env.importer.cleanupABSSourcedAliases(ctx); err == nil || !strings.Contains(err.Error(), "covimp boom") {
		t.Fatalf("cleanup with failing delete err = %v, want trigger error", err)
	}
}

func TestCovImpRecordSecondaryAuthorsAndVariantAlias(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	author := covImpCreateAuthor(t, env, "OL-WEIR", "Andy Weir")
	matcher, err := env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("newAuthorMatcher: %v", err)
	}

	// Without a matcher the canonical author comes from the repo.
	env.importer.recordSecondaryAuthors(ctx, author.ID, []NormalizedAuthor{
		{Name: "A. Weir"}, {Name: "  "}, {Name: "Somebody Else"},
	}, nil)
	list, _ := env.aliases.ListByAuthor(ctx, author.ID)
	if len(list) != 1 || list[0].Name != "A. Weir" || list[0].SourceOLID != "abs" {
		t.Fatalf("aliases = %+v, want one abs-sourced A. Weir", list)
	}

	// With a matcher, a duplicate alias is skipped and the matcher only learns new ones.
	env.importer.recordSecondaryAuthors(ctx, author.ID, []NormalizedAuthor{{Name: "Andy  Weir"}}, matcher)
	env.importer.recordSecondaryAuthors(ctx, 0, []NormalizedAuthor{{Name: "A. Weir"}}, matcher)
	env.importer.recordSecondaryAuthors(ctx, 424242, []NormalizedAuthor{{Name: "A. Weir"}}, nil)

	env.importer.recordAuthorVariantAlias(ctx, author.ID, "  ", matcher)
	env.importer.recordAuthorVariantAlias(ctx, 0, "Weir A", matcher)
	env.importer.recordAuthorVariantAlias(ctx, author.ID, "Weir, Andy", matcher)
	list, _ = env.aliases.ListByAuthor(ctx, author.ID)
	names := map[string]bool{}
	for _, a := range list {
		names[a.Name] = true
	}
	if !names["Weir, Andy"] || names["Weir A"] {
		t.Fatalf("aliases = %v, want only the valid variant recorded", names)
	}
	before := len(matcher.aliases)
	// Creating the same alias for another author fails and must not reach the matcher.
	other := covImpCreateAuthor(t, env, "OL-OTHER", "Other Person")
	env.importer.recordAuthorVariantAlias(ctx, other.ID, "Weir, Andy", matcher)
	if len(matcher.aliases) != before {
		t.Fatalf("matcher aliases grew from %d to %d on a failed create", before, len(matcher.aliases))
	}

	env.exec(t, "ALTER TABLE authors RENAME TO covimp_authors_gone")
	env.importer.recordSecondaryAuthors(ctx, author.ID+1000, []NormalizedAuthor{{Name: "x"}}, nil)
}

// covImpSearchErrProvider fails author search and lookup, to drive the
// upstream lookup's error returns.
type covImpSearchErrProvider struct {
	stubABSMetadataProvider
	searchErr error
	getErr    error
}

func (p *covImpSearchErrProvider) SearchAuthors(ctx context.Context, q string) ([]models.Author, error) {
	if p.searchErr != nil {
		return nil, p.searchErr
	}
	return p.stubABSMetadataProvider.SearchAuthors(ctx, q)
}

func (p *covImpSearchErrProvider) GetAuthor(ctx context.Context, id string) (*models.Author, error) {
	if p.getErr != nil {
		return nil, p.getErr
	}
	return p.stubABSMetadataProvider.GetAuthor(ctx, id)
}

func TestCovImpLookupUpstreamAuthorOutcomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newImporter := func(p metadata.Provider) *Importer {
		imp := &Importer{}
		imp.WithMetadata(metadata.NewAggregator(p))
		imp.resetUpstreamAuthorCache()
		return imp
	}

	if a, amb, err := (&Importer{}).lookupUpstreamAuthorUncached(ctx, "   "); a != nil || amb || err != nil {
		t.Fatalf("blank name = %v %v %v", a, amb, err)
	}

	boom := errors.New("covimp search down")
	imp := newImporter(&covImpSearchErrProvider{searchErr: boom})
	if _, _, err := imp.lookupUpstreamAuthorUncached(ctx, "Andy Weir"); !errors.Is(err, boom) {
		t.Fatalf("search error = %v, want %v", err, boom)
	}

	getBoom := errors.New("covimp get down")
	imp = newImporter(&covImpSearchErrProvider{
		stubABSMetadataProvider: stubABSMetadataProvider{searchAuthors: []models.Author{{ForeignID: "OL1A", Name: "Andy Weir"}}},
		getErr:                  getBoom,
	})
	if _, _, err := imp.lookupUpstreamAuthorUncached(ctx, "Andy Weir"); !errors.Is(err, getBoom) {
		t.Fatalf("get error = %v, want %v", err, getBoom)
	}

	imp = newImporter(&stubABSMetadataProvider{searchAuthors: []models.Author{{ForeignID: "OL9A", Name: "Zzyzx Quorble"}}})
	if a, amb, err := imp.lookupUpstreamAuthorUncached(ctx, "Andy Weir"); a != nil || amb || err != nil {
		t.Fatalf("no match = %v %v %v, want nil false nil", a, amb, err)
	}
}

func TestCovImpDominantExactAuthorMatch(t *testing.T) {
	t.Parallel()
	withCount := func(id string, n int) models.Author {
		return models.Author{ForeignID: id, Statistics: &models.AuthorStats{BookCount: n}}
	}
	cases := []struct {
		name   string
		in     map[string]models.Author
		wantID string
		wantOK bool
	}{
		{name: "empty", in: map[string]models.Author{}},
		{name: "no counts", in: map[string]models.Author{"a": {ForeignID: "a"}, "b": {ForeignID: "b"}}},
		{name: "single with count", in: map[string]models.Author{"a": withCount("a", 3)}, wantID: "a", wantOK: true},
		{name: "clear winner", in: map[string]models.Author{"a": withCount("a", 40), "b": withCount("b", 2)}, wantID: "a", wantOK: true},
		{name: "winner over zero", in: map[string]models.Author{"a": withCount("a", 12), "b": {ForeignID: "b"}}, wantID: "a", wantOK: true},
		{name: "gap too small", in: map[string]models.Author{"a": withCount("a", 9), "b": withCount("b", 2)}},
		{name: "not double", in: map[string]models.Author{"a": withCount("a", 30), "b": withCount("b", 18)}},
	}
	for _, tc := range cases {
		got, ok := dominantExactAuthorMatch(tc.in)
		if ok != tc.wantOK || got.ForeignID != tc.wantID {
			t.Fatalf("%s: dominantExactAuthorMatch = %q %v, want %q %v", tc.name, got.ForeignID, ok, tc.wantID, tc.wantOK)
		}
	}
	if authorSearchWorkCount(models.Author{}) != 0 || authorSearchWorkCount(withCount("x", 7)) != 7 {
		t.Fatal("authorSearchWorkCount mismatch")
	}
	err := &PrimaryProviderUnavailableError{Primary: "hardcover"}
	if !strings.Contains(err.Error(), `"hardcover"`) || err.Unwrap() != nil {
		t.Fatalf("PrimaryProviderUnavailableError = %q", err.Error())
	}
	inner := errors.New("429")
	err = &PrimaryProviderUnavailableError{Primary: "hardcover", Failed: "hardcover (rate limited)", Err: inner}
	if !strings.Contains(err.Error(), "rate limited") || !strings.HasSuffix(err.Error(), ": 429") || !errors.Is(err, inner) {
		t.Fatalf("PrimaryProviderUnavailableError = %q", err.Error())
	}
}
