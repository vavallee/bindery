package calibre

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestNormalizeLanguageForCalibre(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"eng", "en"},
		{"fre", "fr"},
		{"fra", "fr"},
		{"ger", "de"},
		{"deu", "de"},
		{"EN", "en"},
		{"  spa  ", "es"},
		{"cat", "cat"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeLanguageForCalibre(tt.in); got != tt.want {
			t.Errorf("NormalizeLanguageForCalibre(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatCalibredbPubdate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"date only", "1965-08-01", "1965-08-01T00:00:00+00:00"},
		{"timestamp", "1965-08-01T00:00:00+00:00", "1965-08-01T00:00:00+00:00"},
		{"blank", "   ", ""},
		{"unexpected", "August 1965", "August 1965"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatCalibredbPubdate(tt.in); got != tt.want {
				t.Fatalf("formatCalibredbPubdate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCalibredbSeriesIndexSkipsNonNumericValues(t *testing.T) {
	meta := Metadata{Series: "Discworld", SeriesIndex: "Book 2"}
	if got, want := meta.addArgs(), []string{"--series", "Discworld"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addArgs = %#v, want %#v", got, want)
	}
	if got, want := meta.setFields(), []string{"series:Discworld"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("setFields = %#v, want %#v", got, want)
	}
}

func TestCalibredbSeriesIndexKeepsNumericValues(t *testing.T) {
	meta := Metadata{Series: "Dune Chronicles", SeriesIndex: " 1.5 "}
	if got, want := meta.addArgs(), []string{"--series", "Dune Chronicles", "--series-index", "1.5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addArgs = %#v, want %#v", got, want)
	}
	if got, want := meta.setFields(), []string{"series:Dune Chronicles", "series_index:1.5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("setFields = %#v, want %#v", got, want)
	}
}

func TestIdentifiersForBook_UsesPresentBookAndEditionData(t *testing.T) {
	editionASIN := "B000FC1BN8"
	edition := &models.Edition{
		ForeignID: "/books/OL999M",
		ISBN13:    strPtr("9780441172719"),
		ISBN10:    strPtr("0441172717"),
		ASIN:      &editionASIN,
	}
	book := &models.Book{
		ID:               42,
		ForeignID:        "/works/OL123W",
		MetadataProvider: "openlibrary",
		ASIN:             "BOOKASIN",
	}

	got := IdentifiersForBook(book, edition)
	want := map[string]string{
		"asin":                "B000FC1BN8",
		"bindery":             "42",
		"isbn":                "9780441172719",
		"openlibrary":         "OL123W",
		"openlibrary_edition": "OL999M",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IdentifiersForBook = %#v, want %#v", got, want)
	}
}

func TestIdentifiersForBook_NormalizesProviderIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		foreignID string
		wantType  string
		wantValue string
	}{
		{"hardcover", "hardcover", "hc:dune", "hardcover", "dune"},
		{"googlebooks", "googlebooks", "gb:zyTCAlFPjgYC", "google", "zyTCAlFPjgYC"},
		{"google", "google", "gb:zyTCAlFPjgYC", "google", "zyTCAlFPjgYC"},
		{"dnb", "dnb", "dnb:123456789", "dnb", "123456789"},
		{"fallback", "Other.Provider", " provider:value ", "other_provider", "provider:value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IdentifiersForBook(&models.Book{
				ID:               7,
				ForeignID:        tt.foreignID,
				MetadataProvider: tt.provider,
			}, nil)
			if got[tt.wantType] != tt.wantValue {
				t.Fatalf("identifier %q = %q, want %q in %#v", tt.wantType, got[tt.wantType], tt.wantValue, got)
			}
			if got["bindery"] != "7" {
				t.Fatalf("bindery identifier = %q, want 7", got["bindery"])
			}
		})
	}
}

func TestIdentifierArgs_CleansAndSortsIdentifiers(t *testing.T) {
	got := identifierArgs(map[string]string{
		"Google Books": " gb:abc,123 ",
		"isbn":         " 9780441172719 ",
		"empty":        "",
	})
	want := []string{"google_books:gb:abc 123", "isbn:9780441172719"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identifierArgs = %#v, want %#v", got, want)
	}
}

func strPtr(s string) *string { return &s }

// TestCoverFetchClientCheckRedirect covers the redirect policy on the shared
// cover client. MaterializeCover validates the URL it is given, but a cover
// host can answer with a 302 to somewhere else entirely, so the redirect hook
// is the control that stops a public cover URL being used to reach a private
// address. It had no test.
func TestCoverFetchClientCheckRedirect(t *testing.T) {
	if coverFetchClient.CheckRedirect == nil {
		t.Fatal("cover client has no CheckRedirect hook")
	}

	newReq := func(rawURL string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("build request for %q: %v", rawURL, err)
		}
		return req
	}

	// Every URL below uses an IP literal. ValidateOutboundURL resolves
	// hostnames, so a name here would make the test depend on DNS and fail
	// wherever lookups are unavailable.
	const publicHost = "https://93.184.216.34/a.jpg"

	t.Run("allows a public redirect", func(t *testing.T) {
		if err := coverFetchClient.CheckRedirect(newReq(publicHost), nil); err != nil {
			t.Errorf("public redirect rejected: %v", err)
		}
	})

	t.Run("blocks a redirect to a private address", func(t *testing.T) {
		for _, target := range []string{
			"http://127.0.0.1/a.jpg",
			"http://169.254.169.254/latest/meta-data/",
			"http://10.0.0.5/a.jpg",
		} {
			err := coverFetchClient.CheckRedirect(newReq(target), nil)
			if err == nil {
				t.Errorf("redirect to %s was allowed", target)
				continue
			}
			if !strings.Contains(err.Error(), "redirect blocked") {
				t.Errorf("redirect to %s: error = %v, want it to name the block", target, err)
			}
		}
	})

	t.Run("caps the redirect chain", func(t *testing.T) {
		via := make([]*http.Request, 5)
		for i := range via {
			via[i] = newReq(publicHost)
		}
		err := coverFetchClient.CheckRedirect(newReq(publicHost), via)
		if err == nil {
			t.Fatal("a 5 hop chain was allowed to continue")
		}
		if !strings.Contains(err.Error(), "too many redirects") {
			t.Errorf("error = %v, want it to name the hop limit", err)
		}
	})
}
