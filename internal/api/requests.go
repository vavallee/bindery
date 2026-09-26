package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// SettingRequestsMaxPendingPerUser caps how many requests one requester may
// have awaiting a decision (security review item S4).
const SettingRequestsMaxPendingPerUser = "requests.max_pending_per_user"

// defaultRequestsMaxPendingPerUser applies when the setting is unset.
const defaultRequestsMaxPendingPerUser = 25

// Body caps. A create carries three short fields; an approval a handful of
// ids and flags.
const (
	requestCreateMaxBody  = 4 << 10
	requestApproveMaxBody = 8 << 10
)

// requestAdder is the part of AuthorHandler an approval needs: the two add
// cores, and nothing else. *AuthorHandler satisfies it.
//
// It is an interface rather than a *AuthorHandler field for two reasons. The
// requests handler cannot reach into the author handler's repositories,
// provider or searcher, so the only way an approval adds anything is the same
// code path the Add dialog runs. And the approval's own logic (the claim, the
// revalidation, the owner in the context, releasing the claim on failure) is
// tested against a fake adder, without a metadata provider.
type requestAdder interface {
	addBookCore(ctx context.Context, req addBookParams) (addBookResult, error)
	createAuthorCore(ctx context.Context, req createAuthorParams) (createAuthorResult, error)
}

// requestMetadata is the provider lookup a create uses to learn the title and
// author itself (security review item S2). *metadata.Aggregator satisfies it.
type requestMetadata interface {
	GetBook(ctx context.Context, foreignID string) (*models.Book, error)
	GetAuthor(ctx context.Context, foreignID string) (*models.Author, error)
}

// requestPayload is the add a request replays at approval. The server builds
// it from the provider's record at create time; the requester supplies only
// the kind, the foreign id and the media type. Approval revalidates it against
// the row before use.
type requestPayload struct {
	Kind            string `json:"kind"`
	ForeignID       string `json:"foreignId"`
	ForeignAuthorID string `json:"foreignAuthorId,omitempty"`
	AuthorName      string `json:"authorName,omitempty"`
	MediaType       string `json:"mediaType,omitempty"`
}

// RequestHandler serves /requests: a requester's own requests and the read
// only library projection, and the admin queue with approve and decline.
type RequestHandler struct {
	requests *db.RequestRepo
	books    *db.BookRepo
	authors  *db.AuthorRepo
	settings *db.SettingsRepo
	meta     requestMetadata
	adder    requestAdder
	notif    requestNotifier
	users    *db.UserRepo
	// providerLimit is spent by Create for a requester when the guard did not
	// already charge the request, so the limit holds even if the allow list
	// entry for POST /requests loses its limit.
	providerLimit *auth.RequesterLimiter
	// notified suppresses a repeat requestCreated for the same request within
	// requestNotifyWindow: create and withdraw in a loop is one webhook.
	notified *recentKeys
	// ownerNotifies caps requestCreated webhooks per requester per hour, so
	// cycling through different ids cannot flood the admin's channel.
	ownerNotifies *ownerBudget
	// activeRenewers counts running claim renewal goroutines.
	activeRenewers atomic.Int32
}

// requestNotifyWindow is how long a requestCreated for one (owner, kind,
// foreign id) suppresses another.
const requestNotifyWindow = time.Hour

// requestNotifyMaxKeys bounds the suppression map.
const requestNotifyMaxKeys = 4096

// recentKeys remembers when each key last fired, for window, holding at most
// max keys. When full it drops expired keys, then the oldest.
type recentKeys struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	window time.Duration
	max    int
	now    func() time.Time
}

func newRecentKeys(window time.Duration, max int) *recentKeys {
	return &recentKeys{seen: make(map[string]time.Time), window: window, max: max, now: time.Now}
}

// first reports whether key has not fired within the window, and records it.
func (k *recentKeys) first(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if at, ok := k.seen[key]; ok && now.Sub(at) < k.window {
		return false
	}
	if _, ok := k.seen[key]; !ok && len(k.seen) >= k.max {
		oldestKey, oldest, found := "", time.Time{}, false
		for key2, at := range k.seen {
			if now.Sub(at) >= k.window {
				delete(k.seen, key2)
				continue
			}
			if !found || at.Before(oldest) {
				oldestKey, oldest, found = key2, at, true
			}
		}
		if len(k.seen) >= k.max && oldestKey != "" {
			delete(k.seen, oldestKey)
		}
	}
	k.seen[key] = now
	return true
}

// requestNotifyPerOwner is how many requestCreated webhooks one requester can
// cause in requestNotifyWindow. Requests past it are stored and shown in the
// queue as usual; only the webhook is skipped.
const requestNotifyPerOwner = 10

// ownerBudget allows each owner limit events per window, holding at most max
// owners. An owner over the limit is logged once per window.
type ownerBudget struct {
	mu     sync.Mutex
	owners map[int64]*ownerWindow
	limit  int
	window time.Duration
	max    int
	now    func() time.Time
}

