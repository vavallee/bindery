package importer

import (
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestCheckTransmissionDownloads_PollsAudiobookCategory is the regression test
// for audiobook torrents that were never polled. An audiobook grab goes to
// CategoryAudiobook, which for Transmission is a download directory, but the
// poller only listed Category. The torrent finished and the download sat at
// "downloading" for good.
func TestCheckTransmissionDownloads_PollsAudiobookCategory(t *testing.T) {
	const hash = "89abcdef0123456789abcdef0123456789abcdef"
	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		"id":          3,
		"hashString":  hash,
		"name":        "Audiobook Release",
		"status":      6, // seeding
		"percentDone": 1.0,
		"downloadDir": "/downloads/audiobooks",
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads/books")
	client.CategoryAudiobook = "/downloads/audiobooks"
	if err := s.clients.Update(ctx, client); err != nil {
		t.Fatalf("update client: %v", err)
	}

	torrentID := hash
	dl := &models.Download{
		GUID:             "guid-audiobook-category",
		DownloadClientID: &client.ID,
		Title:            "Audiobook Release",
		NZBURL:           "magnet:?xt=urn:btih:89ab",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &torrentID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.Status == models.DownloadStatusDownloading {
		t.Fatalf("the finished torrent in the audiobook category was not picked up: download is still %q", got.Status)
	}
}
