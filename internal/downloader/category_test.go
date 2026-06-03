package downloader

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestResolveCategory(t *testing.T) {
	tests := []struct {
		name      string
		client    *models.DownloadClient
		mediaType string
		want      string
	}{
		{
			name:      "nil client returns empty",
			client:    nil,
			mediaType: models.MediaTypeEbook,
			want:      "",
		},
		{
			name:      "ebook mediaType returns Category",
			client:    &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"},
			mediaType: models.MediaTypeEbook,
			want:      "books",
		},
		{
			name:      "audiobook mediaType returns CategoryAudiobook when set",
			client:    &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"},
			mediaType: models.MediaTypeAudiobook,
			want:      "audiobooks",
		},
		{
			name:      "audiobook mediaType falls back to Category when CategoryAudiobook empty",
			client:    &models.DownloadClient{Category: "books"},
			mediaType: models.MediaTypeAudiobook,
			want:      "books",
		},
		{
			name:      "both mediaType returns Category (not the audiobook one)",
			client:    &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"},
			mediaType: models.MediaTypeBoth,
			want:      "books",
		},
		{
			name:      "empty mediaType returns Category",
			client:    &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"},
			mediaType: "",
			want:      "books",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveCategory(tc.client, tc.mediaType); got != tc.want {
				t.Fatalf("ResolveCategory = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCategoriesToPoll(t *testing.T) {
	tests := []struct {
		name   string
		client *models.DownloadClient
		want   []string
	}{
		{
			name:   "nil client",
			client: nil,
			want:   nil,
		},
		{
			name:   "category only",
			client: &models.DownloadClient{Category: "books"},
			want:   []string{"books"},
		},
		{
			name:   "category and audiobook category",
			client: &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"},
			want:   []string{"books", "audiobooks"},
		},
		{
			name:   "duplicate values are not polled twice",
			client: &models.DownloadClient{Category: "books", CategoryAudiobook: "books"},
			want:   []string{"books"},
		},
		{
			name:   "empty category still produces one entry (qbit treats as all)",
			client: &models.DownloadClient{Category: ""},
			want:   []string{""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CategoriesToPoll(tc.client)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got[%d]=%q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
