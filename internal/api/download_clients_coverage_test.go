package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestDownloadClientUpdateCoverageRefusals covers the refusals on update:
// unknown id, malformed body, a credential set and cleared in one request, and
// a host the outbound guard refuses. None may touch the stored row.
func TestDownloadClientUpdateCoverageRefusals(t *testing.T) {
	h, clients := downloadClientFixture(t)
	c := &models.DownloadClient{Name: "SAB", Type: "sabnzbd", Host: "10.0.0.7", Port: 8080, APIKey: "sab-key", Enabled: true}
	if err := clients.Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(c.ID, 10)
	for _, tc := range []struct {
		name, id, body string
		want           int
		wantErr        string
	}{
		{"bad id", "x", `{}`, http.StatusBadRequest, ""},
		{"missing", "9999", `{}`, http.StatusNotFound, "download client not found"},
		{"bad body", idStr, `{"name":`, http.StatusBadRequest, "invalid request body"},
		{"api key and clear", idStr, `{"apiKey":"new","clearApiKey":true}`, http.StatusBadRequest, "apiKey and clearApiKey cannot both be set"},
		{"password and clear", idStr, `{"password":"pw","clearPassword":true}`, http.StatusBadRequest, "password and clearPassword cannot both be set"},
		{"clear flag wrong type", idStr, `{"clearApiKey":"yes"}`, http.StatusBadRequest, ""},
		{"link local host", idStr, `{"host":"169.254.169.254"}`, http.StatusBadRequest, "link-local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/downloadclient/"+tc.id, bytes.NewBufferString(tc.body)), "id", tc.id))
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body.String(), tc.want, tc.wantErr)
			}
			got, _ := clients.GetByID(t.Context(), c.ID)
			if got.Host != "10.0.0.7" || got.APIKey != "sab-key" || got.Name != "SAB" {
				t.Fatalf("a refused update changed the client: %+v", got)
			}
		})
	}
}
