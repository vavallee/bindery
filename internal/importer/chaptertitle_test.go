package importer

import "testing"

func TestLooksLikeChapterTitle(t *testing.T) {
	chapters := []string{
		"04 - Sinister Grey Mists Rolled Thru Docks Of Morpork",
		"07 - The Temple Frescoes Of Al-Khali",
		"04. Something",
		"04_Something",
		"1 - Intro",
		"Chapter 3",
		"chapter 12 - The End",
		"Track 5",
		"Part 2",
		"Disc 1",
		"CD 2",
	}
	for _, c := range chapters {
		if !looksLikeChapterTitle(c) {
			t.Errorf("expected %q to look like a chapter title", c)
		}
	}

	titles := []string{
		"Sourcery",
		"1984",
		"2001: A Space Odyssey",
		"11/22/63",
		"Slaughterhouse-Five",
		"The Temporal Leader",
		"Catch-22",
		"Part of the Pattern", // "Part" not followed by a number
	}
	for _, ti := range titles {
		if looksLikeChapterTitle(ti) {
			t.Errorf("expected %q to NOT look like a chapter title", ti)
		}
	}
}
