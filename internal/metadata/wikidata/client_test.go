package wikidata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// fakeWikimedia stands in for the three Wikimedia APIs. Each handler field can
// be swapped per test; calls counts every request so cache tests can assert
// that nothing was sent.
type fakeWikimedia struct {
	srv      *httptest.Server
	calls    atomic.Int32
	search   func(w http.ResponseWriter, srsearch string)
	entity   func(w http.ResponseWriter, qid string)
	commons  func(w http.ResponseWriter, title string)
	wikipage func(w http.ResponseWriter, lang, title string)
}

func newFake(t *testing.T) *fakeWikimedia {
	f := &fakeWikimedia{}
	f.search = func(w http.ResponseWriter, _ string) {
		fmt.Fprint(w, `{"query":{"search":[{"title":"Q100"}]}}`)
	}
	f.entity = func(w http.ResponseWriter, qid string) {
		fmt.Fprintf(w, `{"entities":{%q:{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"Kari Nordmann 2019.jpg"}}}]},
			"sitelinks":{"nowiki":{"title":"Kari Nordmann"},"enwiki":{"title":"Kari Nordmann"}}}}}`, qid)
	}
	f.commons = func(w http.ResponseWriter, _ string) {
		fmt.Fprint(w, `{"query":{"pages":{"1":{"imageinfo":[{
			"thumburl":"https://upload.example/thumb/Kari_Nordmann_2019.jpg/400px.jpg?utm_source=commons&utm_campaign=imageinfo",
			"descriptionurl":"https://commons.example/wiki/File:Kari_Nordmann_2019.jpg",
			"extmetadata":{"Artist":{"value":"<a href=\"//commons.example/wiki/User:Ola\">Ola &amp; Co</a>"},
				"LicenseShortName":{"value":"CC BY-SA 4.0"},
				"CommonsMetadataExtension":{"value":1.2,"source":"extension"},
				"LicenseUrl":{"value":"https://creativecommons.org/licenses/by-sa/4.0"}}}]}}}}`)
	}
	f.wikipage = func(w http.ResponseWriter, lang, _ string) {
		fmt.Fprintf(w, `{"type":"standard","extract":"Kari Nordmann is a writer (%s).","content_urls":{"desktop":{"page":"https://%s.wikipedia.example/wiki/Kari_Nordmann"}}}`, lang, lang)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		// Wikimedia's policy wants a client name and a way to reach its operator.
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "bindery/") || !strings.Contains(ua, "github.com/vavallee/bindery") {
			t.Errorf("request sent without Bindery's identifying User-Agent: %q", ua)
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/wikidata" && q.Get("list") == "search":
			f.search(w, q.Get("srsearch"))
		case r.URL.Path == "/wikidata" && q.Get("action") == "wbgetentities":
			f.entity(w, q.Get("ids"))
		case r.URL.Path == "/commons":
			f.commons(w, q.Get("titles"))
		case strings.HasPrefix(r.URL.Path, "/wiki/"):
			rest := strings.TrimPrefix(r.URL.Path, "/wiki/")
			lang, title, _ := strings.Cut(rest, "/page/summary/")
			f.wikipage(w, lang, title)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWikimedia) client() *Client {
	c := New()
	c.wikidataAPI = f.srv.URL + "/wikidata"
	c.commonsAPI = f.srv.URL + "/commons"
	c.wikipediaREST = f.srv.URL + "/wiki/%s"
	return c
}

func TestAuthorityID(t *testing.T) {
	for _, tc := range []struct {
		id, property, value string
		ok                  bool
	}{
		{"nb:author:90087965", "P1015", "90087965", true},
		{"nb:author:abc", "", "", false},
		{"dnb:gnd:118540238", "P227", "118540238", true},
		{"dnb:author:kari-nordmann", "", "", false},
		{"bnf:author:11918228", "P268", "11918228x", true},
		{"bnf:author:123", "", "", false},
		{"OL118077A", "P648", "OL118077A", true},
		{"hc:kari-nordmann", "", "", false},
		{"gb:Kari Nordmann", "", "", false},
		{"calibre:author:7", "", "", false},
	} {
		p, v, ok := authorityID(tc.id)
		if ok != tc.ok || (ok && (p != tc.property || v != tc.value)) {
			t.Errorf("authorityID(%q) = %q, %q, %v; want %q, %q, %v", tc.id, p, v, ok, tc.property, tc.value, tc.ok)
		}
	}
}

// The check character must match what Wikidata stores for P268, or the
// haswbstatement lookup finds nothing. Both values are from live items.
func TestBnFCheckChar(t *testing.T) {
	for name, want := range map[string]string{"cb11918228": "x", "cb11889282": "7"} {
		if got := bnfCheckChar(name); got != want {
			t.Errorf("bnfCheckChar(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEnrichAuthorFindsPhotoAndBioByAuthorityID(t *testing.T) {
	f := newFake(t)
	var searched string
	f.search = func(w http.ResponseWriter, srsearch string) {
		searched = srsearch
		fmt.Fprint(w, `{"query":{"search":[{"title":"Q100"}]}}`)
	}
	c := f.client()

	p, err := c.EnrichAuthor(context.Background(), &models.Author{ForeignID: "nb:author:90087965", Name: "Kari Nordmann"})
	if err != nil {
		t.Fatal(err)
	}
	if searched != "haswbstatement:P1015=90087965" {
		t.Errorf("searched %q, want the NORAF statement", searched)
	}
	if p == nil {
		t.Fatal("no profile")
	}
	if p.ImageURL != "https://upload.example/thumb/Kari_Nordmann_2019.jpg/400px.jpg" {
		t.Errorf("image %q", p.ImageURL)
	}
	want := models.ProfileSource{
		Provider: "wikimedia-commons", URL: "https://commons.example/wiki/File:Kari_Nordmann_2019.jpg",
		Credit: "Ola & Co", License: "CC BY-SA 4.0", LicenseURL: "https://creativecommons.org/licenses/by-sa/4.0",
	}
	if p.ImageSource == nil || *p.ImageSource != want {
		t.Errorf("image source %+v, want %+v", p.ImageSource, want)
	}
	// An nb author's bio comes from the Norwegian edition first.
	if p.Description != "Kari Nordmann is a writer (no)." {
		t.Errorf("description %q", p.Description)
	}
	if p.DescriptionSource == nil || p.DescriptionSource.Provider != "wikipedia" ||
		p.DescriptionSource.URL != "https://no.wikipedia.example/wiki/Kari_Nordmann" || p.DescriptionSource.License != wikipediaLicense {
		t.Errorf("description source %+v", p.DescriptionSource)
	}
}

func TestEnrichAuthorSkipsIDsWikidataDoesNotRecord(t *testing.T) {
	f := newFake(t)
	p, err := f.client().EnrichAuthor(context.Background(), &models.Author{ForeignID: "hc:kari-nordmann"})
	if p != nil || err != nil || f.calls.Load() != 0 {
		t.Errorf("got %v, %v after %d requests; want nothing asked", p, err, f.calls.Load())
	}
}

func TestEnrichAuthorCachesHitsAndMisses(t *testing.T) {
	f := newFake(t)
	c := f.client()
	ctx := context.Background()
	hit := &models.Author{ForeignID: "nb:author:1"}
	if _, err := c.EnrichAuthor(ctx, hit); err != nil {
		t.Fatal(err)
	}
	after := f.calls.Load()
	if p, _ := c.EnrichAuthor(ctx, hit); p == nil || f.calls.Load() != after {
		t.Errorf("a cached hit was asked again (%d -> %d requests) or lost", after, f.calls.Load())
	}

	f.search = func(w http.ResponseWriter, _ string) { fmt.Fprint(w, `{"query":{"search":[]}}`) }
	miss := &models.Author{ForeignID: "nb:author:2"}
	if p, err := c.EnrichAuthor(ctx, miss); p != nil || err != nil {
		t.Fatalf("miss returned %v, %v", p, err)
	}
	after = f.calls.Load()
	if _, _ = c.EnrichAuthor(ctx, miss); f.calls.Load() != after {
		t.Error("a cached miss was asked again")
	}

	// After the TTL both are asked again.
	c.now = func() time.Time { return time.Now().Add(cacheTTL + time.Hour) }
	_, _ = c.EnrichAuthor(ctx, miss)
	if f.calls.Load() == after {
		t.Error("an expired miss was not asked again")
	}
}

// An answer built while Commons or Wikipedia failed is kept for partialTTL,
// not a month: one bad response must not cost the photo for 30 days.
func TestEnrichAuthorRetriesAPartialAnswerSooner(t *testing.T) {
	for name, breakIt := range map[string]func(f *fakeWikimedia){
		"Commons": func(f *fakeWikimedia) {
			f.commons = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusBadGateway) }
		},
		"Wikipedia": func(f *fakeWikimedia) {
			f.wikipage = func(w http.ResponseWriter, _, _ string) { w.WriteHeader(http.StatusInternalServerError) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			breakIt(f)
			c := f.client()
			ctx := context.Background()
			author := &models.Author{ForeignID: "nb:author:1"}
			if p, err := c.EnrichAuthor(ctx, author); err != nil || p == nil || !p.Partial {
				t.Fatalf("got %+v, %v; want an answer marked partial", p, err)
			}
			// Within partialTTL the answer is served from the cache.
			after := f.calls.Load()
			c.now = func() time.Time { return time.Now().Add(partialTTL - time.Minute) }
			if _, _ = c.EnrichAuthor(ctx, author); f.calls.Load() != after {
				t.Error("a partial answer was asked again before partialTTL")
			}
			// After it, with Wikimedia healthy again, the full answer comes back.
			fresh := newFake(t)
			c.wikidataAPI, c.commonsAPI, c.wikipediaREST = fresh.srv.URL+"/wikidata", fresh.srv.URL+"/commons", fresh.srv.URL+"/wiki/%s"
			c.now = func() time.Time { return time.Now().Add(partialTTL + time.Minute) }
			p, err := c.EnrichAuthor(ctx, author)
			if err != nil || p == nil || p.ImageURL == "" || !strings.Contains(p.Description, "(no)") {
				t.Errorf("after partialTTL: %+v, %v; want the photo and the Norwegian bio", p, err)
			}
		})
	}
}

func TestEnrichAuthorIgnoresAmbiguousIDs(t *testing.T) {
	f := newFake(t)
	f.search = func(w http.ResponseWriter, _ string) {
		fmt.Fprint(w, `{"query":{"search":[{"title":"Q1"},{"title":"Q2"}]}}`)
	}
	if p, err := f.client().EnrichAuthor(context.Background(), &models.Author{ForeignID: "nb:author:3"}); p != nil || err != nil {
		t.Errorf("an ID on two items gave %v, %v; want no answer", p, err)
	}
}

func TestEnrichAuthorHoldsAfterRateLimit(t *testing.T) {
	f := newFake(t)
	f.search = func(w http.ResponseWriter, _ string) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}
	c := f.client()
	ctx := context.Background()
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:4"}); !errors.Is(err, ErrHeld) {
		t.Fatalf("429 gave %v, want ErrHeld", err)
	}
	before := f.calls.Load()
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:5"}); !errors.Is(err, ErrHeld) || f.calls.Load() != before {
		t.Errorf("during the hold: %v after %d new requests; want ErrHeld and none", err, f.calls.Load()-before)
	}
	// Retry-After was 120s; past it the client asks again.
	c.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	f.search = func(w http.ResponseWriter, _ string) { fmt.Fprint(w, `{"query":{"search":[]}}`) }
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:5"}); err != nil || f.calls.Load() == before {
		t.Errorf("after the hold: %v, requests %d -> %d", err, before, f.calls.Load())
	}
}

