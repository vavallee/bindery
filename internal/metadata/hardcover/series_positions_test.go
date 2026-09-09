package hardcover

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
)

// Hardcover records a translation as its own book sharing the original's
// position, so a slot arrives holding one entry per language. The shape and
// the reader counts here are those the API returns for position 1 of Harry
// Potter (series 1185).
func TestCollapseSeriesPositionsKeepsTheMostHeldEntry(t *testing.T) {
	books := []metadata.SeriesCatalogBook{
		{ProviderID: "2456202", Title: "Droga królów", Position: "1", UsersCount: 12},
		{ProviderID: "328491", Title: "Harry Potter and the Philosopher's Stone", Position: "1", UsersCount: 17351},
		{ProviderID: "1945721", Title: "Гарри Поттер и философский камень", Position: "1", UsersCount: 40},
		{ProviderID: "429306", Title: "Harry Potter and the Chamber of Secrets", Position: "2", UsersCount: 13542},
	}

	got := collapseSeriesPositions(books)

	if len(got) != 2 {
		t.Fatalf("collapseSeriesPositions() kept %d books, want one per position", len(got))
	}
	if got[0].ProviderID != "328491" {
		t.Errorf("position 1 = %q (%s), want the English novel", got[0].Title, got[0].ProviderID)
	}
	if got[1].ProviderID != "429306" {
		t.Errorf("position 2 = %q (%s)", got[1].Title, got[1].ProviderID)
	}
}

// A book with no position is an extra the series accumulated, not a duplicate
// of a volume, so several of them must all survive.
func TestCollapseSeriesPositionsLeavesUnpositionedBooksAlone(t *testing.T) {
	books := []metadata.SeriesCatalogBook{
		{ProviderID: "1", Title: "Companion", Position: "", UsersCount: 5},
		{ProviderID: "2", Title: "Artbook", Position: "  ", UsersCount: 3},
		{ProviderID: "3", Title: "Volume One", Position: "1", UsersCount: 10},
	}

	got := collapseSeriesPositions(books)

	if len(got) != 3 {
		t.Fatalf("collapseSeriesPositions() kept %d books, want all three", len(got))
	}
}

// Fractional positions are their own slots: a 2.5 novella is not a competing
// edition of volume 2.
func TestCollapseSeriesPositionsTreatsFractionalPositionsSeparately(t *testing.T) {
	books := []metadata.SeriesCatalogBook{
		{ProviderID: "1", Title: "Words of Radiance", Position: "2", UsersCount: 6088},
		{ProviderID: "2", Title: "Edgedancer", Position: "2.5", UsersCount: 3315},
	}

	got := collapseSeriesPositions(books)

	if len(got) != 2 {
		t.Fatalf("collapseSeriesPositions() kept %d books, want both", len(got))
	}
}

// Order is the caller's; collapsing must not reshuffle what it keeps.
func TestCollapseSeriesPositionsPreservesOrder(t *testing.T) {
	books := []metadata.SeriesCatalogBook{
		{ProviderID: "a", Title: "One", Position: "1", UsersCount: 1},
		{ProviderID: "b", Title: "Two", Position: "2", UsersCount: 1},
		{ProviderID: "c", Title: "One, translated", Position: "1", UsersCount: 99},
		{ProviderID: "d", Title: "Three", Position: "3", UsersCount: 1},
	}

	got := collapseSeriesPositions(books)

	want := []string{"c", "b", "d"}
	if len(got) != len(want) {
		t.Fatalf("collapseSeriesPositions() kept %d books, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ProviderID != id {
			t.Errorf("book %d = %q, want %q", i, got[i].ProviderID, id)
		}
	}
}

// Box sets are dropped by the query rather than in Go, because Hardcover files
// them under the position of the first book they contain and there is no way
// to tell one from a volume once it has arrived.
func TestSeriesCatalogQueryExcludesCompilations(t *testing.T) {
	if !strings.Contains(seriesCatalogQuery, "{compilation: {_eq: false}}") {
		t.Error("GetBooksBySeries no longer excludes compilations")
	}
	if !strings.Contains(seriesCatalogQuery, "{compilation: {_is_null: true}}") {
		t.Error("the compilation filter dropped its null arm: an _eq never matches a null in Hasura, so a book with the column unset would vanish from every series catalog")
	}
	if !strings.Contains(seriesCatalogQuery, "users_count: desc_nulls_last") {
		t.Error("GetBooksBySeries no longer orders competing entries by reader count")
	}
}

// The collapse has to be reached through GetSeriesCatalog, not only called
// directly: the whole bug is that title dedup runs on this path and cannot see
// a translation, so a test that skips the path would not have caught it.
func TestGetSeriesCatalogKeepsOneBookPerPosition(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"series_by_pk": map[string]interface{}{
				"id":          1185,
				"name":        "Harry Potter",
				"books_count": 7,
				"author":      map[string]interface{}{"name": "J.K. Rowling"},
				"book_series": []map[string]interface{}{
					{
						"position": 1,
						"book": map[string]interface{}{
							"id":          328491,
							"title":       "Harry Potter and the Philosopher's Stone",
							"slug":        "harry-potter-and-the-philosophers-stone",
							"users_count": 17351,
						},
					},
					{
						"position": 1,
						"book": map[string]interface{}{
							"id":          1945721,
							"title":       "Гарри Поттер и философский камень",
							"slug":        "garri-potter-i-filosofskii-kamen",
							"users_count": 40,
						},
					},
					{
						"position": 2,
						"book": map[string]interface{}{
							"id":          429306,
							"title":       "Harry Potter and the Chamber of Secrets",
							"slug":        "harry-potter-and-the-chamber-of-secrets",
							"users_count": 13542,
						},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	catalog, err := c.GetSeriesCatalog(context.Background(), "hc-series:1185")
	if err != nil {
		t.Fatalf("GetSeriesCatalog: %v", err)
	}
	if len(catalog.Books) != 2 {
		ids := make([]string, 0, len(catalog.Books))
		for _, book := range catalog.Books {
			ids = append(ids, book.ForeignID)
		}
		t.Fatalf("catalog holds %d books, want one per position: %s", len(catalog.Books), strings.Join(ids, ", "))
	}
	if got := catalog.Books[0].ForeignID; got != "hc:harry-potter-and-the-philosophers-stone" {
		t.Errorf("position 1 = %q, want the English novel", got)
	}
	if got := catalog.Books[1].ForeignID; got != "hc:harry-potter-and-the-chamber-of-secrets" {
		t.Errorf("position 2 = %q", got)
	}
}
