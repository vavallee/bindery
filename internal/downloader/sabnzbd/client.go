// Package sabnzbd provides a client for the SABnzbd JSON API, used to
// submit NZB URLs and poll queue/history for Usenet downloads.
package sabnzbd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

// Client interacts with the SABnzbd API.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client // SABnzbd JSON API transport
	fetchHTTP *http.Client // used to fetch NZB content from indexers before submission
	// validateNZBURL is injectable for tests; nil uses httpsec.ValidateOutboundURL.
	validateNZBURL func(string) error
}

// New creates a SABnzbd client. urlBase is the optional reverse-proxy
// subpath that is appended between host:port and the /api endpoint.
func New(host string, port int, apiKey, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return &Client{
		baseURL:   fmt.Sprintf("%s://%s:%d%s", scheme, host, port, urlbase.Normalize(urlBase)),
		apiKey:    apiKey,
		http:      &http.Client{Timeout: 15 * time.Second},
		fetchHTTP: &http.Client{Timeout: 60 * time.Second},
		validateNZBURL: func(raw string) error {
			return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLAN)
		},
	}
}

// Test verifies connectivity and, when categories are supplied, that each
// configured category actually exists in SABnzbd. It fetches the category list
// once: a failure there is a reachability/auth problem (with a network hint),
// while a category that's present-but-unknown is a configuration problem that
// would otherwise only surface as downloads silently landing in SAB's default
// category. The variadic wanted lets callers (the save-time TestClient path)
// pass client.Category and client.CategoryAudiobook; an empty Test() call still
// works as a pure reachability check.
func (c *Client) Test(ctx context.Context, wanted ...string) error {
	have, err := c.GetCategories(ctx)
	if err != nil {
		return fmt.Errorf("could not reach SABnzbd at %s — %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	return checkCategories(have, wanted)
}

// checkCategories reports an error when any non-empty entry in wanted is not in
// have. Empty entries are skipped — an empty category means "use SAB's default
// destination". Mirrors NZBGet's CheckCategories behaviour and message style.
func checkCategories(have, wanted []string) error {
	var nonEmpty []string
	for _, w := range wanted {
		if strings.TrimSpace(w) != "" {
			nonEmpty = append(nonEmpty, w)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
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

// categoryMismatchError formats the actionable error returned when one or more
// Bindery-configured categories aren't defined in SABnzbd. We surface both
// sides so the user can see what to rename on which side.
func categoryMismatchError(missing, have []string) error {
	quote := func(in []string) string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = fmt.Sprintf("%q", s)
		}
		return strings.Join(out, ", ")
	}
	haveStr := "none defined"
	if len(have) > 0 {
		haveStr = quote(have)
	}
	if len(missing) == 1 {
		return fmt.Errorf("SABnzbd has no category %q configured (existing categories: %s). Add it in SABnzbd's Config → Categories, or change the category in Bindery's download-client config to match", missing[0], haveStr)
	}
	return fmt.Errorf("SABnzbd has no categories %s configured (existing categories: %s). Add them in SABnzbd's Config → Categories, or change the categories in Bindery's download-client config to match", quote(missing), haveStr)
}

// AddURL fetches the NZB file from nzbURL (using Bindery's own HTTP client,
// which holds the indexer credentials and the network path) then submits the
// content to SABnzbd via mode=addfile multipart upload. The name is kept for
// call-site compatibility — the URL never reaches SAB.
//
// Sending content rather than a URL means SAB never has to reach the indexer
// directly. In containerised setups where Bindery and SAB sit on different
// Docker networks (or only Bindery has DNS for the indexer), SAB's addurl
// path fails silently and the queued item sits in "Waiting" with a resetting
// retry countdown rather than producing a clear rejection. This mirrors the
// fix the NZBGet client got — see internal/downloader/nzbget/client.go's Add.
func (c *Client) AddURL(ctx context.Context, nzbURL, title, category string, priority int) (*AddURLResponse, error) {
	content, err := c.fetchNZBContent(ctx, nzbURL)
	if err != nil {
		return nil, err
	}

	filename := nzbFilename(title)
	body, contentType, err := buildAddFileBody(filename, content)
	if err != nil {
		return nil, fmt.Errorf("build addfile body: %w", err)
	}

	params := url.Values{
		"mode":     {"addfile"},
		"nzbname":  {title},
		"cat":      {category},
		"priority": {fmt.Sprintf("%d", priority)},
		"pp":       {"3"}, // repair + unpack + delete archives
	}

	var resp AddURLResponse
	if err := c.apiUpload(ctx, params, body, contentType, &resp); err != nil {
		return nil, fmt.Errorf("add nzb: %w", err)
	}
	if !resp.Status {
		return nil, fmt.Errorf("SABnzbd rejected download")
	}
	return &resp, nil
}

// nzbFilename returns a safe .nzb filename for the SAB multipart upload. SAB
// uses the upload filename as the job's display name when nzbname is not
// provided; we always set nzbname, but the filename still needs to be benign.
func nzbFilename(title string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '\x00':
			return '_'
		}
		return r
	}, strings.TrimSpace(title))
	if cleaned == "" {
		cleaned = "bindery"
	}
	return cleaned + ".nzb"
}

