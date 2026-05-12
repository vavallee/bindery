package metadata_test

import (
	"context"
	"fmt"
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

const liveISBNProviderCallDelay = 250 * time.Millisecond

type isbnTortureCase struct {
	id                      string
	providers               []string
	isbnInputs              []string
	expectedOpenLibraryWork string
}

type isbnDirectProvider struct {
	name     string
	provider metadata.Provider
	skip     string
}

func TestLiveISBNProviderFallbackTortureCorpus(t *testing.T) {
	skipUnlessIntegration(t)

	ol := openlibrary.New()
	gb := googlebooks.New(os.Getenv(googleBooksAPIKeyEnv))
	dn := dnb.New()
	providers := []metadata.Provider{gb, dn}
	if token := os.Getenv(binderyHardcoverAPITokenEnv); token != "" {
		providers = append(providers, hardcover.New().WithToken(token))
	}
	agg := metadata.NewAggregator(ol, providers...)

	for _, tc := range isbnTortureCorpus() {
		t.Run(tc.id, func(t *testing.T) {
			for _, isbnInput := range tc.isbnInputs {
				t.Run(fmt.Sprintf("aggregator/%q", isbnInput), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
					t.Cleanup(cancel)

					got, err := agg.GetBookByISBN(ctx, isbnInput)
					sleepAfterLiveISBNProviderCall()
					if err != nil {
						skipIfLiveProviderUnavailableError(t, "aggregator", err)
						t.Fatalf("GetBookByISBN(%q): %v", isbnInput, err)
					}
					if tc.expectedOpenLibraryWork == "" {
						t.Logf("GetBookByISBN(%q) = %s; fixture has no expected_openlibrary_work", isbnInput, describeLiveISBNBook(got))
						return
					}
					if got == nil {
						t.Fatalf("GetBookByISBN(%q) = nil, want OpenLibrary work %s", isbnInput, tc.expectedOpenLibraryWork)
					}
					if got.MetadataProvider != "openlibrary" || got.ForeignID != tc.expectedOpenLibraryWork {
						t.Fatalf("GetBookByISBN(%q) = %s, want provider=%q foreignID=%q", isbnInput, describeLiveISBNBook(got), "openlibrary", tc.expectedOpenLibraryWork)
					}
				})
			}
		})
	}
}

func TestLiveDirectISBNProvidersTortureCorpus(t *testing.T) {
	skipUnlessIntegration(t)

	directProviders := []isbnDirectProvider{
		{name: "googlebooks", provider: googlebooks.New(os.Getenv(googleBooksAPIKeyEnv))},
		{name: "dnb", provider: dnb.New()},
	}
	if token := os.Getenv(binderyHardcoverAPITokenEnv); token != "" {
		directProviders = append(directProviders, isbnDirectProvider{name: "hardcover", provider: hardcover.New().WithToken(token)})
	} else {
		directProviders = append(directProviders, isbnDirectProvider{name: "hardcover", skip: "set " + binderyHardcoverAPITokenEnv + " to run Hardcover live ISBN lookups"})
	}

	for _, tc := range isbnTortureCorpus() {
		t.Run(tc.id, func(t *testing.T) {
			for _, directProvider := range directProviders {
				if !isbnCaseHasProvider(tc, directProvider.name) {
					continue
				}
				t.Run(directProvider.name, func(t *testing.T) {
					if directProvider.skip != "" {
						t.Skip(directProvider.skip)
					}
					for _, isbnInput := range tc.isbnInputs {
						t.Run(fmt.Sprintf("%q", isbnInput), func(t *testing.T) {
							ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
							t.Cleanup(cancel)

							got, err := directProvider.provider.GetBookByISBN(ctx, isbnInput)
							sleepAfterLiveISBNProviderCall()
							if err != nil {
								skipIfLiveProviderUnavailableError(t, directProvider.name, err)
								t.Fatalf("%s.GetBookByISBN(%q): %v", directProvider.name, isbnInput, err)
							}
							if got == nil {
								t.Fatalf("%s.GetBookByISBN(%q) = nil, want upstream provider result", directProvider.name, isbnInput)
							}
							t.Logf("%s.GetBookByISBN(%q) = %s", directProvider.name, isbnInput, describeLiveISBNBook(got))
						})
					}
				})
			}
		})
	}
}

