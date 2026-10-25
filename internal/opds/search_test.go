package opds

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func relsOfType(links []Link, rel, typ string) []Link {
	var out []Link
	for _, l := range links {
		if l.Rel == rel && l.Type == typ {
			out = append(out, l)
		}
	}
	return out
}

func TestBuildRoot_HasSearchLinks(t *testing.T) {
	f := newBuilder().BuildRoot("http://host:8787/")

	os := relsOfType(f.Links, RelSearch, "application/opensearchdescription+xml")
	if len(os) != 1 || os[0].Href != "http://host:8787/opds/opensearch.xml" {
		t.Fatalf("opensearch link = %+v", os)
	}
	tpl := relsOfType(f.Links, RelSearch, "application/atom+xml")
	if len(tpl) != 1 || tpl[0].Href != "http://host:8787/opds/search?q={searchTerms}" {
		t.Fatalf("template link = %+v", tpl)
	}
}

func TestBuildSearch_MatchesAndEntryShape(t *testing.T) {
	f, err := newBuilder().BuildSearch(context.Background(), "http://host", "lightning", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 || f.Entries[0].Title != "Too Like the Lightning" {
		t.Fatalf("entries = %+v", f.Entries)
	}
	e := f.Entries[0]
	if len(e.Authors) != 1 || e.Authors[0].Name != "Ada Palmer" {
		t.Errorf("authors = %+v", e.Authors)
	}
	if len(relsOfType(e.Links, RelAcquisition, "application/epub+zip")) != 1 {
		t.Errorf("no acquisition link: %+v", e.Links)
	}
	if f.TotalResults != 1 {
		t.Errorf("total = %d", f.TotalResults)
	}
	mustHaveRel(t, f.Links, RelSelf, "http://host/opds/search?q=lightning")
}

func TestBuildSearch_EmptyQueryIsEmptyFeed(t *testing.T) {
	for _, q := range []string{"", "   "} {
		f, err := newBuilder().BuildSearch(context.Background(), "http://host", q, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.Entries) != 0 || f.TotalResults != 0 {
			t.Errorf("q=%q: entries=%d total=%d", q, len(f.Entries), f.TotalResults)
		}
	}
}

func TestBuildSearch_QueryTooLong(t *testing.T) {
	_, err := newBuilder().BuildSearch(context.Background(), "http://host", strings.Repeat("é", MaxSearchQueryLen+1), 1, 0)
	if !errors.Is(err, ErrQueryTooLong) {
		t.Fatalf("err = %v", err)
	}
	// Exactly at the limit is fine, and the limit counts characters, not bytes.
	if _, err := newBuilder().BuildSearch(context.Background(), "http://host", strings.Repeat("é", MaxSearchQueryLen), 1, 0); err != nil {
		t.Fatalf("at limit: %v", err)
	}
}

func TestBuildSearch_Pagination(t *testing.T) {
	var all []models.Book
	for i := 1; i <= 5; i++ {
		all = append(all, models.Book{ID: int64(i), Title: fmt.Sprintf("Saga %d", i), FilePath: "/l/x.epub", Status: models.BookStatusImported})
	}
	b := NewBuilder(Config{PageSize: 2}, &fakeBooks{all: all}, &fakeAuthors{}, &fakeSeries{})

	f, _ := b.BuildSearch(context.Background(), "http://h", "saga & más", 1, 0)
	if len(f.Entries) != 0 {
		t.Fatalf("unexpected hits for odd query: %d", len(f.Entries))
	}

	f, _ = b.BuildSearch(context.Background(), "http://h", "saga", 2, 0)
	if len(f.Entries) != 2 || f.TotalResults != 5 || f.StartIndex != 3 {
		t.Fatalf("page 2: entries=%d total=%d start=%d", len(f.Entries), f.TotalResults, f.StartIndex)
	}
	mustHaveRel(t, f.Links, RelNext, "http://h/opds/search?q=saga&page=3")
	mustHaveRel(t, f.Links, RelPrevious, "http://h/opds/search?q=saga")

	f, _ = b.BuildSearch(context.Background(), "http://h", "saga", 3, 0)
	if len(f.Entries) != 1 {
		t.Fatalf("page 3: %d", len(f.Entries))
	}
	if len(relsOfType(f.Links, RelNext, TypeAcquisition)) != 0 {
		t.Error("last page has a next link")
	}

	// A hostile page number is clamped, not an overflow.
	if _, err := b.BuildSearch(context.Background(), "http://h", "saga", 1<<62, 0); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSearch_EscapesQueryInLinksAndXML(t *testing.T) {
	f, err := newBuilder().BuildSearch(context.Background(), "http://host", `a&b <c> "d"`, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := xml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(out, new(Feed)); err != nil {
		t.Fatalf("not well formed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "<c>") {
		t.Errorf("raw markup leaked: %s", out)
	}
	mustHaveRel(t, f.Links, RelSelf, "http://host/opds/search?q=a%26b+%3Cc%3E+%22d%22")
}

func TestBuildOpenSearch(t *testing.T) {
	d := NewBuilder(Config{Title: "A very long catalogue title <&>"}, &fakeBooks{}, &fakeAuthors{}, &fakeSeries{}).BuildOpenSearch("http://host/")
	out, err := xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(out, new(OpenSearchDescription)); err != nil {
		t.Fatalf("not well formed: %v", err)
	}
	if len([]rune(d.ShortName)) > 16 {
		t.Errorf("ShortName too long: %q", d.ShortName)
	}
	if len(d.URLs) != 1 || d.URLs[0].Type != TypeAtom ||
		d.URLs[0].Template != "http://host/opds/search?q={searchTerms}&page={startPage?}" {
		t.Errorf("urls = %+v", d.URLs)
	}
}
