package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// scanLibrary walks the library directory (and the separate audiobook directory
// when configured) for book files not yet tracked in the database and reconciles
// found files with existing "wanted" book records. Callers must hold the
// scanRunning single-flight flag (via StartScan or ScanLibrary).
func (s *Scanner) scanLibrary(ctx context.Context) {
	if s.testScanHook != nil {
		s.testScanHook()
	}
	if s.libraryDir == "" {
		// #965: previously this returned without writing any result, so the UI
		// kept showing a stale prior scan and the #962 "no files found" warning
		// never fired for a literally-unset library dir. Persist a zero-files
		// result so the frontend surfaces the misconfiguration (library_dir is
		// "" → the warning names "?" and no_files_found is true).
		s.writeScanError(ctx, "library directory not configured")
		return
	}

	scanStartedAt := time.Now()

	// walkDir appends all book files found under root to foundFiles, tracking
	// which root each file belongs to so the author-inference fallback can
	// strip the correct prefix. The size and mode the walk already has are
	// kept for the unmatched units (P2).
	walked := make(map[string]walkedFile)
	walkDir := func(root string) []string {
		var files []string
		// walkRoot enters a root that is itself a symlink and reports paths
		// under the configured root; links inside it are still not followed.
		if err := walkRoot(root, s.excludedLibraryDirs(), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			// filepath.Walk Lstats, so a symlink shows up here as itself.
			// Only regular files are book files: a link (planted by a
			// download, or pointing anywhere on the host) must never be
			// reconciled onto a book and then served as that book's file.
			// The Unmatched list already dropped links by mode
			// (eligibleUnmatched); this keeps the reconcile pass in step.
			if !info.Mode().IsRegular() {
				return nil
			}
			if IsBookFile(path) {
				files = append(files, path)
				walked[path] = walkedFile{size: info.Size(), mode: info.Mode()}
			}
			return nil
		}); err != nil {
			slog.Warn("library scan walk encountered errors", "path", root, "error", err)
		}
		return files
	}

	foundFiles := walkDir(s.libraryDir)
	// rootsWithFiles tells the unmatched unit purge which roots this scan
	// actually saw files under, so an unmounted root keeps its ignores.
	var rootsWithFiles []string
	if len(foundFiles) > 0 {
		rootsWithFiles = append(rootsWithFiles, s.libraryDir)
	}

	// Also scan the audiobook directory when it is configured separately.
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		audioFiles := walkDir(s.audiobookDir)
		if len(audioFiles) > 0 {
			rootsWithFiles = append(rootsWithFiles, s.audiobookDir)
		}
		foundFiles = append(foundFiles, audioFiles...)
	}

	slog.Info("library scan found files", "paths", []string{s.libraryDir, s.audiobookDir}, "count", len(foundFiles))

	if len(foundFiles) == 0 {
		s.writeScanResult(ctx, len(foundFiles), 0, 0, 0, 0, s.unmatchedUnitCounts(ctx, false))
		return
	}

	// Load all books from the DB for reconciliation
	allBooks, err := s.books.List(ctx)
	if err != nil {
		slog.Error("library scan: failed to list books", "error", err)
		// #965: write a result on this early return too — otherwise the UI keeps
		// showing the previous (stale) scan and never reflects that the scan
		// failed.
		s.writeScanError(ctx, fmt.Sprintf("scan failed: %v", err))
		return
	}

	// Build a set of tracked file paths, plus the parent directories of
	// tracked AUDIO files: a multi-track audiobook registers one path but its
	// folder holds sibling tracks belonging to the same book. Ebook parents
	// must not be tracked — in a flat Author/Title.epub layout the parent is
	// the author folder, and tracking it hid every untracked sibling ebook in
	// that folder from the scan (#1436).
	trackedPaths := make(map[string]bool)
	// flatAudio is the parent marking for an audio file with no book folder
	// of its own (#1985). In a flat Author/Title.mp3 layout the parent is the
	// AUTHOR folder, and marking it absorbed every other audiobook by that
	// author as already tracked, so a wanted one never reconciled and never
	// reached Unmatched either. Such a folder instead remembers the track
	// stems of its tracked files, and only a sibling that is another track of
	// one of them (see audioTrackStem) is absorbed.
	flatAudio := make(map[string]map[string]bool)
	markAudioParent := func(cleanPath string) {
		dir := filepath.Clean(filepath.Dir(cleanPath))
		if !flatAudioDir(cleanPath, s.libraryDir, s.audiobookDir) {
			trackedPaths[s.pathKey(dir)] = true
			return
		}
		if flatAudio[s.pathKey(dir)] == nil {
			flatAudio[s.pathKey(dir)] = make(map[string]bool)
		}
		flatAudio[s.pathKey(dir)][audioTrackStem(cleanPath)] = true
	}
	audioParentTracked := func(cleanPath string) bool {
		dir := filepath.Clean(filepath.Dir(cleanPath))
		if trackedPaths[s.pathKey(dir)] {
			return true
		}
		stems := flatAudio[s.pathKey(dir)]
		if stems == nil {
			return false
		}
		stem := audioTrackStem(cleanPath)
		// A bare track number names no book, so it stays with the tracked
		// audiobook beside it, as it did before #1985.
		return stem == "" || stems[stem]
	}
	// ebookStems maps a tracked ebook file's path without its extension to
	// the file, so an untracked "Dune.azw3" can find the book that tracks
	// "Dune.epub" in the same folder (#1357). A file reconciled during this
	// pass is added with its book, so both formats of a new book attach in
	// one pass.
	//
	// Only a path with a real ebook extension counts. An audiobook is tracked
	// as its folder, and detectDownloadFormat calls anything without an audio
	// extension an ebook, so a folder "Alpha/" would otherwise vouch for a
	// loose "Alpha.epub", and "Mr. Mercedes/" (cut at the dot) for "Mr.epub".
	ebookStems := make(map[string]*ebookStemOwner)
	ebookStemKey := func(cleanPath string) string {
		return s.pathKey(strings.TrimSuffix(cleanPath, filepath.Ext(cleanPath)))
	}
	noteEbookStem := func(cleanPath string, bookID int64, trackedPath string) {
		if !IsEbookFile(cleanPath) {
			return
		}
		key := ebookStemKey(cleanPath)
		owner := ebookStems[key]
		if owner == nil {
			owner = &ebookStemOwner{bookID: bookID, trackedPath: trackedPath, exts: make(map[string]bool)}
			ebookStems[key] = owner
		}
		owner.exts[strings.ToLower(filepath.Ext(cleanPath))] = true
	}
	if allPaths, err := s.books.ListAllBookFilePaths(ctx); err == nil {
		for _, p := range allPaths {
			cleanP := filepath.Clean(p)
			trackedPaths[s.pathKey(cleanP)] = true
			if detectDownloadFormat([]string{cleanP}) == models.MediaTypeAudiobook {
				markAudioParent(cleanP)
			}
			noteEbookStem(cleanP, 0, cleanP)
		}
	}

	// audioDirs holds every folder the walk found an audio file in, tracked or
	// not. It is what tells a supplement PDF apart from a real ebook edition:
	// see supplementClassExts and the file loop below. Computed from
	// foundFiles rather than inline so the answer never depends on walk order
	// (the .pdf can be reached before the .m4b that explains it).
	audioDirs := make(map[string]bool)
	for _, p := range foundFiles {
		if detectDownloadFormat([]string{p}) == models.MediaTypeAudiobook {
			audioDirs[filepath.Clean(filepath.Dir(p))] = true
		}
	}

	// Rank the pass before anything claims (#2188). The file loop below takes a
	// (book, format) slot first-come, so until this sort the winner of two files
	// competing for one book was whichever the directory listing yielded first:
	// a "… (notes).txt" sorting ahead of the .epub took the book's ebook slot and
	// sent the epub to Unmatched. Ordering every real container ahead of every
	// supplement-class file makes the outcome a property of the files instead.
	// The sort is stable, so equal-rank files keep walk order and nothing else
	// about the pass changes.
	slices.SortStableFunc(foundFiles, func(a, b string) int {
		return scanClaimRank(a) - scanClaimRank(b)
	})

	// Build author name and full-author caches for the reconciliation loop.
	// authorMap is needed for the path-under-library-dir constraint check.
	authorNames := make(map[int64]string)
	authorMap := make(map[int64]*models.Author)
	if allAuthors, err := s.authors.List(ctx); err == nil {
		for i := range allAuthors {
			authorNames[allAuthors[i].ID] = allAuthors[i].Name
			authorMap[allAuthors[i].ID] = &allAuthors[i]
		}
	}

	// reconciledBooks prevents a single DB book from being matched to more
	// than one file OF THE SAME FORMAT in the same scan pass. allBooks is
	// loaded once and is never mutated in-memory, so without this guard a loose
	// titleMatch would attach every file whose title fuzzy-matches the same book
	// to that book. Nothing is overwritten when that happens — book_files rows
	// are additive (see bookFormatClaim) — but a scan that silently hangs five
	// same-format files off one book is worse than reporting the extras as
	// unmatched, where the user can see and correct them.
	//
	// Keying on (book, format) rather than the bare book ID is what lets a
	// dual-format folder attach both editions in ONE pass (#1957). AddBookFile
	// is per-format: an epub and an m4b sitting side by side are two different
	// rows on the same book, not a conflict. The old bare-ID guard claimed the
	// book for whichever file the walk reached first and sent the other to
	// Unmatched, so users had to run a second full scan — which then only
	// worked if the second file's author/title parse survived (#1956).
	reconciledBooks := make(map[bookFormatClaim]bool)

	// Precompute per-book reconciliation data once. The title tier previously
	// called normalizeTitle(book.Title) inside the per-file loop — a pure,
	// loop-invariant function recomputed files×books times. Hoisting it here
	// makes it run once per book. The candidate set is filtered down up front
	// and ASIN lookups become O(1).
	//
	// Two book states count as candidates:
	//   - Wanted: the book has no file yet by definition; this is the main case.
	//   - Imported with no file actually on disk: covers two situations the
	//     scanner used to ignore entirely. (a) #875: Calibre import sets
	//     Status=Imported on every book it creates, but in container setups
	//     where Calibre's library mount differs from Bindery's view, FilePath
	//     stays empty and the user's 3700 epubs found 0 reconciliation
	//     targets. (b) The user moved or renamed their files, leaving Imported
	//     rows pointing at locations that no longer exist; re-scan now relinks
	//     them rather than leaving the rows orphaned.
	wantedBooks := make([]scanBook, 0, len(allBooks))
	asinIndex := make(map[string][]scanBook)
	// booksByAuthor maps an author ID to the indices (into wantedBooks, i.e. in
	// library order) of that author's wanted books, so the title tier can build
	// its candidate list from just the matching authors instead of walking
	// every book in the library.
	booksByAuthor := make(map[int64][]int)
	// listedBooks is every book the scan may write to: List leaves out
	// excluded books, and a row left by a deleted book has no book here.
	listedBooks := make(map[int64]*models.Book, len(allBooks))
	for i := range allBooks {
		b := &allBooks[i]
		listedBooks[b.ID] = b
		if !isReconcileCandidate(b) {
			continue
		}
		sb := newScanBook(b, true)
		idx := len(wantedBooks)
		wantedBooks = append(wantedBooks, sb)
		booksByAuthor[b.AuthorID] = append(booksByAuthor[b.AuthorID], idx)
		if b.ASIN != "" {
			asinIndex[b.ASIN] = append(asinIndex[b.ASIN], sb)
		}
	}

	// authorWordIndex maps each author-name token to the authors containing it.
	// The title tier only matches a book when authorMatch(authorName,
	// parsed.Author) holds, so resolving the matching authors up front lets the
	// title comparison skip every other author's books — turning the tier from
	// O(files × books) into roughly O(files × books-per-author).
	//
	// Keyed by authorNameTokens, the SAME reduction authorMatch compares with,
	// so the index is an exact super-set by construction. It previously used
	// raw ASCII word runs, which stopped being a super-set the moment either
	// side carried a non-ASCII letter (#1646).
	authorWordIndex := make(map[string][]int64)
	for id, name := range authorNames {
		for _, w := range authorNameTokens(name) {
			authorWordIndex[w] = append(authorWordIndex[w], id)
		}
	}

	// matchingAuthors returns the set of author IDs satisfying authorMatch for
	// parsedAuthor. A nil result means "every author" — authorMatch's contract
	// for an empty or initials-only parsed author. Candidates come from the
	// rarest token's word-run bucket (a guaranteed super-set of authorMatch
	// matches) and are then verified with the exact authorMatch, so the result
	// is identical to scanning every author. Memoised: a {Author}/{Title}/file
	// library yields the same parsed author for all of that author's files.
	authorMatchCache := make(map[string]map[int64]bool)
	matchingAuthors := func(parsedAuthor string) map[int64]bool {
		if parsedAuthor == "" {
			return nil
		}
		if set, ok := authorMatchCache[parsedAuthor]; ok {
			return set
		}
		tokens := significantAuthorTokens(parsedAuthor)
		if len(tokens) == 0 {
			authorMatchCache[parsedAuthor] = nil
			return nil
		}
		// Both the index and these tokens come from authorNameTokens, so a
		// significant token is looked up verbatim. authorMatch requires EVERY
		// significant token to be a token of the author's name, so each of
		// their buckets is on its own a super-set of the answer — take the
		// smallest and verify.
		candidates := authorWordIndex[tokens[0]]
		for _, tok := range tokens[1:] {
			if bucket := authorWordIndex[tok]; len(bucket) < len(candidates) {
				candidates = bucket
			}
		}
		set := make(map[int64]bool)
		for _, id := range candidates {
			if !set[id] && authorMatch(authorNames[id], parsedAuthor) {
				set[id] = true
			}
		}
		authorMatchCache[parsedAuthor] = set
		return set
	}

	// resolveAuthors picks the author set the title tier searches for a file,
	// and reports which author string produced it.
	//
	// The parsed author (embedded tag when present, folder otherwise) is tried
	// first, so well-tagged releases keep the #303 behaviour. When it matches
	// NO catalogue author the file used to be unmatchable: authorMatch requires
	// every significant token of the parsed author to be a token of the
	// catalogue author's name, so an Audible-style contributor list — "Álvaro
	// Enrigue, Natasha Wimmer - translator, Gabriel Porras", author plus
	// translator plus narrator — can never match, and it had already overwritten
	// the folder author that would have (#1956). Two fallbacks, in order:
	//
	//   1. the folder-derived author, which a {Author}/{Title}/ library resolves
	//      correctly and which the tag overwrote;
	//   2. the credited names in the list, for layouts with no author folder.
	//
	// A fallback is used only when it matches at least one author, so a file
	// whose author simply isn't in the library still reports the parsed value.
	//
	// Tier 2 unions EVERY credited name that matches a catalogue author instead
	// of trusting the first one. A contributor list carries no evidence about
	// which credit the book is catalogued under: for "Bill Clinton, James
	// Patterson" (catalogued under Patterson, both in the library) taking the
	// first name searched only Clinton's books, so the right book wasn't even a
	// candidate and any Clinton title within the fuzzy gate could take the file.
	// With the union the correct book is always reachable and title similarity
	// decides between them.
	resolveAuthors := func(parsedAuthor, layoutAuthor string) (map[int64]bool, string) {
		set := matchingAuthors(parsedAuthor)
		if set == nil || len(set) > 0 {
			return set, parsedAuthor
		}
		if layoutAuthor != "" && layoutAuthor != parsedAuthor {
			if altSet := matchingAuthors(layoutAuthor); len(altSet) > 0 {
				slog.Debug("library scan: parsed author matched no catalogue author, falling back",
					"parsedAuthor", parsedAuthor, "using", layoutAuthor)
				return altSet, layoutAuthor
			}
		}
		var union map[int64]bool
		var used []string
		for _, alt := range contributorCandidates(parsedAuthor) {
			if alt == parsedAuthor {
				continue
			}
			altSet := matchingAuthors(alt)
			if len(altSet) == 0 {
				continue
			}
			if union == nil {
				// A fresh map: matchingAuthors' results are memoised and must
				// not be mutated.
				union = make(map[int64]bool, len(altSet))
			}
			for id := range altSet {
				union[id] = true
			}
			used = append(used, alt)
		}
		if len(union) > 0 {
			alt := strings.Join(used, ", ")
			slog.Debug("library scan: parsed author matched no catalogue author, falling back",
				"parsedAuthor", parsedAuthor, "using", alt)
			return union, alt
		}
		return set, parsedAuthor
	}

	var unmatchedFiles unmatchedCollector
	var reconciled, unmatched, alreadyTracked, tagReadFailed int

	// titleReconcileMargin is how far the best hit must lead the runner-up
	// before the scan claims the file (#2941). It is findExistingMargin, so the
	// scan and the add path settle a near tie the same way from one constant.
	// It sits beside the 0.85 gate below: a score under that floor is never a
	// hit, and two hits closer than this margin are left unmatched unless one
	// of them is an exact normalised title.
	const titleReconcileMargin = findExistingMargin

	// tryReconcileTitle reports whether one catalogue book clears the fuzzy title
	// tier for this reading of the file. It does not claim: reconcileByTitle
	// scores every candidate, then picks. The candidate's author is already
	// known to satisfy authorMatch — see the title tier in the file loop below.
	var titleCand []int // reused candidate-index scratch
	var titleHits []titleReconcileHit
	// claimBlocked records that the file currently being processed matched a book
	// that cannot accept another file of this format. It is reset per file
	// and read only in the unmatched branch, where it tells a
	// supplement-class sidecar apart from a genuine orphan (#2188).
	var claimBlocked bool
	// registeredPath is what the file currently being processed is recorded as
	// in book_files. For an audiobook inside a book folder of its own that is
	// the folder, not the track that matched (see reconciledAudiobookPath); for
	// everything else it is the file. Set per file in the loop below.
	var registeredPath string
	// fileLayoutTitle is the cleaned book folder name of the file being
	// processed, "" when it has none. The title tier reads the volume number
	// from it before the file's own title (see libraryVolumeConflict). Set per
	// file in the loop below.
	var fileLayoutTitle string
	// fileTooSmall is set per file when it is an ebook format file too small
	// to be a book (#2944). Every tier still runs its claim check, so a
	// notes .txt whose book a real container already claimed is recognised
	// as that container's sidecar (#2188), but no tier attaches it: the gate
	// sits after the claim check and before AddBookFile (in claimTitleHit for
	// this tier). A 1 KB notes file reconciled onto a Wanted book flipped it
	// to Imported and the real ebook was never searched for.
	var fileTooSmall bool
	tryReconcileTitle := func(sb *scanBook, path, title, normParsed, detectedFmt string) (titleReconcileHit, bool) {
		b := sb.book
		// Length gate: Jaro-Winkler is bounded above by 0.8 + 0.2·(minLen/
		// maxLen), so a score >= 0.85 is impossible once the shorter normalised
		// title drops below a quarter of the longer. Cheap, never skips a match.
		lo, hi := sb.normLen, len(normParsed)
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo == 0 || lo*4 < hi {
			return titleReconcileHit{}, false
		}
		// Require Jaro-Winkler >= 0.85 to prevent low-confidence matches from
		// reconciling the wrong book after a delete+rescan (#343).
		jwScore := textutil.JaroWinkler(sb.normTitle, normParsed)
		if jwScore < 0.85 {
			return titleReconcileHit{}, false
		}
		// Two volumes of one series clear any similarity threshold:
		// "Defiance of the Fall 17" against "Defiance of the Fall 01" scores
		// 0.983. Without this an untracked volume 1 folder was reconciled onto
		// a wanted volume 17, which flipped to imported with the wrong file
		// and was never searched (#2860). The number is read from the book
		// folder when it carries one, the same rule FindExisting applies on
		// the add path (#2810). Checked before the claim so a vetoed book
		// never sets claimBlocked: it is not a book this file matches.
		if libraryVolumeConflict(title, fileLayoutTitle, b.Title) {
			slog.Debug("library scan: title match rejected (different series volume)",
				"title", b.Title, "path", path, "fileTitle", title, "folderTitle", fileLayoutTitle, "jw", jwScore)
			return titleReconcileHit{}, false
		}
		// File must live under the candidate book's effective library root to
		// prevent cross-author mismapping after delete+rescan (#343).
		effDir := s.effectiveRootForFormat(ctx, authorMap[b.AuthorID], detectedFmt)
		if !pathUnderDir(path, effDir) {
			slog.Debug("library scan: title+author match rejected (outside library root)",
				"title", b.Title, "path", path, "root", effDir)
			// A book another file already claimed still makes this file its
			// sidecar when the root check turns the file away (#2188), as it
			// did when the claim check ran first.
			if reconciledBooks[bookFormatClaim{b.ID, detectedFmt}] {
				claimBlocked = true
			}
			return titleReconcileHit{}, false
		}
		existingPath := b.EbookFilePath
		if detectedFmt == models.MediaTypeAudiobook {
			existingPath = b.AudiobookFilePath
		}
		_, existingErr := os.Stat(existingPath)
		claimed := reconciledBooks[bookFormatClaim{b.ID, detectedFmt}]
		blocked := !sb.reconcilable || existingErr == nil || claimed
		return titleReconcileHit{sb: sb, score: jwScore, blocked: blocked, claimed: claimed}, true
	}

	// rememberEbookStem records an ebook file this pass attached, so another
	// format of it later in the pass finds its book (see ebookStems).
	rememberEbookStem := func(bookID int64, detectedFmt, cleanPath string) {
		if detectedFmt == models.MediaTypeEbook {
			noteEbookStem(cleanPath, bookID, "")
		}
	}
	// otherFormatOwner returns the listed book that tracks an ebook file with
	// cleanPath's name in cleanPath's folder, or 0. Only a real ebook format
	// qualifies: a same name .pdf, .txt, .rtf, .cbz or .cbr beside an epub is
	// its sidecar under #2188, and those rules still decide it.
	otherFormatOwner := func(cleanPath string) int64 {
		ext := strings.ToLower(filepath.Ext(cleanPath))
		if !IsEbookFile(cleanPath) || supplementClassExts[ext] {
			return 0
		}
		owner := ebookStems[ebookStemKey(cleanPath)]
		if owner == nil || owner.exts[ext] {
			return 0
		}
		bookID := owner.bookID
		if bookID == 0 {
			f, err := s.books.FileByPath(ctx, owner.trackedPath)
			if err != nil {
				slog.Warn("library scan: could not look up the book of a tracked file", "path", owner.trackedPath, "error", err)
				return 0
			}
			if f == nil {
				return 0
			}
			bookID = f.BookID
		}
		if listedBooks[bookID] == nil {
			return 0
		}
		return bookID
	}

	// claimTitleHit records the chosen hit and reports a write error as unmatched.
	claimTitleHit := func(hit titleReconcileHit, path, cleanPath, detectedFmt string) bool {
		b := hit.sb.book
		if fileTooSmall {
			slog.Debug("library scan: title+author match rejected (too small to be a book)",
				"title", b.Title, "path", path)
			return false
		}
		if err := s.books.AddBookFile(ctx, b.ID, detectedFmt, registeredPath); err != nil {
			slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
			return false
		}
		slog.Info("library scan: reconciled book", "title", b.Title, "path", path, "jw", hit.score)
		s.fillCoverFromFile(ctx, b, registeredPath)
		if detectedFmt == models.MediaTypeEbook {
			s.fillLanguageFromFile(ctx, b, authorMap[b.AuthorID], registeredPath)
		}
		trackedPaths[s.pathKey(registeredPath)] = true
		if detectedFmt == models.MediaTypeAudiobook {
			// Sibling tracks of a just-reconciled audiobook folder belong to
			// this book — count them as tracked, not unmatched.
			markAudioParent(cleanPath)
		}
		reconciledBooks[bookFormatClaim{b.ID, detectedFmt}] = true
		rememberEbookStem(b.ID, detectedFmt, cleanPath)
		reconciled++
		return true
	}

	// reconcileByTitle runs the fuzzy title tier for one reading of a file,
	// returning true once a book is claimed. Every candidate is scored. The
	// best hit is claimed when it is the only one, when its normalised title
	// is identical to the file (score 1) and strictly ahead of the runner-up,
	// or when it leads by titleReconcileMargin. A closer pair is left
	// unmatched (#2941).
	//
	// The catalogue is built on first use, so a scan whose files all resolve
	// before the title tier, and leave nothing unmatched, pays nothing for it.
	// The suggestion ranking below shares the same build.
	var catalogue []scanBook
	var catalogueByAuthor map[int64][]int
	ensureCatalogue := func() {
		if catalogueByAuthor == nil {
			catalogue, catalogueByAuthor = suggestionCatalogue(allBooks, wantedBooks)
		}
	}
	// exactTitleOnly narrows the next reconcileByTitle to a book whose
	// normalised title is the file's, for the reading that is most likely the
	// author's name read as a title (#3494).
	exactTitleOnly := false
	reconcileByTitle := func(path, cleanPath, detectedFmt, title, author, layoutAuthor string) bool {
		ensureCatalogue()
		// normalizeTitle strips leading articles and inverts comma-suffix
		// sort form ("Title, A" → "title") so librarian-sorted folders
		// reconcile correctly (#513). Hoisted: computed once per file.
		normParsed := normalizeTitle(title)
		// authorMatch is part of the title-tier predicate. Resolving the
		// matching authors first lets the candidate list be built from just
		// their books (catalogueByAuthor), iterated in library order. A nil set
		// means the parsed author is empty or initials only, so authorMatch
		// accepts any author and every catalogue book is considered.
		authorSet, _ := resolveAuthors(author, layoutAuthor)
		titleHits = titleHits[:0]
		consider := func(sb *scanBook) {
			if hit, ok := tryReconcileTitle(sb, path, title, normParsed, detectedFmt); ok {
				titleHits = append(titleHits, hit)
			}
		}
		if authorSet == nil {
			for i := range catalogue {
				consider(&catalogue[i])
			}
		} else {
			titleCand = titleCand[:0]
			for id := range authorSet {
				titleCand = append(titleCand, catalogueByAuthor[id]...)
			}
			slices.Sort(titleCand) // restore library order across authors
			for _, idx := range titleCand {
				consider(&catalogue[idx])
			}
		}
		hits := titleHits
		if len(hits) > 1 {
			slices.SortStableFunc(hits, func(a, b titleReconcileHit) int {
				if a.score > b.score {
					return -1
				}
				if a.score < b.score {
					return 1
				}
				return 0
			})
		}
		if len(hits) > 0 {
			best := hits[0]
			// An exact normalised title that is wanted, still claimable and
			// strictly ahead of every other hit owns the file, whatever
			// blocked books lie within the margin. A blocked near sibling (a
			// Skipped book, or one already holding a file) must not push a
			// file named exactly as the wanted book back to Unmatched, which
			// would leave that book Wanted and downloaded again. This is the
			// exact-strictly-ahead rule FindExisting uses (fileBelongsToRival).
			// A tie at 1.0, or a blocked best, still refuses below.
			exactLead := best.score == 1 && !best.blocked && (len(hits) == 1 || best.score > hits[1].score)
			if !exactLead {
				for _, hit := range hits {
					if hit.blocked && best.score-hit.score < titleReconcileMargin {
						if hit.claimed {
							claimBlocked = true
						}
						return false
					}
				}
			}
			if len(hits) > 1 {
				runner := hits[1]
				// An exact normalised title still wins inside the margin.
				// A tie at 1.0 is not strictly ahead, so it is left unmatched.
				exact := best.score == 1 && best.score > runner.score
				if !exact && best.score-runner.score < titleReconcileMargin {
					slog.Debug("library scan: title match rejected (close title scores)",
						"title", best.sb.book.Title, "jw", best.score,
						"runnerUp", runner.sb.book.Title, "runnerUpJw", runner.score,
						"path", path)
					return false
				}
			}
			// The winner is settled; now ask whether it is the file's book at
			// all. A shared series name carries the score: "Harry Potter en de
			// Relieken van de Dood" against "Harry Potter en de Orde van de
			// Feniks" is 0.929, and the volume veto has no number to read in a
			// Dutch catalogue title. With the file's own book excluded nothing
			// competed and the file went to the next best title (#3388). Words
			// each title has that the other lacks, none of them close, name two
			// works, so the file is left unmatched. This runs on the winner and
			// not on every candidate so the ranking above, and the near ties it
			// leaves unmatched (#2941), are exactly what they were. A different
			// work is not a book this file matches, so claimBlocked stays unset.
			if exactTitleOnly && normalizeTitle(best.sb.book.Title) != normParsed {
				return false
			}
			if titlesNameDifferentWorks(title, best.sb.book.Title) {
				slog.Debug("library scan: title match rejected (different work)",
					"title", best.sb.book.Title, "path", path, "fileTitle", title, "jw", best.score)
				return false
			}
			return claimTitleHit(best, path, cleanPath, detectedFmt)
		}
		return false
	}

	for _, path := range foundFiles {
		// Files already registered, and sibling tracks inside a tracked
		// audiobook folder, are counted instead of silently skipped so
		// files_found always equals reconciled + unmatched + already_tracked
		// (#1436).
		cleanPath := filepath.Clean(path)
		detectedFmt := detectDownloadFormat([]string{path})
		claimBlocked = false
		fileTooSmall = TooSmallToBeABook(path, walked[path].size)
		// What the file is recorded as in book_files once it reconciles: an
		// audiobook inside a book folder of its own is the folder, not the
		// track that matched (see reconciledAudiobookPath). Decided before the
		// already-tracked check below so that check can see the folder an
		// earlier track of the same book registered.
		registeredPath = path
		if detectedFmt == models.MediaTypeAudiobook {
			registeredPath = reconciledAudiobookPath(path, s.libraryDir, s.audiobookDir)
		}
		// The parent-directory entry in trackedPaths stands for "the sibling
		// TRACKS of a tracked audiobook", so only an audio file may be absorbed
		// by it. An ebook sharing that folder is a separate format on a
		// possibly different book, and swallowing it as already-tracked hid
		// every epub sitting next to an attached audiobook from the scan
		// (#1957) — the mirror image of the one-format-per-pass claim below.
		// The registered folder is the same idea one level up: it stands for
		// the whole audiobook folder, so the second disc of a disc-split
		// audiobook is counted with its book instead of re-claiming it
		// (#2723).
		if trackedPaths[s.pathKey(cleanPath)] ||
			(detectedFmt == models.MediaTypeAudiobook &&
				(audioParentTracked(cleanPath) || trackedPaths[s.pathKey(registeredPath)])) {
			alreadyTracked++
			continue
		}
		// An audiobook release routinely ships a chapter PDF, liner notes, or a
		// stray .txt beside its audio. Those carry ebook extensions, so
		// detectDownloadFormat calls them "ebook" and the tiers below would
		// happily attach "Project Hail Mary.pdf" as the EBOOK FILE of the book
		// whose m4b sits next to it — a 'both' book missing its ebook is a
		// reconcile candidate by design (#1148), and the title fuzzy-matches at
		// 1.0. Before the parent-directory narrowing above they were absorbed as
		// already-tracked; keep that outcome for supplement-class files, decided
		// by folder context rather than extension alone so a PDF-only or
		// comics-only library still reconciles normally.
		if detectedFmt == models.MediaTypeEbook &&
			supplementClassExts[strings.ToLower(filepath.Ext(cleanPath))] &&
			audioDirs[filepath.Clean(filepath.Dir(cleanPath))] {
			slog.Debug("library scan: treating file as an audiobook supplement",
				"path", path, "reason", "ebook-extension file in a folder that holds audio")
			alreadyTracked++
			continue
		}

		// Another format of a book that already has this file's name in this
		// folder (#1357): "Dune.azw3" next to the "Dune.epub" a book tracks.
		// That is the layout an import gives a multi-format download, so it is
		// not the fuzzy title guess the one file per format claim exists to
		// stop. A same-format copy has another name ("Dune (2).epub") and is
		// not matched here.
		if detectedFmt == models.MediaTypeEbook && !fileTooSmall {
			if bookID := otherFormatOwner(cleanPath); bookID != 0 {
				err := s.books.AddBookFile(ctx, bookID, models.MediaTypeEbook, registeredPath)
				if err == nil {
					slog.Info("library scan: added another format beside the book's file", "id", bookID, "path", path)
					// Same fill as the other attach points (#1160): the book
					// may have no language yet, and this file may declare one.
					if b := listedBooks[bookID]; b != nil {
						s.fillLanguageFromFile(ctx, b, authorMap[b.AuthorID], registeredPath)
					}
					trackedPaths[s.pathKey(registeredPath)] = true
					reconciledBooks[bookFormatClaim{bookID, models.MediaTypeEbook}] = true
					rememberEbookStem(bookID, models.MediaTypeEbook, cleanPath)
					reconciled++
					continue
				}
				slog.Error("library scan: failed to add another format to a book", "id", bookID, "path", path, "error", err)
			}
		}

		// Parse the filename for title/author hints, then let the folder
		// hierarchy correct them. A file under <root>/<Author>/<Book>/<file>
		// names the AUTHOR unambiguously and dash-safe, unlike splitting an
		// "Author - Title" / "Title - Author" filename — the scan must not
		// assume a single filename order (#754). The book folder is a weaker
		// signal than the author folder and no longer overrides the filename
		// for an ebook: see scanTitle (#2171).
		parsed := parseScanFile(path, detectedFmt)
		var layoutTitle, layoutAuthor string
		// flipped is the filename read the other way round, for an
		// "Author - Title" name that sits in its author's folder. The title
		// tier tries it first, and only for an author the catalogue knows
		// (flipByLayout, #2331). It used to be offered only where there was no
		// book folder, because the book folder's own name was then taken as
		// the title regardless. Now that the filename leads (#2171), a
		// "Cal Newport - Deep Work.epub" under Cal Newport/Deep Work/ needs
		// the same flip as the one directly under Cal Newport/, and it lands
		// on the same title the folder names.
		var flipped ParsedFile
		var canFlip bool
		// authorNamedTitle is the other side of an "Author - Title" name whose
		// author side is only an ambiguous match for the folder author (#3494).
		var authorNamedTitle string
		if a, t, ok := authorTitleFromLayout(path, s.libraryDir, s.audiobookDir); ok {
			flipped, canFlip = flipByLayout(parsed, a)
			if !canFlip && namesAuthorAmbiguously(parsed, a) {
				authorNamedTitle = parsed.Author
			}
			if a != "" {
				parsed.Author = a
				layoutAuthor = a
			}
			if t != "" {
				layoutTitle = t
				parsed.Title = scanTitle(parsed.Title, t, detectedFmt)
			}
		}
		fileLayoutTitle = layoutTitle

		// Prefer embedded audio tags over filename and folder parsing for
		// audiobook files. Well-tagged M4B/MP3 releases carry the author,
		// title and often an ASIN in their ID3/iTunes atoms; using them avoids
		// the fuzzy-match noise seen on users' organised libraries (#303).
		// fileTags keeps the tags as read, for library adoption's evidence of
		// which author and book a unit's files name (#2942).
		var fileTags AudioTags
		if IsAudioTagFile(path) {
			if tags, err := ReadAudioTags(path); err != nil {
				slog.Warn("library scan: tag read failed, falling back to filename",
					"path", path, "error", err)
				tagReadFailed++
			} else {
				fileTags = tags
				// Tags describe the file itself. A tag title leaves no
				// filename reading to flip, and neither does a tag author that
				// is not the folder's. A tag author that is the folder's (an
				// m4b with only its artist set, an mp3 with album and album
				// artist) says nothing about which side of the filename is the
				// title, so the flip stays open, as it is in bulk import.
				if tags.Title != "" || (tags.Author != "" && !confidentAuthorMatch(tags.Author, layoutAuthor)) {
					canFlip = false
					authorNamedTitle = ""
				}
				// Multi-part audiobooks tag each track with its chapter name
				// ("04 - Sinister Grey Mists..."). When the folder hierarchy
				// already gave a real book title, don't let a per-chapter tag
				// title clobber it — every track would otherwise parse to a
				// different "book" and none would reconcile (#1239).
				if tags.Title != "" && (layoutTitle == "" || !looksLikeChapterTitle(tags.Title)) {
					parsed.Title = tags.Title
				}
				// The tag author still wins (#303), but it no longer DESTROYS
				// the folder-derived author: layoutAuthor is kept as a fallback
				// for the author tier below, because Audible-style contributor
				// lists ("Author, X - translator, Narrator") match no catalogue
				// author and used to leave such files permanently unmatched
				// (#1956).
				if tags.Author != "" {
					parsed.Author = tags.Author
				}
				if tags.ASIN != "" {
					parsed.ASIN = tags.ASIN
				}
			}
		}
		// fallbackAuthor is the author the title tiers fall back to when the
		// parsed one matches nobody in the catalogue: the folder's. Not when
		// every author tag (Artist, Album Artist, Composer) names someone else
		// and the tags do not name the folder's book either: those are another
		// author's files in the wrong folder, and falling back handed seven
		// Katy Evans tracks to the Patterson book their folder was named after
		// (#2942). The book folder title tier is skipped for the same reason.
		// The unit then waits on Import with the conflict shown.
		fallbackAuthor := layoutAuthor
		tagsNameOther := tagsNameAnotherAuthor(fileTags, layoutAuthor, layoutTitle)
		if tagsNameOther {
			fallbackAuthor = ""
		}

		// Search existing books for a match: ASIN takes priority over fuzzy title+author.
		matched := false
		if parsed.ASIN != "" {
			for _, sb := range asinIndex[parsed.ASIN] {
				b := sb.book
				if reconciledBooks[bookFormatClaim{b.ID, detectedFmt}] {
					claimBlocked = true
					continue
				}
				// File must live under the candidate book's effective library root.
				effDir := s.effectiveRootForFormat(ctx, authorMap[b.AuthorID], detectedFmt)
				if !pathUnderDir(path, effDir) {
					slog.Debug("library scan: ASIN match rejected (outside library root)",
						"asin", parsed.ASIN, "path", path, "root", effDir)
					continue
				}
				if fileTooSmall {
					continue
				}
				if err := s.books.AddBookFile(ctx, b.ID, detectedFmt, registeredPath); err != nil {
					slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
					continue
				}
				slog.Info("library scan: reconciled book via ASIN", "asin", parsed.ASIN, "title", b.Title, "path", path)
				s.fillCoverFromFile(ctx, b, registeredPath)
				if detectedFmt == models.MediaTypeEbook {
					s.fillLanguageFromFile(ctx, b, authorMap[b.AuthorID], registeredPath)
				}
				trackedPaths[s.pathKey(registeredPath)] = true
				if detectedFmt == models.MediaTypeAudiobook {
					markAudioParent(cleanPath)
				}
				reconciledBooks[bookFormatClaim{b.ID, detectedFmt}] = true
				rememberEbookStem(b.ID, detectedFmt, cleanPath)
				reconciled++
				matched = true
				break
			}
		}
		// The #2331 flip comes before the parse as it is. Read as it is, an
		// "Author - Title" filename in its author folder has the author's own
		// name for a title, and the fuzzy title tier pairs that with any of the
		// author's books named after them ("Tom Clancy Enemy Contact"). The
		// flip is taken only for a folder author the catalogue knows, so a
		// flat book folder (It/It - Stephen King.epub) still reconciles as it
		// is.
		flipTried := canFlip && len(matchingAuthors(flipped.Author)) > 0
		if !matched && flipTried {
			if matched = reconcileByTitle(path, cleanPath, detectedFmt, flipped.Title, flipped.Author, layoutAuthor); matched {
				slog.Debug("library scan: reconciled a backwards filename in its author folder",
					"path", path, "title", flipped.Title, "author", flipped.Author)
			}
		}
		// When the flip was tried and found no book, the plain reading's title
		// is the author's own name, which only fuzzy-matches catalogue rows
		// named after them ("Stephen King 8"). Do not try it: take the title
		// side as the parsed title and let the book folder tier go next.
		if flipTried && !matched {
			parsed.Title = flipped.Title
		} else if !matched && authorNamedTitle != "" {
			// A variant spelling the flip refuses to act on. The title side is
			// most likely the author's name, so it may claim a book only by
			// naming it exactly ("Dostoevski" the book), never by fuzzy
			// matching "Dostoevsky 8". Otherwise the other side is the title.
			exactTitleOnly = true
			matched = reconcileByTitle(path, cleanPath, detectedFmt, parsed.Title, parsed.Author, fallbackAuthor)
			exactTitleOnly = false
			if !matched {
				parsed.Title = authorNamedTitle
			}
		} else if !matched && parsed.Title != "" {
			matched = reconcileByTitle(path, cleanPath, detectedFmt, parsed.Title, parsed.Author, fallbackAuthor)
		}
		// The book folder's title, one tier down (#2171). The filename now
		// leads, so this is what keeps everything the folder used to match
		// matching: a file whose own name resolves to no book at all is still
		// offered to the book its folder names. It is also how a notes .txt or
		// chapter .pdf beside an epub is recognised as that epub's companion
		// rather than an orphan — its own name says "notes", only the folder
		// says which book it belongs to (#2188).
		if !matched && !tagsNameOther && layoutTitle != "" && layoutTitle != parsed.Title {
			if matched = reconcileByTitle(path, cleanPath, detectedFmt, layoutTitle, parsed.Author, layoutAuthor); matched {
				// The log used to print only the winner, which is why the
				// reported libraries looked healthy (#2171). Name both.
				slog.Debug("library scan: reconciled on the book folder's title, not the file's",
					"path", path, "folderTitle", layoutTitle, "fileTitle", parsed.Title)
			}
		}

		if !matched && s.series != nil && parsed.Series != "" && parsed.SeriesNumber != "" {
			book, seriesErr := s.series.GetBookBySeriesPosition(ctx, parsed.Series, parsed.SeriesNumber)
			if seriesErr != nil {
				slog.Warn("library scan: series position lookup error",
					"series", parsed.Series, "position", parsed.SeriesNumber, "error", seriesErr)
			} else if book != nil && reconciledBooks[bookFormatClaim{book.ID, detectedFmt}] {
				claimBlocked = true
			} else if book != nil {
				effDir := s.effectiveRootForFormat(ctx, authorMap[book.AuthorID], detectedFmt)
				if pathUnderDir(path, effDir) && !fileTooSmall {
					if err := s.books.AddBookFile(ctx, book.ID, detectedFmt, registeredPath); err != nil {
						slog.Error("library scan: failed to update book via series match", "id", book.ID, "error", err)
					} else {
						slog.Info("library scan: reconciled book via series position",
							"series", parsed.Series, "position", parsed.SeriesNumber, "title", book.Title, "path", path)
						s.fillCoverFromFile(ctx, book, registeredPath)
						// GetBookBySeriesPosition reads no field locks;
						// BookRepo.FillLanguage checks the lock itself.
						if detectedFmt == models.MediaTypeEbook {
							s.fillLanguageFromFile(ctx, book, authorMap[book.AuthorID], registeredPath)
						}
						trackedPaths[s.pathKey(registeredPath)] = true
						if detectedFmt == models.MediaTypeAudiobook {
							markAudioParent(cleanPath)
						}
						reconciledBooks[bookFormatClaim{book.ID, detectedFmt}] = true
						rememberEbookStem(book.ID, detectedFmt, cleanPath)
						reconciled++
						matched = true
					}
				}
			}
		}

		if !matched && claimBlocked && detectedFmt == models.MediaTypeEbook &&
			supplementClassExts[strings.ToLower(filepath.Ext(cleanPath))] {
			// A supplement-class file whose ONLY obstacle was that a real container
			// had already claimed the same book's ebook slot in this pass is that
			// container's sidecar — the notes .txt beside the .epub — not an orphan
			// for the user to go and fix. Count it the way the audio-folder guard
			// above counts the same file so files_found still equals reconciled +
			// unmatched + already_tracked (#1436), and keep the Unmatched list to
			// files that genuinely need attention (#2188). Reporting it as unmatched
			// would have named a reason that isn't true of it: the title matched,
			// the author matched, a better file simply won.
			slog.Debug("library scan: treating file as a sidecar of an already-matched book",
				"path", path, "reason", "supplement-class file, a container already claimed this book's ebook slot")
			alreadyTracked++
			continue
		}

		if !matched {
			// Record WHY the file didn't match. The UI used to tell every user
			// with an all-unmatched scan to refresh the author's book catalogue,
			// which is only ever right for one of these three cases (#1958).
			reason := unmatchedReasonNoTitleMatch
			if parsed.Title == "" {
				// The title tier was skipped entirely — don't blame a title
				// match that never happened.
				reason = unmatchedReasonNoTitleParsed
			}
			authorSet, matchAuthor := resolveAuthors(parsed.Author, fallbackAuthor)
			if authorSet != nil {
				if len(authorSet) == 0 {
					reason = unmatchedReasonAuthorNotInLibrary
				} else {
					candidates := 0
					for id := range authorSet {
						candidates += len(booksByAuthor[id])
					}
					if candidates == 0 {
						reason = unmatchedReasonNoCandidateBooks
					}
				}
			}
			// A file too small to be a book is labelled as such whatever else
			// is true of it, so the list does not present it as a book that
			// merely failed to match (#2944).
			if fileTooSmall {
				reason = unmatchedReasonTooSmall
			}
			// matchAuthor is the author string the matcher actually used — it
			// differs from parsedAuthor when a #1956 fallback fired, which is
			// exactly what a support log needs to show.
			slog.Debug("library scan: unmatched file", "path", path, "parsedTitle", parsed.Title,
				"parsedAuthor", parsed.Author, "matchAuthor", matchAuthor, "reason", reason)
			unmatched++
			unmatchedFiles.add(unmatchedScanFile{
				path: path, format: detectedFmt, size: walked[path].size, mode: walked[path].mode,
				title: parsed.Title, layoutTitle: layoutTitle, author: parsed.Author, layoutAuthor: layoutAuthor, reason: reason,
				tags: fileTags,
			})
		}
	}

	// Books already stuck on a vanished path have nothing left to register,
	// so re-derive them here rather than leaving the rescan a no-op (#2186).
	s.refreshStaleRenderedPaths(ctx)

	// Surface every walked root in the completion log. The scan can union
	// the audiobook root with the library root (see ScanLibrary), and the
	// pre-fix log only showed s.libraryDir, which let users with separate
	// roots interpret the file count as coming from the wrong directory
	// (issue #905, second symptom).
	scanRoots := []string{s.libraryDir}
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		scanRoots = append(scanRoots, s.audiobookDir)
	}
	slog.Info("library scan complete", "paths", scanRoots, "bookFiles", len(foundFiles),
		"reconciled", reconciled, "unmatched", unmatched, "tagReadFailed", tagReadFailed)

	// Suggestions come from the catalogue already in memory, ranked once per
	// unit rather than per file. A person confirms a suggestion, so for a
	// resolved author they are drawn from every book of that author, not only
	// the ones the scan may claim by itself: a Skipped book, or one already
	// Imported with a file elsewhere, is often exactly what an untracked copy
	// is (#2879). Excluded books are not in allBooks and so are never offered.
	units := s.recordUnmatchedUnits(ctx, &unmatchedFiles, scanRoots, rootsWithFiles, scanStartedAt,
		func(rep unmatchedScanFile, ev unitEvidence) ([]db.UnmatchedCandidate, string) {
			ensureCatalogue()
			if !ev.conflict() {
				authorSet, _ := resolveAuthors(rep.author, rep.layoutAuthor)
				return rankCandidates(rep.title, rep.layoutTitle, wantedBooks, catalogue, catalogueByAuthor, authorSet), ""
			}
			// The files name another author than their folder (#2942). Their
			// author is searched without the folder fallback, since the folder
			// is what is in doubt, and everything is scored on the title the
			// files name.
			title := firstNonEmpty(ev.title, rep.title)
			filesSet, _ := resolveAuthors(ev.author, "")
			folderSet := matchingAuthors(ev.folder)
			reason := conflictReason(title, filesSet, func(id int64) int { return len(booksByAuthor[id]) })
			return rankConflictCandidates(title, catalogue, catalogueByAuthor, filesSet, folderSet), reason
		})

	s.backfillFileCovers(ctx)
	s.writeScanResult(ctx, len(foundFiles), reconciled, unmatched, alreadyTracked, tagReadFailed, units)
}
