package soulseek

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

func file(name string, size int64) slskd.SearchFile {
	return slskd.SearchFile{Filename: name, Size: size}
}

func TestReleasesGroupsByUserFolderAndFormat(t *testing.T) {
	resp := []slskd.SearchResponse{
		{Username: "alice", HasFreeUploadSlot: true, UploadSpeed: 500, Files: []slskd.SearchFile{
			file(`Music\Frank Herbert\Dune\Dune.epub`, 900),
			file(`Music\Frank Herbert\Dune\Dune.pdf`, 4000),
			file(`Music\Frank Herbert\Dune\cover.jpg`, 50),
			file(`Music\Frank Herbert\Dune\readme.txt`, 5),
		}},
		{Username: "bob", QueueLength: 7, Files: []slskd.SearchFile{
			file(`Audio\Dune\01.mp3`, 10), file(`Audio\Dune\02.mp3`, 20), file(`Audio\Dune\03.mp3`, 30),
		}},
	}
	got := Releases("Soulseek", resp, "")
	if len(got) != 3 {
		t.Fatalf("got %d releases, want 3 (alice epub, alice pdf, bob mp3): %+v", len(got), got)
	}
	by := map[string]int{}
	for _, r := range got {
		by[r.Soulseek.Username+"/"+r.Soulseek.Format]++
		if r.IndexerID != nil {
			t.Errorf("IndexerID = %v, want nil", *r.IndexerID)
		}
		if r.Protocol != Protocol || !strings.HasPrefix(r.GUID, GUIDPrefix) || r.NZBURL != "" {
			t.Errorf("release shape wrong: %+v", r)
		}
	}
	if by["alice/epub"] != 1 || by["alice/pdf"] != 1 || by["bob/mp3"] != 1 {
		t.Errorf("groups = %v", by)
	}
	// alice has a free slot, so her releases lead bob's queued one.
	if got[len(got)-1].Soulseek.Username != "bob" {
		t.Errorf("queued peer not last: %+v", got[len(got)-1].Soulseek)
	}
	for _, r := range got {
		if r.Soulseek.Username == "bob" {
			if r.Size != 60 || r.Soulseek.FileCount != 3 || r.MediaType != models.MediaTypeAudiobook || r.Soulseek.QueueLength != 7 {
				t.Errorf("bob release = %+v", r)
			}
			if r.Title != "Dune [MP3]" {
				t.Errorf("folder release title = %q", r.Title)
			}
		}
	}
}

func TestReleasesDropsNonBookAndEmptyFiles(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`a\song.flac`, 10), file(`a\notes.txt`, 10), file(`a\x.zip`, 10), file(`a\x.exe`, 10),
		file(`a\Book.epub`, 0), file(`a\NoExt`, 10), file(`a\sheet.cue`, 10),
	}}}
	if got := Releases("S", resp, ""); len(got) != 0 {
		t.Fatalf("kept non book files: %+v", got)
	}
}

func TestReleasesMediaTypeFilter(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`d\Book.epub`, 10), file(`e\Book\01.m4b`, 10),
	}}}
	if got := Releases("S", resp, models.MediaTypeEbook); len(got) != 1 || got[0].Soulseek.Format != "epub" {
		t.Errorf("ebook filter: %+v", got)
	}
	if got := Releases("S", resp, models.MediaTypeAudiobook); len(got) != 1 || got[0].Soulseek.Format != "m4b" {
		t.Errorf("audiobook filter: %+v", got)
	}
	if got := Releases("S", resp, ""); len(got) != 2 {
		t.Errorf("no filter: %+v", got)
	}
}

func TestReleasesNeverInventsMetadata(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`x\Frank Herbert - Dune.epub`, 10),
		file(`y\book1.epub`, 10),
	}}}
	got := Releases("S", resp, "")
	for _, r := range got {
		switch {
		case strings.Contains(r.Title, "Dune"):
			if r.Author != "Frank Herbert" && r.Author != "Dune" && r.Author != "" {
				t.Errorf("author = %q", r.Author)
			}
		case strings.Contains(r.Title, "book1"):
			if r.Author != "" {
				t.Errorf("author invented for an unparseable name: %q", r.Author)
			}
		}
	}
}

