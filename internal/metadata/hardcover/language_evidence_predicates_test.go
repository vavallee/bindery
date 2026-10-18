package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// fakeEditionsAPI answers the editions queries behind the language evidence
// lookup the way Hardcover answered them when #3333 was measured:
//
//   - a query that joins the book.slug and book_id predicates in one _or runs
//     into Hardcover's statement timeout and comes back as HTTP 408
//     {"error":"Request timeout"}. Live that takes about 15 s, for every
//     author with Hardcover works; here it is immediate.
//   - a variable that the query declares but never uses, or sends without
//     declaring, is refused with validation-failed, as Hasura does.
//   - otherwise the slug or the book_id predicate picks the works, the
//     language filter picks their editions, and distinct_on(book_id) keeps the
//     first matching edition of each work, up to $limit.
type fakeEditionsAPI struct {
	t     *testing.T
	works []fakeWork
	// fail, when set, answers the requests it matches with HTTP 500.
	fail     func(gqlRequest) bool
	requests []gqlRequest
}

type fakeWork struct {
	id   int
	slug string
	// editions in edition-id order. A zero hcLanguage records no language.
	editions []hcLanguage
}

var (
	english = hcLanguage{Code2: "en", Code3: "eng", Language: "English"}
	italian = hcLanguage{Code2: "it", Code3: "ita", Language: "Italian"}
	spanish = hcLanguage{Code2: "es", Code3: "spa", Language: "Spanish"}

	gqlVariableDecl = regexp.MustCompile(`\$(\w+)\s*:`)
)

func (f *fakeEditionsAPI) client() *Client {
	return newMockClient(f.handle)
}

func (f *fakeEditionsAPI) handle(r *http.Request) (*http.Response, error) {
	t := f.t
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	f.requests = append(f.requests, req)

	bySlug := strings.Contains(req.Query, "book: {slug: {_in: $slugs}}")
	byID := strings.Contains(req.Query, "book_id: {_in: $bookIds}")
	if bySlug && byID {
		return gqlResponse(t, http.StatusRequestTimeout, `{"error":"Request timeout"}`), nil
	}

	head, selection, _ := strings.Cut(req.Query, ")")
	declared := map[string]bool{}
	for _, m := range gqlVariableDecl.FindAllStringSubmatch(head, -1) {
		declared[m[1]] = true
		_, sent := req.Variables[m[1]]
		used := regexp.MustCompile(`\$` + m[1] + `\b`).MatchString(selection)
		if !sent || !used {
			return validationFailed(t, m[1]), nil
		}
	}
	for name := range req.Variables {
		if !declared[name] {
			return validationFailed(t, name), nil
		}
	}

	if f.fail != nil && f.fail(req) {
		return gqlResponse(t, http.StatusInternalServerError, `{"error":"upstream unavailable"}`), nil
	}

	var named func(fakeWork) bool
	switch {
	case bySlug:
		slugs := stringVars(t, req.Variables["slugs"])
		named = func(w fakeWork) bool { return slices.Contains(slugs, w.slug) }
	case byID:
		ids := intVars(t, req.Variables["bookIds"])
		named = func(w fakeWork) bool { return slices.Contains(ids, w.id) }
	default:
		t.Fatalf("editions query names no works: %s", req.Query)
	}
	var keep func(hcLanguage) bool
	switch {
	case strings.Contains(selection, "$languageCodes"):
		codes := stringVars(t, req.Variables["languageCodes"])
		keep = func(l hcLanguage) bool { return slices.Contains(codes, l.Code2) || slices.Contains(codes, l.Code3) }
	case strings.Contains(selection, "language: {id: {_is_null: false}}"):
		keep = func(l hcLanguage) bool { return l != hcLanguage{} }
	default:
		t.Fatalf("unrecognised edition filter: %s", req.Query)
	}
	limit, _ := req.Variables["limit"].(float64)

	works := slices.Clone(f.works)
	slices.SortFunc(works, func(a, b fakeWork) int { return a.id - b.id })
	rows := []map[string]any{}
	for _, w := range works {
		if len(rows) == int(limit) {
			break
		}
		if !named(w) {
			continue
		}
		for _, l := range w.editions {
			if keep(l) {
				rows = append(rows, map[string]any{
					"book":     map[string]any{"id": w.id, "slug": w.slug},
					"language": map[string]any{"code2": l.Code2, "code3": l.Code3, "language": l.Language},
				})
				break
			}
		}
	}
	return gqlResponse(t, http.StatusOK, map[string]any{"editions": rows}), nil
}

