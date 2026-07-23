package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAudiobookFileName(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Brandon Sanderson"}
	book := &models.Book{Title: "The Way of Kings"}

	cases := []struct {
		name     string
		template string
		part     int
		ext      string
		want     string
	}{
		{"default template pads part", "", 1, "mp3", "The Way of Kings - Part 001.mp3"},
		{"default template pads part 12", "", 12, "m4b", "The Way of Kings - Part 012.m4b"},
		{"custom template with author", "{Author} - {Title} - {Part:2}.{ext}", 3, "mp3", "Brandon Sanderson - The Way of Kings - 03.mp3"},
		{"no pad", "{Title}.{Part}.{ext}", 5, "flac", "The Way of Kings.5.flac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.AudiobookFileName(tc.template, author, book, "", "", tc.ext, tc.part)
			if got != tc.want {
				t.Errorf("AudiobookFileName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAudiobookFileName_NoSeparatorEscape ensures a template that would inject a
// path separator collapses to a bare filename (defence against directory escape).
func TestAudiobookFileName_NoSeparatorEscape(t *testing.T) {
	r := NewRenamer("")
	book := &models.Book{Title: "Book"}
	// {Author} of "../evil" sanitises, and any separator is stripped by Base.
	got := r.AudiobookFileName("{Author}/{Title} - {Part:3}.{ext}", &models.Author{Name: "../evil"}, book, "", "", "mp3", 1)
	if filepath.Base(got) != got {
		t.Errorf("AudiobookFileName produced a path separator: %q", got)
	}
}

func TestFlattenAudiobookDirNamed_Ordering(t *testing.T) {
	src := buildMultiDiscTree(t)
	dst := filepath.Join(t.TempDir(), "Author", "Book")

	r := NewRenamer("")
	book := &models.Book{Title: "Recursion"}
	author := &models.Author{Name: "Blake Crouch"}
	namer := func(index int, ext string) string {
		return r.AudiobookFileName("{Title} - Part {Part:3}.{ext}", author, book, "", "", trimDot(ext), index+1)
	}

	if err := flattenAudiobookDirNamed(context.Background(), "copy", src, dst, namer); err != nil {
		t.Fatalf("flattenAudiobookDirNamed: %v", err)
	}

	// Disc 1 tracks come before Disc 2 tracks, numbered contiguously.
	for i, want := range []string{
		"Recursion - Part 001.mp3",
		"Recursion - Part 002.mp3",
		"Recursion - Part 003.mp3",
		"Recursion - Part 004.mp3",
	} {
		if _, err := os.Stat(filepath.Join(dst, want)); err != nil {
			t.Errorf("track %d: expected %q to exist: %v", i+1, want, err)
		}
	}
	// The cover sidecar is carried across unrenamed.
	if _, err := os.Stat(filepath.Join(dst, "cover.jpg")); err != nil {
		t.Errorf("cover.jpg sidecar not carried across: %v", err)
	}
}

// TestFlattenAudiobookDirNamed_DuplicateNameFails guards the {Part}-less template
// case: a namer that returns the same name for every track must fail loudly
// rather than silently drop all but the last file.
func TestFlattenAudiobookDirNamed_DuplicateNameFails(t *testing.T) {
	src := buildMultiDiscTree(t)
	dst := filepath.Join(t.TempDir(), "out")
	namer := func(index int, ext string) string { return "same" + ext } // no {Part}
	if err := flattenAudiobookDirNamed(context.Background(), "copy", src, dst, namer); err == nil {
		t.Error("expected a duplicate-name error when the namer omits Part")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("failed flatten must not leave a partial destination: %v", err)
	}
}

func trimDot(ext string) string {
	if len(ext) > 0 && ext[0] == '.' {
		return ext[1:]
	}
	return ext
}
