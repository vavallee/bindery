package importer

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	// minFileTitleScore is the lowest score at which a file counts as the book
	// the download was grabbed for. Below it the file name says too little to
	// move anything, and walk order stays.
	minFileTitleScore = 0.85
	// fileTitleMargin is how far a file must beat the walk order leader of its
	// format before it is moved ahead of it.
	fileTitleMargin = 0.05
	// containedTitleScore is a book title found whole, on word boundaries,
	// inside a longer file name: "Wheel of Time 01 - The Eye of the World".
	// Below an exact match, so "Foundation" beats "Forward the Foundation".
	containedTitleScore = 0.9
)

// orderByBookTitle moves the file that is the book to the front of its format
// in a download holding several. Every ebook file of one format shares a
// destination, and the import loop places the first one and counts the rest as
// already imported, so in a download holding a whole series the matching book
// has to lead (#3224).
//
// Walk order is kept unless a real signal says otherwise: a file moves ahead
// only when its title score clears minFileTitleScore and beats the file that
// would have led by fileTitleMargin. A name that carries no usable title, such
// as "book1.epub", scores low, so it never reorders anything. The decision is
// made per extension, since only files of one format compete for a destination.
func orderByBookTitle(book *models.Book, files []string) []string {
	if book == nil || len(files) < 2 {
		return files
	}
	want := normalizeTitle(book.Title)
	if want == "" {
		return files
	}
	var leaders []string
	byExt := map[string][]string{}
	var exts []string
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if _, ok := byExt[ext]; !ok {
			exts = append(exts, ext)
		}
		byExt[ext] = append(byExt[ext], f)
	}
	for _, ext := range exts {
		group := byExt[ext]
		if len(group) < 2 {
			continue
		}
		best, bestScore := group[0], fileTitleScore(want, group[0])
		first := bestScore
		for _, f := range group[1:] {
			if sc := fileTitleScore(want, f); sc > bestScore {
				best, bestScore = f, sc
			}
		}
		if best != group[0] && shouldMoveAhead(bestScore, first) {
			leaders = append(leaders, best)
		}
	}
	if len(leaders) == 0 {
		return files
	}
	out := slices.Clone(leaders)
	for _, f := range files {
		if !slices.Contains(leaders, f) {
			out = append(out, f)
		}
	}
	return out
}

// shouldMoveAhead is the gate on a reorder: the best file's score clears the
// floor and beats the walk order leader's by the margin. The small tolerance
// keeps a margin of exactly fileTitleMargin from being lost to rounding.
func shouldMoveAhead(best, leader float64) bool {
	return best >= minFileTitleScore && best-leader >= fileTitleMargin-1e-9
}

// fileTitleScore is how well the file names the book, 0 to 1, with want the
// book title already normalised. It is the best of:
//   - 1 when the parsed title, or any " - " part of the file name, is the title
//     ("Wheel of Time 01 - The Eye of the World" has the part "The Eye of the World");
//   - containedTitleScore when the title sits whole inside the file name;
//   - the similarity of the parsed title, and of the EPUB's dc:title.
func fileTitleScore(want, path string) float64 {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parsed := ParseFilename(path)
	score := textutil.JaroWinkler(want, normalizeTitle(parsed.Title))
	for _, part := range strings.Split(dashNormalizer.Replace(base), " - ") {
		if normalizeTitle(part) == want {
			score = 1
		}
	}
	if score < containedTitleScore && containsTokens(titleTokens(base), titleTokens(want)) {
		score = containedTitleScore
	}
	if IsEpubFile(path) {
		if meta, err := ReadEpubMetadata(path); err == nil && meta.Title != "" {
			score = max(score, textutil.JaroWinkler(want, normalizeTitle(meta.Title)))
		}
	}
	return score
}

// titleTokens folds s as a title is compared and splits it into letter and digit runs.
func titleTokens(s string) []string {
	return strings.FieldsFunc(textutil.FoldForTitleMatch(strings.ToLower(strings.TrimSpace(s))), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// containsTokens reports whether needle appears in hay as consecutive tokens.
func containsTokens(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if slices.Equal(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}
