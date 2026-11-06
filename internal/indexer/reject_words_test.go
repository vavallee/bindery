package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func rejectRun(words string, titles ...string) (kept []string, dropped []DroppedRelease) {
	var in []newznab.SearchResult
	for i, t := range titles {
		in = append(in, newznab.SearchResult{GUID: string(rune('a' + i)), Title: t, NZBURL: "https://fake/" + string(rune('a'+i))})
	}
	k, d := filterRelevantKeepDropped(in, "Dune", "Frank Herbert", nil,
		MatchCriteria{RejectWords: ParseRejectWords(words)}.context())
	for _, r := range k {
		kept = append(kept, r.Title)
	}
	return kept, d
}

func TestRejectWords_DroppedOnMatchKeptWithout(t *testing.T) {
	kept, dropped := rejectRun("Graphic Audio",
		"Frank Herbert - Dune (Graphic Audio) mp3",
		"Frank Herbert - Dune mp3")
	if len(kept) != 1 || kept[0] != "Frank Herbert - Dune mp3" {
		t.Fatalf("kept = %v", kept)
	}
	if len(dropped) != 1 || dropped[0].Result.GUID != "a" {
		t.Fatalf("dropped = %+v", dropped)
	}
	if dropped[0].Stage != StageRelevance || !strings.Contains(dropped[0].Reason, "rejected word") ||
		!strings.Contains(dropped[0].Reason, "Graphic Audio") {
		t.Errorf("stage/reason = %q/%q", dropped[0].Stage, dropped[0].Reason)
	}
}

func TestRejectWords_SingleWordAndList(t *testing.T) {
	kept, dropped := rejectRun("Full Cast, abridged\nunrelated words",
		"Frank Herbert - Dune FULL CAST",
		"Frank Herbert - Dune Abridged",
		"Frank Herbert - Dune")
	if len(kept) != 1 || len(dropped) != 2 {
		t.Fatalf("kept=%v dropped=%+v", kept, dropped)
	}
}

func TestRejectWords_NoMatchInsideLongerWord(t *testing.T) {
	kept, dropped := rejectRun("cast", "Frank Herbert - Dune Broadcast Edition", "Frank Herbert - Dune Podcast")
	if len(dropped) != 0 || len(kept) != 2 {
		t.Fatalf("a substring dropped a release: kept=%v dropped=%+v", kept, dropped)
	}
	kept, dropped = rejectRun("abridged", "Frank Herbert - Dune Unabridged")
	if len(dropped) != 0 || len(kept) != 1 {
		t.Fatalf("abridged dropped Unabridged: kept=%v dropped=%+v", kept, dropped)
	}
}

func TestRejectWords_SeparatorsAccentsAndCase(t *testing.T) {
	for _, title := range []string{
		"Frank.Herbert-Dune.Graphic.Audio.mp3",
		"Frank_Herbert_Dune_Graphic_Audio_mp3",
		"Frank Herbert - Dune [GRAPHIC-AUDIO]",
		"Frank Herbert - Dune graphic   audio",
	} {
		_, dropped := rejectRun("Graphic Audio", title)
		if len(dropped) != 1 {
			t.Errorf("%q was not dropped", title)
		}
	}
	for _, words := range []string{"full-cast", "FULL.CAST", "  Full   Cast "} {
		_, dropped := rejectRun(words, "Frank Herbert - Dune Full Cast")
		if len(dropped) != 1 {
			t.Errorf("setting %q did not match", words)
		}
	}
	_, dropped := rejectRun("Dramatisé", "Frank Herbert - Dune DRAMATISÉ")
	if len(dropped) != 1 {
		t.Error("accented word did not match")
	}
	_, dropped = rejectRun("Graphic Audio", "Frank Herbert - Dune Audio Graphic")
	if len(dropped) != 0 {
		t.Error("reordered words matched")
	}
}

func TestRejectWords_NoRegex(t *testing.T) {
	kept, dropped := rejectRun("c.st", "Frank Herbert - Dune cast")
	if len(dropped) != 0 || len(kept) != 1 {
		t.Fatalf("pattern characters were treated as a regex: dropped=%+v", dropped)
	}
	_, dropped = rejectRun(".*", "Frank Herbert - Dune")
	if len(dropped) != 0 {
		t.Fatalf(".* dropped a release: %+v", dropped)
	}
}

func TestRejectWords_EmptyChangesNothing(t *testing.T) {
	titles := []string{"Frank Herbert - Dune Graphic Audio", "Frank Herbert - Dune", "Totally Unrelated"}
	k1, d1 := rejectRun("", titles...)
	k2, d2 := rejectRun(" , \n ,", titles...)
	if len(ParseRejectWords(" , \n ,")) != 0 {
		t.Fatal("blank entries survived parsing")
	}
	if len(k1) != len(k2) || len(d1) != len(d2) || len(k1) != 2 {
		t.Fatalf("empty setting changed the outcome: %v/%d vs %v/%d", k1, len(d1), k2, len(d2))
	}
}

const rejectFeedRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item><title>Frank Herbert - Dune - retail epub</title><guid isPermaLink="false">ok-1</guid>
      <enclosure url="https://fake/dl/ok" length="1000" type="application/x-nzb"/></item>
    <item><title>Frank Herbert - Dune - Graphic Audio epub</title><guid isPermaLink="false">ga-1</guid>
      <enclosure url="https://fake/dl/ga?apikey=SECRET" length="2000" type="application/x-nzb"/></item>
  </channel>
</rss>`

func rejectIndexers(t *testing.T) []models.Indexer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(rejectFeedRSS))
	}))
	t.Cleanup(srv.Close)
	return []models.Indexer{{ID: 7, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
}

// Interactive and debug path: the reason is in Dropped and Filters, and the
// release stays grabbable by hand because it is a relevance stage drop.
func TestRejectWords_SearchDebugDropped(t *testing.T) {
	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert", RejectWords: ParseRejectWords("graphic audio")}
	results, dbg := newTestSearcher().SearchBookWithDebug(context.Background(), rejectIndexers(t), crit)
	if len(results) != 1 || results[0].GUID != "ok-1" {
		t.Fatalf("results = %+v", results)
	}
	if len(dbg.Dropped) != 1 || dbg.Dropped[0].Result.GUID != "ga-1" ||
		!strings.Contains(dbg.Dropped[0].Reason, "rejected word") {
		t.Fatalf("Dropped = %+v", dbg.Dropped)
	}
	if !strings.Contains(dbg.Dropped[0].Result.NZBURL, "apikey=SECRET") {
		t.Error("dropped release lost its download URL, so it could not be grabbed by hand")
	}
	var seen bool
	for _, f := range dbg.Filters {
		if f.Stage == StageRelevance && strings.Contains(f.Reason, "rejected word") {
			seen = true
		}
	}
	if !seen {
		t.Errorf("Filters = %+v", dbg.Filters)
	}
}

// Automatic path: SearchBook must honour the same setting.
func TestRejectWords_SearchBookAutomatic(t *testing.T) {
	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert", RejectWords: ParseRejectWords("graphic audio")}
	results := newTestSearcher().SearchBook(context.Background(), rejectIndexers(t), crit)
	if len(results) != 1 || results[0].GUID != "ok-1" {
		t.Fatalf("results = %+v", results)
	}
	crit.RejectWords = nil
	if got := newTestSearcher().SearchBook(context.Background(), rejectIndexers(t), crit); len(got) != 2 {
		t.Fatalf("without the setting results = %+v, want both", got)
	}
}