func TestEnrichAuthorSkipsAPhotoWithNoStatedLicence(t *testing.T) {
	f := newFake(t)
	f.commons = func(w http.ResponseWriter, _ string) {
		fmt.Fprint(w, `{"query":{"pages":{"1":{"imageinfo":[{"thumburl":"https://upload.example/x.jpg","extmetadata":{}}]}}}}`)
	}
	p, err := f.client().EnrichAuthor(context.Background(), &models.Author{ForeignID: "OL1A"})
	if err != nil || p == nil {
		t.Fatalf("got %v, %v", p, err)
	}
	if p.ImageURL != "" || p.ImageSource != nil {
		t.Errorf("an unattributable photo was used: %q", p.ImageURL)
	}
	if !strings.HasPrefix(p.Description, "Kari Nordmann is a writer (en)") {
		t.Errorf("the bio should still come through, got %q", p.Description)
	}
}

func TestEnrichAuthorPassesOverDisambiguationPages(t *testing.T) {
	f := newFake(t)
	f.wikipage = func(w http.ResponseWriter, lang, _ string) {
		if lang == "no" {
			fmt.Fprint(w, `{"type":"disambiguation","extract":"Kari Nordmann may refer to"}`)
			return
		}
		fmt.Fprintf(w, `{"type":"standard","extract":"Kari Nordmann (%s).","content_urls":{"desktop":{"page":"https://%s.example/k"}}}`, lang, lang)
	}
	p, _ := f.client().EnrichAuthor(context.Background(), &models.Author{ForeignID: "nb:author:6"})
	if p == nil || p.Description != "Kari Nordmann (en)." {
		t.Errorf("got %+v; want the English summary after the Norwegian disambiguation page", p)
	}
}

