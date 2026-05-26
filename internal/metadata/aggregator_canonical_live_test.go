package metadata

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
)

const canonicalPrimaryLiveTestTimeout = 45 * time.Second
const canonicalPrimaryLiveLookupDelay = 250 * time.Millisecond

type canonicalTortureCase struct {
	id                        string
	titleInputs               []string
	authorInputs              []string
	expectedOpenLibraryWork   string
	forbiddenOpenLibraryWorks []string
	forbiddenCanonicalTitles  []string
}

type canonicalProviderSourceCase struct {
	id                      string
	provider                string
	isbnInputs              []string
	expectedSourceProvider  string
	expectedSourceLanguage  string
	expectedOpenLibraryWork string
}

func TestLiveAggregatorCanonicalPrimaryBookTortureCorpus(t *testing.T) {
	if os.Getenv(binderyIntegrationEnv) != "1" {
		t.Skip("skipping live metadata test; set BINDERY_INTEGRATION=1 to run")
	}

	agg := NewAggregator(openlibrary.New())
	for _, tc := range canonicalProviderSourceCorpus() {
		t.Run("provider_source/"+tc.id, func(t *testing.T) {
			sourceProvider, skip := canonicalLiveSourceProvider(tc.provider)
			if skip != "" {
				t.Skip(skip)
			}
			for _, isbnInput := range tc.isbnInputs {
				t.Run(isbnInput, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), canonicalPrimaryLiveTestTimeout)
					t.Cleanup(cancel)

					source, err := sourceProvider.GetBookByISBN(ctx, isbnInput)
					sleepAfterCanonicalPrimaryLiveLookup()
					if err != nil {
						skipIfLiveProviderUnavailableError(t, tc.provider, err)
						t.Fatalf("%s.GetBookByISBN(%q): %v", tc.provider, isbnInput, err)
					}
					assertCanonicalProviderSource(t, source, tc)

					canonical, ok := agg.canonicalPrimaryBook(ctx, isbnInput, *source)
					sleepAfterCanonicalPrimaryLiveLookup()
					if !ok {
						t.Fatalf("canonicalPrimaryBook(%q source=%s) ok = false, want true", isbnInput, describeCanonicalLiveBook(source))
					}
					if canonical == nil {
						t.Fatalf("canonicalPrimaryBook(%q source=%s) = nil, want OpenLibrary work %s", isbnInput, describeCanonicalLiveBook(source), tc.expectedOpenLibraryWork)
						return
					}
					if canonical.ForeignID != tc.expectedOpenLibraryWork {
						t.Fatalf("canonical.ForeignID = %q, want %q (isbn=%q source=%s)", canonical.ForeignID, tc.expectedOpenLibraryWork, isbnInput, describeCanonicalLiveBook(source))
					}
				})
			}
		})
	}
	for _, tc := range canonicalTortureCorpus() {
		for _, titleInput := range tc.titleInputs {
			for _, authorInput := range tc.authorInputs {
				t.Run(fmt.Sprintf("synthetic/%s/%s/%s", tc.id, titleInput, authorInput), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), canonicalPrimaryLiveTestTimeout)
					t.Cleanup(cancel)

					source := models.Book{
						Title:            titleInput,
						Author:           &models.Author{Name: authorInput},
						MetadataProvider: "torture-fixture",
					}
					canonical, ok := agg.canonicalPrimaryBook(ctx, "", source)
					sleepAfterCanonicalPrimaryLiveLookup()
					assertNoForbiddenCanonicalLiveMatch(t, canonical, tc)
					if tc.expectedOpenLibraryWork == "" {
						if !ok || canonical == nil {
							t.Logf("canonicalPrimaryBook(%q, %q) did not match; fixture has no expected_openlibrary_work", titleInput, authorInput)
							return
						}
						t.Logf("canonicalPrimaryBook(%q, %q) = %q; fixture has no expected_openlibrary_work", titleInput, authorInput, canonical.ForeignID)
						return
					}
					if !ok {
						t.Fatalf("canonicalPrimaryBook(%q, %q) ok = false, want true", titleInput, authorInput)
					}
					if canonical == nil {
						t.Fatalf("canonicalPrimaryBook(%q, %q) = nil, want OpenLibrary work %s", titleInput, authorInput, tc.expectedOpenLibraryWork)
						return
					}
					if canonical.ForeignID != tc.expectedOpenLibraryWork {
						t.Fatalf("canonical.ForeignID = %q, want %q (title=%q author=%q)", canonical.ForeignID, tc.expectedOpenLibraryWork, titleInput, authorInput)
					}
				})
			}
		}
	}
}

