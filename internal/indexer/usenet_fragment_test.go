package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// fragmentPosts are per-chapter and per-part postings that the junk filter
// must drop (#3268, part 3). Grabbing one yields a few minutes of a book.
var fragmentPosts = []string{
	"Terry Goodkind - SOT06 Faith of the Fallen Ch63 of 71.mp3",
	"Terry Goodkind - SOT06 Faith of the Fallen Ch63.mp3",
	"Terry Goodkind - Faith of the Fallen Chapter 12 MP3",
	"Terry Goodkind - Faith of the Fallen Ch. 5 m4b",
	"Terry Goodkind - Faith of the Fallen Ch 5",
	"Author - Some Book - 12 of 40 (MP3)",
	"Author - Some Story 07 of 24",
	"Author - Some Book Part 03 of 12 m4b",
	"Author - Some Book (Part 3 of 12) FLAC",
	"Author - Some Book Chapter 1 of 30 ogg",
}

// fragmentKeepers are releases that look a little like fragments and are not.
var fragmentKeepers = []string{
	"Author - Catch 22 of Them MP3",
	"Author - 1 of a Kind MP3",
	"Author - Series Book 3 of 12 EPUB",
	"Author - Some Book (Series Book 3 of 10) (retail) (epub)",
	"Author - Some Book Book 3 of 12",
	"Author - Some Book Vol 2 of 10 MP3",
	"Author - Some Book Chapter 12 epub",
	"Author - Some Book 3 of 12 (retail) (epub)",
	"Author - Some Book Part 4 of 12 PDF",
	"Author - Catch22 MP3",
	"Author - Catch 22 (Unabridged) MP3",
	"Author - Some Book - Chapter One MP3",
	"Author - The Chronicles of Narnia (Unabridged) MP3",
	"Author - Sapiens 2014 MP3",
	"Author - Some Book 64 kbps MP3",
	"Author - Some Book (Unabridged) 12 hours MP3",
	"Terry Goodkind - Faith of the Fallen (Unabridged) M4B",
}

func TestUsenetJunkDropsPerChapterFragments(t *testing.T) {
	out := filterUsenetJunk(toResults(append(append([]string{}, fragmentPosts...), fragmentKeepers...)...), junkScope{})
	for _, j := range fragmentPosts {
		if contains(out, j) {
			t.Errorf("fragment slipped through: %q", j)
		}
	}
	for _, k := range fragmentKeepers {
		if !contains(out, k) {
			t.Errorf("keeper was dropped: %q", k)
		}
	}
}

func TestUsenetJunkDebugReasons(t *testing.T) {
	const old = "looks like a raw per-article posting (RAR/PAR2/SFV/yEnc/[N/M])"
	in := toResults(
		"Terry Goodkind - SOT06 Faith of the Fallen Ch63 of 71.mp3",
		"Post [1/5] - \"chunk\" yEnc",
		"Terry Goodkind - Faith of the Fallen (Unabridged) M4B",
	)
	kept, dropped := filterUsenetJunkDebug(in, junkScope{})
	if len(kept) != 1 || len(dropped) != 2 {
		t.Fatalf("kept=%d dropped=%d, want 1 and 2", len(kept), len(dropped))
	}
	byTitle := map[string]string{}
	for _, d := range dropped {
		if d.Stage != "usenet-junk" {
			t.Errorf("stage = %q, want usenet-junk", d.Stage)
		}
		byTitle[d.Title] = d.Reason
	}
	if got := byTitle["Terry Goodkind - SOT06 Faith of the Fallen Ch63 of 71.mp3"]; got != reasonChapterFragment {
		t.Errorf("fragment reason = %q, want %q", got, reasonChapterFragment)
	}
	if got := byTitle["Post [1/5] - \"chunk\" yEnc"]; got != old {
		t.Errorf("existing junk reason changed: %q", got)
	}
}

// TestUsenetJunkFragmentReachesEveryEntrypoint runs the production entry points
// against a fake indexer serving the Goodkind fragment next to the whole book.
func TestUsenetJunkFragmentReachesEveryEntrypoint(t *testing.T) {
	right := "Terry Goodkind - Faith of the Fallen (Unabridged) M4B"
	wrong := "Terry Goodkind - SOT06 Faith of the Fallen Ch63 of 71.mp3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feedOf(wrong, right)))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	crit := MatchCriteria{Title: "Faith of the Fallen", Author: "Terry Goodkind", MediaType: models.MediaTypeAudiobook}
	ctx := context.Background()

	plain := newTestSearcher().SearchBook(ctx, idxs, crit)
	debugged, dbg := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBook":          resultTitles(plain),
		"SearchBookWithDebug": resultTitles(debugged),
	} {
		if len(got) != 1 || got[0] != right {
			t.Errorf("%s kept %v, want only %q", name, got, right)
		}
	}
	if dbg.Pipeline.RawCount != 2 || dbg.Pipeline.AfterUsenetJunk != 1 {
		t.Errorf("pipeline raw=%d afterJunk=%d, want 2 and 1", dbg.Pipeline.RawCount, dbg.Pipeline.AfterUsenetJunk)
	}
}

