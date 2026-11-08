package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestQueueGrab_StampsTheGrabbedMediaType is #1964: the slot a grab was made
// for is recorded on the download row, so the importer reads it back instead
// of re-deriving it from the release.
func TestQueueGrab_StampsTheGrabbedMediaType(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	cases := []struct {
		name      string
		title     string
		mediaType string
		want      string
	}{
		{"result classified audiobook, title names nothing", "Zogarth - The Primal Hunter 9", models.MediaTypeAudiobook, models.MediaTypeAudiobook},
		{"result classified ebook, title says m4b", "Zogarth - The Primal Hunter 9 [M4B]", models.MediaTypeEbook, models.MediaTypeEbook},
		{"both falls back to the title token", "Zogarth - The Primal Hunter 9 [M4B]", models.MediaTypeBoth, models.MediaTypeAudiobook},
		{"both with no token in the title is recorded as unknown", "Zogarth - The Primal Hunter 9", models.MediaTypeBoth, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newCategoryRecorder(t)
			h, _, book, ctx := categoryFixture(t, srv.URL)

			payload, _ := json.Marshal(map[string]any{
				"guid": "stamp-" + strconv.Itoa(i), "title": tc.title, "nzbUrl": srv.URL + "/release.nzb",
				"size": 100, "bookId": book.ID, "protocol": "usenet", "mediaType": tc.mediaType,
			})
			w := httptest.NewRecorder()
			h.Grab(w, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(payload)))
			if w.Code != http.StatusAccepted {
				t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
			}
			row, err := h.downloads.GetByGUID(ctx, "stamp-"+strconv.Itoa(i))
			if err != nil || row == nil {
				t.Fatalf("download row missing: %v", err)
			}
			if row.MediaType != tc.want {
				t.Errorf("stored media type = %q, want %q", row.MediaType, tc.want)
			}
		})
	}
}

// TestRetryGrabRequest_PrefersTheStampedMediaType: a retry re-grabs under the
// slot the row was grabbed for. Quality alone would send an ebook grab of a
// dual-format book out as an audiobook when the release carries an audio token.
func TestRetryGrabRequest_PrefersTheStampedMediaType(t *testing.T) {
	stamped := models.Download{Quality: "m4b", MediaType: models.MediaTypeEbook}
	if got := retryGrabRequest(stamped).MediaType; got != models.MediaTypeEbook {
		t.Errorf("stamped row retried as %q, want ebook", got)
	}
	legacy := models.Download{Quality: "m4b"}
	if got := retryGrabRequest(legacy).MediaType; got != models.MediaTypeAudiobook {
		t.Errorf("unstamped row retried as %q, want audiobook from its quality", got)
	}
}
