package nb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/models"
)

// The fixtures in testdata/ have the exact shape of api.nb.no and
// authority.bibsys.no responses, with an invented author, titles and ISBNs.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeNB answers each request from route, which maps it to (fixture file,
// status), and records every request it saw.
type fakeNB struct {
	t     *testing.T
	mu    sync.Mutex
	reqs  []*http.Request
	route func(*http.Request) (string, int)
}

func (f *fakeNB) client() *Client {
	return &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.mu.Unlock()
		file, status := f.route(r)
		body := ""
		if file != "" {
			b, err := os.ReadFile("testdata/" + file)
			if err != nil {
				f.t.Fatalf("fixture: %v", err)
			}
			body = string(b)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func isAuthority(r *http.Request) bool { return r.URL.Host == "authority.bibsys.no" }

func TestSearchAuthors(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "author_works.json", 200 }}
	authors, err := f.client().SearchAuthors(context.Background(), "kari nordmann")
	if err != nil {
		t.Fatal(err)
	}
	// Two people share the name; the authority ID keeps them apart. The
	// co-credited author and translators are dropped by the name match.
	if len(authors) != 2 {
		t.Fatalf("got %d authors, want 2: %+v", len(authors), authors)
	}
	a := authors[0]
	if a.ForeignID != "nb:author:10000001" || a.Name != "Kari Nordmann" || a.SortName != "Nordmann, Kari" || a.MetadataProvider != "nb" {
		t.Errorf("first author = %+v", a)
	}
	if a.Statistics == nil || a.Statistics.BookCount != 6 {
		t.Errorf("record count = %+v, want 6 (author credits only, not the translator credit)", a.Statistics)
	}
	if authors[1].ForeignID != "nb:author:10000002" {
		t.Errorf("second author = %q", authors[1].ForeignID)
	}

	q := f.reqs[0].URL.Query()
	if got := q["filter"]; strings.Join(got, ",") != "api_nameauthor:kari,api_nameauthor:nordmann,mediatype:bøker" {
		t.Errorf("filters = %v", got)
	}
	if q.Get("expand") != "metadata" {
		t.Errorf("expand = %q; without it search hits carry no author credits", q.Get("expand"))
	}
	if ua := f.reqs[0].Header.Get("User-Agent"); !strings.HasPrefix(ua, "bindery/") {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestGetBookByISBN_Audiobook(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	b, err := f.client().GetBookByISBN(context.Background(), "978-82-00-00002-8")
	if err != nil || b == nil {
		t.Fatalf("book=%v err=%v", b, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
	if got := f.reqs[0].URL.Query()["filter"]; len(got) != 0 {
		t.Errorf("ISBN lookup must not filter by media type, got %v", got)
	}
	if b.ForeignID != "nb:a0000000000000000000000000000002" || b.Title != "Fjellvinden" || b.Language != "nob" {
		t.Errorf("book = %q %q %q", b.ForeignID, b.Title, b.Language)
	}
	if b.Author == nil || b.Author.ForeignID != "nb:author:10000001" {
		t.Errorf("author = %+v; the narrator must not be taken for the author", b.Author)
	}
	if len(b.Editions) != 1 || b.Editions[0].Format != "audiobook" || *b.Editions[0].ISBN13 != "9788200000028" {
		t.Errorf("editions = %+v", b.Editions)
	}
}

func TestSearchBooks_ISBNQueryUsesISBNLookup(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	books, err := f.client().SearchBooks(context.Background(), "9788200000028")
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
}

// The aggregator's canonical lookup searches the primary with "isbn:<n>".
func TestSearchBooks_PrefixedISBNQueryUsesISBNLookup(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	books, err := f.client().SearchBooks(context.Background(), "isbn:9788200000028")
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
}

func TestSearchBooks_TextSearchIsMetadataOnly(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "author_works.json", 200 }}
	if _, err := f.client().SearchBooks(context.Background(), "fjellvinden: roman"); err != nil {
		t.Fatal(err)
	}
	q := f.reqs[0].URL.Query()
	// The default searchType includes OCR'd full text of digitised books.
	if q.Get("searchType") != "FIELD_RESTRICTED_SEARCH" || q.Get("q") != `fjellvinden\: roman` {
		t.Errorf("searchType=%q q=%q", q.Get("searchType"), q.Get("q"))
	}
	// Same media types as the author catalogue, so a work found by search
	// carries the same editions (audiobook ISBNs included) as in the catalogue.
	if got := q.Get("filter"); got != "mediatype:(bøker OR lydopptak)" {
		t.Errorf("filter = %q", got)
	}
}

// The translation case this provider exists for: the author's catalogue
// carries the Norwegian original title, with the English and French
// translations folded into the same work instead of listed beside it.
func TestGetAuthorWorks_TranslationJoinsOriginal(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if isAuthority(r) {
			return "authority.json", 200
		}
		return "author_works.json", 200
	}}
	books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Error("a single-page catalogue must be complete")
	}
	// Homonym's book (10000002) and the book she only translated are excluded.
	if len(books) != 3 {
		titles := make([]string, len(books))
		for i, b := range books {
			titles[i] = b.Title
		}
		t.Fatalf("got %d works %v, want 3", len(books), titles)
	}
	w := books[0]
	if w.Title != "Fjellvinden" || w.ForeignID != "nb:a0000000000000000000000000000001" || w.Language != "nob" {
		t.Errorf("work = %q %q %q, want the Norwegian print edition as representative", w.Title, w.ForeignID, w.Language)
	}
	if len(w.Editions) != 4 {
		t.Errorf("editions = %d, want 4 (print, audio, English, French)", len(w.Editions))
	}
	if w.ReleaseDate == nil || w.ReleaseDate.Year() != 2019 {
		t.Errorf("release = %v, want the earliest edition's year", w.ReleaseDate)
	}
	if w.Description == "" {
		t.Error("description missing")
	}
	if strings.Join(w.ProviderISBNs, ",") != "9788200000011,9788200000028,9781000000012,9782000000013" {
		t.Errorf("ISBNs = %v", w.ProviderISBNs)
	}
	// An original's noisy uniform title ("Noveller Utvalg") must not rename it.
	if books[1].Title != "Havets stemme" || books[1].ReleaseDate.Year() != 2015 {
		t.Errorf("second work = %q %v", books[1].Title, books[1].ReleaseDate)
	}

	q := f.reqs[1].URL.Query()
	if got := strings.Join(q["filter"], ","); got != `namecreators:"Nordmann, Kari",mediatype:(bøker OR lydopptak)` {
		t.Errorf("filters = %s", got)
	}
}

