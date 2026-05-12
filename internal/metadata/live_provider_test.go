package metadata_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const liveProviderTestTimeout = 45 * time.Second

type liveProvider struct {
	name    string
	envSkip string
	lookup  func(context.Context, string) (*models.Book, error)
}

type liveWorkCase struct {
	name            string
	isbn            string
	canonicalTitle  string
	canonicalAuthor string
	expects         map[string]liveBookExpectation
}

type liveBookExpectation struct {
	title     string
	author    string
	foreignID string
}

type openLibraryNoISBN struct {
	*openlibrary.Client
}

func (o openLibraryNoISBN) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return nil, nil
}

func TestLiveProviderISBNLookups(t *testing.T) {
	skipUnlessIntegration(t)

	providers := []liveProvider{
		{
			name: "openlibrary",
			lookup: func(ctx context.Context, isbn string) (*models.Book, error) {
				return openlibrary.New().GetBookByISBN(ctx, isbn)
			},
		},
		{
			name: "googlebooks",
			lookup: func(ctx context.Context, isbn string) (*models.Book, error) {
				return googlebooks.New(os.Getenv(googleBooksAPIKeyEnv)).GetBookByISBN(ctx, isbn)
			},
		},
		{
			name:    "hardcover",
			envSkip: binderyHardcoverAPITokenEnv,
			lookup: func(ctx context.Context, isbn string) (*models.Book, error) {
				return hardcover.New().WithToken(os.Getenv(binderyHardcoverAPITokenEnv)).GetBookByISBN(ctx, isbn)
			},
		},
	}

	for _, tc := range liveWorkCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, provider := range providers {
				want, ok := tc.expects[provider.name]
				if !ok {
					continue
				}
				t.Run(provider.name, func(t *testing.T) {
					if provider.envSkip != "" && os.Getenv(provider.envSkip) == "" {
						t.Skipf("skipping %s live ISBN lookup; set %s", provider.name, provider.envSkip)
					}
					ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
					t.Cleanup(cancel)

					book, err := provider.lookup(ctx, tc.isbn)
					if err != nil {
						skipIfLiveProviderUnavailableError(t, provider.name, err)
						t.Fatalf("GetBookByISBN(%s): %v", tc.isbn, err)
					}
					assertLiveBook(t, book, provider.name, want)
					assertCanonicalWork(t, book, tc.canonicalTitle, tc.canonicalAuthor)
				})
			}
		})
	}
}

func TestLiveDNBISBNLookups(t *testing.T) {
	skipUnlessIntegration(t)

	for _, tt := range []struct {
		name      string
		isbn      string
		title     string
		author    string
		foreignID string
	}{
		{
			name:      "strunz-fitness",
			isbn:      "9783453198975",
			title:     "Fit wie Tiger, Panther & Co. oder was man von den Tieren lernen kann",
			author:    "Ulrich Strunz",
			foreignID: "dnb:1011317877",
		},
		{
			name:      "elbenthal-saga",
			isbn:      "9783423625668",
			title:     "Elbenthal-Saga",
			author:    "Ivo Pala",
			foreignID: "dnb:1034321560",
		},
		{
			name:      "manhattan-karma",
			isbn:      "9783518462553",
			title:     "Manhattan-Karma: ein Leonid-McGill-Roman",
			author:    "Walter Mosley",
			foreignID: "dnb:1008350516",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
			t.Cleanup(cancel)

			book, err := dnb.New().GetBookByISBN(ctx, tt.isbn)
			if err != nil {
				skipIfLiveProviderUnavailableError(t, "dnb", err)
				t.Fatalf("GetBookByISBN(%s): %v", tt.isbn, err)
			}
			assertLiveBook(t, book, "dnb", liveBookExpectation{
				title:     tt.title,
				author:    tt.author,
				foreignID: tt.foreignID,
			})
		})
	}
}

