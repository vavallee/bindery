package importer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// LibrarySnapshot answers FindExisting queries from one walk of each library
// root instead of one walk per query.
//
// FindExisting is called once per book an author sync creates, and each call
// used to re-walk the whole library tree (#1888/#1929). On local disk with a
// warm dentry cache that hides; on an NFS/SMB mount with a large library a
// single walk can take seconds, and a 65-book sync paid it 65 times — which is
// the right order of magnitude for the reported "an hour to add 65 books". The
// walk also parses every filename per call, so the sync re-ran the same
// ParseFilename work per book.
//
// A snapshot walks a root at most once, on the first query that needs it, and
// serves every later query from the parsed entries. It deliberately holds only
// the two root paths, not a *Scanner, so tests and callers can build one over
// a bare directory.
//
// Staleness, stated: files that appear under a root after that root's walk are
// invisible to later queries on the same snapshot. Within an author sync that
// window is close to vacuous — auto-search for the books being created runs
// after the create loop, so any file that can match one of them was on disk
// before the sync started; only a file copied in by hand mid-sync is missed,
// and the next sync sees it. One-off callers get a fresh snapshot per call
// (Scanner.FindExisting), which is exactly the old semantics.
type LibrarySnapshot struct {
	libraryDir   string
	audiobookDir string

	mu    sync.Mutex
	roots map[string][]libraryEntry
}

// libraryEntry is one book file, pre-parsed at walk time so queries are pure
// in-memory comparisons.
type libraryEntry struct {
	path string
	// firstDir is the first path segment under the root — the author folder in
	// an Author/Title layout. Empty for files sitting directly under the root,
	// which the author pre-filter has always exempted.
	firstDir string
	title    string
	// layoutTitle is the cleaned book-folder name, "" when the file has no
	// book folder of its own. It never makes a match on its words, it only
	// supplies the volume number when it carries one: a Libation-style
	// "Series NN/Series_ASIN_….m4b" layout keeps the volume number only in
	// the folder, and without it every volume's file looked like the one the
	// next volume was asking for (#2810).
	layoutTitle string
	author      string
}

// NewLibrarySnapshot builds an empty snapshot over the given roots. Roots are
// walked lazily, each on the first query that selects it, so a snapshot that
// only ever sees ebook queries never touches the audiobook root.
func NewLibrarySnapshot(libraryDir, audiobookDir string) *LibrarySnapshot {
	return &LibrarySnapshot{libraryDir: libraryDir, audiobookDir: audiobookDir}
}

// findExistingMargin is how far the best file must lead a file of a different
// title before FindExisting answers with it (#2941). It is the library scan's
// title margin, so the add path and the scan settle a near tie the same way:
// five points on the 0 to 1 Jaro-Winkler scale, overridden only by an exact
// normalised title that is strictly ahead.
const findExistingMargin = 0.05

// FindExisting reports the library file that best matches title/author, or
// "". Root selection, the author-folder pre-filter and the title/author match
// are those of the pre-snapshot Scanner walk; the candidate set is filtered
// and ranked by walkLibraryEntries so a supplement-class file beside audio
// never answers and a real container outranks a supplement (#2188/#2240).
//
// Every file that clears the match is scored, not just the first one walked
// (#2941). See FindExistingAmong for the ranking rule.
func (ls *LibrarySnapshot) FindExisting(ctx context.Context, title, authorName, mediaType string) string {
	return ls.FindExistingAmong(ctx, title, authorName, mediaType, nil)
}

