package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPrimaryTitleForQuery_AsteriskBecomesSpace covers #1637: the asterisk in
// a censored title is sent as a word break, not left for Prowlarr to delete.
func TestPrimaryTitleForQuery_AsteriskBecomesSpace(t *testing.T) {
	cases := map[string]string{
		"The Subtle Art of Not Giving a F*ck": "The Subtle Art of Not Giving a F ck",
		"F**k":                                "F k",
		"*Star":                               "Star",
		"Plain Title":                         "Plain Title",
		"Sh*t: A Subtitle":                    "Sh t",
	}
	for in, want := range cases {
		if got := primaryTitleForQuery(in); got != want {
			t.Errorf("primaryTitleForQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBookSearch_AsteriskTitleSendsSpacedQuery checks every request on the
// wire: no tier carries the asterisk, and the cascade still reaches the
// title-only tier (#1637).
func TestBookSearch_AsteriskTitleSendsSpacedQuery(t *testing.T) {
	var titles, qs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		titles = append(titles, q.Get("title"))
		qs = append(qs, q.Get("q"))
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	if _, err := c.BookSearch(context.Background(), "The Subtle Art of Not Giving a F*ck", "Mark Manson", []int{7020}); err != nil {
		t.Fatalf("BookSearch: %v", err)
	}
	if len(titles) == 0 {
		t.Fatal("no requests reached the indexer")
	}
	for i := range titles {
		if strings.Contains(titles[i], "*") || strings.Contains(qs[i], "*") {
			t.Errorf("request %d carried an asterisk: title=%q q=%q", i, titles[i], qs[i])
		}
	}
	if titles[0] != "The Subtle Art of Not Giving a F ck" {
		t.Errorf("tier 1 title = %q", titles[0])
	}
	if last := qs[len(qs)-1]; last != "The Subtle Art of Not Giving a F ck" {
		t.Errorf("last freeform query = %q", last)
	}
}

// The query-side gate must accept a release that keeps the asterisk, since the
// outgoing title no longer carries it.
func TestTitleHasRelevantResult_AsteriskRelease(t *testing.T) {
	results := []SearchResult{{Title: "Mark Manson - The Subtle Art of Not Giving a F*ck (epub)"}}
	if !titleHasRelevantResult(primaryTitleForQuery("The Subtle Art of Not Giving a F*ck"), results) {
		t.Fatal("release naming the censored title was judged irrelevant")
	}
}
