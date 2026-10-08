package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/duplicates"
	"github.com/vavallee/bindery/internal/models"
)

// DuplicateReviewHandler serves the library-wide duplicate review (#2999).
type DuplicateReviewHandler struct {
	books  *db.BookRepo
	series *db.SeriesRepo

	// cache holds the sorted group list per owner scope, keyed on
	// db.BookRepo.DuplicateScanStamp, so turning a page does not rescan the
	// whole library. See scanGroups.
	mu    sync.Mutex
	cache map[int64]duplicateScanEntry
	// scans counts full library scans; tests read it to tell a cache hit
	// from a rescan.
	scans int
	now   func() time.Time
}

// duplicateScanEntry is one cached library scan: the detected, named and
// sorted groups (thin member rows only) and the series memberships of those
// members, which the evidence reuses.
type duplicateScanEntry struct {
	stamp       string
	at          time.Time
	groups      []duplicates.Group
	memberships map[int64][]db.BookSeriesMembership
}

const (
	// duplicateScanTTL bounds how long a cached scan is trusted even when the
	// stamp has not moved, for the edits a fingerprint cannot see.
	duplicateScanTTL = 2 * time.Minute
	// duplicateScanMaxEntries bounds the cache: one entry per owner scope,
	// which is one for a single-user install and one per non-admin user with
	// tenancy on.
	duplicateScanMaxEntries = 32
)

// NewDuplicateReviewHandler builds the library-wide duplicate review handler.
// series may be nil, which disables the series-position guard exactly as it
// does for the per-author window.
func NewDuplicateReviewHandler(books *db.BookRepo, series *db.SeriesRepo) *DuplicateReviewHandler {
	return &DuplicateReviewHandler{books: books, series: series, cache: map[int64]duplicateScanEntry{}, now: time.Now}
}

