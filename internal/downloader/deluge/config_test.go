package deluge_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
)

// TestLabels lists the labels the plugin holds.
func TestLabels(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.labels = map[string]bool{"books": true, "audiobooks": true}
	c := clientFromServer(srv, "pw")

	got, err := c.Labels(context.Background())
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if strings.Join(got, ",") != "audiobooks,books" {
		t.Errorf("Labels = %v", got)
	}
}

// TestLabels_PluginOff: with the plugin off the method does not exist, which
// is an error rather than an empty list.
func TestLabels_PluginOff(t *testing.T) {
	srv, _ := newTestServer(t, "pw")
	c := clientFromServer(srv, "pw")
	if _, err := c.Labels(context.Background()); err == nil {
		t.Fatal("expected an error with the Label plugin off")
	}
}

func TestLabelID(t *testing.T) {
	for in, want := range map[string]string{"Books": "books", "books": "books", "AudioBooks_2": "audiobooks_2", "": ""} {
		if got := deluge.LabelID(in); got != want {
			t.Errorf("LabelID(%q) = %q, want %q", in, got, want)
		}
	}
}