func TestGetAuthorWorks_PartialWhenCapped(t *testing.T) {
	b, err := os.ReadFile("testdata/author_works.json")
	if err != nil {
		t.Fatal(err)
	}
	many := strings.Replace(string(b), `"totalPages": 1`, `"totalPages": 99`, 1)
	calls := 0
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := many
		if isAuthority(r) {
			a, _ := os.ReadFile("testdata/authority.json")
			body = string(a)
		} else if strings.HasSuffix(r.URL.Path, "/items") {
			calls++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	_, complete, err := c.GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	if complete || calls != maxWorksPages {
		t.Errorf("complete=%v pages=%d; a capped catalogue must report partial", complete, calls)
	}
}

// An upstream failure must surface as an error, never as an empty catalogue:
// the aggregator treats an empty answer from the primary as fact (#2332).
func TestGetAuthorWorks_ErrorsAreNotEmpty(t *testing.T) {
	for name, route := range map[string]func(*http.Request) (string, int){
		"authority down": func(r *http.Request) (string, int) {
			if isAuthority(r) {
				return "", 503
			}
			return "author_works.json", 200
		},
		"search down": func(r *http.Request) (string, int) {
			if isAuthority(r) {
				return "authority.json", 200
			}
			return "", 500
		},
	} {
		f := &fakeNB{t: t, route: route}
		if books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001"); err == nil {
			t.Errorf("%s: got %d books and no error", name, len(books))
		}
	}
}

