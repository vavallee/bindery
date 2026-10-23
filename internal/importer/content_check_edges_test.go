package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestBookIsCompilation_Shapes(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"The Dune Saga Collection", true},
		{"Dune Saga Collection", true},
		{"The Memory Collection", false},
		{"A Collection of Curses", false},
		{"Dune Chronicles 1-3", true},
		{"Dune Chronicles 3-1", false},
		{"Dune Chronicles 2-2", false},
		{"1984", false},
		{"Project Hail Mary", false},
	}
	for _, tc := range cases {
		if got := bookIsCompilation(tc.title); got != tc.want {
			t.Errorf("bookIsCompilation(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestBookIsCompilationBy_BestOfNeedsTheAuthorsName(t *testing.T) {
	cases := []struct {
		title, author string
		want          bool
	}{
		{"The Best of Poe", "Edgar Allan Poe", true},
		{"Best of Poe and Others", "Edgar Allan Poe", true},
		{"The Best of Me", "Nicholas Sparks", false},
		{"Best of Friends", "Nicholas Sparks", false},
		{"The Best of Poe", "", false},
		{"The Best of Poet Laureates", "Edgar Allan Poe", false},
		{"Some Ordinary Title", "Edgar Allan Poe", false},
	}
	for _, tc := range cases {
		if got := bookIsCompilationBy(tc.title, tc.author); got != tc.want {
			t.Errorf("bookIsCompilationBy(%q, %q) = %v, want %v", tc.title, tc.author, got, tc.want)
		}
	}
}

func TestEbookAuthorMatches_Scripts(t *testing.T) {
	cases := []struct {
		name    string
		names   []string
		creator string
		want    bool
	}{
		{"no creator says nothing", []string{"Jane Austen"}, "", true},
		{"no author on record says nothing", nil, "Mary Trevelyan", true},
		{"only blank names say nothing is known", []string{"  "}, "Mary Trevelyan", true},
		{"placeholder creator", []string{"Jane Austen"}, "Unknown", true},
		{"placeholder creator converter name", []string{"Jane Austen"}, "calibre", true},
		{"last, first against first last", []string{"J.R.R. Tolkien"}, "Tolkien, J. R. R.", true},
		{"co author credit", []string{"James Patterson"}, "James Patterson and Maxine Paetro", true},
		{"clear mismatch", []string{"Jane Austen"}, "Mary Trevelyan", false},
		{"a placeholder alongside a real mismatching name is still a mismatch", []string{"Jane Austen"}, "Unknown; Mary Trevelyan", false},
		{"cyrillic creator with only latin names says nothing", []string{"Leo Tolstoy"}, "Лев Толстой", true},
		{"latin creator disagrees even though the author has a cyrillic alias", []string{"Лев Толстой", "Leo Tolstoy"}, "Mary Trevelyan", false},
		{"latin creator with only a cyrillic name on record says nothing", []string{"Лев Толстой"}, "Mary Trevelyan", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ebookAuthorMatches(tc.names, tc.creator); got != tc.want {
				t.Fatalf("ebookAuthorMatches(%q, %q) = %v, want %v", tc.names, tc.creator, got, tc.want)
			}
		})
	}
}

func TestAuthorNameAndAuthorNamesFor(t *testing.T) {
	if authorName(nil) != "" || authorName(&models.Author{Name: "Jane Austen"}) != "Jane Austen" {
		t.Error("authorName")
	}
	ctx := context.Background()
	if got := (&Scanner{}).authorNamesFor(ctx, nil); got != nil {
		t.Fatalf("no author = %v, want nil", got)
	}
	a := &models.Author{ID: 1, Name: "Jane Austen"}
	if got := (&Scanner{}).authorNamesFor(ctx, a); len(got) != 1 || got[0] != "Jane Austen" {
		t.Fatalf("no alias repo = %v, want just the name on record", got)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authors := db.NewAuthorRepo(database)
	real := &models.Author{ForeignID: "OL1A", Name: "Jane Austen", SortName: "Austen, Jane", Monitored: true}
	if err := authors.Create(ctx, real); err != nil {
		t.Fatal(err)
	}
	aliases := db.NewAuthorAliasRepo(database)
	if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: real.ID, Name: "J. Austen"}); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{authorAliases: aliases}
	got := s.authorNamesFor(ctx, real)
	if len(got) != 2 || got[0] != "Jane Austen" || got[1] != "J. Austen" {
		t.Fatalf("with aliases = %v, want the name then each alias", got)
	}

	closed, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	s2 := &Scanner{authorAliases: db.NewAuthorAliasRepo(closed)}
	closed.Close()
	if got := s2.authorNamesFor(ctx, real); len(got) != 1 || got[0] != "Jane Austen" {
		t.Fatalf("alias lookup failure = %v, want the name on record alone", got)
	}
}

func TestContentTitleHelpers(t *testing.T) {
	t.Run("containsWholeWords", func(t *testing.T) {
		cases := []struct {
			hay, needle string
			want        bool
		}{
			{"dune messiah", "dune", true},
			{"dune messiah", "messiah", true},
			{"dunes of mars", "dune", false},
			{"children of dune", "dune", true},
			{"dune", "", false},
			{"", "dune", false},
			{"dune messiah", "dune messiah", true},
			{"dune messiah", "une messiah", false},
		}
		for _, tc := range cases {
			if got := containsWholeWords(tc.hay, tc.needle); got != tc.want {
				t.Errorf("containsWholeWords(%q, %q) = %v, want %v", tc.hay, tc.needle, got, tc.want)
			}
		}
	})
	t.Run("stripSeriesSuffix", func(t *testing.T) {
		cases := map[string]string{
			"Mary, Mary: Alex Cross, Book 11": "Mary, Mary",
			"Dune #3":                         "Dune",
			"Dune # 3":                        "Dune",
			"Dune Messiah, Book Eleven":       "Dune Messiah",
			"Dune - Volume 3":                 "Dune",
			"Dune":                            "Dune",
			"  Dune  ":                        "Dune",
			"Book 3":                          "Book 3",
		}
		for in, want := range cases {
			if got := stripSeriesSuffix(in); got != want {
				t.Errorf("stripSeriesSuffix(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("volumeStem", func(t *testing.T) {
		cases := []struct {
			in       string
			wantNum  string
			wantOK   bool
			wantStem string
		}{
			{"Foundation #4", "4", true, "foundation"},
			{"Foundation, Book 4", "4", true, "foundation"},
			{"Foundation Vol. 2.5", "2.5", true, "foundation"},
			{"NYPD Red 3", "3", true, "nypd red"},
			{"Book 3", "", false, ""},
			{"#3", "", false, ""},
			{"1984", "", false, ""},
			{"", "", false, ""},
			{"Dune", "", false, ""},
		}
		for _, tc := range cases {
			stem, num, ok := volumeStem(tc.in)
			if ok != tc.wantOK || num != tc.wantNum || stem != tc.wantStem {
				t.Errorf("volumeStem(%q) = %q, %q, %v; want %q, %q, %v", tc.in, stem, num, ok, tc.wantStem, tc.wantNum, tc.wantOK)
			}
		}
	})
}