func TestLiveAggregatorCanonicalizesSecondaryISBNHitToOpenLibrary(t *testing.T) {
	skipUnlessIntegration(t)

	for _, tt := range []struct {
		name            string
		isbn            string
		canonicalTitle  string
		canonicalAuthor string
		foreignID       string
	}{
		{
			name:            "project-hail-mary",
			isbn:            "9780593135204",
			canonicalTitle:  "Project Hail Mary",
			canonicalAuthor: "Andy Weir",
			foreignID:       "OL21745884W",
		},
		{
			name:            "dune",
			isbn:            "9780441172719",
			canonicalTitle:  "Dune",
			canonicalAuthor: "Frank Herbert",
			foreignID:       "OL893415W",
		},
		{
			name:            "left-hand-of-darkness",
			isbn:            "9780441478125",
			canonicalTitle:  "The Left Hand of Darkness",
			canonicalAuthor: "Ursula K. Le Guin",
			foreignID:       "OL59800W",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
			t.Cleanup(cancel)

			agg := metadata.NewAggregator(openLibraryNoISBN{Client: openlibrary.New()}, googlebooks.New(os.Getenv(googleBooksAPIKeyEnv)))
			book, err := agg.GetBookByISBN(ctx, tt.isbn)
			if err != nil {
				skipIfLiveProviderUnavailableError(t, "googlebooks", err)
				t.Fatalf("GetBookByISBN(%s): %v", tt.isbn, err)
			}
			assertLiveBook(t, book, "openlibrary", liveBookExpectation{
				title:     tt.canonicalTitle,
				author:    tt.canonicalAuthor,
				foreignID: tt.foreignID,
			})
		})
	}
}

func liveWorkCases() []liveWorkCase {
	return []liveWorkCase{
		{
			name:            "project-hail-mary-isbn13",
			isbn:            "9780593135204",
			canonicalTitle:  "Project Hail Mary",
			canonicalAuthor: "Andy Weir",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "Project Hail Mary", author: "Andy Weir", foreignID: "OL21745884W"},
				"googlebooks": {title: "Project Hail Mary", author: "Andy Weir"},
				"hardcover":   {title: "Project Hail Mary", author: "Andy Weir", foreignID: "hc:project-hail-mary"},
			},
		},
		{
			name:            "project-hail-mary-isbn10",
			isbn:            "0593135202",
			canonicalTitle:  "Project Hail Mary",
			canonicalAuthor: "Andy Weir",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "Project Hail Mary", author: "Andy Weir", foreignID: "OL21745884W"},
				"googlebooks": {title: "Project Hail Mary", author: "Andy Weir"},
				"hardcover":   {title: "Project Hail Mary", author: "Andy Weir", foreignID: "hc:project-hail-mary"},
			},
		},
		{
			name:            "dune-isbn13",
			isbn:            "9780441172719",
			canonicalTitle:  "Dune",
			canonicalAuthor: "Frank Herbert",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "Dune", author: "Frank Herbert", foreignID: "OL893415W"},
				"googlebooks": {title: "Dune", author: "Frank Herbert"},
				"hardcover":   {title: "Dune", author: "Frank Herbert", foreignID: "hc:dune"},
			},
		},
		{
			name:            "dune-isbn10",
			isbn:            "0441172717",
			canonicalTitle:  "Dune",
			canonicalAuthor: "Frank Herbert",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "Dune", author: "Frank Herbert", foreignID: "OL893415W"},
				"googlebooks": {title: "Dune", author: "Frank Herbert"},
				"hardcover":   {title: "Dune", author: "Frank Herbert", foreignID: "hc:dune"},
			},
		},
		{
			name:            "name-of-the-wind-isbn13",
			isbn:            "9780756404741",
			canonicalTitle:  "The Name of the Wind",
			canonicalAuthor: "Patrick Rothfuss",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Name of the Wind", author: "Patrick Rothfuss"},
				"googlebooks": {title: "The Name of the Wind", author: "Patrick Rothfuss"},
				"hardcover":   {title: "The Name of the Wind", author: "Patrick Rothfuss", foreignID: "hc:the-name-of-the-wind"},
			},
		},
		{
			name:            "name-of-the-wind-isbn10",
			isbn:            "0756404746",
			canonicalTitle:  "The Name of the Wind",
			canonicalAuthor: "Patrick Rothfuss",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Name of the Wind", author: "Patrick Rothfuss"},
				"googlebooks": {title: "The Name of the Wind", author: "Patrick Rothfuss"},
				"hardcover":   {title: "The Name of the Wind", author: "Patrick Rothfuss", foreignID: "hc:the-name-of-the-wind"},
			},
		},
		{
			name:            "to-kill-a-mockingbird",
			isbn:            "9780061120084",
			canonicalTitle:  "To Kill a Mockingbird",
			canonicalAuthor: "Harper Lee",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "To Kill a Mockingbird", author: "Harper Lee", foreignID: "OL3140822W"},
				"googlebooks": {title: "To Kill a Mockingbird", author: "Harper Lee"},
				"hardcover":   {title: "To Kill A Mockingbird", author: "Harper Lee", foreignID: "hc:to-kill-a-mockingbird"},
			},
		},
		{
			name:            "the-hunger-games",
			isbn:            "9780439023528",
			canonicalTitle:  "The Hunger Games",
			canonicalAuthor: "Suzanne Collins",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Hunger Games", author: "Suzanne Collins", foreignID: "OL5735363W"},
				"googlebooks": {title: "The Hunger Games", author: "Suzanne Collins"},
				"hardcover":   {title: "The Hunger Games", author: "Suzanne Collins", foreignID: "hc:the-hunger-games"},
			},
		},
		{
			name:            "the-da-vinci-code",
			isbn:            "9780307277671",
			canonicalTitle:  "The Da Vinci Code",
			canonicalAuthor: "Dan Brown",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Da Vinci Code", author: "Dan Brown", foreignID: "OL76837W"},
				"googlebooks": {title: "The Da Vinci Code", author: "Dan Brown"},
				"hardcover":   {title: "The Da Vinci Code", author: "Dan Brown", foreignID: "hc:the-da-vinci-code"},
			},
		},
		{
			name:            "the-body-keeps-the-score",
			isbn:            "9780143127741",
			canonicalTitle:  "The Body Keeps the Score",
			canonicalAuthor: "Bessel van der Kolk",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Body Keeps the Score", author: "Bessel van der Kolk", foreignID: "OL18147687W"},
				"googlebooks": {title: "The Body Keeps the Score", author: "Bessel A. Van der Kolk"},
				"hardcover":   {title: "The Body Keeps the Score: Brain, Mind, and Body in the Healing of Trauma", author: "Bessel van der Kolk", foreignID: "hc:the-body-keeps-the-score"},
			},
		},
		{
			name:            "the-left-hand-of-darkness",
			isbn:            "9780441478125",
			canonicalTitle:  "The Left Hand of Darkness",
			canonicalAuthor: "Ursula K. Le Guin",
			expects: map[string]liveBookExpectation{
				"openlibrary": {title: "The Left Hand of Darkness", author: "Ursula K. Le Guin", foreignID: "OL59800W"},
				"googlebooks": {title: "The Left Hand of Darkness", author: "Ursula K. Le Guin"},
				"hardcover":   {title: "The Left Hand of Darkness", author: "Ursula K. Le Guin", foreignID: "hc:the-left-hand-of-darkness"},
			},
		},
	}
}

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(binderyIntegrationEnv) != "1" {
		t.Skip("skipping live metadata test; set BINDERY_INTEGRATION=1 to run")
	}
}