// buildAddFileBody builds the multipart/form-data body SAB expects for
// mode=addfile. Field name is "name" — that's what SAB looks for.
func buildAddFileBody(filename string, content []byte) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("name", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &body, mw.FormDataContentType(), nil
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
	// Some indexers (e.g. nzbfinder.ws, #1053) fingerprint the User-Agent and
	// serve an anti-bot 403 to Go's default UA, so use the project UA the
	// search path already relies on.
	req.Header.Set("User-Agent", useragent.Get())
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

// GetQueue returns the current download queue.
func (c *Client) GetQueue(ctx context.Context) (*QueueData, error) {
	params := url.Values{
		"mode":  {"queue"},
		"start": {"0"},
		"limit": {"100"},
	}

	var resp QueueResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get queue: %w", err)
	}
	return &resp.Queue, nil
}

// GetHistory returns completed/failed downloads.
func (c *Client) GetHistory(ctx context.Context, category string, limit int) (*HistoryData, error) {
	params := url.Values{
		"mode":  {"history"},
		"start": {"0"},
		"limit": {fmt.Sprintf("%d", limit)},
	}
	if category != "" {
		params.Set("cat", category)
	}

	var resp HistoryResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	return &resp.History, nil
}

// GetCategories lists all configured categories.
func (c *Client) GetCategories(ctx context.Context) ([]string, error) {
	params := url.Values{"mode": {"get_cats"}}

	var resp CategoriesResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get categories: %w", err)
	}
	return resp.Categories, nil
}

// Pause pauses a download by NZO ID.
func (c *Client) Pause(ctx context.Context, nzoID string) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"pause"},
		"value": {nzoID},
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// Resume resumes a paused download.
func (c *Client) Resume(ctx context.Context, nzoID string) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"resume"},
		"value": {nzoID},
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// Delete removes a download from the queue.
func (c *Client) Delete(ctx context.Context, nzoID string, deleteFiles bool) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"delete"},
		"value": {nzoID},
	}
	if deleteFiles {
		params.Set("del_files", "1")
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// DeleteHistory removes a finished job from SABnzbd's history. When deleteFiles
// is true, SAB also wipes the on-disk completed folder — bindery's importer has
// typically already moved the contents, so callers usually pass false.
func (c *Client) DeleteHistory(ctx context.Context, nzoID string, deleteFiles bool) error {
	params := url.Values{
		"mode":  {"history"},
		"name":  {"delete"},
		"value": {nzoID},
	}
	if deleteFiles {
		params.Set("del_files", "1")
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// redactAPIURL returns a copy of rawURL with the "apikey" query parameter
// replaced by "REDACTED", safe for use in error messages and logs.
func redactAPIURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable url]"
	}
	q := parsed.Query()
	if q.Get("apikey") != "" {
		q.Set("apikey", "REDACTED")
		parsed.RawQuery = q.Encode()
	}
	return parsed.String()
}

func (c *Client) apiCall(ctx context.Context, params url.Values, target interface{}) error {
	params.Set("apikey", c.apiKey)
	params.Set("output", "json")

	u := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", redactAPIURL(u), err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s: %w", redactAPIURL(u), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// apiUpload POSTs a multipart body to the SAB /api endpoint. The api-key
// and output params still travel as query string (SAB accepts both shapes
// for addfile; query is what apiCall does for everything else, so keep the
// surface consistent).
func (c *Client) apiUpload(ctx context.Context, params url.Values, body *bytes.Buffer, contentType string, target interface{}) error {
	params.Set("apikey", c.apiKey)
	params.Set("output", "json")

	u := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return fmt.Errorf("build upload for %s: %w", redactAPIURL(u), err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", redactAPIURL(u), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}
