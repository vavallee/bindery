package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

type QueueHandler struct {
	downloads *db.DownloadRepo
	clients   *db.DownloadClientRepo
	books     *db.BookRepo
	history   *db.HistoryRepo
	notif     *notifier.Notifier
}

func NewQueueHandler(downloads *db.DownloadRepo, clients *db.DownloadClientRepo, books *db.BookRepo, history *db.HistoryRepo) *QueueHandler {
	return &QueueHandler{downloads: downloads, clients: clients, books: books, history: history}
}

// WithNotifier attaches a notifier so grab/failure events fire webhooks.
func (h *QueueHandler) WithNotifier(n *notifier.Notifier) *QueueHandler {
	h.notif = n
	return h
}

// QueueItem combines local download record with live downloader status.
type QueueItem struct {
	models.Download
	Percentage string `json:"percentage,omitempty"`
	TimeLeft   string `json:"timeLeft,omitempty"`
	Speed      string `json:"speed,omitempty"`
}

func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	downloads, err := h.downloads.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	items := make([]QueueItem, len(downloads))
	for i, d := range downloads {
		items[i] = QueueItem{Download: d}
	}

	type liveStatusResult struct {
		statuses      map[string]downloader.LiveStatus
		usesTorrentID bool
	}

	statusByClientID := make(map[int64]liveStatusResult)
	for i, item := range items {
		if item.DownloadClientID == nil {
			continue
		}

		clientID := *item.DownloadClientID
		result, ok := statusByClientID[clientID]
		if !ok {
			client, err := h.clients.GetByID(r.Context(), clientID)
			if err != nil || client == nil || !client.Enabled {
				statusByClientID[clientID] = liveStatusResult{}
				continue
			}

			statuses, usesTorrentID, err := downloader.GetLiveStatuses(r.Context(), client)
			if err != nil {
				statusByClientID[clientID] = liveStatusResult{}
				continue
			}

			result = liveStatusResult{statuses: statuses, usesTorrentID: usesTorrentID}
			statusByClientID[clientID] = result
		}

		if len(result.statuses) == 0 {
			continue
		}

		var remoteID string
		if result.usesTorrentID {
			if item.TorrentID == nil {
				continue
			}
			remoteID = *item.TorrentID
		} else {
			if item.SABnzbdNzoID == nil {
				continue
			}
			remoteID = *item.SABnzbdNzoID
		}

		if status, ok := result.statuses[remoteID]; ok {
			items[i].Percentage = status.Percentage
			items[i].TimeLeft = status.TimeLeft
			items[i].Speed = status.Speed
		}
	}

	writeJSON(w, http.StatusOK, items)
}

// grabRequest is the payload for grab operations (HTTP handler and pending force-grab).
type grabRequest struct {
	GUID      string `json:"guid"`
	Title     string `json:"title"`
	NZBURL    string `json:"nzbUrl"`
	Size      int64  `json:"size"`
	BookID    int64  `json:"bookId"`
	IndexerID *int64 `json:"indexerId"`
	Protocol  string `json:"protocol"`
	MediaType string `json:"mediaType"`
}