// routeWithMODS serves the search, authority and MODS endpoints from the
// fixtures, MODS by record ID.
func routeWithMODS(mods map[string]string) func(*http.Request) (string, int) {
	return func(r *http.Request) (string, int) {
		switch {
		case isAuthority(r):
			return "authority.json", 200
		case strings.HasSuffix(r.URL.Path, "/mods"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/catalog/v1/metadata/"), "/mods")
			if f, ok := mods[id]; ok {
				return f, 200
			}
			return "", 404
		case strings.HasSuffix(r.URL.Path, "/items/a0000000000000000000000000000001"):
			return "item_print.json", 200
		case strings.Contains(r.URL.Query().Get("q"), "Fjellserien"):
			return "series_search.json", 200
		}
		return "author_works.json", 200
	}
}

func modsRequests(f *fakeNB) []string {
	var ids []string
	for _, r := range f.reqs {
		if strings.HasSuffix(r.URL.Path, "/mods") {
			ids = append(ids, r.URL.Path)
		}
	}
	return ids
}

// The series number is only in the per-record MODS. Only the author's own
// series counts: the one linked to their authority ID, not a publisher's
// imprint series, which NB also records as a series.
func TestGetAuthorWorks_SeriesFromMODS(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
		"a0000000000000000000000000000005": "mods_publisher_series.xml",
	})}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil || len(books) != 4 {
		t.Fatalf("books=%d err=%v", len(books), err)
	}
	want := models.SeriesRef{ForeignID: "nb-series:10000001:fjellserien", Title: "Fjellserien", Position: "2", Primary: true}
	if len(books[0].SeriesRefs) != 1 || books[0].SeriesRefs[0] != want {
		t.Errorf("series = %+v, want %+v", books[0].SeriesRefs, want)
	}
	if len(books[1].SeriesRefs) != 0 {
		t.Errorf("publisher imprint taken as a series: %+v", books[1].SeriesRefs)
	}
	// Only records whose search hit lists a series are fetched: two from the
	// catalogue and the one the series recall adds.
	if got := modsRequests(f); len(got) != 3 {
		t.Errorf("MODS requests = %v, want 3", got)
	}
	if ua := f.reqs[len(f.reqs)-1].Header.Get("User-Agent"); !strings.HasPrefix(ua, "bindery/") {
		t.Errorf("MODS User-Agent = %q", ua)
	}
}

// NB sometimes catalogues a volume under its series' name, with the volume's
// own title and number only in the part fields: "<series> : <title>", the
// number on the uniform title. The book must carry the volume's title, and
// the number counts once the author's catalogue links that series elsewhere.
func TestGetAuthorWorks_VolumeTitledBySeries(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
	})}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	var vol *models.Book
	for i := range books {
		if books[i].ForeignID == "nb:a0000000000000000000000000000008" {
			vol = &books[i]
		}
		if books[i].Title == "Fjellserien" {
			t.Errorf("a volume took the series name as its title: %+v", books[i])
		}
	}
	if vol == nil {
		t.Fatalf("volume missing from %d works", len(books))
	}
	if vol.Title != "Siste vinter" || vol.Editions[0].Title != "Siste vinter" {
		t.Errorf("title = %q, edition title = %q, want the part name", vol.Title, vol.Editions[0].Title)
	}
	want := models.SeriesRef{ForeignID: "nb-series:10000001:fjellserien", Title: "Fjellserien", Position: "3", Primary: true}
	if len(vol.SeriesRefs) != 1 || vol.SeriesRefs[0] != want {
		t.Errorf("series = %+v, want %+v", vol.SeriesRefs, want)
	}
}

func TestPartPosition(t *testing.T) {
	for in, want := range map[string]string{"6": "6", "[5]": "5", "3.": "3", " [12]. ": "12", "": ""} {
		if got := partPosition(in); got != want {
			t.Errorf("partPosition(%q) = %q, want %q", in, got, want)
		}
	}
}

