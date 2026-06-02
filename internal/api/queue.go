package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// queueClientPollConcurrency caps how many downloader clients are polled
// in parallel when rendering the queue page. queueClientPollTimeout caps
// how long any one client gets before its result is dropped from the
// payload (with the page marked partial so the UI can surface that). The
// list endpoint is hot — every user with the queue page open hits it
// every 5 seconds — and one slow qBit shouldn't gate the whole render.
//
// queueClientPollTimeout is a var rather than a const so the slow-client
// integration test can shorten it without sleeping a second per case.
const queueClientPollConcurrency = 4

var queueClientPollTimeout = 1 * time.Second

var errAlreadyGrabbed = errors.New("already grabbed")

type QueueHandler struct {
	downloads            *db.DownloadRepo
	clients              *db.DownloadClientRepo
	books                *db.BookRepo
	history              *db.HistoryRepo
	notif                *notifier.Notifier
	downloadDir          string
	audiobookDownloadDir string
}

func NewQueueHandler(downloads *db.DownloadRepo, clients *db.DownloadClientRepo, books *db.BookRepo, history *db.HistoryRepo) *QueueHandler {
	return &QueueHandler{downloads: downloads, clients: clients, books: books, history: history}
}

// WithNotifier attaches a notifier so grab/failure events fire webhooks.
func (h *QueueHandler) WithNotifier(n *notifier.Notifier) *QueueHandler {
	h.notif = n
	return h
}