func sleepAfterCanonicalPrimaryLiveLookup() {
	time.Sleep(canonicalPrimaryLiveLookupDelay)
}

func canonicalLiveSourceProvider(provider string) (Provider, string) {
	switch provider {
	case "dnb":
		return dnb.New(), ""
	case "googlebooks":
		return googlebooks.New(os.Getenv(googleBooksAPIKeyEnv)), ""
	default:
		return nil, "unsupported canonical live source provider " + provider
	}
}

func assertCanonicalProviderSource(t *testing.T, source *models.Book, tc canonicalProviderSourceCase) {
	t.Helper()
	if source == nil {
		t.Fatalf("%s source = nil, want provider result", tc.provider)
		return
	}
	if source.MetadataProvider != tc.expectedSourceProvider {
		t.Fatalf("source provider = %q, want %q (source=%s)", source.MetadataProvider, tc.expectedSourceProvider, describeCanonicalLiveBook(source))
	}
	if tc.expectedSourceLanguage != "" && source.Language != tc.expectedSourceLanguage {
		t.Fatalf("source language = %q, want %q (source=%s)", source.Language, tc.expectedSourceLanguage, describeCanonicalLiveBook(source))
	}
	if strings.TrimSpace(source.Title) == "" {
		t.Fatalf("source title is empty: %s", describeCanonicalLiveBook(source))
	}
	if source.Author == nil || strings.TrimSpace(source.Author.Name) == "" {
		t.Fatalf("source author is empty: %s", describeCanonicalLiveBook(source))
	}
}

func describeCanonicalLiveBook(book *models.Book) string {
	if book == nil {
		return "<nil>"
	}
	author := ""
	if book.Author != nil {
		author = book.Author.Name
	}
	return fmt.Sprintf("title=%q author=%q language=%q provider=%q foreignID=%q", book.Title, author, book.Language, book.MetadataProvider, book.ForeignID)
}

func assertNoForbiddenCanonicalLiveMatch(t *testing.T, canonical *models.Book, tc canonicalTortureCase) {
	t.Helper()
	if canonical == nil {
		return
	}
	for _, workID := range tc.forbiddenOpenLibraryWorks {
		if strings.TrimSpace(canonical.ForeignID) == strings.TrimSpace(workID) {
			t.Fatalf("canonical ForeignID = %q, forbidden for torture case %s", canonical.ForeignID, tc.id)
		}
	}
	gotTitle := indexer.NormalizeTitleForDedup(canonical.Title)
	for _, title := range tc.forbiddenCanonicalTitles {
		if gotTitle == indexer.NormalizeTitleForDedup(title) {
			t.Fatalf("canonical title = %q, forbidden for torture case %s", canonical.Title, tc.id)
		}
	}
}

func canonicalProviderSourceCorpus() []canonicalProviderSourceCase {
	return []canonicalProviderSourceCase{
		{
			id:       "dune_german_dnb_noisy_title_production_source",
			provider: "dnb",
			isbnInputs: []string{
				"9783453323131",
				"9783453317178",
			},
			expectedSourceProvider:  "dnb",
			expectedSourceLanguage:  "ger",
			expectedOpenLibraryWork: "OL893415W",
		},
	}
}

