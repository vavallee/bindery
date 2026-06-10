package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractDiscNumber(t *testing.T) {
	cases := []struct {
		relDir string
		want   int
	}{
		{"Disc 1", 1},
		{"Disc 01", 1},
		{"Disk 02", 2},
		{"CD 3", 3},
		{"CD3", 3},
		{"Part 4", 4},
		{"Volume 2", 2},
		{"Vol. 3", 3},
		{"Disc 1 - The Beginning", 1},
		{filepath.Join("Audiobook", "Disc 2"), 2},
		{"", 0},
		{".", 0},
		{"NotADisc", 0},
		{"Bonus", 0},
	}
	for _, tc := range cases {
		if got := extractDiscNumber(tc.relDir); got != tc.want {
			t.Errorf("extractDiscNumber(%q) = %d, want %d", tc.relDir, got, tc.want)
		}
	}
}

func TestExtractTrackNumber(t *testing.T) {
	cases := []struct {
		base string
		want int
	}{
		{"Track 01.mp3", 1},
		{"Track 12.mp3", 12},
		{"Chapter 02.mp3", 2},
		{"01 - Title.mp3", 1},
		{"05 Title.flac", 5},
		{"1-05 - Title.mp3", 5},
		{"1.05 Title.m4a", 5},
		{"cover.jpg", 0},
		{"book.m4b", 0},
		{"Title Only.mp3", 0},
	}
	for _, tc := range cases {
		if got := extractTrackNumber(tc.base); got != tc.want {
			t.Errorf("extractTrackNumber(%q) = %d, want %d", tc.base, got, tc.want)
		}
	}
}

// buildMultiDiscTree creates Disc 1/Disc 2 folders with tracks (in a
// deliberately non-sorted directory write order) plus a root cover.jpg.
func buildMultiDiscTree(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join("Disc 2", "Track 01.mp3"), "d2t1")
	mk(filepath.Join("Disc 2", "Track 02.mp3"), "d2t2")
	mk(filepath.Join("Disc 1", "Track 02.mp3"), "d1t2")
	mk(filepath.Join("Disc 1", "Track 01.mp3"), "d1t1")
	mk("cover.jpg", "img")
	return root
}

func TestIsMultiDiscAudiobook(t *testing.T) {
	multi := buildMultiDiscTree(t)
	if !isMultiDiscAudiobook(multi) {
		t.Error("multi-disc tree not detected as multi-disc")
	}

	// Single disc folder → not multi-disc.
	single := t.TempDir()
	if err := os.MkdirAll(filepath.Join(single, "Disc 1"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Track 01.mp3", "Track 02.mp3"} {
		if err := os.WriteFile(filepath.Join(single, "Disc 1", n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if isMultiDiscAudiobook(single) {
		t.Error("single-disc tree wrongly detected as multi-disc")
	}

	// Flat folder, no disc dirs → not multi-disc.
	flat := t.TempDir()
	for _, n := range []string{"01.mp3", "02.mp3"} {
		if err := os.WriteFile(filepath.Join(flat, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if isMultiDiscAudiobook(flat) {
		t.Error("flat folder wrongly detected as multi-disc")
	}
}

func TestCollectFlattenTracksOrdering(t *testing.T) {
	root := buildMultiDiscTree(t)
	tracks, err := collectFlattenTracks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantRel := []string{
		filepath.Join("Disc 1", "Track 01.mp3"),
		filepath.Join("Disc 1", "Track 02.mp3"),
		filepath.Join("Disc 2", "Track 01.mp3"),
		filepath.Join("Disc 2", "Track 02.mp3"),
	}
	if len(tracks) != len(wantRel) {
		t.Fatalf("got %d tracks, want %d", len(tracks), len(wantRel))
	}
	for i, tr := range tracks {
		if tr.rel != wantRel[i] {
			t.Errorf("track[%d] rel = %q, want %q", i, tr.rel, wantRel[i])
		}
	}
}

// assertFlatResult checks that destDir contains exactly the expected flat
// Part-named files with the expected contents in order, plus the cover sidecar.
func assertFlatResult(t *testing.T, destDir string) {
	t.Helper()
	want := map[string]string{
		"Part 001.mp3": "d1t1",
		"Part 002.mp3": "d1t2",
		"Part 003.mp3": "d2t1",
		"Part 004.mp3": "d2t2",
		"cover.jpg":    "img",
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s content = %q, want %q", name, got, content)
		}
	}
	// No nested disc directories should remain in the flat output.
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("flat dest contains a subdirectory %q — flattening should produce a flat folder", e.Name())
		}
	}
	if len(entries) != len(want) {
		t.Errorf("flat dest has %d entries, want %d", len(entries), len(want))
	}
}

func TestFlattenAudiobookDir_Copy(t *testing.T) {
	src := buildMultiDiscTree(t)
	dst := filepath.Join(t.TempDir(), "Author", "Book (2020)")

	if err := flattenAudiobookDir(context.Background(), "copy", src, dst); err != nil {
		t.Fatalf("flattenAudiobookDir copy: %v", err)
	}
	assertFlatResult(t, dst)

	// Copy must leave the source fully intact (seeding preserved) and produce
	// an independent inode (not a hard link).
	srcFile := filepath.Join(src, "Disc 1", "Track 01.mp3")
	if _, err := os.Stat(srcFile); err != nil {
		t.Errorf("source track removed after copy: %v", err)
	}
	si, _ := os.Stat(srcFile)
	di, _ := os.Stat(filepath.Join(dst, "Part 001.mp3"))
	if os.SameFile(si, di) {
		t.Error("copy mode produced a hardlink (same inode) — expected an independent copy")
	}
}

func TestFlattenAudiobookDir_Hardlink(t *testing.T) {
	src := buildMultiDiscTree(t)
	dst := filepath.Join(t.TempDir(), "Author", "Book (2020)")

	if err := flattenAudiobookDir(context.Background(), "hardlink", src, dst); err != nil {
		t.Fatalf("flattenAudiobookDir hardlink: %v", err)
	}
	assertFlatResult(t, dst)

	// Hardlink must share inodes with the source and leave the source intact.
	srcFile := filepath.Join(src, "Disc 1", "Track 01.mp3")
	si, err := os.Stat(srcFile)
	if err != nil {
		t.Fatalf("source track removed after hardlink: %v", err)
	}
	di, _ := os.Stat(filepath.Join(dst, "Part 001.mp3"))
	if !os.SameFile(si, di) {
		t.Error("hardlink mode did not share an inode with the source")
	}
}

func TestFlattenAudiobookDir_RejectsMoveMode(t *testing.T) {
	src := buildMultiDiscTree(t)
	dst := filepath.Join(t.TempDir(), "out")
	if err := flattenAudiobookDir(context.Background(), "move", src, dst); err == nil {
		t.Error("flattenAudiobookDir must reject move mode")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("rejected mode must not create the destination")
	}
}
