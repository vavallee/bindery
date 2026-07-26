package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/importer"
)

// fakeReorganizeScanner is a stub implementing reorganizeScanner so the handler
// can be exercised without a real filesystem/import scanner.
type fakeReorganizeScanner struct {
	previewBook    []importer.ReorganizeMove
	previewAuthor  []importer.ReorganizeMove
	previewLibrary []importer.ReorganizeMove
	applied        []int64
	applyResult    []importer.ReorganizeMove
}

func (f *fakeReorganizeScanner) PreviewReorganizeBook(_ context.Context, _ int64) ([]importer.ReorganizeMove, error) {
	return f.previewBook, nil
}
func (f *fakeReorganizeScanner) PreviewReorganizeAuthor(_ context.Context, _ int64) ([]importer.ReorganizeMove, error) {
	return f.previewAuthor, nil
}
func (f *fakeReorganizeScanner) PreviewReorganizeLibrary(_ context.Context) ([]importer.ReorganizeMove, error) {
	return f.previewLibrary, nil
}
func (f *fakeReorganizeScanner) ApplyReorganize(_ context.Context, fileIDs []int64) []importer.ReorganizeMove {
	f.applied = fileIDs
	return f.applyResult
}

func TestReorganizePreview_ScopeValidation(t *testing.T) {
	h := NewReorganizeHandler(&fakeReorganizeScanner{})

	// Missing scope.
	rec := httptest.NewRecorder()
	h.Preview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reorganize/preview", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing scope: code = %d, want 400", rec.Code)
	}

	// book scope without id.
	rec = httptest.NewRecorder()
	h.Preview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reorganize/preview?scope=book", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("book scope no id: code = %d, want 400", rec.Code)
	}
}

func TestReorganizePreview_SummaryCounts(t *testing.T) {
	fake := &fakeReorganizeScanner{
		previewBook: []importer.ReorganizeMove{
			{FileID: 1, Status: importer.ReorgStatusMove},
			{FileID: 2, Status: importer.ReorgStatusNoop},
			{FileID: 3, Status: importer.ReorgStatusCollision},
		},
	}
	h := NewReorganizeHandler(fake)
	rec := httptest.NewRecorder()
	h.Preview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reorganize/preview?scope=book&id=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var resp reorganizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.Total != 3 || resp.Summary.ToMove != 1 || resp.Summary.Noop != 1 || resp.Summary.Collision != 1 {
		t.Errorf("summary = %+v", resp.Summary)
	}
}

func TestReorganizeApply(t *testing.T) {
	fake := &fakeReorganizeScanner{
		applyResult: []importer.ReorganizeMove{{FileID: 7, Status: importer.ReorgStatusMoved}},
	}
	h := NewReorganizeHandler(fake)

	// Empty body.
	rec := httptest.NewRecorder()
	h.Apply(rec, httptest.NewRequest(http.MethodPost, "/api/v1/reorganize/apply", bytes.NewBufferString(`{"fileIds":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty fileIds: code = %d, want 400", rec.Code)
	}

	// Valid apply.
	rec = httptest.NewRecorder()
	h.Apply(rec, httptest.NewRequest(http.MethodPost, "/api/v1/reorganize/apply", bytes.NewBufferString(`{"fileIds":[7]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(fake.applied) != 1 || fake.applied[0] != 7 {
		t.Errorf("applied = %v, want [7]", fake.applied)
	}
	var resp reorganizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.Moved != 1 {
		t.Errorf("summary.Moved = %d, want 1", resp.Summary.Moved)
	}
}