func sleepAfterLiveISBNProviderCall() {
	time.Sleep(liveISBNProviderCallDelay)
}

func isbnCaseHasProvider(tc isbnTortureCase, provider string) bool {
	for _, got := range tc.providers {
		if got == provider {
			return true
		}
	}
	return false
}

func describeLiveISBNBook(book *models.Book) string {
	if book == nil {
		return "<nil>"
	}
	author := ""
	if book.Author != nil {
		author = book.Author.Name
	}
	return fmt.Sprintf("title=%q author=%q provider=%q foreignID=%q", book.Title, author, book.MetadataProvider, book.ForeignID)
}

func isbnTortureCorpus() []isbnTortureCase {
	return []isbnTortureCase{
		{
			id:        "phm_ascii_subtitle_google_hardcover_ol",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9780593135204",
				"978-0-593-13520-4",
				"978 0 593 13520 4",
				"0593135202",
				" 9780593135204 ",
			},
			expectedOpenLibraryWork: "OL21745884W",
		},
		{
			id:        "dune_german_ol_google_dnb_isbn10_x",
			providers: []string{"openlibrary", "googlebooks", "dnb", "hardcover"},
			isbnInputs: []string{
				"9783453305236",
				"978-3-453-30523-6",
				"978 3 453 30523 6",
				"345330523X",
				"3-453-30523-X",
				"345330523x",
			},
			expectedOpenLibraryWork: "OL893415W",
		},
		{
			id:        "dune_german_dnb_new_translation_title_noise",
			providers: []string{"dnb", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9783453323131",
				"978-3-453-32313-1",
				"9783453321229",
				"978-3-453-32122-9",
				"9783453317178",
			},
			expectedOpenLibraryWork: "OL893415W",
		},
		{
			id:        "cien_anos_spanish_accents_bilingual",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9780307474728",
				"978-0-307-47472-8",
				"978 0 307 47472 8",
				"0307474720",
			},
			expectedOpenLibraryWork: "OL274505W",
		},
		{
			id:        "santi_chinese_simplified_transliteration",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9787536692930",
				"978-7-5366-9293-0",
				"978 7 5366 9293 0",
				"7536692935",
				"9780765377067",
				"978-0-7653-7706-7",
			},
			expectedOpenLibraryWork: "OL17267881W",
		},
		{
			id:        "huozhe_chinese_short_title",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9787544210966",
				"978-7-5442-1096-6",
				"978 7 5442 1096 6",
				"7544210960",
			},
			expectedOpenLibraryWork: "OL20903102W",
		},
		{
			id:        "awlad_haratina_arabic_rtl_translations",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9789770915349",
				"978-977-09-1534-9",
				"978 977 09 1534 9",
				"9770915343",
			},
			expectedOpenLibraryWork: "OL1599698W",
		},
		{
			id:        "master_margarita_cyrillic_translated",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9785170878840",
				"978-5-170-87884-0",
				"978 5 170 87884 0",
				"5170878842",
			},
			expectedOpenLibraryWork: "OL676009W",
		},
		{
			id:        "vorleser_dnb_german_generic_english_title",
			providers: []string{"dnb", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9783257060652",
				"978-3-257-06065-2",
				"9783257070668",
				"978-3-257-07066-8",
			},
		},
		{
			id:        "kafka_verwandlung_dnb_google_umlaut_translation",
			providers: []string{"openlibrary", "googlebooks", "dnb", "hardcover"},
			isbnInputs: []string{
				"9783596258758",
				"978-3-596-25875-8",
				"3596258758",
				"9783125560291",
				"978-3-12-556029-1",
			},
		},
		{
			id:        "name_of_the_wind_hardcover_exact_isbn",
			providers: []string{"hardcover", "googlebooks", "openlibrary"},
			isbnInputs: []string{
				"9780756404741",
				"978-0-7564-0474-1",
				"978 0 7564 0474 1",
				"0756404746",
			},
			expectedOpenLibraryWork: "OL8479867W",
		},
		{
			id:        "godel_escher_bach_punctuation_diacritic",
			providers: []string{"openlibrary", "googlebooks", "hardcover"},
			isbnInputs: []string{
				"9780465026562",
				"978-0-465-02656-2",
				"978 0 465 02656 2",
			},
		},
	}
}
