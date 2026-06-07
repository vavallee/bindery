// Package nzbget provides a client for the NZBGet JSON-RPC API, used to
// submit NZB URLs and poll queue/history for Usenet downloads.
package nzbget

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
)

// categoryNameKey matches the NZBGet config keys that carry a category's
// display name (Category1.Name, Category2.Name, …). NZBGet config also has
// CategoryN.DestDir/Unpack/PostScript/Aliases — those are filtered out here.
var categoryNameKey = regexp.MustCompile(`^Category\d+\.Name$`)

// Client interacts with the NZBGet JSON-RPC API.
type Client struct {
	baseURL   string
	http      *http.Client // NZBGet JSON-RPC transport
	fetchHTTP *http.Client // used to fetch NZB content from indexers before submission
	// username and password for HTTP Basic auth
	username       string
	password       string
	validateNZBURL func(string) error // injectable for tests; nil uses httpsec.ValidateOutboundURL
}

// New creates a NZBGet client. urlBase is the optional reverse-proxy
// subpath that is appended before NZBGet's /jsonrpc endpoint.
// NZBGet's JSON-RPC endpoint is http://user:pass@host:port[/url_base]/jsonrpc.
func New(host string, port int, username, password, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return &Client{
		baseURL:   fmt.Sprintf("%s://%s:%d%s/jsonrpc", scheme, host, port, urlbase.Normalize(urlBase)),
		username:  username,
		password:  password,
		http:      &http.Client{Timeout: 15 * time.Second},
		fetchHTTP: &http.Client{Timeout: 60 * time.Second},
		validateNZBURL: func(raw string) error {
			return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLAN)
		},
	}
}