// lonePartPosts are one slice of a set (#3417). Bindery cannot assemble several
// grabs, so each of these is a partial book.
var lonePartPosts = []string{
	"Graphic Audio - Some Book (Part 3 of 5) MP3",
	"Author - Some Book Part 3 of 5 m4b",
	"Author - Some Book Part 3 of 5",
	"Author - Some Book Pt 2 of 3 mp3",
	"Author - Some Book Pt. 2 of 3",
	"Author - Some Book Disc 2 of 4 MP3",
	"Author - Some Book CD 3 of 8",
	"Author - Some Book CD3 of 8 m4b",
	"Author - Some Book Part 1 of 2 MP3",
	"Author - Some Book (Part 2 of 2) M4B",
	"Author_Some_Book_Part_3_of_5_mp3",
	"Author - Some Book Part 4 of 12 FLAC",
}

// lonePartKeepers look a little like lone parts and are not, searched as an
// audiobook for "Some Book".
var lonePartKeepers = []string{
	"Author - Some Book Part 3 of 5 EPUB",
	"Author - Some Book Part 3 of 5 (retail) pdf",
	"Author - Some Book Book 3 of 5",
	"Author - Some Book Book 3 of 5 MP3",
	"Author - Some Book Vol 2 of 4 MP3",
	"Author - Some Book 3 of 5",
	"Author - Some Book 3 of 5 MP3",
	"Author - Some Book Part 1 of 1 MP3",
	"Author - Some Book Parts 1-5 of 5 MP3",
	"Author - Some Book Part 1-2 of 5 MP3",
	"Author - Some Book Parts 1 to 5 MP3",
	"Author - Some Book All 5 Parts MP3",
	"Author - Some Book Complete Audiobook Part 1 of 2 M4B",
	"Author - Some Book (Unabridged) M4B",
	"Author - Some Book Part One MP3",
	"Author - Party 3 of 5 MP3",
}

func TestUsenetJunkDropsLoneAudioParts(t *testing.T) {
	sc := junkScope{MediaType: models.MediaTypeAudiobook, WantedTitle: "Some Book"}
	out := filterUsenetJunk(toResults(append(append([]string{}, lonePartPosts...), lonePartKeepers...)...), sc)
	for _, j := range lonePartPosts {
		if contains(out, j) {
			t.Errorf("lone part slipped through: %q", j)
		}
	}
	for _, k := range lonePartKeepers {
		if !contains(out, k) {
			t.Errorf("keeper was dropped: %q", k)
		}
	}
}

// Without an audiobook search a post with no format is kept, while an audio
// container still marks it as a lone audio part.
func TestUsenetJunkLonePartNeedsAudioEvidence(t *testing.T) {
	noFormat := "Author - Some Book Part 3 of 5"
	withMP3 := "Author - Some Book Part 3 of 5 MP3"
	for name, sc := range map[string]junkScope{
		"free text": {},
		"ebook":     {MediaType: models.MediaTypeEbook, WantedTitle: "Some Book"},
	} {
		out := filterUsenetJunk(toResults(noFormat, withMP3), sc)
		if !contains(out, noFormat) {
			t.Errorf("%s: no format post was dropped", name)
		}
		if contains(out, withMP3) {
			t.Errorf("%s: mp3 lone part slipped through", name)
		}
	}
}

// A wanted book whose own title carries the phrase is not rejected for it.
func TestUsenetJunkLonePartKeepsWantedTitlePhrase(t *testing.T) {
	sc := junkScope{MediaType: models.MediaTypeAudiobook, WantedTitle: "Heist: Part 3 of 5"}
	own := "Author - Heist Part 3 of 5 MP3"
	other := "Author - Heist Part 2 of 5 MP3"
	out := filterUsenetJunk(toResults(own, other), sc)
	if !contains(out, own) {
		t.Errorf("wanted title's own phrase was dropped")
	}
	if contains(out, other) {
		t.Errorf("a different part of the same set slipped through")
	}
}

func TestUsenetJunkLonePartDebugReason(t *testing.T) {
	sc := junkScope{MediaType: models.MediaTypeAudiobook, WantedTitle: "Some Book"}
	kept, dropped := filterUsenetJunkDebug(toResults("Author - Some Book (Part 3 of 5) MP3", "Author - Some Book M4B"), sc)
	if len(kept) != 1 || len(dropped) != 1 {
		t.Fatalf("kept=%d dropped=%d, want 1 and 1", len(kept), len(dropped))
	}
	if dropped[0].Reason != reasonLoneAudioPart || dropped[0].Stage != StageUsenetJunk {
		t.Errorf("got stage %q reason %q", dropped[0].Stage, dropped[0].Reason)
	}
}

// The production entry points carry the media type and title into the filter.
func TestUsenetJunkLonePartReachesEveryEntrypoint(t *testing.T) {
	right := "Graphic Audio - Some Book (Unabridged) M4B"
	wrong := "Graphic Audio - Some Book (Part 3 of 5) MP3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feedOf(wrong, right)))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	crit := MatchCriteria{Title: "Some Book", Author: "Graphic Audio", MediaType: models.MediaTypeAudiobook}
	ctx := context.Background()

	plain := newTestSearcher().SearchBook(ctx, idxs, crit)
	debugged, dbg := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBook":          resultTitles(plain),
		"SearchBookWithDebug": resultTitles(debugged),
	} {
		if len(got) != 1 || got[0] != right {
			t.Errorf("%s kept %v, want only %q", name, got, right)
		}
	}
	found := false
	for _, f := range dbg.Filters {
		if f.Title == wrong && f.Reason == reasonLoneAudioPart {
			found = true
		}
	}
	if !found {
		t.Errorf("debug filters do not carry the lone part reason: %+v", dbg.Filters)
	}
}
