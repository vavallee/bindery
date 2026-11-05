package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

// TestSearchAndGrabFormat_SendFailureStoredAsClientError: when the download
// client cannot be reached, the automatic grab stores the client's error
// marked as the client's (downloader.SendFailureMessage), which is what lets
// the queue show it to admins only. The client's own text is kept in full.
func TestSearchAndGrabFormat_SendFailureStoredAsClientError(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, err := url.Parse(dead.URL)
	if err != nil {
		t.Fatal(err)
	}
	dead.Close() // connection refused from here on
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	clients, err := f.s.clients.List(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients: %v %+v", err, clients)
	}
	c := clients[0]
	c.Host, c.Port = u.Hostname(), port
	if err := f.s.clients.Update(ctx, &c); err != nil {
		t.Fatal(err)
	}

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	got, err := f.downloads.GetByGUID(ctx, regrabGUID)
	if err != nil || got == nil {
		t.Fatalf("download row: %v %+v", err, got)
	}
	if got.Status != models.StateFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	detail, marked := downloader.SendFailureDetail(got.ErrorMessage)
	if !marked {
		t.Errorf("error message %q is not marked as the download client's", got.ErrorMessage)
	}
	if !strings.Contains(detail, u.Host) {
		t.Errorf("client detail %q lost the client's own text (want %s in it)", detail, u.Host)
	}
}
