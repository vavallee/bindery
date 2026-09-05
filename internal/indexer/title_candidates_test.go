package indexer

import (
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func TestBuildTitleCandidatesBilingualSpanish(t *testing.T) {
	book := models.Book{
		Title:         "El imperio final / The Final Empire",
		OriginalTitle: "The Final Empire",
		Language:      "spa",
	}
	got := BuildTitleCandidates(book, nil, []string{"spa"})
	want := []TitleCandidate{
		{Title: "El imperio final", Language: "spa"},
		{Title: "The Final Empire", ManualOnly: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildTitleCandidates() = %#v, want %#v", got, want)
	}

	automatic := effectiveTitleCandidates(MatchCriteria{Title: book.Title, TitleCandidates: got})
	if !reflect.DeepEqual(automatic, want[:1]) {
		t.Fatalf("automatic candidates = %#v, want %#v", automatic, want[:1])
	}
	interactive := effectiveTitleCandidates(MatchCriteria{
		Title:               book.Title,
		TitleCandidates:     got,
		AllowManualFallback: true,
	})
	if !reflect.DeepEqual(interactive, want) {
		t.Fatalf("interactive candidates = %#v, want %#v", interactive, want)
	}
}

func TestBuildTitleCandidatesOrientsReversedOriginalTitle(t *testing.T) {
	book := models.Book{
		Title:         "The Final Empire / El imperio final",
		OriginalTitle: "The Final Empire",
		Language:      "spa",
	}
	want := []TitleCandidate{
		{Title: "El imperio final", Language: "spa"},
		{Title: "The Final Empire", ManualOnly: true},
	}
	if got := BuildTitleCandidates(book, nil, []string{"spa"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildTitleCandidates() = %#v, want %#v", got, want)
	}
}

func TestBuildTitleCandidatesKeepsAmbiguousSlashesWhole(t *testing.T) {
	tests := []string{
		"The Martian / Artemis / Project Hail Mary",
		"Red Rising [ENG / M4B]",
		"AC/DC: Maximum Rock & Roll",
	}
	for _, title := range tests {
		t.Run(title, func(t *testing.T) {
			got := BuildTitleCandidates(models.Book{Title: title, Language: "spa"}, nil, []string{"spa"})
			if len(got) != 1 || got[0].Title != title {
				t.Fatalf("BuildTitleCandidates(%q) = %#v, want unsplit title", title, got)
			}
		})
	}
}

func TestBuildTitleCandidatesUsesOneLocalizedEditionAlias(t *testing.T) {
	selectedID := int64(11)
	book := models.Book{
		Title:             "El nombre del viento",
		OriginalTitle:     "The Name of the Wind",
		Language:          "spa",
		SelectedEditionID: &selectedID,
	}
	editions := []models.Edition{
		{ID: 10, Title: "The Name of the Wind", Language: "eng", Monitored: true},
		{ID: 11, Title: "El nombre del viento: Crónica del asesino de reyes", Language: "spa"},
	}
	got := BuildTitleCandidates(book, editions, []string{"spa"})
	want := []TitleCandidate{
		{Title: "El nombre del viento", Language: "spa"},
		{Title: "El nombre del viento: Crónica del asesino de reyes", Language: "spa"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildTitleCandidates() = %#v, want %#v", got, want)
	}
}

func TestBuildTitleCandidatesFallsBackThroughMetadataSources(t *testing.T) {
	tests := []struct {
		name             string
		book             models.Book
		editions         []models.Edition
		allowedLanguages []string
		want             []TitleCandidate
	}{
		{
			name:             "original title when display title is empty",
			book:             models.Book{OriginalTitle: "Project Hail Mary", Language: "en"},
			allowedLanguages: []string{"en"},
			want:             []TitleCandidate{{Title: "Project Hail Mary", Language: "eng"}},
		},
		{
			name:             "edition when book titles are empty",
			book:             models.Book{},
			editions:         []models.Edition{{ID: 21, Title: "Elantris", Language: "es"}},
			allowedLanguages: []string{"es"},
			want:             []TitleCandidate{{Title: "Elantris", Language: "spa"}},
		},
		{
			name:             "original title as manual fallback",
			book:             models.Book{Title: "El imperio final", OriginalTitle: "The Final Empire", Language: "spa"},
			allowedLanguages: []string{"spa"},
			want: []TitleCandidate{
				{Title: "El imperio final", Language: "spa"},
				{Title: "The Final Empire", ManualOnly: true},
			},
		},
		{
			name:             "out of language edition as manual fallback",
			book:             models.Book{Title: "El imperio final", Language: "spa"},
			editions:         []models.Edition{{ID: 22, Title: "The Final Empire", Language: "eng"}},
			allowedLanguages: []string{"spa"},
			want: []TitleCandidate{
				{Title: "El imperio final", Language: "spa"},
				{Title: "The Final Empire", Language: "eng", ManualOnly: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildTitleCandidates(tt.book, tt.editions, tt.allowedLanguages); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuildTitleCandidates() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildTitleCandidatesRequiresLocalizedEvidenceToSplit(t *testing.T) {
	title := "Left / Right"
	got := BuildTitleCandidates(models.Book{Title: title, Language: "eng"}, nil, []string{"eng"})
	if len(got) != 1 || got[0].Title != title {
		t.Fatalf("BuildTitleCandidates() = %#v, want unsplit title", got)
	}
}

func TestBuildTitleCandidatesRejectsPunctuationOnlyMetadata(t *testing.T) {
	got := BuildTitleCandidates(
		models.Book{Title: "---", OriginalTitle: "..."},
		[]models.Edition{{Title: "()", Language: "spa"}},
		[]string{"spa"},
	)
	if len(got) != 0 {
		t.Fatalf("BuildTitleCandidates() = %#v, want no candidates", got)
	}
}

func TestEffectiveTitleCandidatesUsesLegacyTitleAndBoundsCandidates(t *testing.T) {
	legacy := effectiveTitleCandidates(MatchCriteria{Title: "  Dune  "})
	if !reflect.DeepEqual(legacy, []TitleCandidate{{Title: "Dune"}}) {
		t.Fatalf("legacy candidates = %#v", legacy)
	}

	bounded := effectiveTitleCandidates(MatchCriteria{TitleCandidates: []TitleCandidate{
		{Title: ""},
		{Title: "Dune"},
		{Title: "Dune Messiah"},
		{Title: "Children of Dune"},
	}})
	want := []TitleCandidate{{Title: "Dune"}, {Title: "Dune Messiah"}}
	if !reflect.DeepEqual(bounded, want) {
		t.Fatalf("bounded candidates = %#v, want %#v", bounded, want)
	}
}

func TestInteractiveLanguageFilterKeepsManualFallback(t *testing.T) {
	results := []newznab.SearchResult{
		{Title: "El Imperio Final SPANISH EPUB", Language: "es"},
		{Title: "The Final Empire ENGLISH EPUB", Language: "en", ManualOnly: true},
		{Title: "Unrelated English EPUB", Language: "en"},
	}
	got := FilterByAllowedLanguagesInteractive(results, []string{"spa"})
	if len(got) != 2 || got[0].Title != results[0].Title || got[1].Title != results[1].Title {
		t.Fatalf("interactive filter = %#v, want Spanish plus manual fallback", got)
	}
}