// FindExistingAmong is FindExisting for a book whose author has other
// catalogue books, given as rivals. It ranks in both directions, the way the
// library scan's title tier ranks books for a file (#2941):
//
//   - Files for the book. Every matching file is scored by Jaro-Winkler on the
//     normalised titles. The best one answers when no file of a different
//     title competes, when its title is exactly the wanted one and strictly
//     ahead, or when it leads by findExistingMargin. A closer pair answers
//     nothing, so the book stays Wanted and searchable. Files whose titles
//     differ only in their numbers (an audiobook's tracks, one title in two
//     formats) are one book, not competitors; the best-scoring of them
//     answers, the first in walk order among equals. A file in a numbered
//     book folder is grouped by the folder, which is where such a layout
//     keeps the volume (#2810).
//   - Books for the files. Before ranking, a file whose normalised title is
//     exactly a rival's and not the wanted one is dropped, so the next best
//     file can answer: "Harry Potter" must not take "Harry Potter en het
//     vervloekte kind.epub" when that title is also the author's, and "Dune"
//     still finds its own file beside "Dune Messiah.epub". Partial rival
//     matches never drop a file (see fileBelongsToRival).
func (ls *LibrarySnapshot) FindExistingAmong(ctx context.Context, title, authorName, mediaType string, rivals []string) string {
	if title == "" {
		return ""
	}
	var hits []*libraryEntry
	for _, root := range ls.rootsForMediaType(mediaType) {
		entries, ok := ls.entriesFor(ctx, root)
		if !ok {
			// Cancelled mid-walk: match the old walk's behaviour of quietly
			// finding nothing, and leave the root uncached so a live caller
			// gets a real walk.
			return ""
		}
		for i := range entries {
			e := &entries[i]
			if authorName != "" && e.firstDir != "" && !authorMatch(authorName, e.firstDir) {
				continue
			}
			if !authorMatch(authorName, e.author) {
				continue
			}
			// A numbered book folder settles the volume before the filename
			// is read, so track numbers never veto a book's own files
			// (#2810). libraryVolumeConflict is the same rule the library
			// scan applies (#2860).
			if libraryVolumeConflict(e.title, e.layoutTitle, title) {
				continue
			}
			if titleWordsMatch(e.title, title) {
				hits = append(hits, e)
			}
		}
	}
	best := bestExistingFile(dropRivalFiles(hits, title, rivals), title)
	if best == nil {
		return ""
	}
	return best.path
}