type ownerWindow struct {
	start  time.Time
	count  int
	logged bool
}

func newOwnerBudget(limit int, window time.Duration, max int) *ownerBudget {
	return &ownerBudget{owners: make(map[int64]*ownerWindow), limit: limit, window: window, max: max, now: time.Now}
}

// allow spends one event for owner and reports whether it is within the
// limit, and whether this is the first refusal in the window (to log once).
func (b *ownerBudget) allow(owner int64) (ok, firstRefusal bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	w, found := b.owners[owner]
	if found && now.Sub(w.start) >= b.window {
		found = false
	}
	if !found {
		if _, exists := b.owners[owner]; !exists && len(b.owners) >= b.max {
			for id, other := range b.owners {
				if now.Sub(other.start) >= b.window {
					delete(b.owners, id)
				}
			}
			if len(b.owners) >= b.max {
				for id := range b.owners {
					delete(b.owners, id)
					break
				}
			}
		}
		w = &ownerWindow{start: now}
		b.owners[owner] = w
	}
	if w.count < b.limit {
		w.count++
		return true, false
	}
	first := !w.logged
	w.logged = true
	return false, first
}

// WithProviderLimiter replaces the provider bucket Create spends from.
func (h *RequestHandler) WithProviderLimiter(l *auth.RequesterLimiter) *RequestHandler {
	h.providerLimit = l
	return h
}

// requestNotifier sends the requestCreated event. *notifier.Notifier
// satisfies it.
type requestNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// WithNotifier sends notifier.EventRequestCreated for every new request.
func (h *RequestHandler) WithNotifier(n requestNotifier) *RequestHandler {
	h.notif = n
	return h
}

// requestCreatedPayload is the requestCreated event payload (security review
// item S3). Titles come from a metadata provider that anyone can edit, and a
// username from whoever made the account, and the payload lands in a chat
// channel: every text field goes through notifier.SafeText, which strips
// control and invisible characters, caps the length, and neutralises mentions,
// Slack escapes and markdown links.
func requestCreatedPayload(req models.LibraryRequest, username string) map[string]interface{} {
	return map[string]interface{}{
		"title":     notifier.SafeText(req.Title, requestTitleMaxRunes),
		"author":    notifier.SafeText(req.AuthorName, requestAuthorMaxRunes),
		"username":  notifier.SafeText(username, requestUsernameMaxRunes),
		"kind":      req.Kind,
		"mediaType": req.MediaType,
		"requestId": req.ID,
	}
}

// notifyCreated sends the event without holding up the requester's response:
// webhook targets can be slow, and the request is already stored.
func (h *RequestHandler) notifyCreated(ctx context.Context, req models.LibraryRequest) {
	if h.notif == nil {
		return
	}
	if h.notified != nil && !h.notified.first(fmt.Sprintf("%d|%s|%s", req.OwnerUserID, req.Kind, req.ForeignID)) {
		return
	}
	if h.ownerNotifies != nil {
		if ok, firstRefusal := h.ownerNotifies.allow(req.OwnerUserID); !ok {
			if firstRefusal {
				slog.Warn("requests: requestCreated webhooks for this requester paused for the rest of the hour",
					"owner_user_id", req.OwnerUserID, "limit", requestNotifyPerOwner)
			}
			return
		}
	}
	username := ""
	if h.users != nil {
		if u, err := h.users.GetByID(ctx, req.OwnerUserID); err == nil && u != nil {
			username = u.Username
		}
	}
	payload := requestCreatedPayload(req, username)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	go func() {
		defer cancel()
		h.notif.Send(sendCtx, notifier.EventRequestCreated, payload)
	}()
}

// NewRequestHandler wires the requests API. adder is normally the
// *AuthorHandler the Add dialog uses. users is the account directory Create
// reads per-account auto approval from.
func NewRequestHandler(requests *db.RequestRepo, books *db.BookRepo, authors *db.AuthorRepo, settings *db.SettingsRepo, users *db.UserRepo, meta requestMetadata, adder requestAdder) *RequestHandler {
	return &RequestHandler{
		requests: requests, books: books, authors: authors, settings: settings, users: users, meta: meta, adder: adder,
		providerLimit: auth.DefaultRequesterProviderLimiter(),
		notified:      newRecentKeys(requestNotifyWindow, requestNotifyMaxKeys),
		ownerNotifies: newOwnerBudget(requestNotifyPerOwner, requestNotifyWindow, requestNotifyMaxKeys),
	}
}

