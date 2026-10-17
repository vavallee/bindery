package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/telemetry"
)

// createAuthorParams is everything an Add Author needs, independent of HTTP.
// The Create handler fills it from the request body; request approval fills
// it itself. Ownership is not a field: the core reads the acting user from
// ctx, exactly as the handler always has.
type createAuthorParams struct {
	ForeignID             string
	Name                  string
	QualityProfileID      *int64
	MetadataProfileID     *int64
	RootFolderID          *int64
	AudiobookRootFolderID *int64
	Monitored             bool
	MonitorMode           *string
	MonitorLatestCount    *int
	// Settable at add time as of the Add Author redesign: before this it
	// could only be changed on the author afterwards, so "catalogue this
	// author once and never let a refresh grow it" was not expressible
	// when adding. Same validation as Update.
	MonitorNewItems *string
	SearchOnAdd     bool
	MediaType       string
	// SkipCatalogueSync creates or relinks the author without starting its
	// catalogue sync. False keeps the handler's behaviour.
	//
	// The sync is everything that happens to the author after the row is
	// written, so skipping it skips all of: the author profile refresh
	// (description, image, disambiguation, ratings), the Calibre relink, the
	// secondary provider identities, the catalogue works fetch and its
	// Audible supplement, every book row the sync would create along with
	// the library file check for each, the sync summary shown on the author
	// page, and the search on add, which only ever runs inside that sync.
	// Because SearchOnAdd would silently do nothing, the core refuses the
	// combination with errCreateAuthorSearchNeedsSync.
	SkipCatalogueSync bool
}

// createAuthorResult is what an Add Author produced.
type createAuthorResult struct {
	// Author is the created author, or the existing author relinked to the
	// requested upstream record.
	Author *models.Author
	// Created is true when a new author row was inserted (the handler's 201)
	// and false when an existing author was relinked instead (its 200).
	Created bool
}

// Refusals from createAuthorCore that carry no data. The Create handler maps
// each to a 400 with the body it has always answered with.
var (
	errCreateAuthorFieldsRequired         = errors.New("create author: foreignAuthorId and authorName required")
	errCreateAuthorInvalidMonitorNewItems = errors.New("create author: invalid monitorNewItems")
	// errCreateAuthorSearchNeedsSync refuses SearchOnAdd with
	// SkipCatalogueSync. The search runs inside the catalogue sync, so the
	// pair would accept a search request and never search. Only a non HTTP
	// caller can set SkipCatalogueSync, so the handler never returns it.
	errCreateAuthorSearchNeedsSync = errors.New("create author: searchOnAdd requires the catalogue sync")
)

var createAuthorErrorResponses = []struct {
	err  error
	body string
}{
	{errCreateAuthorFieldsRequired, "foreignAuthorId and authorName required"},
	{errCreateAuthorInvalidMonitorNewItems, "invalid monitorNewItems"},
	{errCreateAuthorSearchNeedsSync, "searchOnAdd requires the catalogue sync"},
}

// createAuthorOptionError is an invalid monitorMode or monitorLatestCount.
// The handler answers 400 with the wrapped error's text.
type createAuthorOptionError struct {
	Err error
}

func (e *createAuthorOptionError) Error() string { return e.Err.Error() }
func (e *createAuthorOptionError) Unwrap() error { return e.Err }

// createAuthorLookupError is a failed upstream author fetch. The handler
// answers 502 with the wrapped error's text.
type createAuthorLookupError struct {
	Err error
}

func (e *createAuthorLookupError) Error() string { return e.Err.Error() }
func (e *createAuthorLookupError) Unwrap() error { return e.Err }

// authorConflictError is a 409: the author already exists, or its name
// resolves to one that does. Canonical, when set, is the existing author the
// handler returns so the client can offer to open it.
type authorConflictError struct {
	Canonical *models.Author
	Message   string
}

func (e *authorConflictError) Error() string { return "create author: " + e.Message }

// writeCreateAuthorError answers a createAuthorCore error with the status
// and body the Create handler returned before the core was split out.
func (h *AuthorHandler) writeCreateAuthorError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *authorConflictError
	if errors.As(err, &conflict) {
		h.writeCanonicalAuthorConflict(w, conflict.Canonical, conflict.Message)
		return
	}
	var option *createAuthorOptionError
	if errors.As(err, &option) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": option.Err.Error()})
		return
	}
	var lookup *createAuthorLookupError
	if errors.As(err, &lookup) {
		writeUpstreamError(w, lookup.Err)
		return
	}
	for _, resp := range createAuthorErrorResponses {
		if errors.Is(err, resp.err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": resp.body})
			return
		}
	}
	writeServerError(w, r, err)
}

