package calibre

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestReader_Books_NormalizesLanguage: Calibre spells its three-letter codes
// the ISO 639-2/T way ("deu", "fra") where Bindery stores 639-2/B ("ger",
// "fre"). English is "eng" in both, so only a non-English library showed the
// difference: its books sat beside the same language from every other source
// under a second spelling (#3086).
func TestReader_Books_NormalizesLanguage(t *testing.T) {
	root := buildFixtureLibrary(t)
	raw, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT NOT NULL)`,
		`CREATE TABLE books_languages_link (
			id INTEGER PRIMARY KEY, book INTEGER NOT NULL,
			lang_code INTEGER NOT NULL, item_order INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO languages (id, lang_code) VALUES (1, 'deu'), (2, 'eng')`,
		`INSERT INTO books_languages_link (book, lang_code) VALUES (1, 1), (2, 2)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed languages: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(root)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer r.Close()

	got := map[int64]string{}
	if err := r.Books(context.Background(), func(b CalibreBook) error {
		got[b.CalibreID] = b.Language
		return nil
	}); err != nil {
		t.Fatalf("Books: %v", err)
	}
	for id, want := range map[int64]string{1: "ger", 2: "eng", 3: ""} {
		if got[id] != want {
			t.Errorf("book %d language = %q, want %q", id, got[id], want)
		}
	}
}
