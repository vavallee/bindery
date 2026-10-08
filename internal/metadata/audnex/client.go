// Package audnex provides a read-only client for api.audnex.us, a public
// JSON wrapper around Audible's catalogue. Used to enrich audiobook records
// with narrator, duration, and cover art once an ASIN is known.
package audnex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

const defaultBaseURL = "https://api.audnex.us"

// Client talks to the audnex.us API. Zero-value is NOT usable; construct
// with New().
type Client struct {
	baseURL string
	http    *http.Client
	region  string
	// gate holds every request this client makes while audnex is refusing.
	gate *providerhttp.Gate
}

// New returns an audnex client. Region defaults to "us" — other supported
// regions include uk, de, fr, jp, it, es, au, in, ca.
func New(region string) *Client {
	if region == "" {
		region = "us"
	}
	return &Client{
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
		region:  region,
		gate:    providerhttp.NewGate(),
	}
}

// Book is the subset of fields we currently use. audnex returns more
// structure (chapters, series, etc.) — add here as needed.
type Book struct {
	ASIN             string   `json:"asin"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	Authors          []Person `json:"authors"`
	Narrators        []Person `json:"narrators"`
	PublisherName    string   `json:"publisherName"`
	Summary          string   `json:"summary"`
	ReleaseDate      string   `json:"releaseDate"`
	RuntimeLengthMin int      `json:"runtimeLengthMin"`
	Image            string   `json:"image"`
	Language         string   `json:"language"`
}

type Person struct {
	ASIN string `json:"asin,omitempty"`
	Name string `json:"name"`
}

// GetBook fetches metadata for a known ASIN. Returns nil, nil for 404.
func (c *Client) GetBook(ctx context.Context, asin string) (*Book, error) {
	asin = strings.ToUpper(strings.TrimSpace(asin))
	if asin == "" {
		return nil, fmt.Errorf("asin required")
	}
	u := fmt.Sprintf("%s/books/%s?region=%s", c.baseURL, url.PathEscape(asin), url.QueryEscape(c.region))
	// The shared provider request loop retries a 429 or 5xx with Retry-After
	// and backoff, and holds every audnex request while one is refused
	// (#2369).
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		URL:  u,
		Pass: []int{http.StatusNotFound},
	})
	if err != nil {
		var status *providerhttp.StatusError
		if errors.As(err, &status) {
			return nil, fmt.Errorf("audnex GET %s: %w", u, err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	var b Book
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, fmt.Errorf("decode audnex response: %w", err)
	}
	return &b, nil
}

// NarratorList joins narrator names into a comma-separated string.
func (b *Book) NarratorList() string {
	names := make([]string, 0, len(b.Narrators))
	for _, n := range b.Narrators {
		if n.Name != "" {
			names = append(names, n.Name)
		}
	}
	return strings.Join(names, ", ")
}

// DurationSeconds returns the book's runtime in seconds, or 0 when unknown.
func (b *Book) DurationSeconds() int {
	return b.RuntimeLengthMin * 60
}
