package importer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Review of #3649, case 1: SABnzbd category "books" with folder "books*"
// (jobs land straight in the shared category folder). The misc complete_dir
// lookup succeeds but the per category lookup fails (HTTP 500, transient).
// CompleteDir swallows that error and returns complete_dir, so the guard
// never learns about the shared category folder.
func TestUsenetJobExtras_SABCategoryLookupFailureKeepsFolder(t *testing.T) {
	completeDir := t.TempDir()
	category := filepath.Join(completeDir, "books")
	if err := os.MkdirAll(category, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"book.epub":         bookSized("epub"),
		"other-job.nfo":     []byte("nfo"),
		"Another Novel.txt": bookSized("a different job's plain text ebook, not imported yet"),
		"Another Novel.opf": []byte("<package/>"),
		"Another Novel.jpg": []byte("cover"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(category, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("mode") == "get_config" && q.Get("section") == "misc" {
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"misc": map[string]any{"complete_dir": completeDir}}})
			return
		}
		if q.Get("mode") == "get_config" && q.Get("section") == "categories" {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k", Category: "books"}

	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, s.usenetClientRoots(client, "books"))
	s.tryImportInternal(ctx, dl, filepath.Join(category, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(category, "Another Novel.txt"), "another job's .txt ebook in the shared category folder")
	assertKept(t, category, "the shared SABnzbd category folder")
}

// Case 2: the job sits in a category folder Bindery did not ask about
// (job recategorised in SABnzbd, or Bindery's category setting changed
// between grab and import). Roots cover "" and client.Category only.
func TestUsenetJobExtras_RecategorisedJobTxtBookKept(t *testing.T) {
	completeDir := t.TempDir()
	misc := filepath.Join(completeDir, "misc")
	if err := os.MkdirAll(misc, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"book.epub":   bookSized("epub"),
		"Notes.txt":   bookSized("someone else's text ebook"),
		"release.nfo": []byte("nfo"),
	} {
		if err := os.WriteFile(filepath.Join(misc, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir, filepath.Join(completeDir, "books")))
	s.tryImportInternal(ctx, dl, filepath.Join(misc, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(misc, "Notes.txt"), "a .txt ebook in a shared folder the guard did not know")
}

// Case 3: downloadPath is the file, so bookFiles holds only that file and a
// sibling .txt ebook (>= 4 KiB, which the importer itself treats as a book)
// in the same folder is not "unplaced": it is swept as a release extra.
func TestUsenetJobExtras_SingleFileSiblingTxtBookKept(t *testing.T) {
	completeDir, jobDir := usenetJob(t, map[string][]byte{
		"book.epub":   bookSized("epub"),
		"book.txt":    bookSized("plain text edition of the same book"),
		"release.nfo": []byte("nfo"),
	})
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))
	s.tryImportInternal(ctx, dl, filepath.Join(jobDir, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(jobDir, "book.txt"), "a sibling .txt book the walk never discovered")
}

// fakeSABCategories answers get_config like a SABnzbd whose complete_dir is
// completeDir and whose categories map name to folder.
func fakeSABCategories(t *testing.T, completeDir string, cats map[string]string) *models.DownloadClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("mode") == "get_config" && q.Get("section") == "misc":
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"misc": map[string]any{"complete_dir": completeDir}}})
		case q.Get("mode") == "get_config" && q.Get("section") == "categories":
			dir, ok := cats[q.Get("keyword")]
			if dir == "!500" {
				http.Error(w, "busy", http.StatusInternalServerError)
				return
			}
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "keyword not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"categories": []map[string]string{{"name": q.Get("keyword"), "dir": dir}}}})
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	host, port := scannerTestHostPort(t, srv.URL)
	return &models.DownloadClient{Type: "sabnzbd", Host: host, Port: port, APIKey: "k", Category: "books"}
}

// A job recategorised in SABnzbd to "misc", whose folder "misc*" takes jobs
// directly, sits in a folder the configured categories never name. The
// category SABnzbd reports for the job is asked about too, so the shared
// folder is refused even when everything in it is an extra.
func TestUsenetJobExtras_ReportedCategoryFolderNeverPruned(t *testing.T) {
	completeDir := t.TempDir()
	misc := filepath.Join(completeDir, "misc")
	if err := os.MkdirAll(misc, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"book.epub": bookSized("epub"), "other-job.nfo": []byte("nfo")} {
		if err := os.WriteFile(filepath.Join(misc, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	client := fakeSABCategories(t, completeDir, map[string]string{"*": "", "books": "books", "misc": "misc*"})

	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, s.usenetClientRoots(client, "misc"))
	s.tryImportInternal(ctx, dl, filepath.Join(misc, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(misc, "other-job.nfo"), "another job's extra in the reported category folder")
}

// Case 1 with nothing but extras in the shared folder, so only the strict
// category lookup can keep it: a failed per category lookup is a refusal,
// not a fallback to complete_dir.
func TestUsenetJobExtras_SABCategoryLookupFailureKeepsExtrasOnlyFolder(t *testing.T) {
	completeDir := t.TempDir()
	books := filepath.Join(completeDir, "books")
	if err := os.MkdirAll(books, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"book.epub": bookSized("epub"), "other-job.nfo": []byte("nfo")} {
		if err := os.WriteFile(filepath.Join(books, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	client := fakeSABCategories(t, completeDir, map[string]string{"*": "", "books": "!500"})

	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, s.usenetClientRoots(client, "books"))
	s.tryImportInternal(ctx, dl, filepath.Join(books, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(books, "other-job.nfo"), "another job's extra in the category folder")
}

// The real lookup still lets a job folder of its own go, and refuses when
// SABnzbd gave no category for the job or does not know it.
func TestUsenetJobExtras_SABRootsLookup(t *testing.T) {
	for name, tc := range map[string]struct {
		reported string
		wantGone bool
	}{
		"job in its own folder": {"books", true},
		"default category":      {"*", true},
		"category not reported": {"", false},
		"category SAB lacks":    {"gone", false},
	} {
		t.Run(name, func(t *testing.T) {
			completeDir, jobDir := extrasJob(t)
			client := fakeSABCategories(t, completeDir, map[string]string{"*": "", "books": "books"})
			s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
			ctx = withClientDownloadRoots(ctx, s.usenetClientRoots(client, tc.reported))
			s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

			assertImported(t, ctx, s, dl)
			if tc.wantGone {
				assertGone(t, jobDir, "job folder")
			} else {
				assertKept(t, filepath.Join(jobDir, "cto.nfo"), "extras when the job's category is unknown")
			}
		})
	}
}