func TestPlainText(t *testing.T) {
	if got := plainText(`<span><a href="x">Ola</a>  Nordmann &amp; <b>Co</b></span>`); got != "Ola Nordmann & Co" {
		t.Errorf("plainText = %q", got)
	}
}

func TestEnrichAuthorGuardsAndName(t *testing.T) {
	f := newFake(t)
	c := f.client()
	if c.Name() != "wikidata" {
		t.Errorf("Name() = %q", c.Name())
	}
	if p, err := c.EnrichAuthor(context.Background(), nil); p != nil || err != nil {
		t.Errorf("nil author gave %v, %v", p, err)
	}
	if _, _, ok := authorityID("nb:author:"); ok {
		t.Error("an empty NORAF number was accepted")
	}
	if f.calls.Load() != 0 {
		t.Errorf("%d requests for authors that cannot be looked up", f.calls.Load())
	}
}

func TestWikiOrderFollowsTheProvidersLanguage(t *testing.T) {
	for id, want := range map[string]string{
		"nb:author:1": "no,nn,en", "bnf:author:11918228": "fr,en", "dnb:gnd:118540238": "de,en", "OL1A": "en",
	} {
		if got := strings.Join(wikiOrder(id), ","); got != want {
			t.Errorf("wikiOrder(%q) = %s, want %s", id, got, want)
		}
	}
}

