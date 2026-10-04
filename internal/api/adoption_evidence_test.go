package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// listItems reads GET /library/unmatched as raw JSON, so a test can assert on
// the wire shape the web reads.
func (f adoptionFixture) listItems(t *testing.T) []map[string]any {
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
	return resp.Items
}

func (f adoptionFixture) seedAuthorBook(t *testing.T, authorName, title string) *models.Book {
	t.Helper()
	ctx := context.Background()
	a, err := f.authors.GetByForeignID(ctx, "ol:"+authorName)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		a = &models.Author{ForeignID: "ol:" + authorName, Name: authorName, SortName: authorName, MetadataProvider: "openlibrary"}
		if err := f.authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	b := &models.Book{ForeignID: "ol:b:" + title, AuthorID: a.ID, Title: title, Status: models.BookStatusWanted,
		Monitored: true, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAdoptionList_SurfacesAnAuthorConflict is #2942 on the wire: files that
// name Katy Evans in a James Patterson folder. The row says both authors, and
// a suggestion that is only the folder author's book says so, so the page can
// refuse to make it a one click adopt.
func TestAdoptionList_SurfacesAnAuthorConflict(t *testing.T) {
	f := newAdoptionFixture(t, &countingProvider{})
	folderBook := f.seedAuthorBook(t, "James Patterson", "Private Monaco")
	evansBook := f.seedAuthorBook(t, "Katy Evans", "Tycoon")
	p := f.write(t, "James Patterson/$10,000,000 Marriage Proposition/Katy Evans - Tycoon 1-7.mp3")
	f.seedUnit(t, db.UnmatchedUnitScan{
		UnitPath: p, Format: models.MediaTypeAudiobook, AuthorFolder: "James Patterson",
		ParsedTitle: "Tycoon", ParsedAuthor: "Katy Evans", FilesAuthor: "Katy Evans", Reason: "no_title_match", MemberPaths: []string{p},
		Candidates: []db.UnmatchedCandidate{{BookID: evansBook.ID, Score: 1}, {BookID: folderBook.ID, Score: 0.61, FolderAuthorOnly: true}},
	})

	items := f.listItems(t)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	conflict, ok := items[0]["authorConflict"].(map[string]any)
	if !ok {
		t.Fatalf("authorConflict = %v, want files and folder authors", items[0]["authorConflict"])
	}
	if conflict["files"] != "Katy Evans" || conflict["folder"] != "James Patterson" {
		t.Errorf("authorConflict = %v", conflict)
	}
	cands, _ := items[0]["candidates"].([]any)
	if len(cands) != 2 {
		t.Fatalf("candidates = %v", cands)
	}
	if c := cands[0].(map[string]any); c["folderAuthorOnly"] == true {
		t.Errorf("the files' own author's book is flagged as folder only: %v", c)
	}
	if c := cands[1].(map[string]any); c["folderAuthorOnly"] != true {
		t.Errorf("the folder author's book is not flagged: %v", c)
	}
}

// TestAdoptionList_StoredNarratorRowIsNotAConflict: a row a scan stored before
// conflicts were recorded, whose parsed author is the narrator from the Artist
// tag (Scott Brick) under a James Patterson folder. Nothing says the files name
// another author, so the page must not show a conflict or demote the 1.0
// suggestion.
func TestAdoptionList_StoredNarratorRowIsNotAConflict(t *testing.T) {
	f := newAdoptionFixture(t, &countingProvider{})
	b := f.seedAuthorBook(t, "James Patterson", "Kiss the Girls")
	p := f.write(t, "James Patterson/Kiss the Girls/01.mp3")
	f.seedUnit(t, db.UnmatchedUnitScan{
		UnitPath: p, Format: models.MediaTypeAudiobook, AuthorFolder: "James Patterson",
		ParsedTitle: "Kiss the Girls", ParsedAuthor: "Scott Brick", Reason: "no_title_match", MemberPaths: []string{p},
		Candidates: []db.UnmatchedCandidate{{BookID: b.ID, Score: 1}},
	})
	items := f.listItems(t)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	if c, present := items[0]["authorConflict"]; present && c != nil {
		t.Errorf("authorConflict = %v, want none", c)
	}
	if c := items[0]["candidates"].([]any)[0].(map[string]any); c["folderAuthorOnly"] == true {
		t.Errorf("the folder author's exact title is flagged: %v", c)
	}
}

// TestAdoptionList_NoConflictWhenTheAuthorsAgree:the same parsed and folder
// author, a comma inverted spelling of it, or a contributor list naming it, is
// not a conflict.
func TestAdoptionList_NoConflictWhenTheAuthorsAgree(t *testing.T) {
	for _, parsed := range []string{"James Patterson", "Patterson, James", "James Patterson, Peter Hermes - narrator", ""} {
		t.Run(parsed, func(t *testing.T) {
			f := newAdoptionFixture(t, &countingProvider{})
			b := f.seedAuthorBook(t, "James Patterson", "Private Monaco")
			p := f.write(t, "James Patterson/Private Monaca/track.mp3")
			f.seedUnit(t, db.UnmatchedUnitScan{
				UnitPath: p, Format: models.MediaTypeAudiobook, AuthorFolder: "James Patterson",
				ParsedTitle: "Private Monaca", ParsedAuthor: parsed, MemberPaths: []string{p},
				Candidates: []db.UnmatchedCandidate{{BookID: b.ID, Score: 0.95}},
			})
			items := f.listItems(t)
			if len(items) != 1 {
				t.Fatalf("items = %v", items)
			}
			if c, present := items[0]["authorConflict"]; present && c != nil {
				t.Errorf("authorConflict = %v, want none", c)
			}
			if c := items[0]["candidates"].([]any)[0].(map[string]any); c["folderAuthorOnly"] == true {
				t.Errorf("candidate flagged without a conflict: %v", c)
			}
		})
	}
}

// TestAdoptionList_FolderOnlyLookAlikeDoesNotSetTheTopScore: the score a
// conflicting row sorts by is its best suggestion by the files' author, not a
// folder author's look alike (#2942).
func TestAdoptionList_FolderOnlyLookAlikeDoesNotSetTheTopScore(t *testing.T) {
	f := newAdoptionFixture(t, &countingProvider{})
	folderBook := f.seedAuthorBook(t, "James Patterson", "Katt vs. Dogg")
	p := f.write(t, "James Patterson/X/Katy Evans - Tycoon 1-7.mp3")
	f.seedUnit(t, db.UnmatchedUnitScan{
		UnitPath: p, Format: models.MediaTypeAudiobook, AuthorFolder: "James Patterson",
		ParsedTitle: "Tycoon", ParsedAuthor: "Katy Evans", FilesAuthor: "Katy Evans", MemberPaths: []string{p},
		Candidates: []db.UnmatchedCandidate{{BookID: folderBook.ID, Score: 0.8, FolderAuthorOnly: true}},
	})
	items := f.listItems(t)
	if len(items) != 1 || items[0]["topScore"] != float64(0) {
		t.Fatalf("topScore = %v, want 0", items[0]["topScore"])
	}
}
