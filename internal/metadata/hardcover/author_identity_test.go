package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// capturedRequest is one GraphQL call the client made.
type capturedRequest struct {
	Query     string
	Variables map[string]any
}

// newRecordingClient answers each call with the next response in order and
// records every request, so a test can assert which queries ran and in what
// sequence. Running out of responses answers with an empty book list.
func newRecordingClient(t *testing.T, responses []string, got *[]capturedRequest) *Client {
	t.Helper()
	call := 0
	return newMockClient(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		*got = append(*got, capturedRequest(req))

		resp := `{"data":{"books":[]}}`
		if call < len(responses) {
			resp = responses[call]
		}
		call++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(resp)),
			Header:     make(http.Header),
		}, nil
	}).WithToken("hc-token")
}

const oneBookJSON = `{"id":1,"title":"A Book","slug":"a-book","contributions":[{"author":{"id":9,"name":"J.A. Andrews","slug":"j-a-andrews"}}]}`

// TestGetAuthorWorksByIdentity_QueriesBySlugNotName is the #1734 fix: two real
// people publishing as "J.A. Andrews" are indistinguishable to a name query, so
// once the row is linked the works must be selected by the linked author.
func TestGetAuthorWorksByIdentity_QueriesBySlugNotName(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, []string{`{"data":{"books":[` + oneBookJSON + `]}}`}, &reqs)

	books, err := c.GetAuthorWorksByIdentity(context.Background(), "hc:j-a-andrews")
	if err != nil {
		t.Fatalf("GetAuthorWorksByIdentity: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if len(reqs) != 1 {
		t.Fatalf("made %d queries, want 1", len(reqs))
	}
	q := reqs[0].Query
	if !strings.Contains(q, `contributions: {author: {slug: {_eq: $slug}}`) {
		t.Errorf("query does not select by author slug: %s", q)
	}
	if strings.Contains(q, `name: {_eq:`) {
		t.Errorf("query still matches on author name: %s", q)
	}
	if got := reqs[0].Variables["slug"]; got != "j-a-andrews" {
		t.Errorf("slug variable = %v, want the hc: prefix stripped", got)
	}
	// The role filter from #1733 must survive: both same-named people hold
	// genuine author contributions, so role alone never fixed this, but
	// dropping it would re-import narrator and translator credits.
	if !strings.Contains(q, authorContributionFilter) {
		t.Errorf("query dropped the contribution-role filter: %s", q)
	}
}

// TestGetAuthorWorksByIdentity_FallsBackToNumericID covers the degenerate
// "hc:<number>" identity toAuthor emits when an author has no slug. Slug first,
// because a numeric value may legitimately be a slug (#1256).
func TestGetAuthorWorksByIdentity_FallsBackToNumericID(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, []string{
		`{"data":{"books":[]}}`, // slug lookup finds nothing
		`{"data":{"books":[` + oneBookJSON + `]}}`,
	}, &reqs)

	books, err := c.GetAuthorWorksByIdentity(context.Background(), "hc:12345")
	if err != nil {
		t.Fatalf("GetAuthorWorksByIdentity: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if len(reqs) != 2 {
		t.Fatalf("made %d queries, want 2 (slug then id)", len(reqs))
	}
	if !strings.Contains(reqs[0].Query, `slug: {_eq: $slug}`) {
		t.Errorf("first query was not the slug lookup: %s", reqs[0].Query)
	}
	if !strings.Contains(reqs[1].Query, `id: {_eq: $authorId}`) {
		t.Errorf("second query was not the id lookup: %s", reqs[1].Query)
	}
	if got := reqs[1].Variables["authorId"]; got != float64(12345) {
		t.Errorf("authorId variable = %v, want 12345", got)
	}
}

// TestGetAuthorWorksByIdentity_NeverFallsBackToName is the important negative.
// Widening to the name query when an identity finds nothing would re-merge the
// same-named authors at exactly the moment the caller asked for precision.
func TestGetAuthorWorksByIdentity_NeverFallsBackToName(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, nil, &reqs) // every response is an empty list

	books, err := c.GetAuthorWorksByIdentity(context.Background(), "hc:nobody-here")
	if err != nil {
		t.Fatalf("GetAuthorWorksByIdentity: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("got %d books, want 0", len(books))
	}
	for _, r := range reqs {
		if strings.Contains(r.Query, `name: {_eq:`) {
			t.Fatalf("fell back to a name query: %s", r.Query)
		}
	}
	// A non-numeric slug that matched nothing must not even try the id form.
	if len(reqs) != 1 {
		t.Errorf("made %d queries, want 1 for a non-numeric slug", len(reqs))
	}
}

// TestGetAuthorWorksByIdentity_EmptyIdentityDoesNothing: no identity means no
// query at all, rather than a query with an empty predicate.
func TestGetAuthorWorksByIdentity_EmptyIdentityDoesNothing(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, nil, &reqs)

	books, err := c.GetAuthorWorksByIdentity(context.Background(), "hc:")
	if err != nil {
		t.Fatalf("GetAuthorWorksByIdentity: %v", err)
	}
	if len(books) != 0 || len(reqs) != 0 {
		t.Errorf("got %d books from %d queries, want 0 and 0", len(books), len(reqs))
	}
}

// TestGetAuthorWorks_UsesIdentity: implementing worksProvider is what stops an
// hc-primary author falling through to SearchBooks, which is another
// name-shaped lookup with the same merging problem.
func TestGetAuthorWorks_UsesIdentity(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, []string{`{"data":{"books":[` + oneBookJSON + `]}}`}, &reqs)

	if _, err := c.GetAuthorWorks(context.Background(), "hc:j-a-andrews"); err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(reqs) != 1 || !strings.Contains(reqs[0].Query, `slug: {_eq: $slug}`) {
		t.Fatalf("GetAuthorWorks did not run the identity query: %+v", reqs)
	}
}

// TestGetAuthorWorksByName_StillMatchesOnName: the name query is still correct
// for the initial resolve step, where no better identity exists yet.
func TestGetAuthorWorksByName_StillMatchesOnName(t *testing.T) {
	var reqs []capturedRequest
	c := newRecordingClient(t, []string{`{"data":{"books":[` + oneBookJSON + `]}}`}, &reqs)

	if _, err := c.GetAuthorWorksByName(context.Background(), "J.A. Andrews"); err != nil {
		t.Fatalf("GetAuthorWorksByName: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("made %d queries, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0].Query, `name: {_eq: $author}`) {
		t.Errorf("name query changed shape: %s", reqs[0].Query)
	}
}
