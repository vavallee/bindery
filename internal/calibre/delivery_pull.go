package calibre

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// The pull transport (#2833): the Calibre plugin connects out to Bindery,
// lists the due deliveries, downloads each file, adds it and acknowledges.
// Everything here reuses the push worker's rules (what is skipped and why,
// which file of a book goes first, the backoff schedule, ownership and the
// books.calibre_id rule), so a book lands in Calibre the same way whichever
// side opened the connection.

// Pull actions: what the plugin should do with a listed file.
const (
	// PullActionAdd makes a new Calibre record.
	PullActionAdd = "add"
	// PullActionAddFormat puts the file on the record an earlier delivery
	// of the same book made.
	PullActionAddFormat = "add_format"
)

// Bridge protocol limits.
const (
	// BridgeProtocol is the pull protocol version GET /bridge/v1/hello
	// reports.
	BridgeProtocol = 1
	// PullDefaultBatch is the page size when the plugin does not ask.
	PullDefaultBatch = 20
	// PullMaxBatch is the largest page served.
	PullMaxBatch = 50

	// bridgeMaxCapabilities and bridgeMaxToken bound what is kept of the
	// plugin's self description, which is only ever shown back to the admin.
	bridgeMaxCapabilities = 32
	bridgeMaxToken        = 64
	// pullMaxErrorLen bounds the error text a nack stores on the row.
	pullMaxErrorLen = 2000
	// pullMaxLibraryLen bounds the library an ack records.
	pullMaxLibraryLen = 1024
)

// Pull errors the bridge routes turn into status codes.
var (
	// ErrPullNotFound is a delivery that does not exist.
	ErrPullNotFound = errors.New("delivery not found")
	// ErrPullNotPending is a delivery that is no longer waiting: already
	// delivered to a different id, failed or skipped.
	ErrPullNotPending = errors.New("delivery is not pending")
	// ErrPullInvalid is a request the plugin got wrong.
	ErrPullInvalid = errors.New("invalid request")
)

// PullCaps is what the plugin said it can do, from X-Bridge-Capabilities.
type PullCaps struct {
	AddFormat bool
	Cover     bool
}

// PullCapsFrom reads the capability names that matter to the listing.
func PullCapsFrom(names []string) PullCaps {
	var c PullCaps
	for _, n := range names {
		switch n {
		case pluginCapabilityAddFormat:
			c.AddFormat = true
		case pluginCapabilityCover:
			c.Cover = true
		}
	}
	return c
}

// ParseBridgeCapabilities splits a comma separated capability header into
// lower case names. Anything that is not a plain identifier is dropped, and
// the list is capped, because the values end up in the settings UI.
func ParseBridgeCapabilities(header string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(header, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" || len(name) > bridgeMaxToken || !isBridgeToken(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) == bridgeMaxCapabilities {
			break
		}
	}
	return out
}

