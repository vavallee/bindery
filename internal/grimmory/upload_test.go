package grimmory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// grimmoryStub emulates the Grimmory endpoints the client touches: JWT login,
// refresh, and the BookDrop upload.
type grimmoryStub struct {
	t              *testing.T
	logins         atomic.Int32
	refreshes      atomic.Int32
	uploads        atomic.Int32
	rejectFirstJWT bool // force one 401 to exercise the re-auth retry
	sawFilename    string
	sawBody        string
}

func (g *grimmoryStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var creds map[string]string
		_ = json.NewDecoder(r.Body).Decode(&creds)
		if creds["username"] != "bindery" || creds["password"] != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.logins.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "jwt-1", "refreshToken": "refresh-1", "isDefaultPassword": false,
		})
	})
	mux.HandleFunc("POST /api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		g.refreshes.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken": "jwt-2", "refreshToken": "refresh-2",
		})
	})
	mux.HandleFunc("POST /api/v1/files/upload/bookdrop", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer jwt-1" && g.rejectFirstJWT {
			g.rejectFirstJWT = false
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth != "Bearer jwt-1" && auth != "Bearer jwt-2" && auth != "Bearer static-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			g.t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			g.t.Errorf("form file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		body, _ := io.ReadAll(f)
		g.sawFilename, g.sawBody = hdr.Filename, string(body)
		g.uploads.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "title": "Night Flights"})
	})
	return mux
}

func writeTempBook(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Night Flights.epub")
	if err := os.WriteFile(p, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploadBookDrop_JWTLoginFlow(t *testing.T) {
	stub := &grimmoryStub{t: t}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	c.WithCredentials("bindery", "s3cret")

	id, err := c.UploadBookDrop(context.Background(), writeTempBook(t))
	if err != nil {
		t.Fatalf("UploadBookDrop: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if stub.logins.Load() != 1 {
		t.Errorf("logins = %d, want 1", stub.logins.Load())
	}
	if stub.sawFilename != "Night Flights.epub" || stub.sawBody != "epub-bytes" {
		t.Errorf("upload carried %q/%q", stub.sawFilename, stub.sawBody)
	}

	// Second upload reuses the cached JWT — no second login.
	if _, err := c.UploadBookDrop(context.Background(), writeTempBook(t)); err != nil {
		t.Fatalf("second UploadBookDrop: %v", err)
	}
	if stub.logins.Load() != 1 {
		t.Errorf("logins after second push = %d, want 1 (token cached)", stub.logins.Load())
	}
}

func TestUploadBookDrop_RetriesOnceOn401(t *testing.T) {
	stub := &grimmoryStub{t: t, rejectFirstJWT: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	c.WithCredentials("bindery", "s3cret")

	if _, err := c.UploadBookDrop(context.Background(), writeTempBook(t)); err != nil {
		t.Fatalf("UploadBookDrop after 401: %v", err)
	}
	if stub.refreshes.Load() != 1 {
		t.Errorf("refreshes = %d, want 1 (401 forces re-auth via refresh)", stub.refreshes.Load())
	}
	if stub.uploads.Load() != 1 {
		t.Errorf("successful uploads = %d, want 1", stub.uploads.Load())
	}
}

func TestUploadBookDrop_StaticAPIKeySkipsLogin(t *testing.T) {
	stub := &grimmoryStub{t: t}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "static-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadBookDrop(context.Background(), writeTempBook(t)); err != nil {
		t.Fatalf("UploadBookDrop: %v", err)
	}
	if stub.logins.Load() != 0 {
		t.Errorf("logins = %d, want 0 (static key bypasses login)", stub.logins.Load())
	}
}

func TestUploadBookDrop_NoCredentials(t *testing.T) {
	stub := &grimmoryStub{t: t}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadBookDrop(context.Background(), writeTempBook(t)); err == nil {
		t.Fatal("expected ErrNoCredentials, got nil")
	}
}

func TestVerifyAuth_BadPassword(t *testing.T) {
	stub := &grimmoryStub{t: t}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	c.WithCredentials("bindery", "wrong")
	if err := c.VerifyAuth(context.Background()); err == nil {
		t.Fatal("expected login failure, got nil")
	}
}