func validationFailed(t *testing.T, variable string) *http.Response {
	return gqlResponse(t, http.StatusOK, `{"errors":[{"message":"unexpected variables in variableValues: `+variable+`","extensions":{"path":"$","code":"validation-failed"}}]}`)
}

func stringVars(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("variable = %#v, want a list", v)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}

func intVars(t *testing.T, v any) []int {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("variable = %#v, want a list", v)
	}
	out := make([]int, 0, len(raw))
	for _, item := range raw {
		out = append(out, int(item.(float64)))
	}
	return out
}

// requestsUsing returns the requests whose query names works by the given
// predicate variable ("slugs" or "bookIds").
func (f *fakeEditionsAPI) requestsUsing(variable string) []gqlRequest {
	var out []gqlRequest
	for _, req := range f.requests {
		if strings.Contains(req.Query, "$"+variable) {
			out = append(out, req)
		}
	}
	return out
}

// Works that Hardcover names by slug, which is every supplement work in
// practice, must be looked up by slug alone (#3333). The lookup used to join
// the slug predicate to an empty book_id predicate in one _or, Hardcover
// cancelled that query with HTTP 408 after about 15 s for every author, and
// the works fell back to their default-edition language.
func TestGetAuthorWorkLanguageEvidence_SlugNamedWorksQueryBySlugAlone(t *testing.T) {
	api := &fakeEditionsAPI{t: t, works: []fakeWork{
		{id: 101, slug: "four-thousand-weeks", editions: []hcLanguage{italian, english}},
		{id: 102, slug: "quattromila-settimane", editions: []hcLanguage{italian}},
		{id: 103, slug: "the-antidote", editions: []hcLanguage{english}},
		{id: 104, slug: "el-antidoto", editions: []hcLanguage{spanish}},
		{id: 105, slug: "aliveness", editions: []hcLanguage{{}}},
	}}
	books := []models.Book{
		{ForeignID: "hc:four-thousand-weeks"},
		{ForeignID: "hc:quattromila-settimane"},
		{ForeignID: "hc:the-antidote", Language: "eng"},
		{ForeignID: "hc:el-antidoto", Language: "spa"},
		{ForeignID: "hc:aliveness"},
	}

	got, err := api.client().GetAuthorWorkLanguageEvidence(context.Background(), books, []string{"eng"})
	if err != nil {
		t.Fatalf("language evidence lookup failed: %v", err)
	}
	want := map[string]metadata.AuthorWorkLanguageEvidence{
		"hc:four-thousand-weeks":   {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:quattromila-settimane": {State: metadata.AuthorWorkLanguageNotAllowed, Language: "ita"},
		"hc:the-antidote":          {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:el-antidoto":           {State: metadata.AuthorWorkLanguageNotAllowed, Language: "spa"},
		"hc:aliveness":             {State: metadata.AuthorWorkLanguageIndeterminate},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
	// The allowed-language query, then the follow up for the blank works that
	// it did not settle. Both by slug, and neither carries $bookIds.
	if len(api.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (allowed languages, then known languages)", len(api.requests))
	}
	for _, req := range api.requests {
		if strings.Contains(req.Query, "bookIds") {
			t.Errorf("slug-named works sent the book_id predicate: %s", req.Query)
		}
		if _, ok := req.Variables["bookIds"]; ok {
			t.Errorf("slug-named works sent a bookIds variable: %v", req.Variables)
		}
	}
	if limit := api.requests[0].Variables["limit"]; limit != float64(len(books)) {
		t.Errorf("allowed-language limit = %v, want %d", limit, len(books))
	}
}

// A work Bindery knows only by its numeric Hardcover id (the slug was empty)
// still needs the book_id predicate. It goes out as its own query and the rows
// are merged, rather than joined to the slug predicate in an _or (#3333).
func TestGetAuthorWorkLanguageEvidence_NumericIDsQueriedSeparatelyAndMerged(t *testing.T) {
	api := &fakeEditionsAPI{t: t, works: []fakeWork{
		{id: 42, slug: "", editions: []hcLanguage{spanish, english}},
		{id: 77, slug: "", editions: []hcLanguage{italian}},
		{id: 101, slug: "four-thousand-weeks", editions: []hcLanguage{english}},
		{id: 102, slug: "quattromila-settimane", editions: []hcLanguage{italian}},
	}}
	books := []models.Book{
		{ForeignID: "hc:four-thousand-weeks"},
		{ForeignID: "hc:42", Language: "spa"},
		{ForeignID: "hc:quattromila-settimane"},
		{ForeignID: "hc:77"},
	}

	got, err := api.client().GetAuthorWorkLanguageEvidence(context.Background(), books, []string{"eng"})
	if err != nil {
		t.Fatalf("language evidence lookup failed: %v", err)
	}
	want := map[string]metadata.AuthorWorkLanguageEvidence{
		"hc:four-thousand-weeks":   {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:42":                    {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:quattromila-settimane": {State: metadata.AuthorWorkLanguageNotAllowed, Language: "ita"},
		"hc:77":                    {State: metadata.AuthorWorkLanguageNotAllowed, Language: "ita"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}

	bySlug, byID := api.requestsUsing("slugs"), api.requestsUsing("bookIds")
	if len(bySlug)+len(byID) != len(api.requests) {
		t.Fatalf("a request named works by both predicates: %d requests, %d by slug, %d by id", len(api.requests), len(bySlug), len(byID))
	}
	// Allowed languages, then known languages for the blank works left: once
	// by slug and once by id each.
	if len(bySlug) != 2 || len(byID) != 2 {
		t.Fatalf("requests by slug = %d, by id = %d; want 2 and 2", len(bySlug), len(byID))
	}
	if ids := intVars(t, byID[0].Variables["bookIds"]); !slices.Equal(ids, []int{42, 77}) {
		t.Errorf("allowed-language bookIds = %v, want [42 77]", ids)
	}
	if limit := byID[0].Variables["limit"]; limit != float64(2) {
		t.Errorf("allowed-language book_id limit = %v, want 2", limit)
	}
	if limit := bySlug[0].Variables["limit"]; limit != float64(4) {
		t.Errorf("allowed-language slug limit = %v, want 4", limit)
	}
	// hc:42 is settled by its English edition; only hc:77 is still blank.
	if ids := intVars(t, byID[1].Variables["bookIds"]); !slices.Equal(ids, []int{77}) {
		t.Errorf("known-language bookIds = %v, want [77]", ids)
	}
}

// The evidence is authoritative only when every query that ran succeeded:
// absence of an allowed edition from a partial answer proves nothing. A
// failure of either query fails the whole lookup, so the caller keeps its
// existing fallbacks, as it did when the single query failed.
func TestGetAuthorWorkLanguageEvidence_EitherPredicateFailingReturnsNoEvidence(t *testing.T) {
	for _, variable := range []string{"slugs", "bookIds"} {
		t.Run(variable, func(t *testing.T) {
			api := &fakeEditionsAPI{
				t: t,
				works: []fakeWork{
					{id: 42, slug: "", editions: []hcLanguage{english}},
					{id: 101, slug: "four-thousand-weeks", editions: []hcLanguage{english}},
				},
				fail: func(req gqlRequest) bool {
					_, ok := req.Variables[variable]
					return ok
				},
			}
			got, err := api.client().GetAuthorWorkLanguageEvidence(context.Background(), []models.Book{
				{ForeignID: "hc:four-thousand-weeks", Language: "spa"},
				{ForeignID: "hc:42", Language: "spa"},
			}, []string{"eng"})
			if err == nil {
				t.Fatal("expected the lookup to fail")
			}
			if got != nil {
				t.Fatalf("evidence after a failed %s query = %#v, want nil", variable, got)
			}
		})
	}
}