func (h *QueueHandler) Grab(w http.ResponseWriter, r *http.Request) {
	// Intermediate struct to handle optional bookId from the HTTP body.
	var body struct {
		GUID      string `json:"guid"`
		Title     string `json:"title"`
		NZBURL    string `json:"nzbUrl"`
		Size      int64  `json:"size"`
		BookID    *int64 `json:"bookId"`
		IndexerID *int64 `json:"indexerId"`
		Protocol  string `json:"protocol"`
		MediaType string `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.GUID == "" || body.NZBURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "guid and nzbUrl required"})
		return
	}

	var bookID int64
	if body.BookID != nil {
		bookID = *body.BookID
	}
	req := grabRequest{
		GUID:      body.GUID,
		Title:     body.Title,
		NZBURL:    body.NZBURL,
		Size:      body.Size,
		BookID:    bookID,
		IndexerID: body.IndexerID,
		Protocol:  body.Protocol,
		MediaType: body.MediaType,
	}

	existing, err := h.downloads.GetByGUID(r.Context(), req.GUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already grabbed"})
		return
	}

	dl, err := h.grab(r.Context(), req)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "no enabled download client") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	slog.Info("download grabbed", "title", req.Title)
	writeJSON(w, http.StatusAccepted, dl)
}

// selectClient picks the best enabled client for the given protocol and media type.
// It prefers a client whose category hints match the media type when multiple
// clients of the same protocol type are configured.
func (h *QueueHandler) selectClient(ctx context.Context, protocol, mediaType string) (*models.DownloadClient, error) {
	candidates, err := h.clients.GetEnabledByProtocol(ctx, protocol)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled %s download client configured", protocol)
	}
	return db.PickClientForMediaType(candidates, mediaType), nil
}

// grab executes the core grab logic: creates a download record and sends to the client.
// It is called by both the HTTP Grab handler and PendingHandler.Grab.
func (h *QueueHandler) grab(ctx context.Context, req grabRequest) (*models.Download, error) {
	if req.Protocol == "" {
		req.Protocol = "usenet"
	}
	client, err := h.selectClient(ctx, req.Protocol, req.MediaType)
	if err != nil || client == nil {
		return nil, fmt.Errorf("no enabled download client configured")
	}

	protocol := downloader.ProtocolForClient(client.Type)
	bookID := req.BookID
	dl := &models.Download{
		GUID:             req.GUID,
		BookID:           &bookID,
		IndexerID:        req.IndexerID,
		DownloadClientID: &client.ID,
		Title:            req.Title,
		NZBURL:           req.NZBURL,
		Size:             req.Size,
		Status:           models.StateGrabbed,
		Protocol:         protocol,
		Quality:          indexer.ParseRelease(req.Title).Format,
	}
	if err := h.downloads.Create(ctx, dl); err != nil {
		return nil, err
	}

	sendRes, err := downloader.SendDownload(ctx, client, req.NZBURL, req.Title)
	if err != nil {
		slog.Error("failed to send download", "client_type", client.Type, "error", err, "title", req.Title)
		if setErr := h.downloads.SetError(ctx, dl.ID, err.Error()); setErr != nil {
			slog.Warn("failed to persist download error", "download_id", dl.ID, "error", setErr)
		}
		h.recordHistory(ctx, models.HistoryEventDownloadFailed, req.Title, &bookID, map[string]any{"guid": req.GUID, "message": err.Error()})
		if h.notif != nil {
			h.notif.Send(ctx, notifier.EventDownloadFailed, map[string]any{"title": req.Title, "message": err.Error()})
		}
		return nil, fmt.Errorf("failed to send to downloader: %w", err)
	}

	if remoteID := sendRes.RemoteID; remoteID != "" {
		if sendRes.UsesTorrentID {
			if err := h.downloads.SetTorrentID(ctx, dl.ID, remoteID); err != nil {
				slog.Warn("failed to set torrent ID", "download_id", dl.ID, "error", err)
			}
			dl.TorrentID = &remoteID
		} else {
			if err := h.downloads.SetNzoID(ctx, dl.ID, remoteID); err != nil {
				slog.Warn("failed to set NZO ID", "download_id", dl.ID, "error", err)
			}
			dl.SABnzbdNzoID = &remoteID
		}
	}
	if err := h.downloads.UpdateStatus(ctx, dl.ID, models.StateDownloading); err != nil {
		slog.Warn("failed to update download status", "download_id", dl.ID, "status", models.StateDownloading, "error", err)
	}
	dl.Status = models.StateDownloading

	h.recordHistory(ctx, models.HistoryEventGrabbed, req.Title, &bookID, map[string]any{
		"guid":      req.GUID,
		"size":      req.Size,
		"indexerId": req.IndexerID,
	})
	if h.notif != nil {
		h.notif.Send(ctx, notifier.EventGrabbed, map[string]any{"title": req.Title, "size": req.Size})
	}
	return dl, nil
}

// recordHistory is a helper to write a history event, swallowing errors.
func (h *QueueHandler) recordHistory(ctx context.Context, eventType, sourceTitle string, bookID *int64, data any) {
	if h.history == nil {
		return
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal history data", "event_type", eventType, "error", err)
		return
	}
	evt := &models.HistoryEvent{
		BookID:      bookID,
		EventType:   eventType,
		SourceTitle: sourceTitle,
		Data:        string(dataJSON),
	}
	if err := h.history.Create(ctx, evt); err != nil {
		slog.Warn("failed to record history", "error", err)
	}
}

func (h *QueueHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	downloads, err := h.downloads.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var target *models.Download
	for _, d := range downloads {
		if d.ID == id {
			target = &d
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
		return
	}

	if target.DownloadClientID != nil {
		client, err := h.clients.GetByID(r.Context(), *target.DownloadClientID)
		if err == nil && client != nil {
			if err := downloader.RemoveDownload(r.Context(), client, target, true); err != nil {
				slog.Warn("failed to remove download from client", "download_id", target.ID, "client_id", client.ID, "error", err)
			}
		} else if err != nil {
			slog.Warn("failed to load download client", "download_id", target.ID, "client_id", *target.DownloadClientID, "error", err)
		}
	}

	if target.BookID != nil {
		book, err := h.books.GetByID(r.Context(), *target.BookID)
		if err != nil {
			slog.Warn("failed to load book for download delete", "download_id", target.ID, "book_id", *target.BookID, "error", err)
		} else if book != nil && (book.Status == models.BookStatusDownloading || book.Status == models.BookStatusDownloaded) {
			book.Status = models.BookStatusWanted
			if err := h.books.Update(r.Context(), book); err != nil {
				slog.Warn("failed to reset book status after download delete", "download_id", target.ID, "book_id", book.ID, "error", err)
			}
		}
	}

	if err := h.downloads.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
