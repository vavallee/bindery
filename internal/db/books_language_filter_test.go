package db

import (
	"context"
	"slices"
	"testing"
)

// TestListPageFiltered_Language pins the Books page language filter (#1160):
// any spelling of a code matches the stored normalised one, and "unknown"
// matches an empty or blank language.
func TestListPageFiltered_Language(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books := NewBookRepo(database)
	author := seedAuthor(t, NewAuthorRepo(database), "ol:lang-filter", "Lang Filter")

	ita := seedBook(t, books, author.ID, "ol:1", "Italian")
	eng := seedBook(t, books, author.ID, "ol:2", "English")
	empty := seedBook(t, books, author.ID, "ol:3", "Empty")
	blank := seedBook(t, books, author.ID, "ol:5", "Blank")
	if err := books.SetLanguage(ctx, ita.ID, "it"); err != nil {
		t.Fatal(err)
	}
	if err := books.SetLanguage(ctx, eng.ID, "en-GB"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET language = '  ' WHERE id = ?", blank.ID); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		language string
		want     []int64
	}{
		{"", []int64{ita.ID, eng.ID, empty.ID, blank.ID}},
		{"ita", []int64{ita.ID}},
		{"it-IT", []int64{ita.ID}},
		{"ENG", []int64{eng.ID}},
		{"fre", nil},
		{"unknown", []int64{empty.ID, blank.ID}},
		{"Unknown", []int64{empty.ID, blank.ID}},
	} {
		got, total, err := books.ListPageFiltered(ctx, BookListFilter{Language: c.language}, 100, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.language, err)
		}
		ids := []int64{}
		for _, b := range got {
			ids = append(ids, b.ID)
		}
		slices.Sort(ids)
		want := append([]int64{}, c.want...)
		slices.Sort(want)
		if !slices.Equal(ids, want) || total != len(want) {
			t.Errorf("language %q: got %v (total %d), want %v", c.language, ids, total, want)
		}
	}

	counts, err := books.LanguageCounts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []LanguageCount{{Code: "", Count: 2}, {Code: "eng", Count: 1}, {Code: "ita", Count: 1}}
	if !slices.Equal(counts, want) {
		t.Errorf("LanguageCounts = %+v, want %+v", counts, want)
	}
}