// createAuthorCore adds one author and starts its catalogue sync, or relinks
// an existing local author to the requested upstream record. It is Create
// without the HTTP layer.
func (h *AuthorHandler) createAuthorCore(ctx context.Context, req createAuthorParams) (createAuthorResult, error) {
	if req.ForeignID == "" || req.Name == "" {
		return createAuthorResult{}, errCreateAuthorFieldsRequired
	}
	if req.SearchOnAdd && req.SkipCatalogueSync {
		return createAuthorResult{}, errCreateAuthorSearchNeedsSync
	}
	monitorMode, monitorLatestCount, err := h.resolveCreateMonitorOptions(ctx, req.MonitorMode, req.MonitorLatestCount)
	monitorNewItems := models.DefaultAuthorMonitorNewItems
	if req.MonitorNewItems != nil {
		v := strings.TrimSpace(*req.MonitorNewItems)
		if !models.IsAuthorMonitorNewItemsValid(v) {
			return createAuthorResult{}, errCreateAuthorInvalidMonitorNewItems
		}
		monitorNewItems = v
	}
	if err != nil {
		return createAuthorResult{}, &createAuthorOptionError{Err: err}
	}

	// Check if already exists — use user-scoped lookup so this agrees with the
	// author list, which filters by owner_user_id. A global GetByForeignID
	// would block re-creation of authors orphaned under a different user ID.
	userID := auth.UserIDFromContext(ctx)
	existing, _ := h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignID, userID)
	if existing != nil {
		return createAuthorResult{}, &authorConflictError{Canonical: existing, Message: "author already exists"}
	}

	author, err := h.fetchAuthorForCreate(ctx, req.ForeignID, req.Name)
	if err != nil {
		return createAuthorResult{}, &createAuthorLookupError{Err: err}
	}
	if author.ForeignID != "" {
		if existing, _ := h.authors.GetByAnyForeignIDForUser(ctx, author.ForeignID, userID); existing != nil {
			return createAuthorResult{}, &authorConflictError{Canonical: existing, Message: "author already exists"}
		}
	}
	if canonical, ambiguous, err := h.findCanonicalAuthorMatch(ctx, req.Name, author.Name); err != nil {
		return createAuthorResult{}, err
	} else if ambiguous {
		return createAuthorResult{}, &authorConflictError{Message: "author name resolves ambiguously — merge manually"}
	} else if canonical != nil {
		if canRelinkAuthorToUpstream(canonical) {
			if err := h.relinkExistingAuthorToUpstream(ctx, canonical, author, req.Name, req.Monitored, monitorMode, monitorLatestCount, monitorNewItems, req.QualityProfileID, req.MetadataProfileID, req.RootFolderID, req.AudiobookRootFolderID); err != nil {
				if isAuthorIdentityConflict(err) {
					return createAuthorResult{}, &authorConflictError{Message: "upstream author already exists locally"}
				}
				return createAuthorResult{}, err
			}
			mediaType := req.MediaType
			if mediaType == "" {
				mediaType = h.resolveDefaultMediaType(ctx)
			}
			// Finish mutating `canonical` (description clean-up) BEFORE spawning
			// the async catalogue+profile refresh. fetchAuthorBooksAsync snapshots
			// the author at spawn time and the goroutine now reads/writes profile
			// fields (Description, ImageURL, ...); cleaning afterwards would race
			// the snapshot read against this write (see fetchAuthorBooksAsync).
			cleanAuthorDescription(canonical)
			h.stampProviderMismatch(canonical)
			if !req.SkipCatalogueSync {
				h.startAddSync(canonical, mediaType, req.SearchOnAdd)
			}
			return createAuthorResult{Author: canonical}, nil
		}
		return createAuthorResult{}, &authorConflictError{Canonical: canonical, Message: "author name already resolves to an existing author — confirm merge"}
	}
	applyAuthorCreateOptions(author, req.Monitored, monitorMode, monitorLatestCount, req.QualityProfileID, req.MetadataProfileID, req.RootFolderID, req.AudiobookRootFolderID)
	author.MonitorNewItems = monitorNewItems

	if err := h.authors.CreateForUser(ctx, author, auth.UserIDFromContext(ctx)); err != nil {
		slog.Error("create author failed", "foreign_id", req.ForeignID, "error", err)
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict) {
			if existing, _ := h.authors.GetByAnyForeignIDForUser(ctx, req.ForeignID, userID); existing != nil {
				return createAuthorResult{}, &authorConflictError{Canonical: existing, Message: "author already exists"}
			}
			if existing, _ := h.authors.GetByAnyForeignIDForUser(ctx, author.ForeignID, userID); existing != nil {
				return createAuthorResult{}, &authorConflictError{Canonical: existing, Message: "author already exists"}
			}
			return createAuthorResult{}, &authorConflictError{Message: "author already exists"}
		}
		return createAuthorResult{}, err
	}
	h.recordAuthorCreateAlias(ctx, author, req.Name)

	// Persist any OL alternate names as alias rows so non-latin primary names
	// (e.g. "村上春樹") get their latin-script alternates ("Haruki Murakami")
	// indexed for release-name matching.
	h.saveAlternateNames(ctx, author)

	// Resolve effective media type for books created under this author:
	// explicit request value wins, else the global default.media_type
	// setting, else ebook (backwards compat).
	mediaType := req.MediaType
	if mediaType == "" {
		mediaType = h.resolveDefaultMediaType(ctx)
	}

	// Clean the description BEFORE spawning the async refresh: the goroutine
	// snapshots `author` and now reads/writes its profile fields (Description,
	// ImageURL, ...). Cleaning after the spawn would race the snapshot read
	// against this write (see fetchAuthorBooksAsync).
	cleanAuthorDescription(author)
	h.stampProviderMismatch(author)

	// Fetch and store books for this author. Always populate the catalogue;
	// pass searchOnAdd so FetchAuthorBooks knows whether to also queue grabs.
	if !req.SkipCatalogueSync {
		h.startAddSync(author, mediaType, req.SearchOnAdd)
	}

	telemetry.MarkFirst(ctx, h.settings, telemetry.SettingFirstAuthorAt)
	return createAuthorResult{Author: author, Created: true}, nil
}

// startAddSync starts the catalogue sync an add runs, counted in runningSyncs
// before the add answers (#3058). Counted only once its goroutine started, a
// page opened straight after the add could read syncInProgress false and
// show the author as having no books while the sync was still fetching.
func (h *AuthorHandler) startAddSync(author *models.Author, mediaType string, autoSearch bool) {
	h.runningSyncs.start(author.ID)
	h.fetchAuthorBooksAsync(author, catalogueSyncOptions{autoSearch: autoSearch, mediaType: mediaType, enrichProfile: true, syncClaimed: true})
}
