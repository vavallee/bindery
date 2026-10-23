package indexer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Per-chapter fragments (#3268, part 3).
//
// Some usenet posts carry one chapter or one slice of an audiobook, named with
// a textual counter rather than the "[NN/MM]" bracket usenetJunkRe knows:
// "Terry Goodkind - SOT06 Faith of the Fallen Ch63 of 71.mp3" (43 MB). Grabbing
// one yields a few minutes of a book that Bindery then tries to import as the
// whole thing.
//
// A title is a fragment when it carries either of two counters:
//
//   - a chapter marker, "Ch", "Ch." or "Chapter", directly followed by a number
//     ("Ch63", "Ch. 5", "Chapter 12"); or
//   - "NN of MM" with MM of 10 or more ("Ch63 of 71", "12 of 40", "Part 3 of
//     12"), unless the word before NN is one that numbers a book in a series
//     ("Book 3 of 12", "Vol 2 of 10").
//
// and it is dropped only when it also names an audio container (mp3, m4a, m4b,
// flac, ogg, aac, wav, wma, opus) or names no format at all. A title that names
// an ebook format is left alone: "Series Book 3 of 12 EPUB" and "Chapter 12
// epub" are books.
//
// What this deliberately does not drop:
//
//   - "Part 1 of 2" and "Part 2 of 2". A two part posting is the likeliest
//     shape of one recording split in two, and the multi-book pack code
//     (pack.go) already makes the same call for "part".
//   - "Catch 22 of Them", "1 of a Kind": MM must be digits, and "Catch22"
//     does not hold a chapter marker because "ch" must start a word.
//
// Lone parts of a set (#3417). Bindery imports one grab as one book and cannot
// assemble several grabs, so a release that says it is one slice of a set is a
// partial book. "Part 3 of 5" is therefore dropped for an audiobook, and so are
// "Pt 2 of 3", "Disc 2 of 4" and "CD 3 of 8". The cost, accepted on purpose, is
// that a recording posted as "Part 1 of 3" is no longer eligible. The rule is
// kept as narrow as it can be:
//
//   - the counter must follow a part word (part, pt, disc, disk, cd) directly,
//     so "Book 3 of 12" and a bare "3 of 5" are not touched;
//   - MM must be 3 or more. "Part 1 of 2" is the likeliest shape of one
//     recording split in two, while "Part N of 5" is almost certainly a slice;
//   - "all parts" or the word "complete" in the name means the set is in one
//     release ("Disc 1 of 8 Complete"). "Unabridged" is not such a word,
//     because individual parts are routinely labelled unabridged;
//   - it applies only when the title names an audio container, or names no
//     format and the search is for an audiobook, so ebooks and ebook searches
//     are never touched;
//   - a counter that the wanted book's own title contains is kept.
//
// The rule runs on every result, so it applies to torrent results as well as
// usenet ones. The wanted title is the one the search used, which keeps its
// subtitle; only the unsearched half of a bilingual "A / B" title (see
// SearchTitle) is not seen, and a release named in the other language would not
// carry that phrase anyway.
//
// Known false keeps (a lone part that is not recognised): "Part 3/5", "3/5",
// "Part III of V", "Vol 3 of 5", "Part One of Two".
//
// Known limits: a title that really contains "NN of MM" with MM 10 or more and
// no ebook format ("12 of 56 Stories") is dropped; a lone part posted without a
// part word ("3 of 5") is kept when MM is under 10; and a whole recording posted
// under a "Part 1 of 3" or longer name is dropped.

// reasonChapterFragment is the FilterDebug reason for a per-chapter fragment.
const reasonChapterFragment = "looks like one chapter or part of a larger post (Ch NN, NN of MM)"

// reasonLoneAudioPart is the FilterDebug reason for one part of a multipart set.
const reasonLoneAudioPart = "looks like one part of a multipart audiobook set (Part N of M), which Bindery cannot assemble"

var (
	chapterMarkerRe = regexp.MustCompile(`(?i)\b(?:ch|chapter)\.?\s*\d{1,3}\b`)
	nOfMRe          = regexp.MustCompile(`(?i)\b(\d{1,3})\s*of\s*(\d{2,3})\b`)
	audioFormatRe   = regexp.MustCompile(`(?i)\b(?:mp3|m4a|m4b|flac|ogg|aac|wav|wma|opus)\b`)
	ebookFormatRe   = regexp.MustCompile(`(?i)\b(?:epub3?|kepub|azw3?|mobi|pdf|djvu|cbr|cbz|fb2|lit|prc|rtf|txt|doc|docx|kfx|ebook|html?)\b`)
	// partOfRe is a part word straight before "N of M".
	partOfRe = regexp.MustCompile(`(?i)\b(?:part|pt|disc|disk|cd)\.?\s*#?\s*(\d{1,3})\s*of\s*(\d{1,3})\b`)
	// wholeSetRe says the release carries every part.
	wholeSetRe = regexp.MustCompile(`(?i)\ball\s+(?:\d+\s+)?parts\b|\bcomplete\b`)
	// precedingWordRe captures the last word before an index.
	precedingWordRe = regexp.MustCompile(`(?i)([a-z]+)\.?\s*#?\s*$`)
)

// seriesCountWords are the words that, directly before "NN of MM", say NN is a
// book's place in a series and not a chapter.
var seriesCountWords = map[string]bool{
	"book": true, "books": true, "bk": true, "vol": true, "volume": true,
	"series": true, "no": true, "number": true, "novel": true,
	"tome": true, "band": true, "season": true, "episode": true, "issue": true,
	"disc": true, "cd": true,
}

// isChapterFragment reports whether title is a per-chapter or per-slice post,
// as described above.
func isChapterFragment(title string) bool {
	// An underscore is a word character to a regexp but a space to a release
	// name ("Author_Title_Ch63_of_71_mp3").
	title = strings.ReplaceAll(title, "_", " ")
	counter := chapterMarkerRe.MatchString(title)
	if !counter {
		for _, m := range nOfMRe.FindAllStringSubmatchIndex(title, -1) {
			total, _ := strconv.Atoi(title[m[4]:m[5]])
			if total < 10 {
				continue
			}
			before := strings.TrimRight(title[:m[0]], " ")
			if strings.HasSuffix(before, "#") {
				continue
			}
			if w := precedingWordRe.FindStringSubmatch(title[:m[0]]); w != nil && seriesCountWords[strings.ToLower(w[1])] {
				continue
			}
			counter = true
			break
		}
	}
	if !counter {
		return false
	}
	return audioFormatRe.MatchString(title) || !ebookFormatRe.MatchString(title)
}

// junkScope is what the usenet junk filter knows about the search. The zero
// value (a free text search) applies only the rules that need no context.
type junkScope struct {
	// MediaType is models.MediaTypeEbook or models.MediaTypeAudiobook, or "".
	MediaType string
	// WantedTitle is the wanted book's title, "" when unknown.
	WantedTitle string
}

func squashSpaces(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " "))
}

