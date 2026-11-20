package soulseek

import (
	"math"
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

func TestReleasesCeilingKeepsLargestPlausibleFile(t *testing.T) {
	resp := []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`a\big.m4b`, maxFileBytes), file(`b\toobig.m4b`, maxFileBytes+1),
	}}}
	got := Releases("S", resp, "")
	if len(got) != 1 || got[0].Size != maxFileBytes {
		t.Errorf("got %+v", got)
	}
}