// libraryDuplicatesResponse is the payload for GET /library/duplicate-candidates.
type libraryDuplicatesResponse struct {
	Groups []duplicates.Group `json:"groups"`
	// Total is the number of groups across every page; Count is this page's.
	Total  int `json:"total"`
	Count  int `json:"count"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

const (
	libraryDuplicatesDefaultLimit = 25
	libraryDuplicatesMaxLimit     = 100
)

// List implements GET /library/duplicate-candidates (#2999): every author's
// duplicate groups in one paginated list, found by the same detection the
// per-author window runs (duplicates.DetectByAuthor runs duplicates.Detect
// once per author), annotated with the same evidence.
//
// Cost on a large library: a fixed number of queries whatever its size. One
// thin query loads the id, author, title and excluded flag of every visible
// book, one loads every series membership, and the scan runs per author in
// memory. Only after the groups are sorted and the page is cut are the full
// rows and evidence loaded, and only for that page's members, in batched
// queries. Memory is therefore one thin row per book plus one page of full
// rows, never the whole library's descriptions and file lists. The sorted
// group list is cached per scope under a library change stamp (scanGroups),
// so turning pages does not repeat the scan.
//
// Scoping follows the per-author window: that window lets a caller see an
// author when auth.CheckOwnership passes on the author's owner, so this lists
// the authors auth.ListScopeUserID allows (every author with tenancy off or
// for an admin; otherwise the caller's own and unowned ones). The route is not
// admin only, because the per-author window is not. It writes nothing; the UI
// excludes rows through the existing exclude routes, which check ownership
// per book.
func (h *DuplicateReviewHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit, offset := parseLimitOffset(r, libraryDuplicatesDefaultLimit, libraryDuplicatesMaxLimit)
	scope := auth.ListScopeUserID(ctx)

	groups, memberships, err := h.scanGroups(ctx, scope)
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	total := len(groups)
	start := min(offset, total)
	end := min(start+limit, total)
	// The page is a copy: hydration and annotation rewrite members, and the
	// cached list must keep its thin rows for the next request.
	page := make([]duplicates.Group, 0, end-start)
	for _, g := range groups[start:end] {
		g.Members = append([]duplicates.Member(nil), g.Members...)
		page = append(page, g)
	}

	if err := h.hydrateMembers(ctx, page); err != nil {
		writeServerError(w, r, err)
		return
	}
	if err := annotateDuplicateGroups(ctx, h.books, page, memberships); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryDuplicatesResponse{
		Groups: page,
		Total:  total,
		Count:  len(page),
		Limit:  limit,
		Offset: offset,
	})
}

// scanGroups returns the sorted group list for one owner scope and the
// series memberships of its members, from the cache when the library's
// DuplicateScanStamp has not changed and the entry is younger than
// duplicateScanTTL, otherwise by scanning. An exclusion changes the stamp, so
// the reload after one always rescans and the group drops out.
//
// A full scan at 20,000 books costs hundreds of milliseconds and a large
// transient allocation; the stamp is one aggregate query, so paging through
// a big library costs one scan, not one per page.
func (h *DuplicateReviewHandler) scanGroups(ctx context.Context, scope int64) ([]duplicates.Group, map[int64][]db.BookSeriesMembership, error) {
	stamp, err := h.books.DuplicateScanStamp(ctx)
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	entry, ok := h.cache[scope]
	h.mu.Unlock()
	if ok && entry.stamp == stamp && h.now().Sub(entry.at) < duplicateScanTTL {
		return entry.groups, entry.memberships, nil
	}

	books, names, err := h.books.ListForDuplicateScan(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	var memberships map[int64][]db.BookSeriesMembership
	if h.series != nil {
		memberships, err = h.series.ListBookSeriesMembershipsForOwner(ctx, scope)
		if err != nil {
			return nil, nil, err
		}
	}

	groups := duplicates.DetectByAuthor(books, seriesSlotsFrom(memberships))
	for i := range groups {
		groups[i].AuthorName = names[groups[i].AuthorID]
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ni, nj := strings.ToLower(groups[i].AuthorName), strings.ToLower(groups[j].AuthorName)
		if ni != nj {
			return ni < nj
		}
		if groups[i].AuthorID != groups[j].AuthorID {
			return groups[i].AuthorID < groups[j].AuthorID
		}
		return groups[i].Key < groups[j].Key
	})

	// Keep only the memberships of grouped books: the evidence never needs
	// the rest, and the cache should not hold the whole library's links.
	var kept map[int64][]db.BookSeriesMembership
	if memberships != nil {
		kept = map[int64][]db.BookSeriesMembership{}
		for _, g := range groups {
			for _, m := range g.Members {
				if ms, ok := memberships[m.ID]; ok {
					kept[m.ID] = ms
				}
			}
		}
	}

	h.mu.Lock()
	h.scans++
	if _, exists := h.cache[scope]; !exists && len(h.cache) >= duplicateScanMaxEntries {
		for k := range h.cache {
			delete(h.cache, k)
			break
		}
	}
	h.cache[scope] = duplicateScanEntry{stamp: stamp, at: h.now(), groups: groups, memberships: kept}
	h.mu.Unlock()
	return groups, kept, nil
}

// hydrateMembers swaps the thin scan rows of one page's members for full book
// rows, so the page carries status, language, release date and file columns.
// Descriptions are blanked: the review never shows them and they are most of
// a row's weight. A member whose row vanished between the two queries keeps
// its thin row rather than failing the page.
func (h *DuplicateReviewHandler) hydrateMembers(ctx context.Context, groups []duplicates.Group) error {
	var ids []int64
	for _, g := range groups {
		for _, m := range g.Members {
			ids = append(ids, m.ID)
		}
	}
	full := make(map[int64]*models.Book, len(ids))
	const chunk = 500
	for s := 0; s < len(ids); s += chunk {
		got, err := h.books.GetByIDs(ctx, ids[s:min(s+chunk, len(ids))])
		if err != nil {
			return err
		}
		for id, b := range got {
			full[id] = b
		}
	}
	for gi := range groups {
		for mi := range groups[gi].Members {
			m := &groups[gi].Members[mi]
			if b, ok := full[m.ID]; ok {
				m.Book = *b
				m.Description = ""
			}
		}
	}
	return nil
}