// isLoneAudioPart reports whether title is one part of a multipart audiobook
// set, as described above.
func isLoneAudioPart(title string, sc junkScope) bool {
	title = strings.ReplaceAll(title, "_", " ")
	audio := audioFormatRe.MatchString(title)
	if !audio && (ebookFormatRe.MatchString(title) || sc.MediaType != models.MediaTypeAudiobook) {
		return false
	}
	if wholeSetRe.MatchString(title) {
		return false
	}
	wanted := squashSpaces(sc.WantedTitle)
	for _, m := range partOfRe.FindAllStringSubmatch(title, -1) {
		if total, _ := strconv.Atoi(m[2]); total < 3 {
			continue
		}
		if wanted != "" && strings.Contains(wanted, squashSpaces(m[0])) {
			continue
		}
		return true
	}
	return false
}

// usenetJunkReason returns why title is dropped by the usenet junk filter, or
// "" when it is kept. filterUsenetJunk and filterUsenetJunkDebug share it so the
// pipeline and the debug view cannot disagree.
func usenetJunkReason(title string, sc junkScope) string {
	if usenetJunkRe.MatchString(title) {
		return reasonRawPosting
	}
	if isChapterFragment(title) {
		return reasonChapterFragment
	}
	if isLoneAudioPart(title, sc) {
		return reasonLoneAudioPart
	}
	return ""
}

// reasonRawPosting is the FilterDebug reason for a raw per-article posting.
const reasonRawPosting = "looks like a raw per-article posting (RAR/PAR2/SFV/yEnc/[N/M])"