// dropRivalFiles removes the files that belong to a rival catalogue title
// before ranking, so the wanted title's own file can still answer when a
// rival's file would have outscored it: "Dune" keeps "Dune_ Deluxe
// Edition.epub" once "Dune Messiah.epub" is known to be "Dune Messiah"'s.
func dropRivalFiles(hits []*libraryEntry, title string, rivals []string) []*libraryEntry {
	if len(rivals) == 0 {
		return hits
	}
	rivalNorms := make(map[string]bool, len(rivals))
	for _, r := range rivals {
		if n := normalizeTitle(r); n != "" {
			rivalNorms[n] = true
		}
	}
	wanted := normalizeTitle(title)
	kept := make([]*libraryEntry, 0, len(hits))
	for _, e := range hits {
		if fileBelongsToRival(e, wanted, rivalNorms) {
			slog.Debug("library: existing file is titled exactly as another book of the author, not offering it",
				"title", title, "path", e.path)
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// existingTitleScore scores a file's normalised title against a wanted one:
// 1 for an identical normalised title, Jaro-Winkler otherwise.
func existingTitleScore(fileNorm, wantedNorm string) (score float64, exact bool) {
	if fileNorm == wantedNorm {
		return 1, true
	}
	return textutil.JaroWinkler(fileNorm, wantedNorm), false
}

// bestExistingFile picks FindExistingAmong's answer from the matching files,
// given in walk order: nil when there is none, or when the best is not clear
// of a file of a different title.
func bestExistingFile(hits []*libraryEntry, title string) *libraryEntry {
	if len(hits) == 0 {
		return nil
	}
	// Supplement ranking stays a tier, not a score: a real container answers
	// whenever one matches, and a supplement only when none does (#2188).
	topRank := scanClaimRank(hits[0].path)
	for _, e := range hits[1:] {
		topRank = min(topRank, scanClaimRank(e.path))
	}
	wanted := normalizeTitle(title)
	// A group answers with its best-scoring member, the first of equals in
	// walk order, so "Dune" gets "Dune.epub" and not "Dune 2.epub" beside it.
	type titleGroup struct {
		best  *libraryEntry
		score float64
		exact bool
	}
	var groups []*titleGroup
	byKey := make(map[string]*titleGroup)
	for _, e := range hits {
		if scanClaimRank(e.path) != topRank {
			continue
		}
		norm := normalizeTitle(e.title)
		score, exact := existingTitleScore(norm, wanted)
		key := titleSansDigits(norm)
		// A numbered book folder is the better evidence of which book a file
		// is (#2810): a Libation file name carries an ASIN and no volume, so
		// every volume's file would read as a different title. The folder
		// names the group, and its score counts when it is the higher.
		if carriesVolumeNumber(e.layoutTitle) {
			folderNorm := normalizeTitle(e.layoutTitle)
			folderScore, folderExact := existingTitleScore(folderNorm, wanted)
			score = max(score, folderScore)
			exact = exact || folderExact
			key = titleSansDigits(folderNorm)
		}
		g, ok := byKey[key]
		if !ok {
			g = &titleGroup{best: e, score: score, exact: exact}
			byKey[key] = g
			groups = append(groups, g)
			continue
		}
		if score > g.score {
			g.best, g.score, g.exact = e, score, exact
		}
	}
	bestIdx := 0
	for i, g := range groups {
		if g.score > groups[bestIdx].score {
			bestIdx = i
		}
	}
	top := groups[bestIdx]
	for i, g := range groups {
		if i != bestIdx && !clearLead(top.score, top.exact, g.score) {
			slog.Debug("library: existing files too close to call, not binding either",
				"title", title, "best", top.best.path, "jw", top.score,
				"runnerUp", g.best.path, "runnerUpJw", g.score)
			return nil
		}
	}
	return top.best
}

// fileBelongsToRival reports whether file is provably another catalogue
// book's: its normalised title is exactly a rival's and not the wanted one
// (wanted and rivalNorms are already normalised). Only an exact title counts.
// A scored rule (any rival within the margin) was tried and withdrew real
// matches: "Project Hail Mary A Novel.epub" for "Project Hail Mary" against a
// "Proyecto Hail Mary" row, or "Mistborn.epub" for "Mistborn: The Final
// Empire" against the rest of the series, leaving an owned book to be
// downloaded again. A file in a numbered book folder never belongs to a
// rival here, because the folder has already settled the volume (#2810).
func fileBelongsToRival(file *libraryEntry, wanted string, rivalNorms map[string]bool) bool {
	if carriesVolumeNumber(file.layoutTitle) {
		return false
	}
	fileNorm := normalizeTitle(file.title)
	return fileNorm != wanted && rivalNorms[fileNorm]
}

// clearLead is the #2941 decision between a leader and one competitor: an
// exact normalised title strictly ahead wins, and otherwise the leader needs
// findExistingMargin in hand.
func clearLead(lead float64, leadExact bool, other float64) bool {
	if leadExact && lead > other {
		return true
	}
	return lead-other >= findExistingMargin
}

// titleSansDigits drops the digits from a normalised title, so files that
// differ only in a track, part or disc number group as one book.
func titleSansDigits(norm string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return ' '
		}
		return r
	}, norm)), " ")
}

// rootsForMediaType selects which roots a query walks: ebook → libraryDir,
// audiobook → audiobookDir (falling back to libraryDir when unset), both or
// unknown → both with libraryDir first (#488).
func (ls *LibrarySnapshot) rootsForMediaType(mediaType string) []string {
	roots := make([]string, 0, 2)
	switch mediaType {
	case models.MediaTypeEbook:
		if ls.libraryDir != "" {
			roots = append(roots, ls.libraryDir)
		}
	case models.MediaTypeAudiobook:
		switch {
		case ls.audiobookDir != "":
			roots = append(roots, ls.audiobookDir)
		case ls.libraryDir != "":
			roots = append(roots, ls.libraryDir)
		}
	default:
		if ls.libraryDir != "" {
			roots = append(roots, ls.libraryDir)
		}
		if ls.audiobookDir != "" && ls.audiobookDir != ls.libraryDir {
			roots = append(roots, ls.audiobookDir)
		}
	}
	return roots
}