// A cataloguer sometimes records the author's series without the authority
// link. That entry is accepted only when the author's catalogue links the same
// series elsewhere; an unlinked series nobody links is still a publisher's.
func TestGetAuthorWorks_UnlinkedEntryOfKnownSeries(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
		"a0000000000000000000000000000005": "mods_unlinked_series.xml",
	})}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil || len(books) != 4 {
		t.Fatalf("books=%d err=%v", len(books), err)
	}
	want := models.SeriesRef{ForeignID: "nb-series:10000001:fjellserien", Title: "Fjellserien", Position: "3", Primary: true}
	if len(books[1].SeriesRefs) != 1 || books[1].SeriesRefs[0] != want {
		t.Errorf("series = %+v, want %+v", books[1].SeriesRefs, want)
	}
}

// NB leaves some records out of every name index, so the author query never
// returns them, though they credit the author. A search on each of the
// author's series names recovers them, keeping only records that credit the
// author's authority ID.
func TestGetAuthorWorks_RecallsVolumesMissingFromNameIndex(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
		"a0000000000000000000000000000009": "mods_series_4.xml",
	})}
	books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	var recalled *models.Book
	for i := range books {
		switch books[i].ForeignID {
		case "nb:a0000000000000000000000000000009":
			recalled = &books[i]
		case "nb:a0000000000000000000000000000010":
			t.Errorf("another author's record was kept: %+v", books[i])
		}
	}
	if recalled == nil {
		t.Fatalf("volume missing from the name index was not recovered (%d works)", len(books))
	}
	want := models.SeriesRef{ForeignID: "nb-series:10000001:fjellserien", Title: "Fjellserien", Position: "4", Primary: true}
	if len(recalled.SeriesRefs) != 1 || recalled.SeriesRefs[0] != want {
		t.Errorf("series = %+v, want %+v", recalled.SeriesRefs, want)
	}
	if len(books) != 4 {
		t.Errorf("works = %d, want 4 (the catalogue's 3 plus the recalled one, no duplicates)", len(books))
	}

	var recall *http.Request
	mods := map[string]int{}
	for _, r := range f.reqs {
		if strings.Contains(r.URL.Query().Get("q"), "Fjellserien") {
			recall = r
		}
		if strings.HasSuffix(r.URL.Path, "/mods") {
			mods[r.URL.Path]++
		}
	}
	if recall == nil {
		t.Fatal("no series-name search was made")
	}
	q := recall.URL.Query()
	if q.Get("q") != `"Fjellserien"` || q.Get("searchType") != "FIELD_RESTRICTED_SEARCH" || q.Get("filter") != "mediatype:(bøker OR lydopptak)" {
		t.Errorf("recall query q=%q searchType=%q filter=%v", q.Get("q"), q.Get("searchType"), q["filter"])
	}
	// Regrouping after the recall must not fetch a record's MODS twice.
	for path, n := range mods {
		if n > 1 {
			t.Errorf("MODS %s fetched %d times", path, n)
		}
	}
}

// The authority record exists but the name search finds nothing: that is a
// gap in NB's name index, not proof the author has no books, so it must not
// pass as a complete, empty catalogue that reconciliation could act on.
func TestGetAuthorWorks_EmptySearchIsPartial(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if isAuthority(r) {
			return "authority.json", 200
		}
		return "empty_search.json", 200
	}}
	books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil || complete || len(books) != 0 {
		t.Errorf("books=%d complete=%v err=%v, want none, partial, no error", len(books), complete, err)
	}
}

// An authority record that is gone (404, or deleted when Sikt merges
// duplicate records) says nothing about the author's books: an empty,
// complete catalogue would have reconciliation offer to remove them all.
func TestGetAuthorWorks_MissingAuthorityIsPartial(t *testing.T) {
	for name, authority := range map[string]func() (string, int){
		"404":     func() (string, int) { return "", 404 },
		"deleted": func() (string, int) { return "authority_deleted.json", 200 },
	} {
		f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
			if isAuthority(r) {
				return authority()
			}
			return "author_works.json", 200
		}}
		books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
		if err != nil || complete || len(books) != 0 {
			t.Errorf("%s: books=%d complete=%v err=%v, want none, partial, no error", name, len(books), complete, err)
		}
	}
}

