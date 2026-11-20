package slskd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Search limits. A search sends the wanted title to the Soulseek network and
// reads what peers answer, so every number here bounds either how long a user
// waits or how much memory a hostile peer population can make Bindery hold.
const (
	// DefaultSearchTimeout is how long slskd is asked to collect responses.
	DefaultSearchTimeout = 15 * time.Second
	// searchGrace is how long past the timeout Bindery waits for slskd to
	// report the search complete before it cancels and reads what exists.
	searchGrace = 5 * time.Second
	// DefaultSearchWait bounds how long a caller queues behind another search,
	// since slskd runs one at a time.
	DefaultSearchWait = 30 * time.Second
	// defaultPollInterval is how often the search state is read.
	defaultPollInterval = time.Second

	// maxResponses and maxFiles cap what is kept from one search, after the
	// same limits were asked of slskd.
	maxResponses = 100
	maxFiles     = 5000
	// maxSearchBytes caps the response body of the final read.
	maxSearchBytes = 16 << 20
	maxStateBytes  = 1 << 20
	maxSearchText  = 200
)

// ErrSearchBusy is returned when slskd refuses a search because another one is
// already running (HTTP 429), or when a caller waited too long for its turn.
var ErrSearchBusy = errors.New("slskd is busy with another search, so this one was skipped")

// SearchFile is one file a peer offers.
type SearchFile struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	Extension string `json:"extension"`
	BitRate   *int   `json:"bitRate"`
	Length    *int   `json:"length"`
}

// SearchResponse is one peer's answer to a search.
type SearchResponse struct {
	Username          string       `json:"username"`
	HasFreeUploadSlot bool         `json:"hasFreeUploadSlot"`
	QueueLength       int          `json:"queueLength"`
	UploadSpeed       int64        `json:"uploadSpeed"`
	Files             []SearchFile `json:"files"`
}

type searchState struct {
	ID         string           `json:"id"`
	IsComplete bool             `json:"isComplete"`
	Responses  []SearchResponse `json:"responses"`
}

type searchRequest struct {
	ID            string `json:"id"`
	SearchText    string `json:"searchText"`
	SearchTimeout int    `json:"searchTimeout"` // milliseconds
	ResponseLimit int    `json:"responseLimit"`
	FileLimit     int    `json:"fileLimit"`
}

// Search runs one Soulseek search and returns the peers' responses, capped.
// slskd allows one search at a time, so callers queue behind each other for up
// to DefaultSearchWait. The search is deleted from slskd afterwards so queries
// do not pile up in its database. Peer content is untrusted: nothing returned
// here is ever joined to a local path.
func (c *Client) Search(ctx context.Context, text string) ([]SearchResponse, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("slskd search text is empty")
	}
	if len(text) > maxSearchText {
		text = text[:maxSearchText]
	}

	wait := c.searchWait
	if wait <= 0 {
		wait = DefaultSearchWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case c.searchSem <- struct{}{}:
		defer func() { <-c.searchSem }()
	case <-timer.C:
		return nil, ErrSearchBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	id, err := randomGUID()
	if err != nil {
		return nil, err
	}
	timeout := c.searchTimeout
	if timeout <= 0 {
		timeout = DefaultSearchTimeout
	}
	status, err := c.doBody(ctx, http.MethodPost, "/api/v0/searches", searchRequest{
		ID:            id,
		SearchText:    text,
		SearchTimeout: int(timeout / time.Millisecond),
		ResponseLimit: maxResponses,
		FileLimit:     maxFiles,
	}, maxStateBytes, &searchState{})
	if status == http.StatusTooManyRequests {
		return nil, ErrSearchBusy
	}
	if err != nil {
		return nil, fmt.Errorf("start slskd search: %w", err)
	}
	path := "/api/v0/searches/" + url.PathEscape(id)
	// Whatever happens next, remove the search. The caller's context may be
	// the reason we are leaving, so cleanup gets its own short one.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.do(cctx, http.MethodDelete, path, 1<<10, nil)
	}()

	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	grace := c.searchGrace
	if grace <= 0 {
		grace = searchGrace
	}
	deadline := time.NewTimer(timeout + grace)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
poll:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			// slskd never reported completion. Stop it and read what exists.
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = c.do(cctx, http.MethodPut, path, 1<<10, nil)
			cancel()
			break poll
		case <-tick.C:
			var st searchState
			if _, err := c.do(ctx, http.MethodGet, path, maxStateBytes, &st); err != nil {
				return nil, fmt.Errorf("read slskd search state: %w", err)
			}
			if st.IsComplete {
				break poll
			}
		}
	}

	var final searchState
	if _, err := c.do(ctx, http.MethodGet, path+"?includeResponses=true", maxSearchBytes, &final); err != nil {
		return nil, fmt.Errorf("read slskd search responses: %w", err)
	}
	return capResponses(final.Responses), nil
}

// capResponses enforces maxResponses and maxFiles and drops responses with no
// username or no files.
func capResponses(in []SearchResponse) []SearchResponse {
	out := make([]SearchResponse, 0, min(len(in), maxResponses))
	files := 0
	for _, r := range in {
		if len(out) >= maxResponses || files >= maxFiles {
			break
		}
		if strings.TrimSpace(r.Username) == "" || len(r.Files) == 0 {
			continue
		}
		if room := maxFiles - files; len(r.Files) > room {
			r.Files = r.Files[:room]
		}
		files += len(r.Files)
		out = append(out, r)
	}
	return out
}
