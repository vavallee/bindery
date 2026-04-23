// Package nzbget provides a client for the NZBGet JSON-RPC API, used to
// submit NZB URLs and poll queue/history for Usenet downloads.
package nzbget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/vavallee/bindery/internal/downloader/urlbase"
)

// Client interacts with the NZBGet JSON-RPC API.
type Client struct {
	baseURL string
	http    *http.Client
	// username and password for HTTP Basic auth
	username string
	password string
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
		baseURL:  fmt.Sprintf("%s://%s:%d%s/jsonrpc", scheme, host, port, urlbase.Normalize(urlBase)),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Test verifies connectivity by calling the "version" RPC method.
func (c *Client) Test(ctx context.Context) error {
	var resp versionResponse
	if err := c.call(ctx, "version", nil, &resp); err != nil {
		return fmt.Errorf("could not reach NZBGet at %s — %w (in Docker use the service/container name, not localhost)", c.baseURL, err)
	}
	if resp.Result == "" {
		return fmt.Errorf("NZBGet returned empty version — check credentials")
	}
	return nil
}

// Add submits an NZB by URL to NZBGet and returns the NZBID as a string.
// The priority parameter maps to NZBGet priorities: 0=normal, 100=high, -100=low.
func (c *Client) Add(ctx context.Context, nzbURL, name, category string, priority int) (int, error) {
	// append params: name, url, category, priority, dupecheck, dupekey, dupescore,
	//                ppparameters (array), addtoTop, addpaused, urlpassword, postscript
	params := []any{name, nzbURL, category, priority, false, "", 0, []any{}, false, false, "", ""}
	var resp appendResponse
	if err := c.call(ctx, "append", params, &resp); err != nil {
		return 0, fmt.Errorf("add nzb: %w", err)
	}
	if resp.Result <= 0 {
		return 0, fmt.Errorf("NZBGet rejected download (returned id %d)", resp.Result)
	}
	return resp.Result, nil
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

	return json.NewDecoder(resp.Body).Decode(target)
}