// requestResponse is a request as the API shows it. Status "approving" is
// reported as "pending": the claim is an implementation detail.
type requestResponse struct {
	ID            int64      `json:"id"`
	Kind          string     `json:"kind"`
	ForeignID     string     `json:"foreignId"`
	MediaType     string     `json:"mediaType"`
	Title         string     `json:"title"`
	AuthorName    string     `json:"authorName"`
	Status        string     `json:"status"`
	DeclineReason string     `json:"declineReason,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	DecidedAt     *time.Time `json:"decidedAt,omitempty"`
	// Fulfilled is true once what was asked for is in the library as a file:
	// the book imported, or at least one of the author's books imported.
	Fulfilled bool `json:"fulfilled"`
	// For an approved author request: that author's books, and how many are
	// imported.
	BooksTotal    int `json:"booksTotal,omitempty"`
	BooksImported int `json:"booksImported,omitempty"`

	// Admin queue only.
	Username       string `json:"username,omitempty"`
	ResultBookID   *int64 `json:"resultBookId,omitempty"`
	ResultAuthorID *int64 `json:"resultAuthorId,omitempty"`
}

type requestListResponse struct {
	Items  []requestResponse `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

func toRequestResponse(req models.LibraryRequest, admin bool) requestResponse {
	status := req.Status
	if status == models.RequestStatusApproving {
		status = models.RequestStatusPending
	}
	out := requestResponse{
		ID:            req.ID,
		Kind:          req.Kind,
		ForeignID:     req.ForeignID,
		MediaType:     req.MediaType,
		Title:         req.Title,
		AuthorName:    req.AuthorName,
		Status:        status,
		DeclineReason: req.DeclineReason,
		CreatedAt:     req.CreatedAt,
		DecidedAt:     req.DecidedAt,
	}
	if req.Status == models.RequestStatusApproved {
		switch req.Kind {
		case models.RequestKindBook:
			out.Fulfilled = req.BookStatus == models.BookStatusImported
		case models.RequestKindAuthor:
			out.BooksTotal, out.BooksImported = req.AuthorBooks, req.AuthorBooksImported
			out.Fulfilled = req.AuthorBooksImported > 0
		}
	}
	if admin {
		out.Username = req.OwnerUsername
		out.ResultBookID = req.ResultBookID
		out.ResultAuthorID = req.ResultAuthorID
	}
	return out
}

// decodeStrict decodes one JSON object from a body capped at limit bytes and
// refuses any key that is not exactly one of v's json field names, so a
// client cannot smuggle options a route does not accept (security review item
// S2). The match is exact: encoding/json folds case, so "Kind" or a second
// "kind" spelled differently would otherwise land in the same field. An empty
// body is an error unless allowEmpty, in which case v keeps its zero value.
func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, allowEmpty bool, v any) error {
	if r.Body == nil {
		if allowEmpty {
			return nil
		}
		return io.EOF
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if allowEmpty {
			return nil
		}
		return io.EOF
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var keys map[string]json.RawMessage
	if err := dec.Decode(&keys); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after JSON object")
	}
	allowed := jsonFieldNames(v)
	for k := range keys {
		if !allowed[k] {
			return fmt.Errorf("field %q not accepted", k)
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	return strict.Decode(v)
}

// jsonFieldNames is the set of json names of the struct v points to.
func jsonFieldNames(v any) map[string]bool {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		names[name] = true
	}
	return names
}

func validRequestMediaType(m string) bool {
	switch m {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
		return true
	}
	return false
}

// Create records a request. POST /requests with exactly
// {"kind": "book"|"author", "foreignId": "...", "mediaType": ""|"ebook"|"audiobook"|"both"}.
//
// The server looks the record up at the metadata provider and stores the
// title and author it reports; nothing else in the stored payload comes from
// the client. 409 with a sentence when the item is already in the library or
// already requested by this user, 429 when the user's pending cap is reached.
func (h *RequestHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := auth.UserIDFromContext(ctx)
	if owner == 0 {
		writeErr(w, http.StatusForbidden, "Sign in as a person to make a request.")
		return
	}
	var body struct {
		Kind      string `json:"kind"`
		ForeignID string `json:"foreignId"`
		MediaType string `json:"mediaType"`
	}
	if err := decodeStrict(w, r, requestCreateMaxBody, false, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "A request takes only kind, foreignId and mediaType.")
		return
	}
	// Every create can reach the metadata provider, and a failed lookup
	// stores nothing, so the pending cap cannot stand in for a rate limit.
	if ok, retry := h.providerLimit.AllowRequester(ctx); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeErr(w, http.StatusTooManyRequests, "Too many requests at once. Try again shortly.")
		return
	}
	body.ForeignID = strings.TrimSpace(body.ForeignID)
	if body.Kind != models.RequestKindBook && body.Kind != models.RequestKindAuthor {
		writeErr(w, http.StatusBadRequest, "kind must be book or author.")
		return
	}
	if !validForeignID(body.ForeignID) {
		writeErr(w, http.StatusBadRequest, "foreignId is missing or not a provider id.")
		return
	}
	if !validRequestMediaType(body.MediaType) {
		writeErr(w, http.StatusBadRequest, "mediaType must be ebook, audiobook, both or empty.")
		return
	}

	existing, err := h.requests.GetForOwner(ctx, owner, body.Kind, body.ForeignID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	reopen := false
	if existing != nil {
		switch existing.Status {
		case models.RequestStatusPending, models.RequestStatusApproving:
			writeErr(w, http.StatusConflict, "You have already requested this. It is waiting for an admin.")
			return
		case models.RequestStatusDeclined:
			writeErr(w, http.StatusConflict, "You requested this before and it was declined.")
			return
		}
		// Approved earlier. Asking again only makes sense if it has since
		// left the library; the in library check below answers otherwise.
		reopen = true
	}
	if msg, err := h.alreadyInLibrary(ctx, body.Kind, body.ForeignID); err != nil {
		writeServerError(w, r, err)
		return
	} else if msg != "" {
		writeErr(w, http.StatusConflict, msg)
		return
	}

	// This count is only a fast refusal before the provider call. The cap
	// that holds under concurrency is enforced by the insert itself.
	limit := h.maxPendingPerUser(ctx)
	capReached := func() {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("You have %d requests waiting for a decision, which is the limit. Wait for some to be decided before asking for more.", limit))
	}
	if pending, err := h.requests.CountPendingByOwner(ctx, owner); err != nil {
		writeServerError(w, r, err)
		return
	} else if pending >= limit {
		capReached()
		return
	}

	req, status, msg := h.buildRequest(ctx, owner, body.Kind, body.ForeignID, body.MediaType)
	if req == nil {
		writeErr(w, status, msg)
		return
	}
	// The provider may answer with a canonical record whose id differs from
	// the one asked for, and that record may be in the library already.
	if req.resolvedID != "" && req.resolvedID != body.ForeignID {
		if msg, err := h.alreadyInLibrary(ctx, req.Kind, req.resolvedID); err != nil {
			writeServerError(w, r, err)
			return
		} else if msg != "" {
			writeErr(w, http.StatusConflict, msg)
			return
		}
	}

	if reopen {
		if err := h.requests.Reopen(ctx, existing.ID, owner, req.MediaType, req.PayloadJSON, limit); err != nil {
			if errors.Is(err, db.ErrRequestCapReached) {
				capReached()
				return
			}
			if errors.Is(err, db.ErrRequestNotPending) {
				writeErr(w, http.StatusConflict, "You have already requested this.")
				return
			}
			writeServerError(w, r, err)
			return
		}
		existing, err = h.requests.GetByID(ctx, existing.ID)
		if err != nil || existing == nil {
			writeServerError(w, r, fmt.Errorf("reload reopened request: %w", err))
			return
		}
		h.writeCreated(ctx, w, *existing, owner)
		return
	}
	if err := h.requests.Create(ctx, &req.LibraryRequest, limit); err != nil {
		if errors.Is(err, db.ErrRequestCapReached) {
			capReached()
			return
		}
		if errors.Is(err, db.ErrRequestExists) {
			writeErr(w, http.StatusConflict, "You have already requested this.")
			return
		}
		writeServerError(w, r, err)
		return
	}
	h.writeCreated(ctx, w, req.LibraryRequest, owner)
}