func TestReleaseGUIDStableAndPerPeer(t *testing.T) {
	a := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{file(`d\1.mp3`, 1), file(`d\2.mp3`, 1)}}}
	b := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{file(`d\2.mp3`, 1), file(`d\1.mp3`, 1)}}}
	c := []slskd.SearchResponse{{Username: "other", Files: []slskd.SearchFile{file(`d\1.mp3`, 1), file(`d\2.mp3`, 1)}}}
	ga, gb, gc := Releases("S", a, "")[0].GUID, Releases("S", b, "")[0].GUID, Releases("S", c, "")[0].GUID
	if ga != gb {
		t.Error("GUID depends on file order")
	}
	if ga == gc {
		t.Error("two peers' copies share a GUID")
	}
}

func TestReleasesSanitisesPeerText(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "evil\x00user\u202e", Files: []slskd.SearchFile{
		file("d\\Bo\x07ok\u202e \n\t  name.epub", 10),
		file("d\\"+strings.Repeat("A", 5000)+".pdf", 10),
	}}}
	for _, r := range Releases("S", resp, "") {
		for _, s := range []string{r.Title, r.Soulseek.Username, r.Author, r.BookTitle} {
			for _, c := range s {
				if c < 0x20 || c == 0x7f || c == 0x202e {
					t.Errorf("control or bidi character left in %q", s)
				}
			}
		}
		if len([]rune(r.Title)) > maxTitleRunes+10 {
			t.Errorf("title not capped: %d runes", len([]rune(r.Title)))
		}
	}
}

func TestReleasesCapped(t *testing.T) {
	var resp []slskd.SearchResponse
	for i := 0; i < maxReleases+40; i++ {
		resp = append(resp, slskd.SearchResponse{Username: "u" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Files: []slskd.SearchFile{file(`d\B.epub`, 5)}})
	}
	if got := Releases("S", resp, ""); len(got) != maxReleases {
		t.Errorf("got %d releases, want cap %d", len(got), maxReleases)
	}
}

func TestSearchText(t *testing.T) {
	cases := []struct {
		q    indexer.SourceQuery
		want string
	}{
		{indexer.SourceQuery{Title: "Dune: Messiah", Author: "Frank Herbert"}, "Dune Herbert"},
		{indexer.SourceQuery{Title: "-Dune's Heir!", Author: ""}, "Dunes Heir"},
		{indexer.SourceQuery{Title: "One Two Three Four Five Six Seven Eight", Author: "A B"}, "One Two Three Four Five Six B"},
		{indexer.SourceQuery{Text: "  free -text  "}, "free text"},
		{indexer.SourceQuery{}, ""},
	}
	for _, c := range cases {
		if got := SearchText(c.q); got != c.want {
			t.Errorf("SearchText(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

type fakeSearcher struct {
	text string
	resp []slskd.SearchResponse
	err  error
	n    int
}

func (f *fakeSearcher) Search(_ context.Context, text string) ([]slskd.SearchResponse, error) {
	f.text = text
	f.n++
	return f.resp, f.err
}

func TestSourceSearch(t *testing.T) {
	f := &fakeSearcher{resp: []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{file(`d\Dune.epub`, 10)}}}}
	s := &Source{name: "My slskd", client: f}
	got, err := s.Search(context.Background(), indexer.SourceQuery{Title: "Dune", Author: "Frank Herbert", MediaType: models.MediaTypeEbook})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.text != "Dune Herbert" || got[0].IndexerName != "My slskd" || s.Name() != "My slskd" {
		t.Errorf("text=%q name=%q", f.text, got[0].IndexerName)
	}
	// Nothing to search for sends nothing.
	if _, err := s.Search(context.Background(), indexer.SourceQuery{}); err != nil || f.n != 1 {
		t.Errorf("empty query reached slskd (calls=%d, err=%v)", f.n, err)
	}
	f.err = errors.New("boom")
	if _, err := s.Search(context.Background(), indexer.SourceQuery{Text: "x"}); err == nil {
		t.Error("error swallowed")
	}
}
