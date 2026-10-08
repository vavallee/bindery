package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// deadlineISBNProvider fails every ISBN lookup and records the deadline each
// one was given.
type deadlineISBNProvider struct {
	stubMetaProvider
	deadlines []time.Time
	bounded   []bool
}

func (p *deadlineISBNProvider) GetBookByISBN(ctx context.Context, _ string) (*models.Book, error) {
	d, ok := ctx.Deadline()
	p.deadlines = append(p.deadlines, d)
	p.bounded = append(p.bounded, ok)
	return nil, errors.New(p.name + ": HTTP 503: down")
}

// The Add Book dialog's ISBN lookup asks the providers one after another, and
// each retries a refusal or an outage, so with no deadline over the whole
// lookup two dead providers ran past the server's 120s write timeout (#3100
// review). Every provider must be asked under one deadline well inside it.
func TestLookupISBN_HasAnOverallDeadline(t *testing.T) {
	primary := &deadlineISBNProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}}
	dnb := &deadlineISBNProvider{stubMetaProvider: stubMetaProvider{name: "dnb"}}
	h := NewSearchHandler(metadata.NewAggregator(primary, dnb), nil, nil)

	start := time.Now()
	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?isbn="+isbnGuardLookupISBN, nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d with every provider failing", rec.Code)
	}

	for _, p := range []*deadlineISBNProvider{primary, dnb} {
		if len(p.bounded) == 0 {
			t.Fatalf("%s was never asked", p.name)
		}
		for i, ok := range p.bounded {
			if !ok {
				t.Fatalf("%s lookup %d ran with no deadline: retries could outlast the server's write timeout", p.name, i)
			}
			if limit := start.Add(30 * time.Second); p.deadlines[i].After(limit) {
				t.Errorf("%s lookup %d deadline %v from the start, want well inside the 120s write timeout",
					p.name, i, p.deadlines[i].Sub(start))
			}
		}
	}
	// Both providers share one budget rather than getting one each.
	if !primary.deadlines[0].Equal(dnb.deadlines[0]) {
		t.Errorf("providers got deadlines %v and %v, want one shared deadline for the whole lookup",
			primary.deadlines[0], dnb.deadlines[0])
	}
}