// writeCreated answers a create with the request as it stands. If the owner's
// account auto-approves requests, the request is approved first and the answer
// is the approved row. An auto approval that fails or is refused logs and
// leaves the request pending, so a provider hiccup cannot lose what the
// requester asked for; the admin queue still sees it.
//
// requestCreated is sent only when the request stays pending, so an admin is
// not pinged for an item that was added without them (#2718).
func (h *RequestHandler) writeCreated(ctx context.Context, w http.ResponseWriter, req models.LibraryRequest, owner int64) {
	if h.autoApproveRequests(ctx, owner) {
		switch done, status, msg, err := h.autoApprove(ctx, req, owner); {
		case err != nil:
			slog.Error("requests: auto approval failed, leaving the request pending", "id", req.ID, "error", err)
		case msg != "":
			slog.Warn("requests: auto approval refused, leaving the request pending", "id", req.ID, "status", status, "reason", msg)
		case done != nil:
			writeJSON(w, http.StatusCreated, toRequestResponse(*done, false))
			return
		}
	}
	h.notifyCreated(ctx, req)
	writeJSON(w, http.StatusCreated, toRequestResponse(req, false))
}

// autoApproveRequests reports whether owner's account approves its requests
// without a human. A missing user repo, an unreadable row or a lookup error
// all answer false: an auto approval that cannot be confirmed is not run.
func (h *RequestHandler) autoApproveRequests(ctx context.Context, owner int64) bool {
	if h.users == nil {
		return false
	}
	u, err := h.users.GetByID(ctx, owner)
	if err != nil || u == nil {
		return false
	}
	return u.RequestsAutoApprove
}

