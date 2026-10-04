package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// #2944: a 1008 byte .txt under the audiobooks root was offered for adoption
// as the ebook of the book its folder named, and adopting it made a 1 KB text
// file that book's ebook.

func (f adoptionFixture) writeSized(t *testing.T, rel string, size int) string {
	t.Helper()
	p := filepath.Join(f.lib, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("a"), size), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAdopt_RefusesAFileTooSmallToBeABook: the API is a guard of its own, not
// only the list. A row stored before the scan learned to label such files, or
// a request made by hand, is refused with the reason and changes nothing.
func TestAdopt_RefusesAFileTooSmallToBeABook(t *testing.T) {
	f := newAdoptionFixture(t, nil)
	p := f.writeSized(t, "James Patterson/Die 6. Geisel ()/Die 6. Geisel.txt", 1008)
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: p, MemberPaths: []string{p}, ParsedTitle: "Die 6. Geisel"})
	book := f.seedBook(t, "Die 6. Geisel")

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("adopt = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too small to be a book") {
		t.Errorf("refusal %s does not say why", rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Errorf("book files = %v, want none", got)
	}
	u, err := f.units.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if u.State != db.UnmatchedStatePending {
		t.Errorf("unit state = %s, want pending", u.State)
	}
}

// TestAdopt_ShortRealEbookStillAdopts: the floor is far below a real book. A
// 30 KB novella adopts as before.
func TestAdopt_ShortRealEbookStillAdopts(t *testing.T) {
	f := newAdoptionFixture(t, nil)
	p := f.writeSized(t, "Ann Leckie/Novella/Novella.epub", 30<<10)
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: p, MemberPaths: []string{p}, ParsedTitle: "Novella"})
	book := f.seedBook(t, "Novella")

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 || got[0] != p {
		t.Errorf("book files = %v, want [%s]", got, p)
	}
}

// listRootFormats lists the pending rows and returns each row's rootFormat by
// path, read from the raw JSON so a missing field reads as "".
func listRootFormats(t *testing.T, f adoptionFixture) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/unmatched", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(resp.Items))
	for _, it := range resp.Items {
		rf, _ := it["rootFormat"].(string)
		out[it["relPath"].(string)] = rf
	}
	return out
}

// TestAdoptionList_SaysWhichRootAFileIsIn: an ebook found under a separate
// audiobooks root is labelled with that root, so the row cannot pass for an
// ordinary ebook. With one combined root there is nothing to say.
func TestAdoptionList_SaysWhichRootAFileIsIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		combined bool
		want     string
	}{
		{name: "separate audiobooks root", want: models.MediaTypeAudiobook},
		{name: "combined root", combined: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdoptionFixture(t, nil)
			// The fixture's library root plays the audiobooks root.
			ebookRoot := t.TempDir()
			if tc.combined {
				ebookRoot = f.lib
			}
			f.h.scanner = importer.NewScanner(nil, nil, nil, nil, nil, ebookRoot, f.lib, "", "", "")
			p := f.writeSized(t, "James Patterson/Die 6. Geisel/Die 6. Geisel.epub", 30<<10)
			f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: p, MemberPaths: []string{p}, RelPath: "James Patterson/Die 6. Geisel/Die 6. Geisel.epub"})

			got := listRootFormats(t, f)
			if rf, ok := got["James Patterson/Die 6. Geisel/Die 6. Geisel.epub"]; !ok || rf != tc.want {
				t.Errorf("rootFormat = %q (listed %v), want %q", rf, ok, tc.want)
			}
		})
	}
}
