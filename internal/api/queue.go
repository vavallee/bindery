package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
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

type enrichedQueueItem struct {
	Download   models.Download
	ClientName string
	RemoteID   string
	Live       downloader.LiveStatus
	HasLive    bool
	PollFailed bool
}

type liveStatusResult struct {
	client        *models.DownloadClient
	statuses      map[string]downloader.LiveStatus
	usesTorrentID bool
	pollFailed    bool
}

func (h *QueueHandler) enrichedQueueItems(ctx context.Context) ([]enrichedQueueItem, error) {
	downloads, err := h.downloads.List(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]enrichedQueueItem, len(downloads))
	for i, d := range downloads {
		items[i] = enrichedQueueItem{
			Download: d,
			RemoteID: storedDownloadID(d),
		}
	}

	statusByClientID := make(map[int64]liveStatusResult)
	for i, item := range items {
		if item.Download.DownloadClientID == nil {
			continue
		}

		clientID := *item.Download.DownloadClientID
		result, ok := statusByClientID[clientID]
		if !ok {
			client, err := h.clients.GetByID(ctx, clientID)
			if err != nil || client == nil {
				statusByClientID[clientID] = liveStatusResult{}
				continue
			}

			result.client = client
			if client.Enabled {
				statuses, usesTorrentID, err := downloader.GetLiveStatuses(ctx, client)
				if err != nil {
					result.pollFailed = true
				} else {
					result.statuses = statuses
					result.usesTorrentID = usesTorrentID
				}
			}
			statusByClientID[clientID] = result
		}

		if result.client != nil {
			items[i].ClientName = result.client.Name
		}
		items[i].PollFailed = result.pollFailed

		if len(result.statuses) == 0 {
			continue
		}

		var remoteID string
		if result.usesTorrentID {
			if item.Download.TorrentID == nil {
				continue
			}
			remoteID = strings.ToLower(*item.Download.TorrentID)
		} else {
			if item.Download.SABnzbdNzoID == nil {
				continue
			}
			remoteID = *item.Download.SABnzbdNzoID
		}

		items[i].RemoteID = remoteID
		if status, ok := result.statuses[remoteID]; ok {
			items[i].Live = status
			items[i].HasLive = true
		}
	}

	return items, nil
}

func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	enriched, err := h.enrichedQueueItems(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	items := make([]QueueItem, len(enriched))
	for i, item := range enriched {
		items[i] = QueueItem{Download: item.Download}
		if item.HasLive {
			items[i].Percentage = item.Live.Percentage
			items[i].TimeLeft = item.Live.TimeLeft
			items[i].Speed = item.Live.Speed
		}
	}

	writeJSON(w, http.StatusOK, items)
}

type arrQueueResponse struct {
	Page          int              `json:"page,omitempty"`
	PageSize      int              `json:"pageSize,omitempty"`
	SortKey       string           `json:"sortKey,omitempty"`
	SortDirection string           `json:"sortDirection,omitempty"`
	TotalRecords  int              `json:"totalRecords"`
	Records       []arrQueueRecord `json:"records"`
}

