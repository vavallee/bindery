package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// reorgExternalEnv wires a real scanner over an in-memory DB so the tests
// observe the files on disk, not a fake (#3946).
type reorgExternalEnv struct {
	h        *ReorganizeHandler
	settings *db.SettingsRepo
	ebookID  int64 // tracked ebook file that Reorganize would move
	audioID  int64 // tracked audiobook folder that Reorganize would move
	ebookOld string
	audioOld string
}

func newReorgExternalEnv(t *testing.T) *reorgExternalEnv {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	libDir, audioDir := t.TempDir(), t.TempDir()
	books, authors := db.NewBookRepo(database), db.NewAuthorRepo(database)
	settings := db.NewSettingsRepo(database)
	s := importer.NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database), libDir, audioDir, "", "", "")
	s.WithSettings(settings)
	s.WithRootFolders(db.NewRootFolderRepo(database))
	s.WithSeriesRepo(db.NewSeriesRepo(database))

	a := &models.Author{ForeignID: "A-1", Name: "Jane Doe", SortName: "Doe", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	rel := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := func(title string) *models.Book {
		b := &models.Book{ForeignID: "OL-" + title, AuthorID: a.ID, Title: title, SortTitle: title,
			Status: models.BookStatusImported, Monitored: true, AnyEditionOK: true,
			MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", ReleaseDate: &rel}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	e := &reorgExternalEnv{h: NewReorganizeHandler(s), settings: settings}

	eb := seed("Ebook Title")
	e.ebookOld = filepath.Join(libDir, "placed-by-tool", "x.epub")
	if err := os.MkdirAll(filepath.Dir(e.ebookOld), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ebookOld, bytes.Repeat([]byte("book bytes"), 200), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, eb.ID, models.MediaTypeEbook, e.ebookOld); err != nil {
		t.Fatal(err)
	}

	ab := seed("Audio Title")
	e.audioOld = filepath.Join(audioDir, "unsorted", "audio")
	if err := os.MkdirAll(e.audioOld, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.audioOld, "part1.mp3"), bytes.Repeat([]byte("a"), 2000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, ab.ID, models.MediaTypeAudiobook, e.audioOld); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{eb.ID, ab.ID} {
		files, err := books.ListBookFiles(ctx, id)
		if err != nil || len(files) != 1 {
			t.Fatalf("files for %d: %v %v", id, files, err)
		}
		if id == eb.ID {
			e.ebookID = files[0].ID
		} else {
			e.audioID = files[0].ID
		}
	}
	return e
}

func (e *reorgExternalEnv) mode(t *testing.T, ebook, audiobook string) {
	t.Helper()
	ctx := context.Background()
	if ebook != "" {
		if err := e.settings.Set(ctx, "import.mode", ebook); err != nil {
			t.Fatal(err)
		}
	}
	if audiobook != "" {
		if err := e.settings.Set(ctx, "import.audiobook.mode", audiobook); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *reorgExternalEnv) preview(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	e.h.Preview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reorganize/preview?scope=library", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code = %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *reorgExternalEnv) apply(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.h.Apply(rec, httptest.NewRequest(http.MethodPost, "/api/v1/reorganize/apply", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func idList(ids ...int64) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

func formatsOf(m map[string]any) []string {
	raw, _ := m["externalFormats"].([]any)
	var out []string
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

func toMove(m map[string]any) float64 {
	return m["summary"].(map[string]any)["toMove"].(float64)
}

func TestReorganizePreview_ExternalModeFlag(t *testing.T) {
	cases := []struct {
		name         string
		ebook, audio string
		wantExternal bool
		wantFormats  []string
	}{
		{"ebooks external, audiobooks follow", "external", "", true, []string{"ebook", "audiobook"}},
		{"ebooks external, audiobooks copy", "external", "copy", true, []string{"ebook"}},
		{"ebooks copy, audiobooks external", "copy", "external", true, []string{"audiobook"}},
		{"copy", "copy", "", false, nil},
		{"move", "move", "", false, nil},
		{"hardlink", "hardlink", "", false, nil},
		{"auto", "auto", "", false, nil},
		{"unset", "", "", false, nil},
		{"ebooks external, audiobooks auto", "external", "auto", true, []string{"ebook"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newReorgExternalEnv(t)
			e.mode(t, c.ebook, c.audio)
			out := e.preview(t)
			// The plan itself is unchanged by the mode.
			if toMove(out) != 2 {
				t.Errorf("toMove = %v, want 2", toMove(out))
			}
			got, _ := out["externalMode"].(bool)
			if got != c.wantExternal {
				t.Fatalf("externalMode = %v, want %v (body %v)", got, c.wantExternal, out)
			}
			warning, _ := out["warning"].(string)
			if (warning != "") != c.wantExternal {
				t.Errorf("warning = %q, want non-empty=%v", warning, c.wantExternal)
			}
			gotF := formatsOf(out)
			if strings.Join(gotF, ",") != strings.Join(c.wantFormats, ",") {
				t.Errorf("externalFormats = %v, want %v", gotF, c.wantFormats)
			}
			if !fileExists(e.ebookOld) {
				t.Error("preview must not touch files")
			}
		})
	}
}

func TestReorganizeApply_ExternalMode(t *testing.T) {
	t.Run("refused with 409 and nothing moves", func(t *testing.T) {
		e := newReorgExternalEnv(t)
		e.mode(t, "external", "copy")
		code, out := e.apply(t, `{"fileIds":`+idList(e.ebookID, e.audioID)+`}`)
		if code != http.StatusConflict {
			t.Fatalf("code = %d, want 409 (%v)", code, out)
		}
		if out["code"] != "reorganize_external_mode" {
			t.Errorf("code field = %v", out["code"])
		}
		if !fileExists(e.ebookOld) || !fileExists(filepath.Join(e.audioOld, "part1.mp3")) {
			t.Error("a refused apply must leave every file where it was")
		}
	})
	t.Run("confirmed apply moves as before", func(t *testing.T) {
		e := newReorgExternalEnv(t)
		e.mode(t, "external", "copy")
		code, out := e.apply(t, `{"fileIds":`+idList(e.ebookID)+`,"confirmExternalMode":true}`)
		if code != http.StatusOK {
			t.Fatalf("code = %d (%v)", code, out)
		}
		if fileExists(e.ebookOld) {
			t.Error("confirmed apply should have moved the ebook")
		}
	})
	t.Run("managed format still reorganizes freely", func(t *testing.T) {
		e := newReorgExternalEnv(t)
		e.mode(t, "external", "copy")
		code, out := e.apply(t, `{"fileIds":`+idList(e.audioID)+`}`)
		if code != http.StatusOK {
			t.Fatalf("code = %d (%v)", code, out)
		}
		if fileExists(e.audioOld) {
			t.Error("audiobook in Copy mode should have moved without confirmation")
		}
		if !fileExists(e.ebookOld) {
			t.Error("ebook was not requested and must stay")
		}
	})
	for _, mode := range []string{"copy", "move", "hardlink", "auto", ""} {
		t.Run("managed mode "+mode+" needs no confirmation", func(t *testing.T) {
			e := newReorgExternalEnv(t)
			e.mode(t, mode, "")
			code, out := e.apply(t, `{"fileIds":`+idList(e.ebookID, e.audioID)+`}`)
			if code != http.StatusOK {
				t.Fatalf("code = %d (%v)", code, out)
			}
			if fileExists(e.ebookOld) {
				t.Error("ebook should have moved")
			}
		})
	}
	t.Run("audiobooks external refuses audiobook only", func(t *testing.T) {
		e := newReorgExternalEnv(t)
		e.mode(t, "copy", "external")
		if code, _ := e.apply(t, `{"fileIds":`+idList(e.audioID)+`}`); code != http.StatusConflict {
			t.Fatalf("audiobook apply code = %d, want 409", code)
		}
		if !fileExists(filepath.Join(e.audioOld, "part1.mp3")) {
			t.Error("audiobook must not move")
		}
		if code, _ := e.apply(t, `{"fileIds":`+idList(e.ebookID)+`}`); code != http.StatusOK {
			t.Fatalf("ebook apply code = %d, want 200", code)
		}
	})
}

// The routes stay admin only (#3946 does not touch the guard).
func TestReorganize_NonAdminForbidden(t *testing.T) {
	e := newReorgExternalEnv(t)
	e.mode(t, "external", "")
	r := chi.NewRouter()
	r.With(auth.RequireAdmin).Get("/reorganize/preview", e.h.Preview)
	r.With(auth.RequireAdmin).Post("/reorganize/apply", e.h.Apply)
	ctx := withAuthCtx(context.Background(), 42, "user")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, jsonReq(http.MethodGet, "/reorganize/preview?scope=library", "", ctx))
	if rec.Code != http.StatusForbidden {
		t.Errorf("preview code = %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonReq(http.MethodPost, "/reorganize/apply", `{"fileIds":[1],"confirmExternalMode":true}`, ctx))
	if rec.Code != http.StatusForbidden {
		t.Errorf("apply code = %d, want 403", rec.Code)
	}
	if !fileExists(e.ebookOld) {
		t.Error("file moved despite 403")
	}
}

// A confirmation that is not a JSON boolean is a malformed request, not a
// confirmation: it must 400 and leave the files alone.
func TestReorganizeApply_NonBooleanConfirmationRejected(t *testing.T) {
	for _, v := range []string{`"true"`, `1`, `null`, `"yes"`, `[]`} {
		t.Run(v, func(t *testing.T) {
			e := newReorgExternalEnv(t)
			e.mode(t, "external", "")
			code, _ := e.apply(t, `{"fileIds":`+idList(e.ebookID)+`,"confirmExternalMode":`+v+`}`)
			if v == `null` {
				// JSON null decodes to the zero value, so it is an absent confirmation: refused, not applied.
				if code != http.StatusConflict {
					t.Fatalf("null: code = %d, want 409", code)
				}
			} else if code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", code)
			}
			if !fileExists(e.ebookOld) {
				t.Error("file moved without a valid confirmation")
			}
		})
	}
}