// autoApprove claims a request just created for owner and runs the same add an
// admin's Approve runs. decided_by stays NULL, so the queue can tell an
// automatic approval from a human one. The approval body is the approve form's
// own starting point — search on add for a book, the ordinary catalogue sync
// for an author — because there is no form to fill in.
//
// The claim carries the owner's daily auto-approve quota, drawn from the same
// requests.max_pending_per_user limit as the pending cap: once the owner has
// had that many requests auto approved since midnight UTC, the claim matches
// nothing and the request stays pending, so the queue is the only way past the
// quota just as it is when the setting is off. The count is part of the claim,
// not a separate read, so a burst of creates cannot all pass it.
func (h *RequestHandler) autoApprove(ctx context.Context, req models.LibraryRequest, owner int64) (*models.LibraryRequest, int, string, error) {
	claimed, err := h.requests.ClaimAutoApprove(ctx, req.ID, owner, startOfUTCDay(time.Now()), h.maxPendingPerUser(ctx))
	if errors.Is(err, db.ErrRequestAutoApproveQuota) {
		// The request is left pending for an admin, exactly as it would be
		// with the setting off. Not a failure, so no log and no refusal.
		return nil, 0, "", nil
	}
	if err != nil {
		return nil, 0, "", fmt.Errorf("claim request %d for auto approval: %w", req.ID, err)
	}
	if claimed == nil {
		return nil, 0, "", fmt.Errorf("claim request %d for auto approval: no row", req.ID)
	}
	if claimed.OwnerUserID != owner {
		return nil, 0, "", fmt.Errorf("request %d is not owned by user %d", req.ID, owner)
	}
	search := claimed.Kind == models.RequestKindBook
	return h.runClaimedApproval(ctx, claimed, approveBody{SearchOnAdd: &search})
}

// startOfUTCDay is midnight UTC of t's day, where the auto-approve quota
// resets. UTC matches the timestamps the request rows are written with.
func startOfUTCDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// builtRequest is a request row plus the payload it serialises.
type builtRequest struct {
	models.LibraryRequest
	payload requestPayload
	// resolvedID is the id the provider's record carries, when it has one.
	resolvedID string
}

// buildRequest looks the item up at the provider and builds the row and its
// payload. On failure it returns nil with the status and sentence to answer.
func (h *RequestHandler) buildRequest(ctx context.Context, owner int64, kind, foreignID, mediaType string) (*builtRequest, int, string) {
	if h.meta == nil {
		return nil, http.StatusServiceUnavailable, "Metadata lookups are not available right now."
	}
	out := &builtRequest{}
	out.OwnerUserID = owner
	out.Kind = kind
	out.ForeignID = foreignID
	out.MediaType = mediaType
	out.payload = requestPayload{Kind: kind, ForeignID: foreignID, MediaType: mediaType}

	switch kind {
	case models.RequestKindBook:
		book, err := h.meta.GetBook(ctx, foreignID)
		if err != nil {
			slog.Warn("requests: book lookup failed", "error", err)
			return nil, http.StatusBadGateway, "Could not look this book up right now. Try again shortly."
		}
		if book == nil || cleanRequestText(book.Title, requestTitleMaxRunes) == "" {
			return nil, http.StatusNotFound, "No book with that id was found."
		}
		out.Title = cleanRequestText(book.Title, requestTitleMaxRunes)
		if validForeignID(book.ForeignID) {
			out.resolvedID = book.ForeignID
		}
		if book.Author != nil {
			out.AuthorName = cleanRequestText(book.Author.Name, requestAuthorMaxRunes)
			if validForeignID(book.Author.ForeignID) {
				out.payload.ForeignAuthorID = book.Author.ForeignID
			}
		}
		out.payload.AuthorName = out.AuthorName
	case models.RequestKindAuthor:
		author, err := h.meta.GetAuthor(ctx, foreignID)
		if err != nil {
			slog.Warn("requests: author lookup failed", "error", err)
			return nil, http.StatusBadGateway, "Could not look this author up right now. Try again shortly."
		}
		if author == nil || cleanRequestText(author.Name, requestAuthorMaxRunes) == "" {
			return nil, http.StatusNotFound, "No author with that id was found."
		}
		out.Title = cleanRequestText(author.Name, requestAuthorMaxRunes)
		if validForeignID(author.ForeignID) {
			out.resolvedID = author.ForeignID
		}
		out.AuthorName = out.Title
		out.payload.AuthorName = out.Title
	}
	raw, err := json.Marshal(out.payload)
	if err != nil {
		return nil, http.StatusInternalServerError, "Could not record the request."
	}
	out.PayloadJSON = string(raw)
	return out, 0, ""
}

