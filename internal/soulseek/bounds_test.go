package soulseek

import (
	"math"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/slskd"
)

func TestReleasesBoundSizesAndDropDuplicates(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`d\1.mp3`, 100), file(`d\1.mp3`, 100), file(`d\2.mp3`, 50),
		file(`d\huge.mp3`, math.MaxInt64), file(`d\huge2.mp3`, math.MaxInt64),
		file(`d\neg.mp3`, -5),
	}}}
	got := Releases("S", resp, "")
	if len(got) != 1 {
		t.Fatalf("got %d releases", len(got))
	}
	r := got[0]
	if r.Size != 150 || r.Soulseek.FileCount != 2 {
		t.Errorf("size %d files %d, want 150 and 2 (duplicate counted once, absurd sizes dropped)", r.Size, r.Soulseek.FileCount)
	}
}

// A folder past what one download may hold is not offered, because a grab of it
// would be refused: the cap is the grab cap, not the 50 GiB search ceiling.
func TestReleasesDropFoldersPastTheGrabCaps(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`a\ok.m4b`, slskd.MaxGrabBytes), file(`b	oobig.m4b`, slskd.MaxGrabBytes+1), file(`c\ceiling.m4b`, maxFileBytes),
	}}}
	got := Releases("S", resp, "")
	if len(got) != 1 || got[0].Size != slskd.MaxGrabBytes {
		t.Fatalf("got %+v", got)
	}
	many := slskd.SearchResponse{Username: "v"}
	for i := 0; i <= slskd.MaxGrabFiles; i++ {
		many.Files = append(many.Files, file(`d\`+string(rune('a'+i%26))+strconv.Itoa(i)+`.mp3`, 10))
	}
	if got := Releases("S", []slskd.SearchResponse{many}, ""); len(got) != 0 {
		t.Errorf("a folder of %d files was offered", len(many.Files))
	}
}

// Every offered release carries an address that names exactly its own files, and
// a folder whose paths could escape is not offered at all.
func TestReleasesCarryAGrabAddressAndDropHostilePaths(t *testing.T) {
	resp := []slskd.SearchResponse{
		{Username: "alice", Files: []slskd.SearchFile{file(`Books\Dune\track1.mp3`, 10), file(`Books\Dune\track2.mp3`, 20)}},
		{Username: "mallory", Files: []slskd.SearchFile{file(`..\..\etc\evil.epub`, 100)}},
	}
	got := Releases("S", resp, "")
	if len(got) != 1 {
		t.Fatalf("got %d releases, want only alice's", len(got))
	}
	g, err := slskd.DecodeGrab(got[0].NZBURL)
	if err != nil {
		t.Fatal(err)
	}
	if g.User != "alice" || len(g.Files) != 2 || g.Files[0].Filename != `Books\Dune\track1.mp3` || g.Files[1].Size != 20 {
		t.Errorf("grab = %+v", g)
	}
}