// CleanBridgeVersion keeps a plugin version string printable and short.
func CleanBridgeVersion(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		if b.Len() >= bridgeMaxToken {
			break
		}
		if r < 0x21 || r > 0x7e {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isBridgeToken(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// PullContact is what Bindery last heard from a pulling plugin. It lives in
// memory only, so it is empty after a restart until the plugin checks in.
type PullContact struct {
	LastSeen      *time.Time `json:"lastSeen,omitempty"`
	PluginVersion string     `json:"pluginVersion,omitempty"`
	Capabilities  []string   `json:"capabilities,omitempty"`
	RemoteAddr    string     `json:"remoteAddr,omitempty"`
	// Library is the Calibre library the plugin last acknowledged a
	// delivery into.
	Library string `json:"library,omitempty"`
}

type pullState struct {
	lastSeen time.Time
	version  string
	caps     []string
	remote   string
	library  string
}

// WithTransport makes the worker read the plugin transport at the start of
// each pass. In pull the pass stands down.
func (d *Deliverer) WithTransport(t func() Transport) *Deliverer {
	d.transport = t
	return d
}

func (d *Deliverer) pulling() bool {
	return d.transport != nil && d.transport() == TransportPull
}

// NotePullContact records that the plugin reached the bridge routes.
func (d *Deliverer) NotePullContact(version string, caps []string, remote string) {
	now := d.now().UTC()
	d.pullMu.Lock()
	defer d.pullMu.Unlock()
	d.pull.lastSeen = now
	d.pull.version = CleanBridgeVersion(version)
	d.pull.caps = append([]string(nil), caps...)
	d.pull.remote = remote
}

// PullContact returns a copy of what the plugin last reported.
func (d *Deliverer) PullContact() PullContact {
	d.pullMu.Lock()
	defer d.pullMu.Unlock()
	var c PullContact
	if !d.pull.lastSeen.IsZero() {
		t := d.pull.lastSeen
		c.LastSeen = &t
	}
	c.PluginVersion = d.pull.version
	c.Capabilities = append([]string(nil), d.pull.caps...)
	c.RemoteAddr = d.pull.remote
	c.Library = d.pull.library
	return c
}

func (d *Deliverer) pullLibrary() string {
	d.pullMu.Lock()
	defer d.pullMu.Unlock()
	return d.pull.library
}

func (d *Deliverer) notePullLibrary(library string) {
	if strings.TrimSpace(library) == "" {
		return
	}
	d.pullMu.Lock()
	d.pull.library = library
	d.pullMu.Unlock()
}

// pullTarget is the deliveryTarget a pull request works against. The
// library is the one the plugin last acknowledged into; until it has, every
// delivered row counts for sibling detection (see libraryMatches).
func (d *Deliverer) pullTarget(caps PullCaps, library string) deliveryTarget {
	return deliveryTarget{
		mode:       ModePlugin,
		cfg:        d.config(),
		library:    library,
		anyLibrary: strings.TrimSpace(library) == "",
		addFormat:  caps.AddFormat,
	}
}

// PullItem is one listed delivery.
type PullItem struct {
	ID        int64    `json:"id"`
	BookID    int64    `json:"bookId"`
	Format    string   `json:"format"`
	SizeBytes int64    `json:"sizeBytes"`
	Action    string   `json:"action"`
	Metadata  Metadata `json:"metadata"`
	HasCover  bool     `json:"hasCover"`
}

// PullPage is GET /bridge/v1/deliveries.
type PullPage struct {
	Deliveries []PullItem `json:"deliveries"`
	NextCursor string     `json:"nextCursor"`
	Pending    int        `json:"pending"`
}

// PullList returns one page of due deliveries after cursor.
//
// Pages are whole books, keyed on the book id, so the preferred format of a
// book is listed ahead of its others and a page boundary never separates
// them. A page holds at most limit items, except that a single book with
// more files than limit is listed whole rather than split.
//
// The first file of a book with nothing delivered yet is listed as "add".
// Its other due files are withheld until that one is acknowledged, then come
// back as "add_format" on a later listing: offering them as a second "add"
// in the same page would make a duplicate Calibre record. A second format
// is listed as "add_format" only to a plugin that advertised add_format;
// otherwise it is skipped with the push worker's reason, and re-queued once
// the plugin advertises it.
func (d *Deliverer) PullList(ctx context.Context, caps PullCaps, cursor string, limit int) (PullPage, error) {
	page := PullPage{Deliveries: []PullItem{}}
	after, err := parsePullCursor(cursor)
	if err != nil {
		return page, err
	}
	if limit <= 0 {
		limit = PullDefaultBatch
	}
	if limit > PullMaxBatch {
		limit = PullMaxBatch
	}
	if caps.AddFormat {
		if n, err := d.store.RearmSkipped(ctx, DeliverySkipNeedsAddFormat); err != nil {
			slog.Warn("calibre pull: re-queueing formats held for a plugin update failed", "error", err)
		} else if n > 0 {
			slog.Info("calibre pull: the Calibre plugin can now add formats; re-queued the formats held for it", "rows", n)
		}
	}
	now := d.now()
	// One book more than a full page can use, to know whether another
	// page follows.
	rows, err := d.store.DueBooksAfter(ctx, now, after, limit+1)
	if err != nil {
		return page, err
	}
	target := d.pullTarget(caps, d.pullLibrary())
	// The listing runs inside the plugin's request, so its edition fetches
	// are capped; a book over the cap is listed on a later check in (#1853).
	ctx = withHydrateBudget(ctx)

	books := groupByBook(rows)
	lastIncluded := int64(0)
	stoppedEarly := false
	for i, group := range books {
		if i == limit {
			stoppedEarly = true
			break
		}
		items, err := d.pullBookItems(ctx, group, target)
		if err != nil {
			return page, err
		}
		if len(page.Deliveries) > 0 && len(page.Deliveries)+len(items) > limit {
			stoppedEarly = true
			break
		}
		page.Deliveries = append(page.Deliveries, items...)
		lastIncluded = group[0].BookID
	}
	if stoppedEarly && lastIncluded > 0 {
		page.NextCursor = strconv.FormatInt(lastIncluded, 10)
	}
	pending, err := d.store.CountDue(ctx, now)
	if err != nil {
		return page, err
	}
	page.Pending = pending
	return page, nil
}

// pullBookItems lists one book's due rows, preferred format first.
func (d *Deliverer) pullBookItems(ctx context.Context, group []models.CalibreDelivery, target deliveryTarget) ([]PullItem, error) {
	orderDeliveries(group)
	var items []PullItem
	firstListed := false
	for i := range group {
		row := &group[i]
		rd, res, ok := d.resolveDelivery(ctx, row, target)
		if !ok {
			if res == deliveryStop && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		action := PullActionAdd
		if rd.secondary {
			action = PullActionAddFormat
		} else if firstListed {
			// Withheld until the first file of the book is acknowledged.
			continue
		} else {
			firstListed = true
		}
		meta, edition := d.metadata(ctx, rd.book, row, rd.path, target)
		items = append(items, PullItem{
			ID:        row.ID,
			BookID:    row.BookID,
			Format:    row.Format,
			SizeBytes: rd.size,
			Action:    action,
			Metadata:  meta,
			HasCover:  d.covers.Available(CoverSourceFor(rd.book, edition)),
		})
	}
	return items, nil
}

// groupByBook splits rows already ordered by book id into one slice per book.
func groupByBook(rows []models.CalibreDelivery) [][]models.CalibreDelivery {
	var out [][]models.CalibreDelivery
	for i, r := range rows {
		if i == 0 || r.BookID != rows[i-1].BookID {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], r)
	}
	return out
}

func parsePullCursor(cursor string) (int64, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: bad cursor", ErrPullInvalid)
	}
	return n, nil
}

// PullPending returns a pending row by id, for the file and cover routes.
func (d *Deliverer) PullPending(ctx context.Context, id int64) (*models.CalibreDelivery, error) {
	row, err := d.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.State != models.CalibreDeliveryPending {
		return nil, ErrPullNotFound
	}
	return row, nil
}

// PullFileMissing takes a row whose file is gone out of the queue, with the
// reason the push worker records for the same thing.
func (d *Deliverer) PullFileMissing(ctx context.Context, row *models.CalibreDelivery) {
	d.skip(ctx, row, deliverySkipNoFile)
}

// PullCoverPath resolves the cover for a pending delivery to a local file,
// through the same helper the push paths use. "" means there is none.
func (d *Deliverer) PullCoverPath(ctx context.Context, row *models.CalibreDelivery) (string, error) {
	book, err := d.books.GetByID(ctx, row.BookID)
	if err != nil {
		return "", err
	}
	if book == nil {
		return "", nil
	}
	edition := d.edition(ctx, book, row, row.FilePath)
	// No target: the plugin asked for the cover, so it can take one.
	return d.covers.PathFor(ctx, CoverSourceFor(book, edition), nil), nil
}

// PullAckRequest is POST /bridge/v1/deliveries/{id}/ack.
type PullAckRequest struct {
	CalibreID    int64  `json:"calibreId"`
	Outcome      string `json:"outcome"`
	CoverApplied *bool  `json:"coverApplied"`
	Library      string `json:"library"`
}

// PullAck records that the plugin added a delivery. Acknowledging a row that
// is already delivered to the same Calibre id is a no op, so a plugin that
// lost the response can safely send it again.
func (d *Deliverer) PullAck(ctx context.Context, id int64, req PullAckRequest) error {
	switch req.Outcome {
	case DeliveryOutcomeAdded, DeliveryOutcomeAlready, DeliveryOutcomeFormatAdded:
	default:
		return fmt.Errorf("%w: outcome must be added, already or format_added", ErrPullInvalid)
	}
	if req.CalibreID <= 0 {
		return fmt.Errorf("%w: calibreId must be positive", ErrPullInvalid)
	}
	if len(req.Library) > pullMaxLibraryLen {
		return fmt.Errorf("%w: library is too long", ErrPullInvalid)
	}
	row, err := d.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrPullNotFound
	}
	switch row.State {
	case models.CalibreDeliveryPending:
	case models.CalibreDeliveryDelivered:
		if row.CalibreID != nil && *row.CalibreID == req.CalibreID {
			return nil
		}
		return ErrPullNotPending
	default:
		return ErrPullNotPending
	}
	book, err := d.books.GetByID(ctx, row.BookID)
	if err != nil {
		return err
	}
	library := strings.TrimSpace(req.Library)
	target := d.pullTarget(PullCaps{}, library)
	if req.Outcome == DeliveryOutcomeAlready && book != nil {
		// Pull has no way to correct the Calibre row from here, so
		// ownership is only reported; the push worker's metadata refresh
		// has no pull counterpart yet.
		owned := d.ownsBeforeRecording(ctx, book.ID, req.CalibreID, library)
		slog.Info("calibre pull: book already in Calibre", "bookId", book.ID, "calibreId", req.CalibreID, "owned", owned)
	}
	if err := d.recordDelivered(context.WithoutCancel(ctx), row, book, req.CalibreID, req.Outcome, target); err != nil {
		return err
	}
	d.notePullLibrary(library)
	slog.Debug("calibre pull: delivery acknowledged", "deliveryId", row.ID, "bookId", row.BookID,
		"calibreId", req.CalibreID, "outcome", req.Outcome, "coverApplied", req.CoverApplied)
	return nil
}

// PullNackRequest is POST /bridge/v1/deliveries/{id}/nack.
type PullNackRequest struct {
	Code      string `json:"code"`
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

// PullNack records a failed attempt the plugin reported. It gives up for good
// when the plugin says retrying cannot help, or the code is one the push
// worker never retries; otherwise the row backs off on the push schedule.
func (d *Deliverer) PullNack(ctx context.Context, id int64, req PullNackRequest) error {
	code := strings.ToLower(strings.TrimSpace(req.Code))
	if code == "" || len(code) > bridgeMaxToken || !isBridgeToken(code) {
		return fmt.Errorf("%w: code must be a short identifier", ErrPullInvalid)
	}
	msg := strings.TrimSpace(req.Error)
	if len(msg) > pullMaxErrorLen {
		msg = msg[:pullMaxErrorLen]
	}
	if msg == "" {
		msg = code
	}
	row, err := d.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrPullNotFound
	}
	if row.State != models.CalibreDeliveryPending {
		return ErrPullNotPending
	}
	_, err = d.recordFailure(ctx, row, code, msg, !req.Retryable)
	return err
}

// Available reports whether PathFor could find a cover for imageURL without
// doing the work: a stored cover that resolves, or a remote URL with a cache
// to download it into. The pull listing uses it for hasCover, so listing a
// page never fetches covers the plugin may not ask for.
func (c CoverSource) Available(imageURL string) bool {
	u := strings.TrimSpace(imageURL)
	if u == "" {
		return false
	}
	if covers.IsRef(u) {
		if c.Store == nil {
			return false
		}
		_, _, ok := c.Store.Resolve(u)
		return ok
	}
	lower := strings.ToLower(u)
	return c.CacheDir != "" && (strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://"))
}
