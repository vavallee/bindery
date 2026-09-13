package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func strptr(s string) *string { return &s }

// The exact-match bonus in scoreResult compares MatchCriteria.ISBN against the
// ISBN parsed out of the release name, and nothing ever populated the criteria
// side, so the bonus could not fire (#1724). CriteriaISBN is what both search
// paths now call to fill it in, and this walks the whole way from a book whose
// only recorded ISBN is an edition's isbn_10 through to the boost changing the
// ranking of real releases fetched through the real searcher.
//
// Non-vacuity: with CriteriaISBN returning "" (the old behaviour) the two
// releases score identically and the stable sort keeps insertion order, so the
// ISBN-carrying release does not come first.
func TestCriteriaISBN_EditionISBN10FiresRankingBoost(t *testing.T) {
	// 0441172717 is the ISBN-10 of Dune; 9780441172719 the ISBN-13 of the same
	// edition. A release name can only ever carry the ISBN-13 form.
	const isbn13 = "9780441172719"
	book := &models.Book{ID: 7, Title: "Dune"}
	editions := []models.Edition{{ID: 1, BookID: 7, ISBN10: strptr("0-441-17271-7")}}

	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert"}
	crit.ISBN = CriteriaISBN(book, editions)
	if crit.ISBN != isbn13 {
		t.Fatalf("CriteriaISBN = %q, want the ISBN-13 form %q", crit.ISBN, isbn13)
	}

	const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="2"/>
    <item>
      <title>Dune Frank Herbert epub</title>
      <guid isPermaLink="false">no-isbn</guid>
      <enclosure url="https://fake/dl/1" length="1000" type="application/x-nzb"/>
      <newznab:attr name="author" value="Frank Herbert"/>
    </item>
    <item>
      <title>Dune Frank Herbert 9780441172719 epub</title>
      <guid isPermaLink="false">isbn</guid>
      <enclosure url="https://fake/dl/2" length="1000" type="application/x-nzb"/>
      <newznab:attr name="author" value="Frank Herbert"/>
    </item>
  </channel>
</rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	results := newTestSearcher().SearchBook(context.Background(), idxs, crit)

	if len(results) != 2 {
		t.Fatalf("expected both releases back, got %d: %v", len(results), resultTitles(results))
	}
	// Sanity: the release side really does parse the ISBN the criteria carries.
	if got := ParseRelease("Dune Frank Herbert 9780441172719 epub").ISBN; got != crit.ISBN {
		t.Fatalf("release ISBN %q != criteria ISBN %q", got, crit.ISBN)
	}
	if results[0].GUID != "isbn" {
		t.Errorf("ISBN-matching release should rank first, got order: %v", resultTitles(results))
	}
}

// A book with several editions carries one ISBN into MatchCriteria, so the
// edition the user actually selected must win over the merely-first one.
func TestCriteriaISBN_PrefersSelectedEdition(t *testing.T) {
	selected := int64(2)
	book := &models.Book{ID: 7, SelectedEditionID: &selected}
	editions := []models.Edition{
		{ID: 1, BookID: 7, ISBN13: strptr("9780441013593")},
		{ID: 2, BookID: 7, ISBN13: strptr("978-0-441-17271-9")},
	}
	if got := CriteriaISBN(book, editions); got != "9780441172719" {
		t.Errorf("CriteriaISBN = %q, want the selected edition's ISBN 9780441172719", got)
	}
}

func TestCriteriaISBN_NoUsableISBN(t *testing.T) {
	book := &models.Book{ID: 7}
	for _, tt := range []struct {
		name     string
		editions []models.Edition
	}{
		{name: "no editions"},
		{name: "editions without isbns", editions: []models.Edition{{ID: 1, BookID: 7}}},
		{name: "unusable isbn", editions: []models.Edition{{ID: 1, BookID: 7, ISBN13: strptr("not-an-isbn")}}},
		// An ISBN whose check digit does not verify is not a usable search
		// criterion: it cannot name a real book, and the ISBN-10 case is
		// worse than useless, because the conversion recomputes the ISBN-13
		// check digit from a mistyped core and so produces a well-formed
		// identifier for a book nobody asked for.
		{name: "isbn13 with a bad check digit", editions: []models.Edition{{ID: 1, BookID: 7, ISBN13: strptr("9780306406152")}}},
		{name: "isbn10 with a bad check digit", editions: []models.Edition{{ID: 1, BookID: 7, ISBN10: strptr("0441127717")}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CriteriaISBN(book, tt.editions); got != "" {
				t.Errorf("CriteriaISBN = %q, want empty", got)
			}
		})
	}
}