// alreadyInLibrary returns the sentence to answer when the item is already in
// the library, or "". The check is global, not scoped to the requester:
// books.foreign_id is unique across users, so an item another user holds
// cannot be added again either.
func (h *RequestHandler) alreadyInLibrary(ctx context.Context, kind, foreignID string) (string, error) {
	switch kind {
	case models.RequestKindBook:
		b, err := h.books.GetByForeignID(ctx, foreignID)
		if err != nil {
			return "", err
		}
		if b != nil {
			return "This book is already in the library.", nil
		}
	case models.RequestKindAuthor:
		a, err := h.authors.GetByAnyForeignID(ctx, foreignID)
		if err != nil {
			return "", err
		}
		if a != nil {
			return "This author is already in the library.", nil
		}
	}
	return "", nil
}

func (h *RequestHandler) maxPendingPerUser(ctx context.Context) int {
	if h.settings == nil {
		return defaultRequestsMaxPendingPerUser
	}
	s, err := h.settings.Get(ctx, SettingRequestsMaxPendingPerUser)
	if err != nil || s == nil || strings.TrimSpace(s.Value) == "" {
		return defaultRequestsMaxPendingPerUser
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.Value))
	if err != nil || n < 1 {
		return defaultRequestsMaxPendingPerUser
	}
	return n
}

