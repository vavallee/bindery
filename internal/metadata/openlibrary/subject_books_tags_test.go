package openlibrary

import (
	"context"
	"slices"
	"testing"
)

// TestGetSubjectBooks_HTTP_DropsMachineTagSubjects covers the genre popular
// recommendation path, which decodes subjectWork.Subject.
func TestGetSubjectBooks_HTTP_DropsMachineTagSubjects(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/science_fiction.json": jsonStr(map[string]any{
			"name": "science_fiction",
			"works": []map[string]any{
				{"key": "/works/OL1W", "title": "Red Rising", "subject": junkSubjects},
			},
		}),
	})
	cands, err := c.GetSubjectBooks(context.Background(), "science_fiction", 5)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if len(cands) != 1 || !slices.Equal(cands[0].Genres, wantGenres) {
		t.Fatalf("candidates = %+v, want one with genres %q", cands, wantGenres)
	}
}
