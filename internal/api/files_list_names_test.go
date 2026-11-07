package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

type listNamesResp struct {
	Files []struct {
		Name      string `json:"name"`
		SizeBytes int64  `json:"sizeBytes"`
	} `json:"files"`
	Truncated bool `json:"truncated"`
}

func listNames(t *testing.T, fx *zipOtherBookFixture, bookID int64, path string, user int64, role string) (int, listNamesResp) {
	t.Helper()
	h := NewFileHandler(fx.env.books, fx.root)
	rec := httptest.NewRecorder()
	h.ListFileNames(rec, covAuRequest(http.MethodGet, "/?path="+url.QueryEscape(path), "", map[string]string{"id": covAuID(bookID)}, user, role))
	var out listNamesResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

func (r listNamesResp) names() []string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Name)
	}
	return out
}

// TestListFileNamesListsEveryFileOfAnAudiobookFolder: every part of a
// multi-file audiobook is named, with subfolders, and what another book tracks
// inside the folder is left out, as in the zip download.
func TestListFileNamesListsEveryFileOfAnAudiobookFolder(t *testing.T) {
	for _, tenancy := range []bool{true, false} {
		t.Run("tenancy="+strconv.FormatBool(tenancy), func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tenancy)
			fx := newZipOtherBookFixture(t)
			code, got := listNames(t, fx, fx.aliceBook.ID, fx.folder, fx.env.alice, "user")
			if code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}
			want := []string{"cover.jpg", "Disc 2/part2.mp3", "part1.mp3"}
			if !slices.Equal(got.names(), want) {
				t.Errorf("names = %v, want %v", got.names(), want)
			}
			if got.Truncated {
				t.Error("truncated = true for a small folder")
			}
		})
	}
}

// A tracked single file lists as itself.
func TestListFileNamesSingleFile(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	epub := filepath.Join(fx.folder, "Shared Title - Jane Doe.epub")
	code, got := listNames(t, fx, fx.bobBook.ID, epub, fx.env.bob, "user")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if want := []string{"Shared Title - Jane Doe.epub"}; !slices.Equal(got.names(), want) {
		t.Errorf("names = %v, want %v", got.names(), want)
	}
}

// The path only selects among the book's own rows, and another user's book is
// a 404.
func TestListFileNamesRefusesUntrackedPathAndOtherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	fx := newZipOtherBookFixture(t)
	if code, _ := listNames(t, fx, fx.aliceBook.ID, filepath.Dir(fx.folder), fx.env.alice, "user"); code != http.StatusNotFound {
		t.Errorf("untracked path = %d, want 404", code)
	}
	if code, _ := listNames(t, fx, fx.aliceBook.ID, "", fx.env.alice, "user"); code != http.StatusBadRequest {
		t.Errorf("missing path = %d, want 400", code)
	}
	if code, _ := listNames(t, fx, fx.aliceBook.ID, fx.folder, fx.env.bob, "user"); code != http.StatusNotFound {
		t.Errorf("other user's book = %d, want 404", code)
	}
}

// Parts sort in natural order, so part2 comes before part10 and a 12 part book
// reads 1 to 12.
func TestListFileNamesNaturalOrder(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	dir := filepath.Join(fx.root, "Jane Doe", "Twelve Parts")
	var want []string
	for i := 1; i <= 12; i++ {
		name := "Part " + strconv.Itoa(i) + ".mp3"
		mustWrite(t, filepath.Join(dir, name))
		want = append(want, name)
	}
	mustWrite(t, filepath.Join(dir, "cover.jpg"))
	if err := fx.env.books.AddBookFile(fx.env.ctx, fx.aliceBook.ID, models.MediaTypeAudiobook, dir); err != nil {
		t.Fatal(err)
	}
	_, got := listNames(t, fx, fx.aliceBook.ID, dir, fx.env.alice, "user")
	want = append([]string{"cover.jpg"}, want...)
	if !slices.Equal(got.names(), want) {
		t.Errorf("names = %v, want %v", got.names(), want)
	}
}

func TestNaturalLess(t *testing.T) {
	in := []string{"part10.mp3", "Part2.mp3", "part1.mp3", "Disc 2/a.mp3", "Disc 10/a.mp3", "b.mp3", "B10.mp3", "b9.mp3"}
	want := []string{"b.mp3", "b9.mp3", "B10.mp3", "Disc 2/a.mp3", "Disc 10/a.mp3", "part1.mp3", "Part2.mp3", "part10.mp3"}
	sort.Slice(in, func(i, j int) bool { return naturalLess(in[i], in[j]) })
	if !slices.Equal(in, want) {
		t.Errorf("sorted = %v, want %v", in, want)
	}
}

// Past the cap the first maxListedFiles in the displayed natural order are
// kept, not whichever the directory walk met first.
func TestListFileNamesTruncatesInDisplayOrder(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	dir := filepath.Join(fx.root, "Jane Doe", "Many Parts")
	for i := 1; i <= maxListedFiles+20; i++ {
		mustWrite(t, filepath.Join(dir, "p"+strconv.Itoa(i)+".mp3"))
	}
	if err := fx.env.books.AddBookFile(fx.env.ctx, fx.aliceBook.ID, models.MediaTypeAudiobook, dir); err != nil {
		t.Fatal(err)
	}
	_, got := listNames(t, fx, fx.aliceBook.ID, dir, fx.env.alice, "user")
	if !got.Truncated || len(got.Files) != maxListedFiles {
		t.Fatalf("len = %d truncated = %v", len(got.Files), got.Truncated)
	}
	for i, f := range got.Files {
		if want := "p" + strconv.Itoa(i+1) + ".mp3"; f.Name != want {
			t.Fatalf("entry %d = %q, want %q", i, f.Name, want)
		}
	}
}

// A folder with more than maxListedFiles files is cut off and flagged.
func TestListFileNamesTruncates(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	for i := 0; i < maxListedFiles+5; i++ {
		mustWrite(t, filepath.Join(fx.folder, "bulk", "f"+strconv.Itoa(i)+".mp3"))
	}
	_, got := listNames(t, fx, fx.aliceBook.ID, fx.folder, fx.env.alice, "user")
	if len(got.Files) != maxListedFiles || !got.Truncated {
		t.Errorf("len = %d truncated = %v, want %d true", len(got.Files), got.Truncated, maxListedFiles)
	}
}