// A failed recall search must not pass as a complete catalogue: a recovered
// volume missing from it would read as removed upstream.
func TestGetAuthorWorks_RecallFailureIsPartial(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.Contains(r.URL.Query().Get("q"), "Fjellserien") {
			return "", 503
		}
		return routeWithMODS(map[string]string{"a0000000000000000000000000000001": "mods_author_series.xml"})(r)
	}}
	books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil || complete || len(books) != 3 {
		t.Errorf("books=%d complete=%v err=%v, want the catalogue, partial, no error", len(books), complete, err)
	}
}

// Series is enrichment: a MODS failure leaves the work without a series but
// must not fail or empty the catalogue.
func TestGetAuthorWorks_SeriesFailureIsNotFatal(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.HasSuffix(r.URL.Path, "/mods") {
			return "", 503
		}
		return routeWithMODS(nil)(r)
	}}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil || len(books) != 3 || len(books[0].SeriesRefs) != 0 {
		t.Errorf("books=%d err=%v series=%v", len(books), err, books[0].SeriesRefs)
	}
}

// A rebind replaces a book's series with what GetBook returns, so GetBook
// must carry the series too.
func TestGetBook_CarriesSeries(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
	})}
	b, err := f.client().GetBook(context.Background(), "nb:a0000000000000000000000000000001")
	if err != nil || b == nil {
		t.Fatalf("book=%v err=%v", b, err)
	}
	if len(b.SeriesRefs) != 1 || b.SeriesRefs[0].Position != "2" {
		t.Errorf("series = %+v", b.SeriesRefs)
	}
}

func TestGetAuthor(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "authority.json", 200 }}
	a, err := f.client().GetAuthor(context.Background(), "nb:author:10000001")
	if err != nil || a == nil {
		t.Fatalf("author=%v err=%v", a, err)
	}
	if a.Name != "Kari Nordmann" || a.ForeignID != "nb:author:10000001" {
		t.Errorf("author = %+v", a)
	}
	// The authority record's name variants, in display form. Bindery decides
	// which become aliases (textutil.LatinAliasBinds).
	if got := strings.Join(a.AlternateNames, "|"); got != "Kari Nordman|Кари Нордманн" {
		t.Errorf("alternate names = %q", got)
	}
	if got := f.reqs[0].URL.String(); got != authorityBase+"10000001?format=json" {
		t.Errorf("url = %s", got)
	}

	missing := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 404 }}
	if a, err := missing.client().GetAuthor(context.Background(), "nb:author:99999999"); a != nil || err != nil {
		t.Errorf("unknown authority: author=%v err=%v, want nil, nil", a, err)
	}
	if _, err := missing.client().GetAuthor(context.Background(), "nb:author:../x"); err == nil {
		t.Error("malformed id must be rejected before any request")
	}
}

// GetBook must return the whole work, not one record: the aggregator refreshes
// an ISBN match through it, and a print record alone would drop the
// audiobook's ISBN from the book.
func TestGetBook_ReturnsWorkEditions(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.HasSuffix(r.URL.Path, "/items/a0000000000000000000000000000001") {
			return "item_print.json", 200
		}
		return "author_works.json", 200
	}}
	b, err := f.client().GetBook(context.Background(), "nb:a0000000000000000000000000000001")
	if err != nil || b == nil {
		t.Fatalf("book=%v err=%v", b, err)
	}
	if b.ForeignID != "nb:a0000000000000000000000000000001" || len(b.Editions) != 4 {
		t.Errorf("book %s has %d editions, want the work's 4", b.ForeignID, len(b.Editions))
	}
	q := f.reqs[1].URL.Query()
	// Not filtered on NB's name index, which misses some records; groupWorks
	// keeps only records crediting the author's authority ID.
	if got := strings.Join(q["filter"], ","); got != `mediatype:(bøker OR lydopptak)` || q.Get("q") != "Fjellvinden" {
		t.Errorf("sibling search q=%q filters=%s", q.Get("q"), got)
	}

	// The sibling search is best-effort: the record itself is still returned.
	down := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.Contains(r.URL.Path, "/items/") {
			return "item_print.json", 200
		}
		return "", 503
	}}
	b, err = down.client().GetBook(context.Background(), "nb:a0000000000000000000000000000001")
	if err != nil || b == nil || len(b.Editions) != 1 {
		t.Errorf("sibling search down: book=%v err=%v", b, err)
	}
}

