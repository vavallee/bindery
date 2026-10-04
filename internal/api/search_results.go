package api

import (
	"context"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// SearchResultRegistry remembers the releases the interactive search endpoints
// returned, keyed by GUID, with the download URL exactly as the indexer gave
// it: before any redaction, credentials included.
//
// It does two jobs. First, a grab from an account that is not an admin is held
// to a release Bindery itself found. POST /queue/grab used to send whatever
// download URL the request carried; Bindery fetches that URL itself for the
// usenet clients, signNZBURL attaches the stored indexer key to any URL on a
// configured indexer's host, and the first bytes of a body that is not an NZB
// come back in the error. Together that let any user role account read an
// indexer's or Prowlarr's own API with the admin's key, or read any HTTP
// service on the LAN. A user account grabs from the search results the server
// handed it, so that is all it may grab.
//
// Second, a grab of a recorded release never depends on the URL the client
// posts back, whoever the caller is. The recorded URL, indexer, protocol,
// title and size replace the posted ones for admins and API key callers too,
// so search responses can drop every secret from the URL (an indexer apikey, a
// Jackett key, a tracker passkey) and the grab still sends the real values.
// An admin or API key grab of a GUID the registry does not hold (a restart,
// an eviction, a result older than the TTL, or a release found elsewhere)
// falls back to the posted URL. See callerMayGrabAnyURL.
//
// Entries are keyed by GUID alone. The searcher already de-duplicates results
// by GUID, and whichever search recorded an entry last, the URL in it is one an
// indexer returned, which is the property that matters. The registry is in
// memory and never leaves the process: lookup is only read by the grab
// handler, and grab() redacts the record it returns. A restart empties it and
// a user re-runs the search, which is the same thing they would do after the
// results page went stale.
type SearchResultRegistry struct {
	mu      sync.Mutex
	entries map[string]searchRelease
	// order lists insertions oldest first so the cap evicts the oldest entry.
	// Re-recording a GUID appends a new record and leaves the old one behind;
	// a record whose seq no longer matches its entry is skipped and dropped.
	order []searchReleaseRecord
	seq   uint64
	ttl   time.Duration
	max   int
	now   func() time.Time
}

// searchRelease is what a non-admin grab takes from the server rather than
// from the request.
type searchRelease struct {
	NZBURL    string // raw, as the indexer returned it: may carry credentials
	Title     string
	Size      int64
	IndexerID int64
	Protocol  string

	seq uint64
	at  time.Time
}

type searchReleaseRecord struct {
	guid string
	seq  uint64
}

const (
	// searchResultTTL is how long a returned result stays grabbable. A results
	// page can sit open for a long time before someone clicks Grab, and an
	// expired entry costs the user a re-search, so this is generous.
	searchResultTTL = 24 * time.Hour
	// searchResultMax caps the registry. An entry is a few hundred bytes, so
	// the cap holds it to a few megabytes on a busy multi-user install, and it
	// is far above what one person's open result pages hold.
	searchResultMax = 20000
)

// NewSearchResultRegistry returns an empty registry. main.go hands the same
// one to the IndexerHandler, which records results, and the QueueHandler,
// which checks grabs against them.
func NewSearchResultRegistry() *SearchResultRegistry {
	return &SearchResultRegistry{
		entries: make(map[string]searchRelease),
		ttl:     searchResultTTL,
		max:     searchResultMax,
		now:     time.Now,
	}
}

// remember records results with their raw download URLs, so callers pass the
// results before redacting them for the response. Results with no GUID are
// skipped: a grab needs one. A nil registry records nothing.
func (r *SearchResultRegistry) remember(results []newznab.SearchResult) {
	if r == nil || len(results) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, res := range results {
		if res.GUID == "" {
			continue
		}
		r.seq++
		r.entries[res.GUID] = searchRelease{
			NZBURL:    res.NZBURL,
			Title:     res.Title,
			Size:      res.Size,
			IndexerID: res.IndexerID,
			Protocol:  res.Protocol,
			seq:       r.seq,
			at:        now,
		}
		r.order = append(r.order, searchReleaseRecord{guid: res.GUID, seq: r.seq})
	}
	r.trimLocked(now)
}

// trimLocked drops expired entries and, past the cap, the oldest ones. Both
// leave from the front of order, which is oldest first. Stale records left by
// re-recorded GUIDs are compacted away once they outnumber the live ones.
func (r *SearchResultRegistry) trimLocked(now time.Time) {
	for len(r.order) > 0 {
		front := r.order[0]
		e, ok := r.entries[front.guid]
		switch {
		case !ok || e.seq != front.seq:
			// Stale record: the GUID was re-recorded or already removed.
		case len(r.entries) > r.max || now.Sub(e.at) > r.ttl:
			delete(r.entries, front.guid)
		default:
			// The oldest live entry is fresh and within the cap.
			if len(r.order) > 2*len(r.entries)+64 {
				r.compactLocked()
			}
			return
		}
		r.order = r.order[1:]
	}
}

// compactLocked rebuilds order from the records that still name their entry.
func (r *SearchResultRegistry) compactLocked() {
	kept := make([]searchReleaseRecord, 0, len(r.entries))
	for _, rec := range r.order {
		if e, ok := r.entries[rec.guid]; ok && e.seq == rec.seq {
			kept = append(kept, rec)
		}
	}
	r.order = kept
}

// lookup returns the release recorded for guid, if one was recorded within the
// TTL. A nil registry knows no releases, so a QueueHandler with none attached
// refuses every non-admin grab rather than trusting the posted URL, and admin
// grabs use the posted URL as they always did.
func (r *SearchResultRegistry) lookup(guid string) (searchRelease, bool) {
	if r == nil || guid == "" {
		return searchRelease{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[guid]
	if !ok || r.now().Sub(e.at) > r.ttl {
		return searchRelease{}, false
	}
	return e, true
}

// apply replaces the release fields of req with the recorded ones. BookID and
// MediaType stay the caller's: they say where the release goes, not what it
// is, and grab() checks the book's ownership.
func (e searchRelease) apply(req *grabRequest) {
	req.NZBURL = e.NZBURL
	req.Title = e.Title
	req.Size = e.Size
	req.Protocol = e.Protocol
	req.IndexerID = nil
	if e.IndexerID != 0 {
		id := e.IndexerID
		req.IndexerID = &id
	}
}

// callerMayGrabAnyURL reports whether a grab request may name its own download
// URL. True for the admin role, which API key, disabled mode and trusted local
// requests also carry, and for a context with no identity at all, which only
// code outside the auth middleware builds (CheckOwnership treats that the same
// way). Everyone else, including a signed in user whose role could not be
// read, grabs only what a search returned.
func callerMayGrabAnyURL(ctx context.Context) bool {
	role := auth.UserRoleFromContext(ctx)
	if role == auth.RoleAdmin {
		return true
	}
	return role == "" && auth.UserIDFromContext(ctx) == 0
}

// WithSearchResults attaches the registry non-admin grabs are checked against.
func (h *QueueHandler) WithSearchResults(reg *SearchResultRegistry) *QueueHandler {
	h.searchResults = reg
	return h
}

// WithSearchResults attaches the registry the interactive search endpoints
// record their results in.
func (h *IndexerHandler) WithSearchResults(reg *SearchResultRegistry) *IndexerHandler {
	h.searchResults = reg
	return h
}