// Each Wikimedia step can fail on its own; none of them may turn into a
// wrong answer, and a rate limit at any step must hold the client.
func TestEnrichAuthorStepFailures(t *testing.T) {
	ctx := context.Background()
	author := func(n int) *models.Author { return &models.Author{ForeignID: fmt.Sprintf("nb:author:%d", n)} }

	t.Run("item lookup error", func(t *testing.T) {
		f := newFake(t)
		f.entity = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusInternalServerError) }
		if p, err := f.client().EnrichAuthor(ctx, author(1)); p != nil || err == nil {
			t.Errorf("got %v, %v; want an error", p, err)
		}
	})
	t.Run("item missing from the answer", func(t *testing.T) {
		f := newFake(t)
		f.entity = func(w http.ResponseWriter, _ string) { fmt.Fprint(w, `{"entities":{}}`) }
		if p, err := f.client().EnrichAuthor(ctx, author(2)); p != nil || err != nil {
			t.Errorf("got %v, %v; want no answer", p, err)
		}
	})
	t.Run("item with no photo and no article", func(t *testing.T) {
		f := newFake(t)
		f.entity = func(w http.ResponseWriter, qid string) {
			fmt.Fprintf(w, `{"entities":{%q:{"claims":{},"sitelinks":{}}}}`, qid)
		}
		if p, err := f.client().EnrichAuthor(ctx, author(3)); p != nil || err != nil {
			t.Errorf("got %v, %v; want no answer", p, err)
		}
	})
	t.Run("rate limit at Commons holds", func(t *testing.T) {
		f := newFake(t)
		f.commons = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusTooManyRequests) }
		if _, err := f.client().EnrichAuthor(ctx, author(4)); !errors.Is(err, ErrHeld) {
			t.Errorf("got %v, want ErrHeld", err)
		}
	})
	t.Run("rate limit at Wikipedia holds", func(t *testing.T) {
		f := newFake(t)
		f.wikipage = func(w http.ResponseWriter, _, _ string) { w.WriteHeader(http.StatusServiceUnavailable) }
		if _, err := f.client().EnrichAuthor(ctx, author(5)); !errors.Is(err, ErrHeld) {
			t.Errorf("got %v, want ErrHeld", err)
		}
	})
	t.Run("Commons error keeps the bio", func(t *testing.T) {
		f := newFake(t)
		f.commons = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusBadGateway) }
		p, err := f.client().EnrichAuthor(ctx, author(6))
		if err != nil || p == nil || p.ImageURL != "" || p.Description == "" {
			t.Errorf("got %+v, %v; want the bio without a photo", p, err)
		}
	})
	t.Run("Commons page without image info", func(t *testing.T) {
		f := newFake(t)
		f.commons = func(w http.ResponseWriter, _ string) { fmt.Fprint(w, `{"query":{"pages":{"-1":{"missing":""}}}}`) }
		p, _ := f.client().EnrichAuthor(ctx, author(7))
		if p == nil || p.ImageURL != "" {
			t.Errorf("got %+v; want no photo", p)
		}
	})
	t.Run("Wikipedia error falls through to the next language", func(t *testing.T) {
		f := newFake(t)
		f.wikipage = func(w http.ResponseWriter, lang, _ string) {
			if lang == "no" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"type":"standard","extract":"Kari Nordmann (%s).","content_urls":{"desktop":{"page":"https://%s.example/k"}}}`, lang, lang)
		}
		if p, _ := f.client().EnrichAuthor(ctx, author(8)); p == nil || p.Description != "Kari Nordmann (en)." {
			t.Errorf("got %+v; want the English summary", p)
		}
	})
	t.Run("not found is no answer", func(t *testing.T) {
		f := newFake(t)
		f.search = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusNotFound) }
		if p, err := f.client().EnrichAuthor(ctx, author(9)); p != nil || err != nil {
			t.Errorf("got %v, %v; want no answer", p, err)
		}
	})
}

func TestEnrichAuthorTransportErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c := f.client()
	c.wikidataAPI = "http://[::1]:namedport"
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:10"}); err == nil {
		t.Error("an unparseable endpoint gave no error")
	}
	f.srv.Close()
	c = f.client()
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:11"}); err == nil {
		t.Error("an unreachable server gave no error")
	}
}

func TestStripTrackingLeavesPlainURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://upload.example/a.jpg":                     "https://upload.example/a.jpg",
		"https://upload.example/a.jpg?utm_source=x&size=2": "https://upload.example/a.jpg?size=2",
	} {
		if got := stripTracking(in); got != want {
			t.Errorf("stripTracking(%q) = %q, want %q", in, got, want)
		}
	}
}

// During an outage each author would wait out the request timeout. After
// failureLimit requests in a row fail outright the client holds for
// failureHold; any answer resets the count.
func TestEnrichAuthorBacksOffAfterRepeatedFailures(t *testing.T) {
	f := newFake(t)
	f.search = func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusBadGateway) }
	c := f.client()
	ctx := context.Background()
	for i := range failureLimit {
		if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: fmt.Sprintf("nb:author:%d", i+1)}); err == nil || errors.Is(err, ErrHeld) {
			t.Fatalf("failure %d: %v, want the request's own error", i+1, err)
		}
	}
	sent := f.calls.Load()
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:99"}); !errors.Is(err, ErrHeld) || f.calls.Load() != sent {
		t.Errorf("after %d failures in a row: %v and %d new requests, want ErrHeld and none", failureLimit, err, f.calls.Load()-sent)
	}
	// Once the hold has passed it asks again.
	c.now = func() time.Time { return time.Now().Add(failureHold + time.Minute) }
	if _, err := c.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:100"}); errors.Is(err, ErrHeld) {
		t.Error("still held after failureHold")
	}

	// An answer between failures resets the count.
	g := newFake(t)
	var fail atomic.Bool
	fail.Store(true)
	g.search = func(w http.ResponseWriter, _ string) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"query":{"search":[]}}`)
	}
	d := g.client()
	for i := range failureLimit - 1 {
		_, _ = d.EnrichAuthor(ctx, &models.Author{ForeignID: fmt.Sprintf("nb:author:%d", i+1)})
	}
	fail.Store(false)
	_, _ = d.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:50"})
	fail.Store(true)
	if _, err := d.EnrichAuthor(ctx, &models.Author{ForeignID: "nb:author:51"}); errors.Is(err, ErrHeld) {
		t.Error("held although an answer came between the failures")
	}
}