type arrQueueRecord struct {
	ID                    int64  `json:"id"`
	BookID                int64  `json:"bookId"`
	Title                 string `json:"title"`
	Status                string `json:"status"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	Size                  int64  `json:"size"`
	SizeLeft              int64  `json:"sizeleft"`
	DownloadClient        string `json:"downloadClient"`
	DownloadID            string `json:"downloadId"`
	Protocol              string `json:"protocol"`
}

// ListArrCompatible exposes a small Sonarr/Radarr-style queue payload for
// external tools such as Harpoon. The existing /api/v1/queue UI shape remains
// unchanged.
func (h *QueueHandler) ListArrCompatible(w http.ResponseWriter, r *http.Request) {
	enriched, err := h.enrichedQueueItems(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	records := make([]arrQueueRecord, 0, len(enriched))
	for _, item := range enriched {
		if !includeArrQueueItem(item) {
			continue
		}
		bookID := int64(0)
		if item.Download.BookID != nil {
			bookID = *item.Download.BookID
		}
		records = append(records, arrQueueRecord{
			ID:                    item.Download.ID,
			BookID:                bookID,
			Title:                 item.Download.Title,
			Status:                string(item.Download.Status),
			TrackedDownloadStatus: trackedDownloadStatus(item),
			Size:                  queueItemSize(item),
			SizeLeft:              queueItemSizeLeft(item),
			DownloadClient:        item.ClientName,
			DownloadID:            item.RemoteID,
			Protocol:              item.Download.Protocol,
		})
	}

	opts := parseArrQueueOptions(r)
	sortArrQueueRecords(records, opts.sortKey, opts.sortDirection)
	total := len(records)
	records = paginateArrQueueRecords(records, opts.page, opts.pageSize)

	resp := arrQueueResponse{
		TotalRecords: total,
		Records:      records,
	}
	if opts.pageSize > 0 {
		resp.Page = opts.page
		resp.PageSize = opts.pageSize
	}
	if opts.sortKey != "" {
		resp.SortKey = opts.sortKey
		resp.SortDirection = opts.sortDirection
	}

	writeJSON(w, http.StatusOK, resp)
}

type arrQueueOptions struct {
	page          int
	pageSize      int
	sortKey       string
	sortDirection string
}

func parseArrQueueOptions(r *http.Request) arrQueueOptions {
	q := r.URL.Query()
	opts := arrQueueOptions{
		page:          1,
		sortKey:       strings.TrimSpace(q.Get("sortKey")),
		sortDirection: strings.ToLower(strings.TrimSpace(q.Get("sortDirection"))),
	}
	if opts.sortDirection != "descending" && opts.sortDirection != "desc" {
		opts.sortDirection = "ascending"
	}
	if page, err := strconv.Atoi(q.Get("page")); err == nil && page > 0 {
		opts.page = page
	}
	if pageSize, err := strconv.Atoi(q.Get("pageSize")); err == nil && pageSize > 0 {
		opts.pageSize = pageSize
	}
	return opts
}

func sortArrQueueRecords(records []arrQueueRecord, sortKey, sortDirection string) {
	sortKey = strings.ToLower(sortKey)
	if sortKey == "" {
		return
	}
	desc := sortDirection == "descending" || sortDirection == "desc"
	less := func(i, j int) bool {
		a, b := records[i], records[j]
		switch sortKey {
		case "id":
			return a.ID < b.ID
		case "title":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "status":
			return a.Status < b.Status
		case "size":
			return a.Size < b.Size
		case "sizeleft":
			return a.SizeLeft < b.SizeLeft
		case "downloadclient":
			return strings.ToLower(a.DownloadClient) < strings.ToLower(b.DownloadClient)
		case "protocol":
			return a.Protocol < b.Protocol
		default:
			return false
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})
}

func paginateArrQueueRecords(records []arrQueueRecord, page, pageSize int) []arrQueueRecord {
	if pageSize <= 0 {
		return records
	}
	if page <= 0 {
		page = 1
	}
	if len(records) == 0 {
		return []arrQueueRecord{}
	}
	if page > 1 && page-1 > (len(records)-1)/pageSize {
		return []arrQueueRecord{}
	}
	start := (page - 1) * pageSize
	end := len(records)
	if pageSize < len(records)-start {
		end = start + pageSize
	}
	return records[start:end]
}

func storedDownloadID(d models.Download) string {
	if d.TorrentID != nil && *d.TorrentID != "" {
		return strings.ToLower(*d.TorrentID)
	}
	if d.SABnzbdNzoID != nil && *d.SABnzbdNzoID != "" {
		return *d.SABnzbdNzoID
	}
	return ""
}

func includeArrQueueItem(item enrichedQueueItem) bool {
	return item.Download.Status != models.StateImported
}

func trackedDownloadStatus(item enrichedQueueItem) string {
	if item.Download.ErrorMessage != "" || liveStatusIsError(item.Live.Status) {
		return "error"
	}
	switch item.Download.Status {
	case models.StateFailed, models.StateImportFailed, models.StateImportBlocked:
		return "error"
	}
	if item.PollFailed {
		return "warning"
	}
	return "ok"
}

func liveStatusIsError(status string) bool {
	status = strings.ToLower(status)
	if strings.Contains(status, "error") || strings.Contains(status, "fail") {
		return true
	}
	if n, err := strconv.Atoi(status); err == nil {
		return n == 16 || n == 32
	}
	return false
}

func queueItemSize(item enrichedQueueItem) int64 {
	if item.HasLive && item.Live.Size > 0 {
		return item.Live.Size
	}
	return item.Download.Size
}

func queueItemSizeLeft(item enrichedQueueItem) int64 {
	if item.HasLive {
		if item.Live.SizeLeft > 0 {
			return item.Live.SizeLeft
		}
		if item.Live.Size > 0 {
			return 0
		}
		if left, ok := sizeLeftFromPercentage(item.Download.Size, item.Live.Percentage); ok {
			return left
		}
	}

	switch item.Download.Status {
	case models.StateCompleted, models.StateImportPending, models.StateImporting,
		models.StateImported, models.StateFailed, models.StateImportFailed, models.StateImportBlocked:
		return 0
	default:
		return item.Download.Size
	}
}

func sizeLeftFromPercentage(size int64, percentage string) (int64, bool) {
	if size <= 0 {
		return 0, false
	}
	percentage = strings.TrimSpace(strings.TrimSuffix(percentage, "%"))
	if percentage == "" {
		return 0, false
	}
	pct, err := strconv.ParseFloat(percentage, 64)
	if err != nil {
		return 0, false
	}
	if pct <= 0 {
		return size, true
	}
	if pct >= 100 {
		return 0, true
	}
	left := int64(math.Round(float64(size) * (100 - pct) / 100))
	if left < 0 {
		return 0, true
	}
	if left > size {
		return size, true
	}
	return left, true
}

// grabRequest is the payload for grab operations (HTTP handler and pending force-grab).
// BookID and IndexerID are optional: the free-text search UI grabs releases that
// aren't tied to any local book or indexer, and the downloads table makes these
// columns nullable to support that flow.
type grabRequest struct {
	GUID      string `json:"guid"`
	Title     string `json:"title"`
	NZBURL    string `json:"nzbUrl"`
	Size      int64  `json:"size"`
	BookID    *int64 `json:"bookId"`
	IndexerID *int64 `json:"indexerId"`
	Protocol  string `json:"protocol"`
	MediaType string `json:"mediaType"`
}

func (h *QueueHandler) Grab(w http.ResponseWriter, r *http.Request) {
	var req grabRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.GUID == "" || req.NZBURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "guid and nzbUrl required"})
		return
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
	// Coerce zero-valued BookID/IndexerID to nil. A caller that JSON-decodes
	// into an older int64-typed grabRequest, or writes an explicit {"bookId":0},
	// would otherwise insert 0 into the FK column and violate the constraint.
	bookID := req.BookID
	if bookID != nil && *bookID == 0 {
		bookID = nil
	}
	indexerID := req.IndexerID
	if indexerID != nil && *indexerID == 0 {
		indexerID = nil
	}
	dl := &models.Download{
		GUID:             req.GUID,
		BookID:           bookID,
		IndexerID:        indexerID,
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
		h.recordHistory(ctx, models.HistoryEventDownloadFailed, req.Title, bookID, map[string]any{"guid": req.GUID, "message": err.Error()})
		if h.notif != nil {
			h.notif.Send(ctx, notifier.EventDownloadFailed, map[string]any{"title": req.Title, "message": err.Error()})
		}
		return nil, fmt.Errorf("failed to send to downloader: %w", err)
	}

	if remoteID := sendRes.RemoteID; remoteID != "" {
		if sendRes.UsesTorrentID {
			normalised := strings.ToLower(remoteID)
			if err := h.downloads.SetTorrentID(ctx, dl.ID, normalised); err != nil {
				slog.Warn("failed to set torrent ID", "download_id", dl.ID, "error", err)
			}
			dl.TorrentID = &normalised
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

	h.recordHistory(ctx, models.HistoryEventGrabbed, req.Title, bookID, map[string]any{
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
