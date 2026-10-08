package migrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/models"
)

// heldPrimary is an OpenLibrary primary behaving the way the shared provider
// request loop does after one 429 with a Retry-After longer than a lookup's
// deadline: the refused lookup gets the 429, and every lookup until the hold
// ends is turned away at once with a *providerhttp.HeldError, without a
// request. After the hold it answers every name with its own author.
type heldPrimary struct {
	mu      sync.Mutex
	hold    time.Duration
	until   time.Time
	refused int
}

func (h *heldPrimary) search(_ context.Context, name string) ([]models.Author, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	switch {
	case h.until.IsZero():
		h.until = now.Add(h.hold)
		h.refused++
		return nil, fmt.Errorf("search authors: %w", &providerhttp.StatusError{Code: 429, Body: "Too Many Requests"})
	case now.Before(h.until):
		h.refused++
		return nil, fmt.Errorf("search authors: %w", &providerhttp.HeldError{Provider: "openlibrary", Until: h.until, Cause: metadata.ErrRateLimited})
	}
	id := "OL" + strings.TrimPrefix(name, "Held Author ") + "A"
	return []models.Author{{ForeignID: id, Name: name, SortName: name}}, nil
}

func (h *heldPrimary) provider() *stubProvider {
	return &stubProvider{
		name:            "openlibrary",
		searchAuthorsFn: h.search,
		getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
			n := strings.TrimSuffix(strings.TrimPrefix(id, "OL"), "A")
			return &models.Author{ForeignID: id, Name: "Held Author " + n, SortName: "Held Author " + n}, nil
		},
	}
}

// TestImportCSVAuthors_WaitsOutAPrimaryHold is the #3100 review finding: the
// provider clients now fail a held request at once, so after a single 429
// every following row was turned away within a millisecond, three of them
// tripped the outage breaker, and every remaining row failed. A hold is the
// primary asking for a pause, not the primary being down: the import waits
// it out and every row is added.
func TestImportCSVAuthors_WaitsOutAPrimaryHold(t *testing.T) {
	primary := &heldPrimary{hold: time.Second}
	agg := metadata.NewAggregator(primary.provider())
	repo := db.NewAuthorRepo(newTestDB(t))

	names := make([]string, 6)
	for i := range names {
		names[i] = fmt.Sprintf("Held Author %d", i+1)
	}
	res, err := ImportCSVAuthors(context.Background(), strings.NewReader(strings.Join(names, "\n")), repo, nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if res.Added != len(names) || res.Errors != 0 {
		t.Fatalf("Added=%d Errors=%d, want %d/0 (failures=%v)", res.Added, res.Errors, len(names), res.Failures)
	}
	if primary.refused == 0 {
		t.Fatal("the primary never refused, so the test exercised nothing")
	}
}

func TestPrimaryHold(t *testing.T) {
	until := time.Now().Add(5 * time.Second)
	held := metadata.SearchOutcome{Primary: "openlibrary", PrimaryFailed: true,
		FirstErr: fmt.Errorf("x: %w", &providerhttp.HeldError{Until: until, Cause: metadata.ErrUnavailable})}
	if d, ok := primaryHold(held); !ok || d <= 4*time.Second || d > 5*time.Second {
		t.Errorf("held outcome: wait %v, %v; want about 5s", d, ok)
	}
	far := metadata.SearchOutcome{Primary: "openlibrary", PrimaryFailed: true,
		FirstErr: &providerhttp.HeldError{Until: time.Now().Add(time.Hour)}}
	if d, _ := primaryHold(far); d != primaryHoldMaxWait {
		t.Errorf("an hour long hold waits %v, want the %v cap", d, primaryHoldMaxWait)
	}
	limited := metadata.SearchOutcome{Primary: "hardcover", PrimaryFailed: true,
		FirstErr: fmt.Errorf("x: %w", metadata.ErrRateLimited)}
	if d, ok := primaryHold(limited); !ok || d != primaryHoldWait {
		t.Errorf("rate limited outcome: wait %v, %v; want %v", d, ok, primaryHoldWait)
	}
	down := metadata.SearchOutcome{Primary: "openlibrary", PrimaryFailed: true,
		FirstErr: fmt.Errorf("x: %w", metadata.ErrUnavailable)}
	if _, ok := primaryHold(down); ok {
		t.Error("a plain outage is not a hold: it belongs to the outage breaker")
	}
	enricher := metadata.SearchOutcome{Primary: "openlibrary", FirstErr: &providerhttp.HeldError{Until: until}}
	if _, ok := primaryHold(enricher); ok {
		t.Error("an enricher's hold must not stall the import while the primary answered")
	}
}
