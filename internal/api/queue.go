package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"fmt"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

type QueueHandler struct {
	downloads *db.DownloadRepo
	clients   *db.DownloadClientRepo
	books     *db.BookRepo
	history   *db.HistoryRepo
}

func NewQueueHandler(downloads *db.DownloadRepo, clients *db.DownloadClientRepo, books *db.BookRepo, history *db.HistoryRepo) *QueueHandler {
	return &QueueHandler{downloads: downloads, clients: clients, books: books, history: history}
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

func (h *QueueHandler) Grab(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GUID      string `json:"guid"`
		Title     string `json:"title"`
		NZBURL    string `json:"nzbUrl"`
		Size      int64  `json:"size"`
		BookID    *int64 `json:"bookId"`
		IndexerID *int64 `json:"indexerId"`
		Protocol  string `json:"protocol"`
		MediaType string `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.GUID == "" || req.NZBURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "guid and nzbUrl required"})
		return
	}
	if req.Protocol == "" {
		req.Protocol = "usenet"
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

	client, err := h.selectClient(r.Context(), req.Protocol, req.MediaType)
	if err != nil || client == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no enabled download client configured"})
		return
	}

	protocol := downloader.ProtocolForClient(client.Type)
	dl := &models.Download{
		GUID:             req.GUID,
		BookID:           req.BookID,
		IndexerID:        req.IndexerID,
		DownloadClientID: &client.ID,
		Title:            req.Title,
		NZBURL:           req.NZBURL,
		Size:             req.Size,
		Status:           models.DownloadStatusQueued,
		Protocol:         protocol,
		Quality:          indexer.ParseRelease(req.Title).Format,
	}
	if err := h.downloads.Create(r.Context(), dl); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	sendRes, err := downloader.SendDownload(r.Context(), client, req.NZBURL, req.Title)
	if err != nil {
		slog.Error("failed to send download", "client_type", client.Type, "error", err, "title", req.Title)
		if setErr := h.downloads.SetError(r.Context(), dl.ID, err.Error()); setErr != nil {
			slog.Warn("failed to persist download error", "download_id", dl.ID, "error", setErr)
		}
		h.recordHistory(r.Context(), models.HistoryEventDownloadFailed, req.Title, req.BookID, map[string]interface{}{"guid": req.GUID, "message": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to send to downloader: " + err.Error()})
		return
	}

	if remoteID := sendRes.RemoteID; remoteID != "" {
		if sendRes.UsesTorrentID {
			if err := h.downloads.SetTorrentID(r.Context(), dl.ID, remoteID); err != nil {
				slog.Warn("failed to set torrent ID", "download_id", dl.ID, "error", err)
			}
			dl.TorrentID = &remoteID
		} else {
			if err := h.downloads.SetNzoID(r.Context(), dl.ID, remoteID); err != nil {
				slog.Warn("failed to set NZO ID", "download_id", dl.ID, "error", err)
			}
			dl.SABnzbdNzoID = &remoteID
		}
	}
	if err := h.downloads.UpdateStatus(r.Context(), dl.ID, models.DownloadStatusDownloading); err != nil {
		slog.Warn("failed to update download status", "download_id", dl.ID, "status", models.DownloadStatusDownloading, "error", err)
	}
	dl.Status = models.DownloadStatusDownloading

	h.recordHistory(r.Context(), models.HistoryEventGrabbed, req.Title, req.BookID, map[string]interface{}{
		"guid":      req.GUID,
		"size":      req.Size,
		"indexerId": req.IndexerID,
	})

	slog.Info("download grabbed", "title", req.Title, "client", client.Type)
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

// recordHistory is a helper to write a history event, swallowing errors.
func (h *QueueHandler) recordHistory(ctx context.Context, eventType, sourceTitle string, bookID *int64, data interface{}) {
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
