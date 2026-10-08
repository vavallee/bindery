package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// TestAdoptionListCoverageScanStatus: the unmatched list carries the last
// scan's summary when one is stored, ignores a corrupt blob rather than
// failing the page, includes facets only on request, and answers 500 when the
// repo cannot be read.
func TestAdoptionListCoverageScanStatus(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{})
	f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: filepath.Join(f.lib, "Loose", "book.epub"), MemberPaths: []string{filepath.Join(f.lib, "Loose", "book.epub")}})
	settings := db.NewSettingsRepo(f.db)

	list := func(query string) (*httptest.ResponseRecorder, adoptionListResponse) {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/unmatched"+query, nil))
		var out adoptionListResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	rec, got := list("")
	if rec.Code != http.StatusOK || got.Scan.Ran || got.Total != 1 || got.Facets != nil {
		t.Fatalf("no scan stored: %d scan=%+v total=%d facets=%v", rec.Code, got.Scan, got.Total, got.Facets)
	}

	blob := `{"ran_at":"2026-10-01T10:00:00Z","files_found":12,"units_truncated":true,"scan_error":"root missing","no_files_found":false}`
	if err := settings.Set(t.Context(), SettingLibraryLastScan, blob); err != nil {
		t.Fatal(err)
	}
	rec, got = list("?facets=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("with scan: %d %s", rec.Code, rec.Body.String())
	}
	want := adoptionScanStatus{Ran: true, RanAt: "2026-10-01T10:00:00Z", FilesFound: 12, Truncated: true, Error: "root missing"}
	if got.Scan != want {
		t.Fatalf("scan = %+v, want %+v", got.Scan, want)
	}
	if got.Facets == nil {
		t.Fatal("facets=1 returned no facets")
	}

	if err := settings.Set(t.Context(), SettingLibraryLastScan, `{not json`); err != nil {
		t.Fatal(err)
	}
	if rec, got = list(""); rec.Code != http.StatusOK || got.Scan.Ran {
		t.Fatalf("corrupt blob: %d scan=%+v, want 200 with ran=false", rec.Code, got.Scan)
	}

	f.db.Close()
	if rec, _ := list(""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("repo error: %d, want 500", rec.Code)
	}
}

// TestAdoptionSummaryCoverage: the nav badge counts pending units and files
// and carries the scan status; a repo failure is a 500.
func TestAdoptionSummaryCoverage(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{})
	a := filepath.Join(f.lib, "A", "one.epub")
	b := filepath.Join(f.lib, "B", "two.epub")
	f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: a, MemberPaths: []string{a}})
	f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: b, MemberPaths: []string{b}})
	if err := db.NewSettingsRepo(f.db).Set(t.Context(), SettingLibraryLastScan, `{"ran_at":"2026-10-02T00:00:00Z","files_found":2}`); err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/unmatched/summary", nil))
		return rec
	}
	rec := get()
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	var got adoptionSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Pending != 2 || got.PendingFiles != 2 || got.Adopted != 0 || !got.Scan.Ran || got.Scan.FilesFound != 2 {
		t.Fatalf("summary = %+v", got)
	}
	f.db.Close()
	if rec := get(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("repo error: %d, want 500", rec.Code)
	}
}