func canonicalTortureCorpus() []canonicalTortureCase {
	return []canonicalTortureCase{
		{
			id: "phm_ascii_subtitle_google_hardcover_ol",
			titleInputs: []string{
				"Project Hail Mary",
				"Project Hail Mary: A Novel",
				"PROJECT HAIL MARY",
			},
			authorInputs: []string{
				"Andy Weir",
				"Weir, Andy",
			},
			expectedOpenLibraryWork: "OL21745884W",
		},
		{
			id: "data_science_real_trailing_descriptor_word",
			titleInputs: []string{
				"Data Science",
			},
			authorInputs: []string{
				"John D. Kelleher",
			},
			forbiddenCanonicalTitles: []string{
				"Data",
			},
		},
		{
			id: "cien_anos_spanish_accents_bilingual",
			titleInputs: []string{
				"Cien años de soledad",
				"Cien Años de Soledad",
				"Cien años de soledad / One Hundred Years of Solitude",
				"One Hundred Years of Solitude",
			},
			authorInputs: []string{
				"Gabriel García Márquez",
				"Gabriel Garcia Marquez",
				"García Márquez, Gabriel",
				"Garcia Marquez, Gabriel",
			},
			expectedOpenLibraryWork: "OL274505W",
		},
		{
			id: "santi_chinese_simplified_transliteration",
			titleInputs: []string{
				"三体",
				"三体 (sān tǐ)",
				"The Three-Body Problem",
				"The three-body problem",
			},
			authorInputs: []string{
				"刘慈欣",
				"Cixin Liu",
				"Liu Cixin",
			},
			expectedOpenLibraryWork: "OL17267881W",
		},
		{
			id: "huozhe_chinese_short_title",
			titleInputs: []string{
				"活着",
				"To Live",
				"Huo Zhe",
			},
			authorInputs: []string{
				"余华",
				"Yu Hua",
				"Hua Yu",
			},
			forbiddenOpenLibraryWorks: []string{
				"OL25129388W",
				"OL15861449W",
				"OL8036242W",
				"OL25686018W",
				"OL3240289W",
				"OL10634244W",
				"OL19963794W",
			},
		},
		{
			id: "awlad_haratina_arabic_rtl_translations",
			titleInputs: []string{
				"أولاد حارتنا",
				"Awlād ḥāratinā",
				"Awlad Haretna (Arabic) أولاد حارتنا",
				"Children of the Alley",
				"Children of Gebelaawi",
				"Children of Gebelawi",
			},
			authorInputs: []string{
				"نجيب محفوظ",
				"Naguib Mahfouz",
				"Najib Mahfuz",
			},
			expectedOpenLibraryWork: "OL1599698W",
		},
		{
			id: "master_margarita_cyrillic_translated",
			titleInputs: []string{
				"Мастер и Маргарита",
				"Мастер и Маргарита: roman",
				"The Master and Margarita",
				"Master and Margarita",
			},
			authorInputs: []string{
				"Михаил Афанасьевич Булгаков",
				"Mikhail Bulgakov",
				"Mikhail Afanasyevich Bulgakov",
				"Bulgakov, Mikhail",
			},
			expectedOpenLibraryWork: "OL676009W",
		},
		{
			id: "vorleser_dnb_german_generic_english_title",
			titleInputs: []string{
				"Der Vorleser",
				"Der Vorleser : Roman",
				"The Reader",
			},
			authorInputs: []string{
				"Bernhard Schlink",
				"Schlink, Bernhard",
			},
		},
		{
			id: "kafka_verwandlung_dnb_google_umlaut_translation",
			titleInputs: []string{
				"Die Verwandlung",
				"Die Verwandlung: Erzählung",
				"The Metamorphosis",
			},
			authorInputs: []string{
				"Franz Kafka",
				"Kafka, Franz",
			},
		},
		{
			id: "name_of_the_wind_hardcover_exact_isbn",
			titleInputs: []string{
				"The Name of the Wind",
				"The Name of the Wind: The Kingkiller Chronicle: day one",
				"Name of the Wind, The",
			},
			authorInputs: []string{
				"Patrick Rothfuss",
				"Rothfuss, Patrick",
			},
			expectedOpenLibraryWork: "OL8479867W",
		},
		{
			id: "godel_escher_bach_punctuation_diacritic",
			titleInputs: []string{
				"Gödel, Escher, Bach: an Eternal Golden Braid",
				"Godel, Escher, Bach: An Eternal Golden Braid",
				"Gödel, Escher, Bach",
				"GEB",
			},
			authorInputs: []string{
				"Douglas R. Hofstadter",
				"Douglas Hofstadter",
				"Hofstadter, Douglas R.",
			},
		},
	}
}
