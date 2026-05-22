// Package prowlarr provides a client for the Prowlarr API and a syncer that
// creates/updates/removes Bindery indexer entries from a Prowlarr instance.
package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/useragent"
)

// Client calls the Prowlarr HTTP API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a Prowlarr client with a 60 s default timeout.
func New(baseURL, apiKey string) *Client {
	return NewWithTimeout(baseURL, apiKey, 60*time.Second)
}

// NewWithTimeout creates a Prowlarr client with a custom HTTP timeout.
// Use this when the caller has read a user-configured timeout from settings.
func NewWithTimeout(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// remoteIndexer is the shape of each element in GET /api/v1/indexer.
type remoteIndexer struct {
	ID             int              `json:"id"`
	Name           string           `json:"name"`
	Enable         bool             `json:"enable"`   // Prowlarr's per-indexer enabled flag
	Protocol       string           `json:"protocol"` // "usenet" or "torrent"
	SupportsSearch bool             `json:"supportsSearch"`
	Tags           []int            `json:"tags"`
	Categories     []remoteCategory `json:"categories"`
	Capabilities   struct {
		Categories []remoteCategory `json:"categories"`
	} `json:"capabilities"`
}

type remoteCategory struct {
	ID            int              `json:"id"`
	Name          string           `json:"name"`
	SubCategories []remoteCategory `json:"subCategories"`
}

type remoteApplication struct {
	Enable    bool          `json:"enable"`
	SyncLevel string        `json:"syncLevel"`
	Tags      []int         `json:"tags"`
	Fields    []remoteField `json:"fields"`
}

type remoteField struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

type applicationCategoryScope struct {
	categories []int
	tags       []int
}

// IndexerInfo holds the information needed to create a Bindery indexer from a
// Prowlarr-managed indexer.
type IndexerInfo struct {
	ProwlarrID     int
	Name           string
	Enable         bool
	Protocol       string
	TorznabURL     string
	APIKey         string
	SupportsSearch bool
	Categories     []int
}

// FetchIndexers returns all indexers configured in Prowlarr.
func (c *Client) FetchIndexers(ctx context.Context) ([]IndexerInfo, error) {
	data, err := c.get(ctx, "/api/v1/indexer")
	if err != nil {
		return nil, err
	}

	var remotes []remoteIndexer
	if err := json.Unmarshal(data, &remotes); err != nil {
		return nil, fmt.Errorf("decode prowlarr indexers: %w", err)
	}
	var scopes []applicationCategoryScope
	if needsApplicationCategoryScopes(remotes) {
		scopes, err = c.fetchApplicationCategoryScopes(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch prowlarr applications: %w", err)
		}
	}

	infos := make([]IndexerInfo, 0, len(remotes))
	for _, ri := range remotes {
		// Build the Torznab/Newznab URL: {base}/{id}/api
		torznabURL := fmt.Sprintf("%s/%d/api", c.baseURL, ri.ID)

		cats := categoryIDs(ri.Categories)
		if len(cats) == 0 {
			cats = categoriesFromApplicationScopes(ri, scopes)
		}
		// Issue #763: when Prowlarr has no book-scoped application registered
		// there is no app signal to scope capability categories against. Most
		// Bindery users run Prowlarr standalone (or alongside only Sonarr/
		// Radarr), so without this fallback every indexer is rejected as
		// having "no book/audiobook categories" and the syncer wipes the lot.
		// Trust the indexer's own capability categories, narrowed to the
		// book/audiobook Newznab ranges.
		if len(cats) == 0 && len(scopes) == 0 {
			cats = bookCapabilityCategories(ri)
		}

		infos = append(infos, IndexerInfo{
			ProwlarrID:     ri.ID,
			Name:           ri.Name,
			Enable:         ri.Enable,
			Protocol:       ri.Protocol,
			TorznabURL:     torznabURL,
			APIKey:         c.apiKey,
			SupportsSearch: ri.SupportsSearch,
			Categories:     cats,
		})
	}
	return infos, nil
}

func needsApplicationCategoryScopes(remotes []remoteIndexer) bool {
	for _, remote := range remotes {
		if len(remote.Categories) == 0 && len(remote.Capabilities.Categories) > 0 {
			return true
		}
	}
	return false
}

func (c *Client) fetchApplicationCategoryScopes(ctx context.Context) ([]applicationCategoryScope, error) {
	data, err := c.get(ctx, "/api/v1/applications")
	if err != nil {
		return nil, err
	}
	var apps []remoteApplication
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil, fmt.Errorf("decode prowlarr applications: %w", err)
	}
	scopes := make([]applicationCategoryScope, 0, len(apps))
	for _, app := range apps {
		if !app.Enable || strings.EqualFold(app.SyncLevel, "disabled") {
			continue
		}
		cats := app.syncCategories()
		if !hasBookOrAudiobookCategory(cats) {
			continue
		}
		scopes = append(scopes, applicationCategoryScope{
			categories: cats,
			tags:       app.Tags,
		})
	}
	return scopes, nil
}

