package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// manualBookTextMax bounds every free text field of a hand-made book, the
// same bound a series title has.
const manualBookTextMax = seriesTitleMaxLength

// AddManualBook adds a book that no metadata provider knows, or knows too
// badly to use (#3840): a short story, an anthology, a fan translation. The
// caller types the title and author, and optionally a series, a position and
// a format. Nothing is fetched from any provider.
//
// The book gets a synthetic "manual:book:" foreign id, like a hand-made
// series. No provider owns that prefix, so a metadata refresh has nothing to
// fetch for it, and an author catalogue sync that lists a work with the same
// title leaves the row alone instead of binding the work to it (see
// FetchAuthorBooks). Re-bind (POST /book/{id}/rebind) binds it to a provider
// work later if the user wants one.
//
// The author is, in order: the library author authorId names, a library
// author whose name matches authorName, or a new author made by hand under a
// "manual:author:" id. No provider answers for that id, so the author has no
// catalogue of its own and the scheduled profile refresh skips it. Adding the
// real author later (Add Author, or Link metadata on its page) relinks it in
// place, the same as a Calibre or Audiobookshelf placeholder.
//
// Answers 201 with the book, 409 with the existing row when the author already
// has a book of that title, 404 when authorId names no author the caller can
// see, and 400 for a missing or oversized field.
func (h *AuthorHandler) AddManualBook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title          string `json:"title"`
		AuthorID       int64  `json:"authorId"`
		AuthorName     string `json:"authorName"`
		SeriesTitle    string `json:"seriesTitle"`
		SeriesPosition string `json:"seriesPosition"`
		// MediaType is ebook, audiobook or both. Empty takes the
		// default.media_type setting.
		MediaType   string `json:"mediaType"`
		SearchOnAdd bool   `json:"searchOnAdd"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.AuthorName = strings.TrimSpace(req.AuthorName)
	req.SeriesTitle = strings.TrimSpace(req.SeriesTitle)
	req.SeriesPosition = strings.TrimSpace(req.SeriesPosition)
	for _, field := range []struct{ name, value string }{
		{"title", req.Title}, {"authorName", req.AuthorName}, {"seriesTitle", req.SeriesTitle}, {"seriesPosition", req.SeriesPosition},
	} {
		if containsControlChar(field.value) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": field.name + " must not contain control characters or line breaks"})
			return
		}
	}
	if msg := validateManualBookRequest(req.Title, req.AuthorID, req.AuthorName, req.SeriesTitle, req.SeriesPosition, req.MediaType); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	// Valid, so collapse runs of spaces, including no-break spaces, to one.
	req.Title = collapseSpaces(req.Title)
	req.AuthorName = collapseSpaces(req.AuthorName)
	req.SeriesTitle = collapseSpaces(req.SeriesTitle)

	ctx := r.Context()
	scopeID := auth.ListScopeUserID(ctx)

	author, authorCreated, err := h.resolveManualBookAuthor(ctx, req.AuthorID, req.AuthorName)
	if err != nil {
		if errors.Is(err, errManualBookAuthorNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
			return
		}
		writeServerError(w, r, err)
		return
	}
	bookCreated := false
	if authorCreated {
		defer h.cleanupOrphanIfNoBooks(author, &bookCreated)
	}

	// The same work identity every other add uses (books.dedup_key), so a
	// second hand-made copy, or a hand-made copy of a provider book the
	// author already has, answers with the existing row as Add Book does
	// (#1227). A different position in the same series is a different book.
	var seriesRefs []models.SeriesRef
	if req.SeriesTitle != "" {
		seriesRefs = []models.SeriesRef{{Title: req.SeriesTitle, Position: req.SeriesPosition}}
	}
	//
	// Only an exact key match counts here. Add Book also treats a subtitle or
	// prefix variant as the same work, which is right for a provider record
	// but wrong for a title the user typed on purpose: "Mistborn: Secret
	// History" is not "Mistborn".
	candidates, err := h.books.FindAllByAuthorAndDedupKeyVisibleTo(ctx, author.ID, req.Title, scopeID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	wantKey := indexer.CanonicalDedupKey(req.Title)
	for i := range candidates {
		if indexer.CanonicalDedupKey(candidates[i].Title) != wantKey {
			continue
		}
		if !h.directInsertSeriesConflict(ctx, candidates[i].ID, seriesRefs) {
			h.writeExistingBookConflict(w, &candidates[i], false)
			return
		}
	}

	mediaType := req.MediaType
	if mediaType == "" {
		mediaType = h.resolveDefaultMediaType(ctx)
	}
	book := &models.Book{
		// A random id: a clock reading can repeat across two requests.
		ForeignID:        models.ManualBookForeignIDPrefix + uuid.NewString(),
		AuthorID:         author.ID,
		Title:            req.Title,
		SortTitle:        req.Title,
		Status:           models.BookStatusWanted,
		Monitored:        true,
		AnyEditionOK:     true,
		Genres:           []string{},
		MediaType:        mediaType,
		MetadataProvider: models.ManualMetadataProvider,
		// Tenancy (#1457): the author's owner, or the caller's library scope
		// under an unowned author, as addBookCore stamps it.
		OwnerUserID: author.OwnerUserID,
	}
	if book.OwnerUserID == 0 {
		book.OwnerUserID = scopeID
	}
	// The title is typed by hand, so lock it: an Audiobookshelf or Calibre
	// import, a library scan or a refresh that matches the book by title must
	// not rewrite it. There is no lock for a series title or position, and the
	// dialog takes no language or genres.
	book.LockField(models.BookFieldTitle)
	if err := h.books.Create(ctx, book); err != nil {
		writeServerError(w, r, err)
		return
	}
	bookCreated = true

	if req.SeriesTitle != "" {
		h.linkManualBookSeries(ctx, book, author.ID, req.SeriesTitle, req.SeriesPosition)
	}

	// A file the library already holds for this title is bound now, as for
	// any other new wanted book, so search on add does not download a book
	// the user has (#940).
	fileFound := handleNewWantedBook(ctx, h.books, h.series, h.finder, *book, author.Name)
	if fresh, err := h.books.GetByID(ctx, book.ID); err == nil && fresh != nil {
		book = fresh
	}
	if req.SearchOnAdd && !fileFound {
		h.startAddSearch(book)
	}
	slog.Info("added a book by hand", "bookId", book.ID, "title", book.Title,
		"author", author.Name, "authorId", author.ID, "authorCreated", authorCreated)
	writeBook(w, http.StatusCreated, book)
}

// validateManualBookRequest returns the 400 message for a hand-made book
// request, or "" when it is acceptable. The strings are already trimmed.
func validateManualBookRequest(title string, authorID int64, authorName, seriesTitle, seriesPosition, mediaType string) string {
	switch {
	case title == "":
		return "title is required"
	case authorID < 0:
		return "authorId must be a positive integer"
	case authorID == 0 && authorName == "":
		return "authorId or authorName is required"
	case authorID == 0 && textutil.AuthorSlug(authorName) == "":
		return "authorName must contain a letter or a digit"
	case seriesPosition != "" && seriesTitle == "":
		return "seriesPosition needs a seriesTitle"
	case seriesPosition != "" && !manualSeriesPositionRe.MatchString(seriesPosition):
		return "seriesPosition must be a number such as 1 or 1.5, or a range such as 2-3"
	}
	for _, field := range []struct{ name, value string }{
		{"title", title}, {"authorName", authorName}, {"seriesTitle", seriesTitle}, {"seriesPosition", seriesPosition},
	} {
		if len(field.value) > manualBookTextMax {
			return fmt.Sprintf("%s is too long", field.name)
		}
	}
	switch mediaType {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
	default:
		return "mediaType must be 'ebook', 'audiobook', or 'both'"
	}
	return ""
}

// manualSeriesPositionRe is a series position as the series code reads one: a
// number with an optional decimal part, or a range of two such numbers.
var manualSeriesPositionRe = regexp.MustCompile(`^\d{1,4}(?:\.\d{1,2})?(?:\s*[-\x{2013}]\s*\d{1,4}(?:\.\d{1,2})?)?$`)

// containsControlChar reports a C0 or C1 control character, which includes
// tabs, line breaks and NUL.
func containsControlChar(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// collapseSpaces trims s and replaces each run of white space with one space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var errManualBookAuthorNotFound = errors.New("manual book: author not found")

// resolveManualBookAuthor finds the author a hand-made book is filed under, or
// makes one, and reports whether it made one. See AddManualBook for the order.
// Every lookup is scoped like the author list the caller sees.
func (h *AuthorHandler) resolveManualBookAuthor(ctx context.Context, authorID int64, name string) (*models.Author, bool, error) {
	scopeID := auth.ListScopeUserID(ctx)
	if authorID > 0 {
		author, err := h.authors.GetByIDForUser(ctx, authorID, scopeID)
		if err != nil {
			return nil, false, err
		}
		if author == nil {
			return nil, false, errManualBookAuthorNotFound
		}
		return author, false, nil
	}
	// Under tenancy an admin lists every user's authors, so a name match
	// could file the admin's book in another user's library. A typed name
	// looks only at the caller's own authors and the unowned ones, and makes
	// the admin's own author otherwise. An explicit authorId above still
	// reaches any author, as the admin may open any author page.
	nameScope := scopeID
	if nameScope == 0 && auth.EnforceTenancy() {
		nameScope = auth.UserIDFromContext(ctx)
	}
	if existing := h.findLibraryAuthorByNameForUser(ctx, name, nameScope); existing != nil {
		return existing, false, nil
	}

	// The id is built from the name, like the synthetic Google Books and DNB
	// author ids, so a second book typed under the same spelling reuses the
	// row even where the name match above found nothing.
	foreignID := models.ManualAuthorForeignIDPrefix + textutil.AuthorSlug(name)
	if existing, err := h.authors.GetByAnyForeignIDForUser(ctx, foreignID, nameScope); err != nil {
		return nil, false, err
	} else if existing != nil {
		return existing, false, nil
	}
	def := models.DefaultMetadataProfileID
	author := &models.Author{
		ForeignID:         foreignID,
		Name:              name,
		SortName:          sortName(name),
		MetadataProvider:  models.ManualMetadataProvider,
		MetadataProfileID: &def,
		// Unmonitored, as addBookCore creates the author a book add brings
		// in: the user asked for one book, not for the author.
		Monitored: false,
	}
	db.ApplyAuthorCreateDefaults(ctx, h.settings, author)
	if err := h.authors.CreateForUser(ctx, author, auth.UserIDFromContext(ctx)); err != nil {
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
			return nil, false, err
		}
		// A concurrent request made the same author first.
		existing, getErr := h.authors.GetByAnyForeignIDForUser(ctx, foreignID, nameScope)
		if getErr != nil {
			return nil, false, getErr
		}
		if existing == nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	return author, true, nil
}

// linkManualBookSeries files a hand-made book under the series titled title,
// at position. It reuses a series of that title, preferring one this author is
// already in and otherwise any the caller can see, and makes a hand-made
// series when there is none, so a story added to "The Horus Heresy" joins the
// series the other books are in rather than starting a second one. Best
// effort: the book exists either way, and a failure is logged.
func (h *AuthorHandler) linkManualBookSeries(ctx context.Context, book *models.Book, authorID int64, title, position string) {
	if h.series == nil {
		return
	}
	series, err := h.findManualBookSeries(ctx, authorID, title)
	if err != nil {
		slog.Warn("manual book: could not look up the series", "bookId", book.ID, "series", title, "error", err)
		return
	}
	if series == nil {
		if series, err = h.series.CreateManual(ctx, title); err != nil {
			slog.Warn("manual book: could not create the series", "bookId", book.ID, "series", title, "error", err)
			return
		}
	}
	// The book is new, so this is its only membership and the primary one.
	if err := h.series.LinkBook(ctx, series.ID, book.ID, position, true); err != nil {
		slog.Warn("manual book: could not link the series", "bookId", book.ID, "seriesId", series.ID, "error", err)
	}
}

// manualBookSeriesSearchLimit bounds the library wide title lookup. Only
// exact title matches are used, and they rank first.
const manualBookSeriesSearchLimit = 10

// findManualBookSeries returns the series titled title that a hand-made book
// should join, or nil when there is none. See linkManualBookSeries.
func (h *AuthorHandler) findManualBookSeries(ctx context.Context, authorID int64, title string) (*models.Series, error) {
	scopeID := auth.ListScopeUserID(ctx)
	want := textutil.FoldForSearch(title)
	authorSeries, err := h.series.ListByAuthorAndUser(ctx, authorID, scopeID)
	if err != nil {
		return nil, err
	}
	for i := range authorSeries {
		if textutil.FoldForSearch(authorSeries[i].Title) == want {
			return &authorSeries[i], nil
		}
	}
	found, err := h.series.SearchTitles(ctx, title, scopeID, manualBookSeriesSearchLimit)
	if err != nil {
		return nil, err
	}
	for i := range found {
		if textutil.FoldForSearch(found[i].Title) == want {
			return &found[i], nil
		}
	}
	return nil, nil
}
