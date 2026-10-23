package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func absImportMultiLibraryHandler(stub *stubABSImporter) *ABSImportHandler {
	return NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:    "https://stored.example.com",
			APIKey:     "stored-secret",
			LibraryID:  "lib-a",
			LibraryIDs: []string{"lib-a", "lib-b", "lib-c"},
			Enabled:    true,
		}
	})
}

func postABSImport(h *ABSImportHandler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", bytes.NewBufferString(body)))
	return rec
}

func TestABSImportHandler_StartDefaultsToEveryConfiguredLibrary(t *testing.T) {
	t.Parallel()
	stub := &stubABSImporter{}
	rec := postABSImport(absImportMultiLibraryHandler(stub), `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if got := stub.lastCfg.LibraryIDs; len(got) != 3 {
		t.Fatalf("LibraryIDs = %v, want all three", got)
	}
	if stub.lastCfg.Full {
		t.Fatal("Full = true without being asked")
	}
}

func TestABSImportHandler_StartScopesToLibrarySubset(t *testing.T) {
	t.Parallel()
	stub := &stubABSImporter{}
	rec := postABSImport(absImportMultiLibraryHandler(stub), `{"libraryIds":["lib-c","lib-b"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	got := stub.lastCfg.LibraryIDs
	if len(got) != 2 || got[0] != "lib-b" || got[1] != "lib-c" {
		t.Fatalf("LibraryIDs = %v, want [lib-b lib-c] in configured order", got)
	}
	if stub.lastCfg.LibraryID != "lib-b" {
		t.Fatalf("LibraryID = %q, want lib-b", stub.lastCfg.LibraryID)
	}
}

func TestABSImportHandler_StartRejectsUnknownLibraryID(t *testing.T) {
	t.Parallel()
	stub := &stubABSImporter{}
	rec := postABSImport(absImportMultiLibraryHandler(stub), `{"libraryIds":["lib-a","lib-nope"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if stub.lastCfg.BaseURL != "" {
		t.Fatalf("importer was started with %+v, want no start on a rejected request", stub.lastCfg)
	}
}

func TestABSImportHandler_StartRejectsBlankLibraryIDs(t *testing.T) {
	t.Parallel()
	stub := &stubABSImporter{}
	rec := postABSImport(absImportMultiLibraryHandler(stub), `{"libraryIds":["  "]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestABSImportHandler_StartForwardsFull(t *testing.T) {
	t.Parallel()
	stub := &stubABSImporter{}
	rec := postABSImport(absImportMultiLibraryHandler(stub), `{"full":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if !stub.lastCfg.Full {
		t.Fatal("Full = false, want it forwarded")
	}
}