// Test verifies connectivity by calling the "version" RPC method.
func (c *Client) Test(ctx context.Context) error {
	var resp versionResponse
	if err := c.call(ctx, "version", nil, &resp); err != nil {
		return fmt.Errorf("could not reach NZBGet at %s — %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	if resp.Result == "" {
		return fmt.Errorf("NZBGet returned empty version — check credentials")
	}
	return nil
}

// ListCategories returns the category display names defined in NZBGet's
// nzbget.conf. NZBGet's append RPC silently rejects a download (returning id
// 0) when the submitted category isn't one of these, so Bindery uses this to
// preflight-validate before submitting.
func (c *Client) ListCategories(ctx context.Context) ([]string, error) {
	var resp configResponse
	if err := c.call(ctx, "config", nil, &resp); err != nil {
		return nil, fmt.Errorf("list nzbget categories: %w", err)
	}
	cats := make([]string, 0, 8)
	for _, e := range resp.Result {
		if categoryNameKey.MatchString(e.Name) && e.Value != "" {
			cats = append(cats, e.Value)
		}
	}
	return cats, nil
}

// CheckCategories reports an error when any of wanted is not present in
// NZBGet's configured category list. Empty entries in wanted are skipped —
// NZBGet treats an empty category as "use the default destination". When all
// wanted entries are empty the call short-circuits and never hits the RPC.
func (c *Client) CheckCategories(ctx context.Context, wanted ...string) error {
	var nonEmpty []string
	for _, w := range wanted {
		if w != "" {
			nonEmpty = append(nonEmpty, w)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	have, err := c.ListCategories(ctx)
	if err != nil {
		return err
	}
	haveSet := make(map[string]struct{}, len(have))
	for _, h := range have {
		haveSet[h] = struct{}{}
	}
	var missing []string
	for _, w := range nonEmpty {
		if _, ok := haveSet[w]; !ok {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return categoryMismatchError(missing, have)
}

// categoryMismatchError formats the actionable error returned when one or
// more Bindery-configured categories aren't defined in NZBGet. We surface
// both sides so the user can see what to rename on which side.
func categoryMismatchError(missing, have []string) error {
	quote := func(in []string) string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = strconv.Quote(s)
		}
		return strings.Join(out, ", ")
	}
	haveStr := "none defined"
	if len(have) > 0 {
		haveStr = quote(have)
	}
	if len(missing) == 1 {
		return fmt.Errorf("NZBGet has no category %s configured (existing categories: %s). Add it in NZBGet's Settings → Categories, or change the category in Bindery's download-client config to match", strconv.Quote(missing[0]), haveStr)
	}
	return fmt.Errorf("NZBGet has no categories %s configured (existing categories: %s). Add them in NZBGet's Settings → Categories, or change the categories in Bindery's download-client config to match", quote(missing), haveStr)
}

// Add fetches the NZB file from nzbURL (using Bindery's own HTTP client, which
// holds the indexer credentials and network path) then submits the content to
// NZBGet as base64. Sending content rather than a URL means NZBGet never needs
// to reach the indexer directly — which fails in containerised setups where
// Prowlarr's signed download URL is only reachable from Bindery's network
// context, not NZBGet's.
//
// The priority parameter maps to NZBGet priorities: 0=normal, 100=high, -100=low.
func (c *Client) Add(ctx context.Context, nzbURL, name, category string, priority int) (int, error) {
	content, err := c.fetchNZBContent(ctx, nzbURL)
	if err != nil {
		return 0, err
	}
	// Best-effort preflight: if the category isn't defined in NZBGet, append
	// will silently return id 0 with no other context. Surface a clear error
	// here instead. If we can't reach the config RPC for any reason (older
	// NZBGet, restricted ControlIP, transient failure) the mismatch error
	// would be misleading — fall through and let append's response stand.
	if category != "" {
		if have, listErr := c.ListCategories(ctx); listErr == nil {
			if !containsString(have, category) {
				return 0, categoryMismatchError([]string{category}, have)
			}
		}
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	params := appendParams(name, encoded, category, priority)
	var resp appendResponse
	if err := c.call(ctx, "append", params, &resp); err != nil {
		return 0, fmt.Errorf("add nzb: %w", err)
	}
	if resp.Result <= 0 {
		// Preflight already validated the category exists (or skipped because
		// it's empty / the config call failed), so this branch is for the
		// other classes of rejection: disk full, write-permission on
		// NZBGet's intermediate dir, NZBGet paused with quota exhausted,
		// invalid NZB content. NZBGet's own log carries the actual reason.
		return 0, fmt.Errorf("NZBGet rejected the download (append returned id 0) — check NZBGet's log for the reason. Common causes: disk full, write-permission on the intermediate or destination directory, NZBGet paused with quota reached, or invalid NZB content")
	}
	return resp.Result, nil
}

// appendParams builds the parameter list for NZBGet's append RPC. It sends the
// nine parameters that NZBGet has required since v13:
//
//	name, content, category, priority, addToTop, addPaused, dupeKey, dupeScore, dupeMode
//
// The two parameters NZBGet added later — ppParameters (v16) and autoCategory
// (v25.3) — are deliberately omitted. Both are optional trailing parameters
// that every NZBGet version parses with unguarded reads (absent ⇒ default), so
// omitting them is accepted on all versions and behaves identically to sending
// their defaults (no post-processing params, autoCategory off). This keeps the
// call version-agnostic and avoids any per-client version state.
//
// dupeMode "FORCE" tells NZBGet to add the download unconditionally rather than
// silently dropping it as a duplicate of a prior history item. Bindery decides
// what to grab and does its own deduplication, so NZBGet must honour the grab.
func appendParams(name, encoded, category string, priority int) []any {
	return []any{name, encoded, category, priority, false, false, "", 0, "FORCE"}
}

// containsString returns true when needle appears in haystack. Tiny helper
// kept local to avoid a slices import for the one site that needs it.
func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func (c *Client) validateNZBFetchURL(raw string) error {
	if c.validateNZBURL == nil {
		return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLAN)
	}
	return c.validateNZBURL(raw)
}

func (c *Client) fetchNZBContent(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.validateNZBFetchURL(nzbURL); err != nil {
		return nil, fmt.Errorf("fetch nzb: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch nzb: %w", err)
	}
	resp, err := c.fetchHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch nzb from indexer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("fetch nzb: indexer returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50 MB cap
}

// GetQueue returns the active download groups from NZBGet.
func (c *Client) GetQueue(ctx context.Context) ([]Group, error) {
	var resp listGroupsResponse
	if err := c.call(ctx, "listgroups", []any{0}, &resp); err != nil {
		return nil, fmt.Errorf("get queue: %w", err)
	}
	return resp.Result, nil
}

// GetHistory returns completed and failed downloads from NZBGet.
// hidden=false returns only visible (non-hidden) history items.
func (c *Client) GetHistory(ctx context.Context) ([]HistoryItem, error) {
	var resp historyResponse
	if err := c.call(ctx, "history", []any{false}, &resp); err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	return resp.Result, nil
}

// Remove permanently deletes a download (queue or history) by NZBID.
// It uses the "DeleteFinal" command which removes both the download and its files
// from the queue or history. For already-completed downloads use RemoveHistory.
func (c *Client) Remove(ctx context.Context, nzbID int) error {
	var resp editQueueResponse
	if err := c.call(ctx, "editqueue", []any{"DeleteFinal", "", []int{nzbID}}, &resp); err != nil {
		return fmt.Errorf("remove nzb %d: %w", nzbID, err)
	}
	return nil
}

// RemoveHistory removes a completed/failed item from NZBGet history by NZBID.
func (c *Client) RemoveHistory(ctx context.Context, nzbID int) error {
	var resp editQueueResponse
	if err := c.call(ctx, "editqueue", []any{"HistoryDelete", "", []int{nzbID}}, &resp); err != nil {
		return fmt.Errorf("remove history %d: %w", nzbID, err)
	}
	return nil
}

// ParseNZBID parses a string NZBID (as stored in the DB) back to an int.
func ParseNZBID(s string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid nzbget id %q: %w", s, err)
	}
	return id, nil
}

// IsSuccess reports whether a NZBGet history status string represents a
// successful download. NZBGet reports "SUCCESS" for clean downloads and
// various "SUCCESS/..." sub-statuses.
func IsSuccess(status string) bool {
	return len(status) >= 7 && status[:7] == "SUCCESS"
}

// IsFailure reports whether a NZBGet history status string represents a
// failed download.
func IsFailure(status string) bool {
	return len(status) >= 7 && status[:7] == "FAILURE" ||
		status == "DELETED" ||
		(len(status) >= 6 && status[:6] == "SCRIPT")
}

func (c *Client) call(ctx context.Context, method string, params []any, target any) error {
	if params == nil {
		params = []any{}
	}
	reqBody := rpcRequest{
		Method: method,
		Params: params,
		ID:     1,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed — check username and password")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// NZBGet signals a failed call with a JSON-RPC "error" object (and a null
	// result). Without this check an append fault — "Invalid parameter",
	// malformed NZB, etc. — silently decodes into result 0 and surfaces as a
	// useless generic "id 0" message. Surface the real reason instead.
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &probe) == nil && len(probe.Error) > 0 && string(probe.Error) != "null" {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(probe.Error, &e) == nil && e.Message != "" {
			return fmt.Errorf("NZBGet %s RPC error %d: %s", method, e.Code, e.Message)
		}
		return fmt.Errorf("NZBGet %s RPC error: %s", method, string(probe.Error))
	}
	return json.Unmarshal(raw, target)
}
