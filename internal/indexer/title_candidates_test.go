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
