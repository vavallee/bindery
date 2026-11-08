package downloader

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The doctor reports the audiobook destination Transmission is sent (#2499).
func TestGrabSavePath_TransmissionAudiobookCategory(t *testing.T) {
	c := fakeClient(t, transmissionDirFake(t, "/tr/default", false), "transmission")
	c.Category, c.CategoryAudiobook = "/tr/books", "/tr/audio"
	for mediaType, want := range map[string]string{
		models.MediaTypeAudiobook: "/tr/audio",
		models.MediaTypeEbook:     "/tr/books",
	} {
		info, err := GrabSavePath(context.Background(), c, mediaType, "", "", "")
		if err != nil {
			t.Fatalf("%s: %v", mediaType, err)
		}
		if info.Path != want || info.Category != want {
			t.Errorf("%s: path %q category %q, want %q", mediaType, info.Path, info.Category, want)
		}
	}
}

// The doctor mirrors the grab when the audiobook category is a bare label:
// the ebook path is what is sent.
func TestGrabSavePath_TransmissionAudiobookLabelFallsBack(t *testing.T) {
	c := fakeClient(t, transmissionDirFake(t, "/tr/default", false), "transmission")
	c.Category, c.CategoryAudiobook = "/tr/books", "audio"
	info, err := GrabSavePath(context.Background(), c, models.MediaTypeAudiobook, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "/tr/books" || info.Source != "the save path Bindery sends" {
		t.Errorf("path %q source %q, want /tr/books from the save path Bindery sends", info.Path, info.Source)
	}
}