func TestGetBook_RejectsMalformedID(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 200 }}
	for _, id := range []string{"OL1W", "nb:author:10000001", "nb:../../x"} {
		if _, err := f.client().GetBook(context.Background(), id); err == nil {
			t.Errorf("GetBook(%q) succeeded", id)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("made %d requests for malformed ids", len(f.reqs))
	}
}

// A refusal or outage is marked with the shared provider errors, so
// scheduled discovery backs off instead of walking on through its queue.
func TestProviderErrors(t *testing.T) {
	for status, want := range map[int]error{
		429: providererr.ErrRateLimited,
		500: providererr.ErrUnavailable,
		502: providererr.ErrUnavailable,
		503: providererr.ErrUnavailable,
	} {
		f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", status }}
		_, err := f.client().GetBookByISBN(context.Background(), "9788200000028")
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: err = %v, want %v", status, err, want)
		}
	}
	// A bad request is about this request, not the provider being down.
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 400 }}
	_, err := f.client().GetBookByISBN(context.Background(), "9788200000028")
	if err == nil || errors.Is(err, providererr.ErrRateLimited) || errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("HTTP 400: err = %v, want a plain error", err)
	}
}

// Narrator, audiobook duration and genres come from the same catalogue
// records the work is built from: no extra requests.
func TestGetAuthorWorks_NarratorDurationGenres(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(nil)}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	var work, part *models.Book
	for i := range books {
		switch books[i].ForeignID {
		case "nb:a0000000000000000000000000000001":
			work = &books[i]
		case "nb:a0000000000000000000000000000008":
			part = &books[i]
		}
	}
	if work == nil || part == nil {
		t.Fatalf("works missing from %d", len(books))
	}
	if work.Narrator != "Ola Leser" {
		t.Errorf("narrator = %q, want the audiobook edition's narrator credit", work.Narrator)
	}
	const want = 11*3600 + 16*60
	if work.DurationSeconds != want {
		t.Errorf("duration = %d, want %d from the audiobook edition", work.DurationSeconds, want)
	}
	for _, ed := range work.Editions {
		if ed.Format == models.MediaTypeAudiobook && ed.DurationSeconds != want {
			t.Errorf("audiobook edition duration = %d, want %d", ed.DurationSeconds, want)
		}
		if ed.Format != models.MediaTypeAudiobook && ed.DurationSeconds != 0 {
			t.Errorf("print edition has a duration: %+v", ed)
		}
	}
	if got := strings.Join(work.Genres, ","); got != "Romaner,Krim,Politi og detektiver" {
		t.Errorf("genres = %q, want the subject genres without the format term or Nynorsk twins", got)
	}
	if part.DurationSeconds != 17*3600+42*60 {
		t.Errorf("hh:mm:ss duration = %d", part.DurationSeconds)
	}
}

