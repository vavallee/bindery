package importer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// Library adoption's side of the scan: the files the reconcile loop could not
// match are grouped into books and stored as unmatched_units rows, where an
// admin can adopt, ignore or leave them (see internal/api/adoption.go).

// Scan bounds. Variables so a test can reach them without 50,000 files.
var (
	// maxUnmatchedFiles bounds how many unmatched files one scan groups. The
	// old blob list stopped at 1000 files, which a single large audiobook
	// could fill on its own (#2547).
	maxUnmatchedFiles = 50000
	// maxUnmatchedUnits bounds how many rows one scan stores.
	maxUnmatchedUnits = 20000
)

const (
	// candidateThreshold is the lowest title similarity offered as a
	// suggestion. The reconcile itself needs 0.85; below that a person decides.
	candidateThreshold = 0.60
	// maxCandidates is how many suggestions a unit keeps.
	maxCandidates = 3
)

// UnmatchedUnitStore is where the scan records its unmatched units.
type UnmatchedUnitStore interface {
	ReconcileScan(ctx context.Context, units []db.UnmatchedUnitScan, opts db.ReconcileScanOptions) (db.ReconcileScanResult, error)
	Summary(ctx context.Context) (db.UnmatchedSummary, error)
}

// WithUnmatchedUnits attaches the store the library scan writes unmatched
// units to. Nil leaves the scan counting unmatched files and storing nothing.
func (s *Scanner) WithUnmatchedUnits(store UnmatchedUnitStore) *Scanner {
	s.unmatchedUnits = store
	return s
}

// ScanRunning reports whether a library scan is in flight, for the adoption
// page's scan status.
func (s *Scanner) ScanRunning() bool {
	return s.scanRunning.Load()
}

// RootFormat names the format the scanned root holds, so the adoption list
// can say when a row sits in the other format's folder (#2944): an ebook
// under the audiobooks root is not where Bindery keeps ebooks. It names the
// root the row was recorded under (the longest scanned root containing it),
// which is a label only; it makes no claim about what the reconcile would
// accept, since an audiobook root nested inside the library is also under
// the ebook root. With one combined root
// (BINDERY_AUDIOBOOK_DIR unset, or the same folder as the library) either
// format belongs anywhere and the answer is "". An unknown root is "" too.
func (s *Scanner) RootFormat(root string) string {
	if root == "" || s.audiobookDir == "" || filepath.Clean(s.audiobookDir) == filepath.Clean(s.libraryDir) {
		return ""
	}
	switch filepath.Clean(root) {
	case filepath.Clean(s.audiobookDir):
		return models.MediaTypeAudiobook
	case filepath.Clean(s.libraryDir):
		return models.MediaTypeEbook
	}
	return ""
}

// walkedFile is what the library walk already knew about a file (P2): its
// size and mode come from the os.FileInfo filepath.Walk hands over, so
// grouping costs no extra stat.
type walkedFile struct {
	size int64
	mode os.FileMode
}

// unmatchedScanFile is one file the reconcile loop left unmatched, with what
// the loop parsed out of it.
type unmatchedScanFile struct {
	path         string
	format       string
	size         int64
	mode         os.FileMode
	title        string
	author       string
	layoutAuthor string
	reason       string
	// layoutTitle is the cleaned book folder name, "" when the file has
	// none. Candidate ranking reads the volume number from it (#2860).
	layoutTitle string
	// tags are the file's audio tags as read, zero for a file without them.
	// Evidence for evidenceFor (#2942).
	tags AudioTags
}

// unmatchedCollector gathers unmatched files up to maxUnmatchedFiles.
type unmatchedCollector struct {
	files     []unmatchedScanFile
	truncated bool
}

func (c *unmatchedCollector) add(f unmatchedScanFile) {
	if len(c.files) >= maxUnmatchedFiles {
		c.truncated = true
		return
	}
	c.files = append(c.files, f)
}

// unmatchedGroup is one unit before candidates are ranked: the stored shape
// plus the member whose parse speaks for the unit, and what its files say
// against its author folder.
type unmatchedGroup struct {
	unit     db.UnmatchedUnitScan
	rep      unmatchedScanFile
	evidence unitEvidence
}