func assertLiveBook(t *testing.T, book *models.Book, provider string, want liveBookExpectation) {
	t.Helper()
	if book == nil {
		t.Fatal("book = nil, want live provider result")
	}
	if book.ForeignID == "" {
		t.Fatal("ForeignID is empty")
	}
	if want.foreignID != "" && book.ForeignID != want.foreignID {
		t.Fatalf("ForeignID = %q, want %q", book.ForeignID, want.foreignID)
	}
	if book.MetadataProvider != provider {
		t.Fatalf("MetadataProvider = %q, want %q", book.MetadataProvider, provider)
	}
	if want.title != "" && book.Title != want.title {
		t.Fatalf("Title = %q, want %q", book.Title, want.title)
	}
	if book.Author == nil {
		t.Fatalf("Author = nil, want %q", want.author)
	}
	if want.author != "" && book.Author.Name != want.author {
		t.Fatalf("Author.Name = %q, want %q", book.Author.Name, want.author)
	}
}

func assertCanonicalWork(t *testing.T, book *models.Book, title, author string) {
	t.Helper()
	gotTitle := normalizedLiveTitle(book.Title)
	wantTitle := normalizedLiveTitle(title)
	if !sameLiveWorkTitle(gotTitle, wantTitle) {
		t.Fatalf("canonical title = %q (%q), want %q", gotTitle, book.Title, wantTitle)
	}
	if match := textutil.MatchAuthorName(book.Author.Name, author); match.Kind != textutil.AuthorMatchExact && match.Kind != textutil.AuthorMatchFuzzyAuto {
		t.Fatalf("canonical author = %q, want %q (match=%+v)", book.Author.Name, author, match)
	}
}

func normalizedLiveTitle(title string) string {
	return strings.TrimSpace(indexer.NormalizeTitleForDedup(title))
}

func sameLiveWorkTitle(got, want string) bool {
	if got == want {
		return true
	}
	if !strings.HasPrefix(got, want) {
		return false
	}
	suffix := strings.TrimPrefix(got, want)
	return strings.HasPrefix(suffix, " ") ||
		strings.HasPrefix(suffix, ":") ||
		strings.HasPrefix(suffix, "-") ||
		strings.HasPrefix(suffix, "(")
}