func TestExtentDuration(t *testing.T) {
	for in, want := range map[string]int{
		"1 lydfil (11 t, 16 min)": 11*3600 + 16*60,
		"21:34:00":                21*3600 + 34*60,
		"3 plater (CD)(3 t, 7 min) digital 12 cm, i eske": 3*3600 + 7*60,
		"1 lydfil (45 min)": 45 * 60,
		"312 s.":            0,
		"":                  0,
	} {
		if got := extentDuration(in); got != want {
			t.Errorf("extentDuration(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNotConfigured(t *testing.T) {
	ctx := context.Background()
	var c Client
	checks := map[string]error{}
	_, checks["SearchAuthors"] = c.SearchAuthors(ctx, "x")
	_, checks["SearchBooks"] = c.SearchBooks(ctx, "x")
	_, checks["GetAuthor"] = c.GetAuthor(ctx, "nb:author:1")
	_, checks["GetAuthorWorks"] = c.GetAuthorWorks(ctx, "nb:author:1")
	_, checks["GetBook"] = c.GetBook(ctx, "nb:a0000000000000000000000000000001")
	_, checks["GetEditions"] = c.GetEditions(ctx, "nb:a0000000000000000000000000000001")
	_, checks["GetBookByISBN"] = c.GetBookByISBN(ctx, "9788200000028")
	for name, err := range checks {
		if !errors.Is(err, metadata.ErrProviderNotConfigured) {
			t.Errorf("%s: err = %v, want ErrProviderNotConfigured", name, err)
		}
	}
}

func TestStripLanguageQualifier(t *testing.T) {
	for in, want := range map[string]string{
		"Fjellvinden Fransk":  "Fjellvinden",
		"The quiet one Norsk": "The quiet one",
		"Fjellvinden":         "Fjellvinden",
		"Havet og Os":         "Havet og Os",
		"Mot øst":             "Mot øst",
		// A name ending like a language word is not a language qualifier.
		"Inspektør Brask": "Inspektør Brask",
		"Kiosk":           "Kiosk",
	} {
		if got := stripLanguageQualifier(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A catalogue slip that splits a title with a space ("Fjel lbyen") must not
// make a second book, nor name the book: the title most editions carry wins.
func TestGroupWorks_FoldsSpacingSlip(t *testing.T) {
	kari := person{Name: "Nordmann, Kari", Identifier: "bibsys.no:authority:10000001", Roles: []struct {
		Name string `json:"name"`
	}{{Name: "aut"}}}
	rec := func(id, title string, tis []titleInfo, media string) item {
		var m itemMetadata
		m.Title, m.TitleInfos, m.People, m.MediaTypes = title, tis, []person{kari}, []string{media}
		m.Identifiers.SesamID = id
		m.Languages = []struct {
			Code string `json:"code"`
		}{{Code: "nob"}}
		return item{ID: id, Metadata: m}
	}
	books := groupWorks([]item{
		rec("b0000000000000000000000000000001", "Fjel lbyen  : roman", []titleInfo{{Title: "lbyen "}}, "bøker"),
		rec("b0000000000000000000000000000002", "Fjellbyen : roman", []titleInfo{{Title: "Fjellbyen"}}, "bøker"),
		rec("b0000000000000000000000000000003", "Fjellserien. [5] : Fjellbyen", []titleInfo{{Title: "jellserien", PartName: "Fjellbyen", PartNumber: "[5]"}}, "lydopptak"),
		rec("b0000000000000000000000000000004", "Fjell byen og havet", []titleInfo{{Title: "Fjell byen og havet"}}, "bøker"),
	}, "10000001")
	if len(books) != 2 {
		t.Fatalf("works = %d, want 2", len(books))
	}
	if books[0].Title != "Fjellbyen" || len(books[0].Editions) != 3 {
		t.Errorf("work = %q with %d editions, want Fjellbyen with 3", books[0].Title, len(books[0].Editions))
	}
	if books[1].Title != "Fjell byen og havet" {
		t.Errorf("a different title was folded in: %q", books[1].Title)
	}
}

// NB can name one series two ways, linking both to the author. Names a
// work's records give the same number are one series across the catalogue,
// under the name most books carry. An unlinked imprint sharing the number is
// not part of it.
func TestGetAuthorWorks_MergesSeriesNamedTwoWays(t *testing.T) {
	f := &fakeNB{t: t, route: routeWithMODS(map[string]string{
		"a0000000000000000000000000000001": "mods_author_series.xml",
		"a0000000000000000000000000000005": "mods_series_alias.xml",
		"a0000000000000000000000000000009": "mods_series_4.xml",
	})}
	books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]string{}
	for _, b := range books {
		for _, ref := range b.SeriesRefs {
			if ref.ForeignID != "nb-series:10000001:fjellserien" || ref.Title != "Fjellserien" {
				t.Errorf("%s: series = %+v, want the one Fjellserien", b.Title, ref)
			}
			positions[b.ForeignID] = ref.Position
		}
	}
	if positions["nb:a0000000000000000000000000000005"] != "3" || len(positions) < 3 {
		t.Errorf("positions = %v, want the aliased volume at 3 among the series", positions)
	}
}
