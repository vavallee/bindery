package importer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/models"
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

// FindExisting reports the first library file matching title/author, or "".
// Root selection, the author-folder pre-filter and the title/author match are
// those of the pre-snapshot Scanner walk; the candidate set is filtered and
// ranked by walkLibraryEntries so a supplement-class file beside audio never
// answers and a real container outranks a supplement (#2188/#2240).
func (ls *LibrarySnapshot) FindExisting(ctx context.Context, title, authorName, mediaType string) string {
	if title == "" {
		return ""
	}
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
				return e.path
			}
		}
	}
	return ""
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
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