// entriesFor returns the cached entries for root, walking it on first use. The
// second return is false only when the walk was abandoned on a cancelled
// context — that result is not cached, so the cancellation of one sync cannot
// blind a later caller.
func (ls *LibrarySnapshot) entriesFor(ctx context.Context, root string) ([]libraryEntry, bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if entries, ok := ls.roots[root]; ok {
		return entries, true
	}
	entries, ok := walkLibraryEntries(ctx, root)
	if !ok {
		return nil, false
	}
	if ls.roots == nil {
		ls.roots = make(map[string][]libraryEntry)
	}
	ls.roots[root] = entries
	return entries, true
}

// walkLibraryEntries collects every book file under root. Unreadable entries
// are skipped rather than aborting the walk, matching the old per-query walk;
// a missing root simply yields no entries. Returns ok=false only on context
// cancellation, which the old walk ignored outright (#1929) — a cancelled sync
// kept walking the whole tree.
//
// The candidate set gets the same two-tier supplement treatment the library
// scan gives its pass (#2188), which used to stop at the scan loop and leave
// FindExisting binding new books to cue sheets and notes files (#2240):
//
//   - A supplement-class file (.txt/.rtf/.pdf/.cbz/.cbr) whose folder also
//     holds audio is the audiobook's material, never a candidate — the scan
//     loop's audio-folder guard, applied while walking. Tracked from the whole
//     walk before filtering so the answer never depends on walk order.
//   - The surviving entries are ordered by scanClaimRank, real containers
//     ahead of supplement-class files, so FindExisting answers with a real
//     container whenever one matches and falls back to a supplement only when
//     nothing better does. Text-only and PDF-only libraries keep matching.
func walkLibraryEntries(ctx context.Context, root string) ([]libraryEntry, bool) {
	var entries []libraryEntry
	audioDirs := make(map[string]bool)
	// walkRoot enters a root that is itself a symlink, reporting paths under
	// root as configured, so FindExisting's answer matches book_files rows.
	_ = walkRoot(root, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort walk: skip unreadable entries rather than abort
		}
		if IsAudioFile(path) {
			audioDirs[filepath.Clean(filepath.Dir(path))] = true
		}
		if !IsBookFile(path) {
			return nil
		}
		// A notes file is never the book a new catalogue entry already owns:
		// the add author path binds what this returns and skips the search
		// (#2944).
		if TooSmallToBeABook(path, info.Size()) {
			return nil
		}
		firstDir := ""
		if rel, relErr := filepath.Rel(root, path); relErr == nil {
			if parts := strings.SplitN(rel, string(filepath.Separator), 2); len(parts) >= 2 {
				firstDir = parts[0]
			}
		}
		parsed := ParseFilename(path)
		_, layoutTitle, _ := authorTitleFromLayout(path, root)
		entries = append(entries, libraryEntry{
			path:        path,
			firstDir:    firstDir,
			title:       parsed.Title,
			layoutTitle: layoutTitle,
			author:      parsed.Author,
		})
		return nil
	})
	if ctx.Err() != nil {
		return nil, false
	}
	kept := entries[:0]
	for _, e := range entries {
		if scanClaimRank(e.path) == supplementClaimRank &&
			audioDirs[filepath.Clean(filepath.Dir(e.path))] {
			continue
		}
		kept = append(kept, e)
	}
	entries = kept
	slices.SortStableFunc(entries, func(a, b libraryEntry) int {
		return scanClaimRank(a.path) - scanClaimRank(b.path)
	})
	return entries, true
}
