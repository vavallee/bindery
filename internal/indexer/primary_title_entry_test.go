package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestPrimaryTitleFallbackReachesEveryEntrypoint runs the three production
// entry points against one fake indexer serving the Legion releases from
// #3268 next to the right one. The sibling rule needs the whole batch, so this
// also pins that every entry point judges the batch the same way.
func TestPrimaryTitleFallbackReachesEveryEntrypoint(t *testing.T) {
	right := "Brandon Sanderson - Legion - Skin Deep (2014) MP3"
	mainOnly := "Brandon Sanderson - Legion (2012) MP3"
	novella := "Brandon Sanderson - Legion Novella (2012) MP3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feedOf(mainOnly, right, novella)))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	crit := MatchCriteria{Title: "Legion: Skin Deep", Author: "Brandon Sanderson", MediaType: models.MediaTypeAudiobook}
	ctx := context.Background()

	plain := newTestSearcher().SearchBook(ctx, idxs, crit)
	debugged, dbg := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	outcomes, _ := newTestSearcher().SearchBookWithOutcomes(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBook":             resultTitles(plain),
		"SearchBookWithDebug":    resultTitles(debugged),
		"SearchBookWithOutcomes": resultTitles(outcomes),
	} {
		if len(got) != 1 || got[0] != right {
			t.Errorf("%s kept %v, want only %q", name, got, right)
		}
	}
	reasons := map[string]string{}
	for _, f := range dbg.Filters {
		reasons[f.Title] = f.Reason
	}
	for _, rel := range []string{mainOnly, novella} {
		if reasons[rel] != reasonMainTitleOnlyFullFound {
			t.Errorf("debug reason for %q = %q, want %q", rel, reasons[rel], reasonMainTitleOnlyFullFound)
		}
	}
}