// WithStoragePaths attaches the process-level download roots used when sending
// torrent clients an explicit save path.
func (h *QueueHandler) WithStoragePaths(downloadDir, audiobookDownloadDir string) *QueueHandler {
	h.downloadDir = downloadDir
	h.audiobookDownloadDir = audiobookDownloadDir
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

// queuePollDiagnostic records a downloader client whose live-status poll
// did not complete inside queueClientPollTimeout. The handler surfaces
// these on the queue response so users can tell a stale percentage from
// a fresh one when one of their clients is dragging.
type queuePollDiagnostic struct {
	ClientID int64  `json:"clientId"`
	Name     string `json:"name,omitempty"`
	Message  string `json:"message,omitempty"`
}

func (h *QueueHandler) enrichedQueueItems(ctx context.Context) ([]enrichedQueueItem, []queuePollDiagnostic, error) {
	// Per-user scoping (D3): when EnforceTenancy is on and the request carries
	// a non-admin user identity, restrict the queue to the caller's downloads.
	// Admin / API-key / disabled-auth fall through to the unscoped List, which
	// matches CheckOwnership's semantics in the Get/Delete paths.
	var (
		downloads []models.Download
		err       error
	)
	if auth.EnforceTenancy() && auth.UserRoleFromContext(ctx) != "admin" {
		if uid := auth.UserIDFromContext(ctx); uid != 0 {
			downloads, err = h.downloads.ListByUser(ctx, uid)
		} else {
			downloads, err = h.downloads.List(ctx)
		}
	} else {
		downloads, err = h.downloads.List(ctx)
	}
	if err != nil {
		return nil, nil, err
	}

	items := make([]enrichedQueueItem, len(downloads))
	for i, d := range downloads {
		items[i] = enrichedQueueItem{
			Download: d,
			RemoteID: storedDownloadID(d),
		}
	}

	// Collect every distinct client id referenced by the current downloads,
	// preserving first-seen order so a flaky client always slots into the
	// same fan-out bucket from one request to the next.
	clientIDs := make([]int64, 0)
	seen := make(map[int64]bool)
	for _, item := range items {
		if item.Download.DownloadClientID == nil {
			continue
		}
		cid := *item.Download.DownloadClientID
		if seen[cid] {
			continue
		}
		seen[cid] = true
		clientIDs = append(clientIDs, cid)
	}

	// Resolve each distinct client once. We do this serially (cheap DB
	// hits) before the parallel poll so the heavy fan-out is purely
	// network-bound and the slow-path is isolated to GetLiveStatuses.
	clients := make([]*models.DownloadClient, len(clientIDs))
	for i, cid := range clientIDs {
		client, err := h.clients.GetByID(ctx, cid)
		if err != nil || client == nil {
			continue
		}
		clients[i] = client
	}

	// Fan out the per-client live-status polls in parallel with a hard
	// per-client deadline. Without this, a single unreachable qBit dragged
	// the whole queue render (which every queue-page open hits every 5s)
	// until the client's own TCP timeout fired.
	pollResults := concurrency.RunBoundedWithTimeout(
		ctx,
		clients,
		queueClientPollConcurrency,
		queueClientPollTimeout,
		func(ctx context.Context, client *models.DownloadClient) (liveStatusResult, error) {
			res := liveStatusResult{client: client}
			if client == nil || !client.Enabled {
				return res, nil
			}
			statuses, usesTorrentID, err := downloader.GetLiveStatuses(ctx, client)
			if err != nil {
				res.pollFailed = true
				return res, err
			}
			res.statuses = statuses
			res.usesTorrentID = usesTorrentID
			return res, nil
		},
	)

	statusByClientID := make(map[int64]liveStatusResult, len(clientIDs))
	var diagnostics []queuePollDiagnostic
	for i, cid := range clientIDs {
		r := pollResults[i]
		client := clients[i]
		var res liveStatusResult
		switch {
		case r.Done:
			res = r.Value
		case client != nil && r.Err != nil:
			// Real upstream error from GetLiveStatuses (not a deadline).
			res = liveStatusResult{client: client, pollFailed: true}
			diagnostics = append(diagnostics, queuePollDiagnostic{
				ClientID: cid,
				Name:     client.Name,
				Message:  r.Err.Error(),
			})
		case client != nil:
			// Per-call deadline fired or parent ctx canceled before this
			// client ran. Treat as a soft failure so the row still renders
			// with whatever live data we already had (none, in this case)
			// and the partial flag tells the UI not to trust freshness.
			res = liveStatusResult{client: client, pollFailed: true}
			diagnostics = append(diagnostics, queuePollDiagnostic{
				ClientID: cid,
				Name:     client.Name,
				Message:  "live-status poll timed out",
			})
		default:
			// Client could not even be resolved; leave statusByClientID
			// empty so per-item enrichment falls back to the stored row.
			res = liveStatusResult{}
		}
		statusByClientID[cid] = res
	}

	for i, item := range items {
		if item.Download.DownloadClientID == nil {
			continue
		}
		result := statusByClientID[*item.Download.DownloadClientID]

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

	return items, diagnostics, nil
}

// queueListResponse wraps the queue items in an envelope so the handler
// can surface partial-data warnings when a downloader client failed or
// timed out during enrichment. Items remains the array clients have
// always rendered; partial/staleClients lets a future UI iteration show
// "1 of 3 download clients did not respond" without breaking the older
// flat-array shape any more than necessary.
type queueListResponse struct {
	Items        []QueueItem           `json:"items"`
	Partial      bool                  `json:"partial,omitempty"`
	StaleClients []queuePollDiagnostic `json:"staleClients,omitempty"`
}

func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	enriched, diagnostics, err := h.enrichedQueueItems(r.Context())
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

	writeJSON(w, http.StatusOK, queueListResponse{
		Items:        items,
		Partial:      len(diagnostics) > 0,
		StaleClients: diagnostics,
	})
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
	enriched, _, err := h.enrichedQueueItems(r.Context())
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
	if item.Download.ErrorMessage != "" || downloader.LiveStatusIsError(item.Live.Status) {
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
		models.StateImported, models.StateFailed, models.StateImportFailed,
		models.StateImportBlocked, models.StateImportExternal:
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

	dl, err := h.grab(r.Context(), req)
	if errors.Is(err, errAlreadyGrabbed) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already grabbed"})
		return
	}
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

func (h *QueueHandler) RetryImport(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Per-user scoping (D3): refuse to operate on someone else's download.
	// Return 404 not 403 — leaking "this exists but is not yours" tells an
	// attacker that the id space is enumerable.
	owner, exists, err := h.downloads.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
		return
	}

	accepted, found, err := h.downloads.ResetImportRetry(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
		return
	}
	if !accepted {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "download is not in importFailed state"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
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
	existing, err := h.downloads.GetByGUID(ctx, req.GUID)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Status != models.StateFailed {
		return nil, errAlreadyGrabbed
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
	editionID := (*int64)(nil)
	indexerFlags := ""
	if existing != nil {
		if bookID == nil {
			bookID = existing.BookID
		}
		if indexerID == nil {
			indexerID = existing.IndexerID
		}
		editionID = existing.EditionID
		indexerFlags = existing.IndexerFlags
	}
	dl := &models.Download{
		GUID:             req.GUID,
		BookID:           bookID,
		EditionID:        editionID,
		IndexerID:        indexerID,
		DownloadClientID: &client.ID,
		Title:            req.Title,
		NZBURL:           req.NZBURL,
		Size:             req.Size,
		Status:           models.StateGrabbed,
		Protocol:         protocol,
		Quality:          indexer.ParseRelease(req.Title).Format,
		IndexerFlags:     indexerFlags,
	}
	if existing != nil {
		dl.ID = existing.ID
		ok, err := h.downloads.RetryFailed(ctx, dl)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errAlreadyGrabbed
		}
	} else if err := h.downloads.Create(ctx, dl); err != nil {
		return nil, err
	}

	sendRes, err := downloader.SendDownload(ctx, client, req.NZBURL, req.Title, downloader.SendOptions{
		MediaType:            req.MediaType,
		DownloadDir:          h.downloadDir,
		AudiobookDownloadDir: h.audiobookDownloadDir,
	})
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

	// Per-user scoping (D3): cheaply resolve owner first so we 404 before
	// loading the full list. The legacy List+linear-scan stays for the
	// happy path because it sources Download state Delete still needs.
	owner, exists, err := h.downloads.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download not found"})
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

	// Removing an item from the queue keeps the downloaded data on disk by
	// default: for torrent clients this also preserves the seed. Destroying
	// the files is opt-in via `?deleteFiles=true`, mirroring book Delete.
	deleteFiles := r.URL.Query().Get("deleteFiles") == "true"

	if target.DownloadClientID != nil {
		client, err := h.clients.GetByID(r.Context(), *target.DownloadClientID)
		if err == nil && client != nil {
			if err := downloader.RemoveDownload(r.Context(), client, target, deleteFiles); err != nil {
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
