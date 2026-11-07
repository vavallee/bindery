package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
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
			if !slices.Equal(got.names(), wantAliceOnly) {
				t.Errorf("names = %v, want %v", got.names(), wantAliceOnly)
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