// scanRootFor returns the longest root that contains path, or "".
func scanRootFor(path string, roots []string) string {
	best := ""
	for _, r := range roots {
		if r != "" && pathUnderDir(path, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// discSetNameRe is the disc folder names library adoption treats as parts of
// one book: CD 1, Disc 2, Disk 3. It is narrower than IsDiscFolderName, which
// also accepts Book, Part, Vol, Chapter and bare numbers: in a library those
// name separate books of a series as often as discs ("Mistborn/Book 1",
// "Series/1"). Wherever the call is unclear adoption keeps folders apart,
// because a split set can be adopted piece by piece into one book, while
// separate books merged into one row cannot be adopted apart.
var discSetNameRe = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*[._-]?\s*\d+$`)

// ignoredSubfolder reports whether a subfolder has no say in whether its
// parent is a disc set: hidden and system folders (".hidden", Synology
// "@eaDir", QNAP ".@__thumb", "#recycle") and folders with no audio anywhere
// beneath them (an Artwork folder of jpgs).
func ignoredSubfolder(dir string) bool {
	name := filepath.Base(dir)
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "@") || strings.HasPrefix(name, "#") {
		return true
	}
	return !dirSubtreeHasAudio(dir)
}

// discSetChecker answers, once per folder, whether a folder is one multi disc
// audiobook: every subfolder that counts (ignoredSubfolder) has a disc name
// and holds audio, as the folder import walker's AllDiscFolders requires. A
// library root is never a disc set; a book folder directly under one can be.
func discSetChecker(roots []string) func(folder string) bool {
	cache := make(map[string]bool)
	return func(folder string) bool {
		if v, ok := cache[folder]; ok {
			return v
		}
		result := false
		isRoot := false
		for _, r := range roots {
			if r != "" && filepath.Clean(r) == folder {
				isRoot = true
			}
		}
		if !isRoot && scanRootFor(folder, roots) != "" {
			if entries, err := os.ReadDir(folder); err == nil {
				var discs []string
				names := true
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					dir := filepath.Join(folder, e.Name())
					if ignoredSubfolder(dir) {
						continue
					}
					discs = append(discs, dir)
					names = names && discSetNameRe.MatchString(e.Name())
				}
				result = names && AllDiscFolders(discs)
			}
		}
		cache[folder] = result
		return result
	}
}

// unitKeyFor decides which unit a file belongs to (#2547). The unit is a book,
// not a file:
//
//   - audio groups by its folder, so 193 tracks are one row;
//   - audio in a disc folder groups by the folder above it, when that folder
//     is a disc set (discSetChecker);
//   - loose audio directly in a library root has no book folder, so each file
//     stands alone, as in the folder import scan;
//   - ebooks group by folder and file stem, so Title.epub and Title.mobi are
//     one book in two formats;
//   - a file too small to be a book stands alone, so a 1 KB Title.txt does
//     not ride along in Title.epub's row and get adopted with it (#2944).
//
// It returns the grouping key, and for a folder unit the folder path.
func unitKeyFor(f unmatchedScanFile, root string, isDiscSet func(string) bool) (key, folder string) {
	if f.reason == unmatchedReasonTooSmall {
		return "file\x00" + f.path, ""
	}
	parent := filepath.Dir(f.path)
	if f.format == models.MediaTypeAudiobook {
		if parent == root {
			return "file\x00" + f.path, ""
		}
		if discSetNameRe.MatchString(filepath.Base(parent)) && isDiscSet(filepath.Dir(parent)) {
			parent = filepath.Dir(parent)
		}
		return "folder\x00" + parent, parent
	}
	base := filepath.Base(f.path)
	return "stem\x00" + parent + "\x00" + strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base))), ""
}

// groupUnmatched turns unmatched files into book units. Every file lands in
// exactly one unit, and the units' file counts sum to the number of files
// given (TestGroupUnmatched_Property). Units come back ordered by path. More
// than maxUnmatchedUnits units is truncated to that many.
func groupUnmatched(files []unmatchedScanFile, roots []string) (groups []unmatchedGroup, truncated bool) {
	type acc struct {
		folder  string
		root    string
		members []unmatchedScanFile
	}
	byKey := make(map[string]*acc)
	var keys []string
	isDiscSet := discSetChecker(roots)
	for _, f := range files {
		root := scanRootFor(f.path, roots)
		key, folder := unitKeyFor(f, root, isDiscSet)
		a := byKey[key]
		if a == nil {
			a = &acc{folder: folder, root: root}
			byKey[key] = a
			keys = append(keys, key)
		}
		a.members = append(a.members, f)
	}

	groups = make([]unmatchedGroup, 0, len(keys))
	for _, key := range keys {
		a := byKey[key]
		slices.SortFunc(a.members, func(x, y unmatchedScanFile) int { return strings.Compare(x.path, y.path) })
		u := db.UnmatchedUnitScan{
			UnitPath:  a.members[0].path,
			UnitKind:  db.UnmatchedKindFile,
			Format:    a.members[0].format,
			FileCount: len(a.members),
			RootPath:  a.root,
		}
		if a.folder != "" {
			u.UnitPath, u.UnitKind = a.folder, db.UnmatchedKindFolder
		}
		u.MemberPaths = make([]string, len(a.members))
		for i, m := range a.members {
			u.MemberPaths[i] = m.path
			u.SizeBytes += m.size
		}
		if a.root != "" {
			if rel, err := filepath.Rel(a.root, u.UnitPath); err == nil {
				u.RelPath = filepath.ToSlash(rel)
			}
			if rel, err := filepath.Rel(a.root, a.members[0].path); err == nil {
				if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) >= 2 {
					u.AuthorFolder = parts[0]
				}
			}
		}
		rep := representative(a.members)
		// A disc set's tracks parse their title from the disc folder ("CD 1");
		// the book is the folder above it, so the unit is named after that.
		if a.folder != "" && filepath.Dir(a.members[0].path) != a.folder {
			rep.title = cleanLayoutTitle(filepath.Base(a.folder))
			rep.layoutTitle = rep.title
		}
		u.ParsedTitle, u.ParsedAuthor, u.Reason = rep.title, rep.author, rep.reason
		// Files that name another author than their folder are recorded as
		// what they name, and FilesAuthor tells the adoption page (#2942).
		ev := evidenceFor(a.members, u.AuthorFolder, rep.layoutTitle)
		if ev.conflict() {
			u.ParsedAuthor, u.FilesAuthor = ev.author, ev.author
			if ev.title != "" {
				u.ParsedTitle = ev.title
			}
		}
		groups = append(groups, unmatchedGroup{unit: u, rep: rep, evidence: ev})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].unit.UnitPath < groups[j].unit.UnitPath })
	if len(groups) > maxUnmatchedUnits {
		groups, truncated = groups[:maxUnmatchedUnits], true
	}
	return groups, truncated
}

// representative picks the member whose parse speaks for the unit: the one
// carrying the title most members agree on. For an audiobook folder that is
// the folder's title rather than one track's chapter name. Members arrive
// sorted, so ties go to the first path.
func representative(members []unmatchedScanFile) unmatchedScanFile {
	counts := make(map[string]int, len(members))
	best := members[0]
	for _, m := range members {
		counts[m.title]++
		if counts[m.title] > counts[best.title] {
			best = m
		}
	}
	return best
}

// eligibleUnmatched keeps only the files that may become rows (S10): regular
// files, by the mode the walk read with Lstat, whose resolved folder lies in
// a scanned root. A symlink inside the library would otherwise become a
// tracked book file that the download and OPDS routes serve from wherever it
// points. Each folder is resolved once.
func eligibleUnmatched(files []unmatchedScanFile, roots []string) []unmatchedScanFile {
	realRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			realRoots = append(realRoots, rr)
		}
	}
	resolved := make(map[string]bool)
	out := files[:0:0]
	for _, f := range files {
		if !f.mode.IsRegular() {
			slog.Debug("library scan: unmatched entry is not a regular file; not listing it", "path", f.path)
			continue
		}
		dir := filepath.Dir(f.path)
		ok, seen := resolved[dir]
		if !seen {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				for _, r := range realRoots {
					if pathUnderDir(real, r) {
						ok = true
						break
					}
				}
			}
			resolved[dir] = ok
		}
		if !ok {
			slog.Debug("library scan: unmatched file resolves outside the library; not listing it", "path", f.path)
			continue
		}
		out = append(out, f)
	}
	return out
}

// suggestionCatalogue indexes every book the scan loaded by author, for
// suggestions only. The reconcile's own candidates (wanted) are marked so a
// tie goes to the book still waiting for a file; they are taken from the set
// the scan already built rather than asking isReconcileCandidate again, which
// stats files on disk.
func suggestionCatalogue(books []models.Book, wanted []scanBook) ([]scanBook, map[int64][]int) {
	reconcilable := make(map[int64]bool, len(wanted))
	for i := range wanted {
		reconcilable[wanted[i].book.ID] = true
	}
	out := make([]scanBook, 0, len(books))
	byAuthor := make(map[int64][]int)
	for i := range books {
		b := &books[i]
		byAuthor[b.AuthorID] = append(byAuthor[b.AuthorID], len(out))
		out = append(out, newScanBook(b, reconcilable[b.ID]))
	}
	return out, byAuthor
}

// rankCandidates returns up to maxCandidates catalogue books whose title
// scores at least candidateThreshold against normParsed, best first. It uses
// the reconcile's own measure (Jaro-Winkler over normalizeTitle). When the
// file's author resolved, the candidates are every catalogue book of those
// authors, whatever its status (#2879): the reconcile only claims books still
// waiting for a file, but a person confirming a suggestion may well be
// pointing an untracked copy at a book that is Skipped or already has one.
// When the file named no author they are the reconcile's own candidates
// (wanted), and titles of wildly different length are skipped, the same
// cheap gate the reconcile uses; the whole library is too wide a net without
// an author. No provider is asked anything.
//
// An exact title scores 1, so it ranks first; between equal scores a book the
// reconcile could claim goes ahead of one that already has its files.
//
// A book that is provably another volume of the same series is never
// suggested, by the same rule the reconcile applies (libraryVolumeConflict):
// volume 1's folder scores 0.983 against a wanted volume 17, and offering it
// as the top suggestion would invite the user to adopt it there (#2860).
func rankCandidates(title, layoutTitle string, wanted, catalogue []scanBook, catalogueByAuthor map[int64][]int, authorSet map[int64]bool) []db.UnmatchedCandidate {
	normParsed := normalizeTitle(title)
	if normParsed == "" {
		return nil
	}
	type scored struct {
		db.UnmatchedCandidate
		reconcilable bool
	}
	var out []scored
	consider := func(sb *scanBook) {
		score := textutil.JaroWinkler(sb.normTitle, normParsed)
		if score < candidateThreshold {
			return
		}
		if libraryVolumeConflict(title, layoutTitle, sb.book.Title) {
			return
		}
		out = append(out, scored{db.UnmatchedCandidate{BookID: sb.book.ID, Score: score}, sb.reconcilable})
	}
	if authorSet == nil {
		for i := range wanted {
			lo, hi := wanted[i].normLen, len(normParsed)
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo == 0 || lo*4 < hi {
				continue
			}
			consider(&wanted[i])
		}
	} else {
		ids := make([]int64, 0, len(authorSet))
		for id := range authorSet {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			for _, idx := range catalogueByAuthor[id] {
				consider(&catalogue[idx])
			}
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		case a.reconcilable && !b.reconcilable:
			return -1
		case b.reconcilable && !a.reconcilable:
			return 1
		}
		return 0
	})
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	if len(out) == 0 {
		return nil
	}
	res := make([]db.UnmatchedCandidate, len(out))
	for i := range out {
		res[i] = out[i].UnmatchedCandidate
	}
	return res
}

// unitCounts is what the scan result blob reports about stored units.
type unitCounts struct {
	pending   int
	ignored   int
	truncated bool
}

// recordUnmatchedUnits groups a finished scan's unmatched files and stores
// them. candidatesFor ranks suggestions for one unit's representative parse
// and its files' evidence, and returns the unit's reason when the evidence
// changes it ("" keeps the representative's).
func (s *Scanner) recordUnmatchedUnits(ctx context.Context, c *unmatchedCollector, roots, rootsWithFiles []string, startedAt time.Time,
	candidatesFor func(rep unmatchedScanFile, ev unitEvidence) ([]db.UnmatchedCandidate, string)) unitCounts {
	if s.unmatchedUnits == nil {
		return unitCounts{}
	}
	groups, unitsTruncated := groupUnmatched(eligibleUnmatched(c.files, roots), roots)
	units := make([]db.UnmatchedUnitScan, len(groups))
	for i, g := range groups {
		units[i] = g.unit
		// Nothing is suggested for a file too small to be a book: a
		// suggestion is an invitation to adopt it, and its reason stays
		// too_small whatever the evidence says (#2944).
		if g.rep.reason != unmatchedReasonTooSmall {
			var reason string
			units[i].Candidates, reason = candidatesFor(g.rep, g.evidence)
			if reason != "" {
				units[i].Reason = reason
			}
		}
	}
	truncated := c.truncated || unitsTruncated
	// A truncated scan did not see every unit, so it removes and purges
	// nothing: a unit beyond the cap is unlisted, not gone.
	res, err := s.unmatchedUnits.ReconcileScan(ctx, units, db.ReconcileScanOptions{
		StartedAt:       startedAt,
		SkipDeletion:    truncated,
		RootsWithFiles:  rootsWithFiles,
		ConfiguredRoots: roots,
	})
	if err != nil {
		slog.Warn("library scan: failed to store unmatched units", "error", err)
		return s.unmatchedUnitCounts(ctx, truncated)
	}
	slog.Info("library scan: stored unmatched units", "units", res.Upserted, "removed", res.RemovedPending,
		"purged", res.Purged, "pending", res.Pending, "ignored", res.Ignored, "truncated", truncated)
	return unitCounts{pending: res.Pending, ignored: res.Ignored, truncated: truncated}
}

// unmatchedUnitCounts reads the counts without changing any row, for a scan
// that found no files or failed: those must not delete anyone's decisions.
func (s *Scanner) unmatchedUnitCounts(ctx context.Context, truncated bool) unitCounts {
	if s.unmatchedUnits == nil {
		return unitCounts{}
	}
	sum, err := s.unmatchedUnits.Summary(ctx)
	if err != nil {
		slog.Warn("library scan: failed to count unmatched units", "error", err)
		return unitCounts{truncated: truncated}
	}
	return unitCounts{pending: sum.Pending, ignored: sum.Ignored, truncated: truncated}
}