func (a remoteApplication) syncCategories() []int {
	for _, field := range a.Fields {
		if field.Name != "syncCategories" {
			continue
		}
		var cats []int
		if err := json.Unmarshal(field.Value, &cats); err == nil {
			return cats
		}
	}
	return nil
}

// isBookOrAudiobookCategory reports whether a Newznab category ID falls in the
// book (7000-7999) or audiobook (3000-3999) ranges.
func isBookOrAudiobookCategory(category int) bool {
	return (category >= 7000 && category < 8000) || (category >= 3000 && category < 4000)
}

func hasBookOrAudiobookCategory(categories []int) bool {
	for _, cat := range categories {
		if isBookOrAudiobookCategory(cat) {
			return true
		}
	}
	return false
}

// bookCapabilityCategories returns an indexer's advertised capability
// categories restricted to the book and audiobook ranges. It is the
// standalone-Prowlarr fallback for FetchIndexers (issue #763): with no
// application scope to consult, the indexer's own capabilities are the only
// signal for whether it can serve books.
func bookCapabilityCategories(indexer remoteIndexer) []int {
	var out []int
	for _, id := range categoryIDs(indexer.Capabilities.Categories) {
		if isBookOrAudiobookCategory(id) {
			out = append(out, id)
		}
	}
	return out
}

func categoryIDs(categories []remoteCategory) []int {
	var ids []int
	seen := map[int]struct{}{}
	for _, cat := range categories {
		collectCategoryIDs(cat, &ids, seen)
	}
	return ids
}

func collectCategoryIDs(cat remoteCategory, ids *[]int, seen map[int]struct{}) {
	if cat.ID != 0 {
		if _, ok := seen[cat.ID]; !ok {
			seen[cat.ID] = struct{}{}
			*ids = append(*ids, cat.ID)
		}
	}
	for _, sub := range cat.SubCategories {
		collectCategoryIDs(sub, ids, seen)
	}
}

func categoriesFromApplicationScopes(indexer remoteIndexer, scopes []applicationCategoryScope) []int {
	if len(scopes) == 0 {
		return nil
	}
	supported := intSet(categoryIDs(indexer.Capabilities.Categories))
	if len(supported) == 0 {
		return nil
	}
	var out []int
	seen := map[int]struct{}{}
	for _, scope := range scopes {
		if !tagsMatch(indexer.Tags, scope.tags) {
			continue
		}
		for _, cat := range scope.categories {
			if _, ok := supported[cat]; !ok {
				continue
			}
			if _, ok := seen[cat]; ok {
				continue
			}
			seen[cat] = struct{}{}
			out = append(out, cat)
		}
	}
	return out
}

func tagsMatch(indexerTags, scopeTags []int) bool {
	if len(scopeTags) == 0 {
		return true
	}
	indexerSet := intSet(indexerTags)
	for _, tag := range scopeTags {
		if _, ok := indexerSet[tag]; ok {
			return true
		}
	}
	return false
}

func intSet(values []int) map[int]struct{} {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// Test verifies connectivity by fetching the Prowlarr system status.
func (c *Client) Test(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v1/system/status")
	if err != nil {
		return "", fmt.Errorf("could not reach Prowlarr at %s — %w", c.baseURL, err)
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return "", nil
	}
	return status.Version, nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("User-Agent", useragent.Get())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid Prowlarr API key")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}