// ListMine lists the caller's own requests. GET /requests
func (h *RequestHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	owner := auth.UserIDFromContext(r.Context())
	limit, offset := parseLimitOffset(r, 50, 200)
	items, total, err := h.requests.ListByOwner(r.Context(), owner, limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	out := make([]requestResponse, 0, len(items))
	for _, it := range items {
		out = append(out, toRequestResponse(it, false))
	}
	writeJSON(w, http.StatusOK, requestListResponse{Items: out, Total: total, Limit: limit, Offset: offset})
}

// Withdraw deletes the caller's own pending request. DELETE /requests/{id}
func (h *RequestHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	owner := auth.UserIDFromContext(r.Context())
	if err := h.requests.DeletePendingForOwner(r.Context(), id, owner); err != nil {
		if errors.Is(err, db.ErrRequestNotPending) {
			writeErr(w, http.StatusNotFound, "You have no pending request with that id.")
			return
		}
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Queue lists every user's requests for the admin. GET /requests/queue
// ?status=pending (default) | approved | declined | all
func (h *RequestHandler) Queue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = models.RequestStatusPending
	case "all":
		status = ""
	case models.RequestStatusPending, models.RequestStatusApproved, models.RequestStatusDeclined:
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending, approved, declined or all")
		return
	}
	limit, offset := parseLimitOffset(r, 50, 200)
	items, total, err := h.requests.ListAll(r.Context(), status, limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	out := make([]requestResponse, 0, len(items))
	for _, it := range items {
		out = append(out, toRequestResponse(it, true))
	}
	writeJSON(w, http.StatusOK, requestListResponse{Items: out, Total: total, Limit: limit, Offset: offset})
}

// PendingCount answers {"count": n} for the admin nav badge.
// GET /requests/pending-count
func (h *RequestHandler) PendingCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.requests.CountPending(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// approveBody is the admin's choices at approval. Every field is optional;
// the web form prefills them from the instance defaults. The profile, root
// folder and monitor fields apply to author requests; a book request uses
// SearchOnAdd and MediaType, the same two choices the Add Book dialog offers.
type approveBody struct {
	QualityProfileID      *int64  `json:"qualityProfileId"`
	MetadataProfileID     *int64  `json:"metadataProfileId"`
	RootFolderID          *int64  `json:"rootFolderId"`
	AudiobookRootFolderID *int64  `json:"audiobookRootFolderId"`
	MonitorMode           *string `json:"monitorMode"`
	MonitorLatestCount    *int    `json:"monitorLatestCount"`
	MonitorNewItems       *string `json:"monitorNewItems"`
	SearchOnAdd           *bool   `json:"searchOnAdd"`
	MediaType             *string `json:"mediaType"`
}

// Approve adds what a request asked for and marks it approved.
// POST /requests/{id}/approve
//
// The request is claimed first with a compare and swap on its status, so two
// admins approving at once produce exactly one add; the loser gets 409. The
// stored payload is revalidated against the row, "already in the library" is
// checked again, and the add runs through the same core the Add dialog uses,
// in a context whose user is the requester, so the new rows are owned by the
// requester. Any failure releases the claim.
func (h *RequestHandler) Approve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body approveBody
	if err := decodeStrict(w, r, requestApproveMaxBody, true, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid approval body")
		return
	}
	if body.MediaType != nil && !validRequestMediaType(*body.MediaType) {
		writeErr(w, http.StatusBadRequest, "mediaType must be ebook, audiobook, both or empty.")
		return
	}

	req, err := h.requests.Claim(ctx, id, auth.UserIDFromContext(ctx))
	if errors.Is(err, db.ErrRequestNotPending) {
		if existing, _ := h.requests.GetByID(ctx, id); existing == nil {
			writeErr(w, http.StatusNotFound, "No request with that id.")
			return
		}
		writeErr(w, http.StatusConflict, "This request has already been decided.")
		return
	}
	if err != nil || req == nil {
		writeServerError(w, r, fmt.Errorf("claim request %d: %w", id, err))
		return
	}

	done, status, msg, err := h.runClaimedApproval(ctx, req, body)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if msg != "" {
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, toRequestResponse(*done, true))
}

// runClaimedApproval runs the add for a request whose claim the caller already
// holds and marks it approved. It returns the approved row, or a status and
// sentence for a refusal, or an error. The claim is released on any failure.
//
// It is split out of Approve so auto approval drives the same path: the claim,
// the payload revalidation, the owner in the context and the release on
// failure are one implementation, whether a human or the account's own setting
// asked for the add.
func (h *RequestHandler) runClaimedApproval(ctx context.Context, req *models.LibraryRequest, body approveBody) (*models.LibraryRequest, int, string, error) {
	// Keep the claim fresh while the add runs. The cores return once the
	// author or book row exists and leave the catalogue sync in the
	// background, but their provider lookups and addBookCore's row poll can
	// still take a while, and a claim that went stale mid add could be
	// retaken by a second approval.
	//
	// If the add panics, the renewer must still stop and the claim must still
	// be released: chi's Recoverer keeps the server up, and without this the
	// request would sit in approving, renewed forever and counted against the
	// requester's cap, until restart. The panic is re-raised so Recoverer
	// still logs it.
	var stopOnce sync.Once
	renewStop := h.renewClaim(req.ID, req.ClaimToken)
	stopRenew := func() { stopOnce.Do(renewStop) }
	defer func() {
		if p := recover(); p != nil {
			stopRenew()
			if relErr := h.requests.Release(context.WithoutCancel(ctx), req.ID, req.ClaimToken); relErr != nil {
				slog.Error("requests: release claim after a panicking approval", "id", req.ID, "error", relErr)
			}
			panic(p)
		}
	}()
	defer stopRenew()
	bookID, authorID, status, msg, err := h.runApproval(ctx, req, body)
	stopRenew()
	if err != nil || msg != "" {
		if relErr := h.requests.Release(ctx, req.ID, req.ClaimToken); relErr != nil {
			slog.Error("requests: release claim after failed approval", "id", req.ID, "error", relErr)
		}
		return nil, status, msg, err
	}
	if err := h.requests.Complete(ctx, req.ID, req.ClaimToken, bookID, authorID); err != nil {
		if errors.Is(err, db.ErrRequestNotPending) {
			// The claim was lost despite renewal (the database was
			// unreachable for longer than the TTL). The add has happened;
			// say so rather than 500.
			slog.Error("requests: approval finished after losing its claim", "id", req.ID)
			return nil, http.StatusConflict, "The item was added, but another approval took over this request meanwhile. Reload the queue.", nil
		}
		return nil, 0, "", err
	}
	done, err := h.requests.GetByID(ctx, req.ID)
	if err != nil || done == nil {
		return nil, 0, "", fmt.Errorf("reload approved request: %w", err)
	}
	return done, 0, "", nil
}

// renewClaim restamps the claim every third of the TTL until the returned
// stop function is called.
func (h *RequestHandler) renewClaim(id int64, token string) (stop func()) {
	interval := h.requests.ClaimTTL() / 3
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	h.activeRenewers.Add(1)
	go func() {
		defer close(finished)
		defer h.activeRenewers.Add(-1)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), interval)
				err := h.requests.RenewClaim(ctx, id, token)
				cancel()
				if err != nil {
					slog.Warn("requests: could not renew approval claim", "id", id, "error", err)
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// runApproval validates a claimed request and runs the add. It returns the
// created ids, or a status and sentence for a refusal, or an error.
func (h *RequestHandler) runApproval(ctx context.Context, req *models.LibraryRequest, body approveBody) (bookID, authorID *int64, status int, msg string, err error) {
	var p requestPayload
	if jerr := json.Unmarshal([]byte(req.PayloadJSON), &p); jerr != nil ||
		p.Kind != req.Kind || p.ForeignID != req.ForeignID || !validForeignID(p.ForeignID) ||
		(p.ForeignAuthorID != "" && !validForeignID(p.ForeignAuthorID)) || !validRequestMediaType(p.MediaType) {
		return nil, nil, http.StatusUnprocessableEntity, "This request's stored details are not valid. Decline it and ask for it again.", nil
	}
	if inLib, lerr := h.alreadyInLibrary(ctx, p.Kind, p.ForeignID); lerr != nil {
		return nil, nil, 0, "", lerr
	} else if inLib != "" {
		return nil, nil, http.StatusConflict, inLib, nil
	}
	if h.adder == nil {
		return nil, nil, http.StatusServiceUnavailable, "Adding is not available right now.", nil
	}

	mediaType := p.MediaType
	if body.MediaType != nil {
		mediaType = *body.MediaType
	}
	searchOnAdd := body.SearchOnAdd != nil && *body.SearchOnAdd

	// The requester owns what the approval creates. Their role goes with
	// their id so the core's scoping treats the add as theirs, never as an
	// admin's unscoped view.
	ownerCtx := auth.WithUserRole(auth.WithUserID(ctx, req.OwnerUserID), auth.RoleRequester)

	switch p.Kind {
	case models.RequestKindBook:
		res, aerr := h.adder.addBookCore(ownerCtx, addBookParams{
			ForeignBookID:   p.ForeignID,
			ForeignAuthorID: p.ForeignAuthorID,
			AuthorName:      p.AuthorName,
			SearchOnAdd:     searchOnAdd,
			MediaType:       mediaType,
		})
		if aerr != nil {
			status, msg, err := approvalErrorResponse(aerr)
			return nil, nil, status, msg, err
		}
		if res.Book != nil {
			bookID = &res.Book.ID
		}
		if res.Author != nil {
			authorID = &res.Author.ID
		}
	case models.RequestKindAuthor:
		res, aerr := h.adder.createAuthorCore(ownerCtx, createAuthorParams{
			ForeignID:             p.ForeignID,
			Name:                  p.AuthorName,
			QualityProfileID:      body.QualityProfileID,
			MetadataProfileID:     body.MetadataProfileID,
			RootFolderID:          body.RootFolderID,
			AudiobookRootFolderID: body.AudiobookRootFolderID,
			Monitored:             true,
			MonitorMode:           body.MonitorMode,
			MonitorLatestCount:    body.MonitorLatestCount,
			MonitorNewItems:       body.MonitorNewItems,
			SearchOnAdd:           searchOnAdd,
			MediaType:             mediaType,
			// The catalogue sync always runs: search on add happens inside
			// it, and the core refuses search on add without it.
			SkipCatalogueSync: false,
		})
		if aerr != nil {
			status, msg, err := approvalErrorResponse(aerr)
			return nil, nil, status, msg, err
		}
		if res.Author != nil {
			authorID = &res.Author.ID
		}
	}
	return bookID, authorID, 0, "", nil
}

// approvalErrorResponse maps an add core error to what the approve route
// answers, reusing the sentences the Add dialog's handlers answer with.
func approvalErrorResponse(err error) (int, string, error) {
	var inLibrary *bookInLibraryError
	if errors.As(err, &inLibrary) {
		return http.StatusConflict, "This book is already in the library.", nil
	}
	var conflict *authorConflictError
	if errors.As(err, &conflict) {
		return http.StatusConflict, "Could not add the author: " + conflict.Message + ".", nil
	}
	var unavailable *primaryProviderUnavailableError
	if errors.As(err, &unavailable) {
		return http.StatusServiceUnavailable, unavailable.Error(), nil
	}
	var lookup *addBookLookupError
	var authorLookup *createAuthorLookupError
	if errors.As(err, &lookup) || errors.As(err, &authorLookup) {
		return http.StatusBadGateway, "The metadata provider did not answer. Try again shortly.", nil
	}
	var option *createAuthorOptionError
	if errors.As(err, &option) {
		return http.StatusBadRequest, option.Err.Error(), nil
	}
	// runApproval never skips the catalogue sync, so this cannot happen
	// today. If a later change skips it, say what went wrong in words an
	// admin can act on rather than the core's internal sentence.
	if errors.Is(err, errCreateAuthorSearchNeedsSync) {
		return http.StatusInternalServerError, "Search on add needs the author's catalogue sync, and this approval skipped it. Approve again without search on add, then search from the author page.", nil
	}
	for _, resp := range addBookErrorResponses {
		if errors.Is(err, resp.err) {
			return resp.status, resp.body, nil
		}
	}
	for _, resp := range createAuthorErrorResponses {
		if errors.Is(err, resp.err) {
			return http.StatusBadRequest, resp.body, nil
		}
	}
	return 0, "", err
}

// Decline declines a pending request with an optional reason the requester
// sees. POST /requests/{id}/decline {"reason": "..."}
func (h *RequestHandler) Decline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeStrict(w, r, requestApproveMaxBody, true, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid decline body")
		return
	}
	reason := cleanRequestText(body.Reason, requestReasonMaxRunes)
	if err := h.requests.Decline(ctx, id, auth.UserIDFromContext(ctx), reason); err != nil {
		if errors.Is(err, db.ErrRequestNotPending) {
			if existing, _ := h.requests.GetByID(ctx, id); existing == nil {
				writeErr(w, http.StatusNotFound, "No request with that id.")
				return
			}
			writeErr(w, http.StatusConflict, "This request has already been decided.")
			return
		}
		writeServerError(w, r, err)
		return
	}
	done, err := h.requests.GetByID(ctx, id)
	if err != nil || done == nil {
		writeServerError(w, r, fmt.Errorf("reload declined request: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, toRequestResponse(*done, true))
}
