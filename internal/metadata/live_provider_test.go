package metadata_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
)

const liveProviderTestTimeout = 30 * time.Second

type liveBookExpectation struct {
	title     string
	author    string
	provider  string
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

	tests := []struct {
		name    string
		envSkip string
		lookup  func(context.Context) (*models.Book, error)
		want    liveBookExpectation
	}{
		{
			name: "openlibrary",
			lookup: func(ctx context.Context) (*models.Book, error) {
				return openlibrary.New().GetBookByISBN(ctx, "9780593135204")
			},
			want: liveBookExpectation{
				title:     "Project Hail Mary",
				author:    "Andy Weir",
				provider:  "openlibrary",
				foreignID: "OL21745884W",
			},
		},
		{
			name: "googlebooks",
			lookup: func(ctx context.Context) (*models.Book, error) {
				return googlebooks.New("").GetBookByISBN(ctx, "9780593135204")
			},
			want: liveBookExpectation{
				title:    "Project Hail Mary",
				author:   "Andy Weir",
				provider: "googlebooks",
			},
		},
		{
			name: "dnb",
			lookup: func(ctx context.Context) (*models.Book, error) {
				return dnb.New().GetBookByISBN(ctx, "9783453198975")
			},
			want: liveBookExpectation{
				title:     "Fit wie Tiger, Panther & Co. oder was man von den Tieren lernen kann",
				author:    "Ulrich Strunz",
				provider:  "dnb",
				foreignID: "dnb:1011317877",
			},
		},
		{
			name:    "hardcover",
			envSkip: "BINDERY_HARDCOVER_API_TOKEN",
			lookup: func(ctx context.Context) (*models.Book, error) {
				return hardcover.New().WithToken(os.Getenv("BINDERY_HARDCOVER_API_TOKEN")).GetBookByISBN(ctx, "9780756404741")
			},
			want: liveBookExpectation{
				title:    "The Name of the Wind",
				author:   "Patrick Rothfuss",
				provider: "hardcover",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSkip != "" && os.Getenv(tt.envSkip) == "" {
				t.Skipf("skipping %s live ISBN lookup; set %s", tt.name, tt.envSkip)
			}
			ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
			t.Cleanup(cancel)

			book, err := tt.lookup(ctx)
			if err != nil {
				t.Fatalf("GetBookByISBN: %v", err)
			}
			assertLiveBook(t, book, tt.want)
		})
	}
}

func TestLiveAggregatorCanonicalizesSecondaryISBNHitToOpenLibrary(t *testing.T) {
	skipUnlessIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
	t.Cleanup(cancel)

	agg := metadata.NewAggregator(openLibraryNoISBN{Client: openlibrary.New()}, googlebooks.New(""))
	book, err := agg.GetBookByISBN(ctx, "9780593135204")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	assertLiveBook(t, book, liveBookExpectation{
		title:     "Project Hail Mary",
		author:    "Andy Weir",
		provider:  "openlibrary",
		foreignID: "OL21745884W",
	})
}

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("BINDERY_INTEGRATION") == "" {
		t.Skip("skipping live metadata test; set BINDERY_INTEGRATION=1 to run")
	}
}

func assertLiveBook(t *testing.T, book *models.Book, want liveBookExpectation) {
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
	if book.Title != want.title {
		t.Fatalf("Title = %q, want %q", book.Title, want.title)
	}
	if book.MetadataProvider != want.provider {
		t.Fatalf("MetadataProvider = %q, want %q", book.MetadataProvider, want.provider)
	}
	if book.Author == nil {
		t.Fatalf("Author = nil, want %q", want.author)
	}
	if book.Author.Name != want.author {
		t.Fatalf("Author.Name = %q, want %q", book.Author.Name, want.author)
	}
}
