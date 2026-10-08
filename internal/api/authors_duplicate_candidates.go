package api

import (
	"context"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/duplicates"
)

// duplicateCandidatesResponse is the payload for GET
// /author/{id}/duplicate-candidates (#1970).
type duplicateCandidatesResponse struct {
	AuthorID int64              `json:"authorId"`
	Groups   []duplicates.Group `json:"groups"`
	Count    int                `json:"count"`
}

// DuplicateCandidates implements GET /author/{id}/duplicate-candidates
// (#1970): a read-only, human-reviewed duplicate-title report for one
// author's catalogue. It scans every book row — excluded rows included, so a
// group whose members have already been excluded can be re-shown as "all
// excluded" instead of vanishing — keeps only groups with at least two
// non-excluded members (duplicates.Detect), and returns them annotated with
// the rule(s) that matched so the UI can explain each group in plain language.
//
// Each group also carries the review evidence from #2999 (files, year,
// language, ISBN/ASIN, series, agreement and conflict signals, and a keeper
// when exactly one row has files). See annotateDuplicateGroups.
//
// The endpoint writes nothing: acting on a candidate is the existing exclude
// route (PUT /book/{id}/exclude, or POST /book/bulk with action "exclude"),
// which the UI calls only after a human confirms. That is the whole safety
// model of #1970 — aggressive detection, human in the loop, no silent merges.
//
// It also passes each book's series memberships to Scan (#1970 review), so
// the substring rule can be suppressed between two books that are different,
// known positions in the same series — a long first title that doubles as
// the series name ("Foundation" vs "Foundation and Empire") would otherwise
// group with its own sequels. h.series is optional (nil in tests that don't
// need it); without it, this simply falls back to the pre-review behaviour.
func (h *AuthorHandler) DuplicateCandidates(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadOwnedAuthor(w, r)
	if !ok {
		return
	}
	books, err := h.books.ListByAuthorIncludingExcluded(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	var memberships map[int64][]db.BookSeriesMembership
	if h.series != nil {
		memberships, err = h.series.ListBookSeriesMembershipsByAuthor(r.Context(), author.ID)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
	}
	groups := duplicates.Detect(books, seriesSlotsFrom(memberships))
	for i := range groups {
		groups[i].AuthorID = author.ID
		groups[i].AuthorName = author.Name
	}
	if err := annotateDuplicateGroups(r.Context(), h.books, groups, memberships); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, duplicateCandidatesResponse{
		AuthorID: author.ID,
		Groups:   groups,
		Count:    len(groups),
	})
}

// seriesSlotsFrom reduces series memberships to the facts Scan reads. A nil
// map in gives a nil map out, which Scan treats as "no series data".
func seriesSlotsFrom(memberships map[int64][]db.BookSeriesMembership) map[int64][]duplicates.SeriesSlot {
	if memberships == nil {
		return nil
	}
	out := make(map[int64][]duplicates.SeriesSlot, len(memberships))
	for bookID, ms := range memberships {
		for _, m := range ms {
			out[bookID] = append(out[bookID], duplicates.SeriesSlot{
				SeriesID: m.SeriesID,
				Position: m.Position,
			})
		}
	}
	return out
}

// annotateDuplicateGroups loads the review evidence for every member of the
// given groups and runs duplicates.Annotate on each group. It is shared by the
// per-author and library-wide views so both explain a group identically.
// Files and edition identifiers come from one batched query per chunk of
// member IDs (db.ListDuplicateEvidence); series come from the memberships the
// caller already loaded for detection, so there is no per-book query.
func annotateDuplicateGroups(ctx context.Context, books *db.BookRepo, groups []duplicates.Group, memberships map[int64][]db.BookSeriesMembership) error {
	var ids []int64
	for _, g := range groups {
		for _, m := range g.Members {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	raw, err := books.ListDuplicateEvidence(ctx, ids)
	if err != nil {
		return err
	}
	for gi := range groups {
		evidence := make(map[int64]duplicates.Evidence, len(groups[gi].Members))
		for _, m := range groups[gi].Members {
			row := raw[m.ID]
			files := make([]duplicates.FileRef, 0, len(row.Files))
			for _, f := range row.Files {
				files = append(files, duplicates.FileRef{Kind: f.Format, Path: f.Path})
			}
			var series []duplicates.SeriesEvidence
			for _, s := range memberships[m.ID] {
				series = append(series, duplicates.SeriesEvidence{
					SeriesID: s.SeriesID,
					Title:    s.SeriesTitle,
					Position: s.Position,
				})
			}
			evidence[m.ID] = duplicates.NewEvidence(m.Book, files, row.ISBNs, row.ASINs, series)
		}
		duplicates.Annotate(&groups[gi], evidence)
	}
	return nil
}
