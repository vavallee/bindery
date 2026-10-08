package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// knownLanguageEvidenceClient answers the allowed-language lookup with
// allowed, and the follow up for blank-language works with known (or an
// error when knownStatus is not 200).
func knownLanguageEvidenceClient(t *testing.T, allowed, known []map[string]interface{}, knownStatus int, knownRequests *[]map[string]interface{}) *Client {
	return newMockClient(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(req.Query, "GetAuthorWorkKnownLanguages") {
			*knownRequests = append(*knownRequests, req.Variables)
			if knownStatus != http.StatusOK {
				return gqlResponse(t, knownStatus, `{"error":"upstream unavailable"}`), nil
			}
			return gqlResponse(t, http.StatusOK, map[string]interface{}{"editions": known}), nil
		}
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"editions": allowed}), nil
	})
}

func evidenceEdition(id int, slug, code2, code3, name string) map[string]interface{} {
	return map[string]interface{}{
		"book":     map[string]interface{}{"id": id, "slug": slug},
		"language": map[string]interface{}{"code2": code2, "code3": code3, "language": name},
	}
}

// A Hardcover work with no default-edition language and no edition in an
// allowed language, but with editions in another language, is a translation:
// it is rejected rather than left indeterminate (#3091). Before, a blank
// default edition language made every such work indeterminate, so the
// unknown-language "pass" default let translations in by the thousand.
func TestGetAuthorWorkLanguageEvidence_BlankLanguageResolvedFromKnownEditions(t *testing.T) {
	var knownRequests []map[string]interface{}
	c := knownLanguageEvidenceClient(t,
		[]map[string]interface{}{evidenceEdition(2, "pale-blue-dot", "en", "eng", "English")},
		[]map[string]interface{}{
			evidenceEdition(1, "o-ponto-azul-claro", "pt", "por", "Portuguese"),
			// Not a requested work: ignored.
			evidenceEdition(9, "not-requested", "fr", "fre", "French"),
		},
		http.StatusOK, &knownRequests)

	books := []models.Book{
		{ForeignID: "hc:o-ponto-azul-claro"},
		{ForeignID: "hc:pale-blue-dot"},
		{ForeignID: "hc:no-language-anywhere"},
		{ForeignID: "hc:spanish-default", Language: "spa"},
	}
	got, err := c.GetAuthorWorkLanguageEvidence(context.Background(), books, []string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]metadata.AuthorWorkLanguageEvidence{
		"hc:o-ponto-azul-claro":   {State: metadata.AuthorWorkLanguageNotAllowed, Language: "por"},
		"hc:pale-blue-dot":        {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:no-language-anywhere": {State: metadata.AuthorWorkLanguageIndeterminate},
		"hc:spanish-default":      {State: metadata.AuthorWorkLanguageNotAllowed, Language: "spa"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
	// Only the works still undecided with no language of their own are asked
	// about: the allowed one and the one with a default language are not.
	if len(knownRequests) != 1 {
		t.Fatalf("known-language lookups = %d, want 1", len(knownRequests))
	}
	slugs, _ := knownRequests[0]["slugs"].([]interface{})
	if len(slugs) != 2 || slugs[0] != "o-ponto-azul-claro" || slugs[1] != "no-language-anywhere" {
		t.Fatalf("known-language lookup asked about %v, want the two blank, undecided works", slugs)
	}
	if limit := knownRequests[0]["limit"]; limit != float64(2) {
		t.Fatalf("known-language lookup limit = %v, want 2", limit)
	}
}

// The allowed-language query matches on code2 and code3 only, so an edition
// whose language Hardcover records by name ("English", no codes) is missed by
// it. The follow up normalises the name and must not then reject the work for
// being in the very language the profile allows.
func TestGetAuthorWorkLanguageEvidence_KnownLanguageThatIsAllowedIsNotRejected(t *testing.T) {
	var knownRequests []map[string]interface{}
	c := knownLanguageEvidenceClient(t,
		nil,
		[]map[string]interface{}{
			evidenceEdition(1, "named-english", "", "", "English"),
			evidenceEdition(2, "o-ponto-azul-claro", "pt", "por", "Portuguese"),
		},
		http.StatusOK, &knownRequests)

	got, err := c.GetAuthorWorkLanguageEvidence(context.Background(), []models.Book{
		{ForeignID: "hc:named-english"},
		{ForeignID: "hc:o-ponto-azul-claro"},
	}, []string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]metadata.AuthorWorkLanguageEvidence{
		"hc:named-english":      {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
		"hc:o-ponto-azul-claro": {State: metadata.AuthorWorkLanguageNotAllowed, Language: "por"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
}

// The follow up is best effort: if it fails, the works it would have resolved
// stay indeterminate and the allowed-language evidence still stands.
func TestGetAuthorWorkLanguageEvidence_KnownLanguageLookupFailureKeepsEvidence(t *testing.T) {
	var knownRequests []map[string]interface{}
	c := knownLanguageEvidenceClient(t,
		[]map[string]interface{}{evidenceEdition(2, "pale-blue-dot", "en", "eng", "English")},
		nil, http.StatusInternalServerError, &knownRequests)

	got, err := c.GetAuthorWorkLanguageEvidence(context.Background(), []models.Book{
		{ForeignID: "hc:o-ponto-azul-claro"},
		{ForeignID: "hc:pale-blue-dot"},
	}, []string{"eng"})
	if err != nil {
		t.Fatalf("a failed follow up failed the whole lookup: %v", err)
	}
	want := map[string]metadata.AuthorWorkLanguageEvidence{
		"hc:o-ponto-azul-claro": {State: metadata.AuthorWorkLanguageIndeterminate},
		"hc:pale-blue-dot":      {State: metadata.AuthorWorkLanguageAllowed, Language: "eng"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
	if len(knownRequests) == 0 {
		t.Fatal("follow up never attempted")
	}
}
