package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type absReviewImporter interface {
	ImportReview(ctx context.Context, cfg abs.ImportConfig, item abs.NormalizedLibraryItem) (abs.ImportItemResult, error)
	ReviewFileMapping(ctx context.Context, cfg abs.ImportConfig, item abs.NormalizedLibraryItem) abs.ReviewFileMapping
}

type ABSReviewHandler struct {
	reviews  *db.ABSReviewItemRepo
	importer absReviewImporter
	loadCfg  func(context.Context) ABSStoredConfig
}

type absReviewListResponse struct {
	Items  []models.ABSReviewItem `json:"items"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

func NewABSReviewHandler(reviews *db.ABSReviewItemRepo, importer absReviewImporter, loadCfg func(context.Context) ABSStoredConfig) *ABSReviewHandler {
	return &ABSReviewHandler{reviews: reviews, importer: importer, loadCfg: loadCfg}
}

func (h *ABSReviewHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 50, 100)
	items, total, err := h.reviews.ListByStatusPaginated(r.Context(), "pending", limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg := h.loadCfg(r.Context())
	runCfg := abs.ImportConfig{
		SourceID:  abs.DefaultSourceID,
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		LibraryID: strings.TrimSpace(cfg.LibraryID),
		PathRemap: cfg.PathRemap,
		Label:     cfg.Label,
		Enabled:   cfg.Enabled,
	}
	for idx := range items {
		var payload abs.NormalizedLibraryItem
		if err := json.Unmarshal([]byte(items[idx].PayloadJSON), &payload); err != nil {
			continue
		}
		mapping := h.importer.ReviewFileMapping(r.Context(), runCfg, payload)
		items[idx].FileMappingFound = mapping.Found
		items[idx].FileMappingMessage = mapping.Message
	}
	if items == nil {
		items = []models.ABSReviewItem{}
	}
	writeJSON(w, http.StatusOK, absReviewListResponse{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

func (h *ABSReviewHandler) Approve(w http.ResponseWriter, r *http.Request) {
	item, err := h.reviewItemFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review item not found"})
		return
	}
	// Reject re-approval of an item that has already left the pending state.
	// Without this, a stale tab or a double-click re-runs ImportReview and
	// imports the book a second time.
	if item.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "review item is not pending (status: " + item.Status + ")"})
		return
	}

	var payload abs.NormalizedLibraryItem
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stored review payload is invalid"})
		return
	}
	applyReviewResolution(item, &payload)

	cfg := h.loadCfg(r.Context())
	runCfg := abs.ImportConfig{
		SourceID:  strings.TrimSpace(item.SourceID),
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		LibraryID: strings.TrimSpace(item.LibraryID),
		PathRemap: cfg.PathRemap,
		Label:     cfg.Label,
		Enabled:   cfg.Enabled,
	}
	if err := runCfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Mark approved before importing so that a crash between the two writes
	// leaves an approved-but-not-imported item (visible and recoverable via
	// status reset) rather than an imported item whose status stays "pending"
	// and triggers a duplicate import on the next approval attempt.
	if err := h.reviews.UpdateStatus(r.Context(), item.ID, "approved"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := h.importer.ImportReview(r.Context(), runCfg, payload); err != nil {
		_ = h.reviews.UpdateStatus(r.Context(), item.ID, "pending")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.reviews.GetByID(r.Context(), item.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *ABSReviewHandler) ResolveAuthor(w http.ResponseWriter, r *http.Request) {
	item, err := h.reviewItemFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review item not found"})
		return
	}
	var req struct {
		ForeignAuthorID string `json:"foreignAuthorId"`
		AuthorName      string `json:"authorName"`
		ApplyTo         string `json:"applyTo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.ApplyTo) != "" && strings.TrimSpace(req.ApplyTo) != "same_author" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "applyTo must be same_author"})
		return
	}
	updated, err := h.reviews.ResolveAuthorForPrimary(r.Context(), item.SourceID, item.LibraryID, item.PrimaryAuthor, req.ForeignAuthorID, req.AuthorName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

func (h *ABSReviewHandler) ResolveBook(w http.ResponseWriter, r *http.Request) {
	item, err := h.reviewItemFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review item not found"})
		return
	}
	var req struct {
		ForeignBookID string `json:"foreignBookId"`
		Title         string `json:"title"`
		EditedTitle   string `json:"editedTitle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := h.reviews.ResolveBook(r.Context(), item.ID, req.ForeignBookID, req.Title, req.EditedTitle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.reviews.GetByID(r.Context(), item.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *ABSReviewHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	item, err := h.reviewItemFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review item not found"})
		return
	}
	if err := h.reviews.UpdateStatus(r.Context(), item.ID, "dismissed"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.reviews.GetByID(r.Context(), item.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *ABSReviewHandler) reviewItemFromRequest(r *http.Request) (*models.ABSReviewItem, error) {
	raw := strings.TrimSpace(chi.URLParam(r, "id"))
	if raw == "" {
		return nil, errors.New("review item id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("invalid review item id")
	}
	return h.reviews.GetByID(r.Context(), id)
}

func applyReviewResolution(item *models.ABSReviewItem, payload *abs.NormalizedLibraryItem) {
	if item == nil || payload == nil {
		return
	}
	payload.ResolvedAuthorForeignID = strings.TrimSpace(item.ResolvedAuthorForeignID)
	payload.ResolvedAuthorName = strings.TrimSpace(item.ResolvedAuthorName)
	payload.ResolvedBookForeignID = strings.TrimSpace(item.ResolvedBookForeignID)
	payload.ResolvedBookTitle = strings.TrimSpace(item.ResolvedBookTitle)
	payload.EditedTitle = strings.TrimSpace(item.EditedTitle)
	if payload.EditedTitle != "" {
		payload.Title = payload.EditedTitle
	}
}
