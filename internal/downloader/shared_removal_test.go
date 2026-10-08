package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// fakeJobLister answers from a fixed set of rows the way the repo does:
// same client, torrent ids folded to lower case, nzo ids exact.
type fakeJobLister struct {
	rows       []models.Download
	err        error
	torrentHit int
	nzoHit     int
}

func (f *fakeJobLister) ListByClientTorrentID(_ context.Context, client *models.DownloadClient, torrentID string) ([]models.Download, error) {
	f.torrentHit++
	var out []models.Download
	for _, d := range f.rows {
		if d.DownloadClientID != nil && *d.DownloadClientID == client.ID && d.TorrentID != nil && strings.EqualFold(*d.TorrentID, torrentID) {
			out = append(out, d)
		}
	}
	return out, f.err
}

func (f *fakeJobLister) ListByClientNzoID(_ context.Context, client *models.DownloadClient, nzoID string) ([]models.Download, error) {
	f.nzoHit++
	var out []models.Download
	for _, d := range f.rows {
		if d.DownloadClientID != nil && *d.DownloadClientID == client.ID && d.SABnzbdNzoID != nil && *d.SABnzbdNzoID == nzoID {
			out = append(out, d)
		}
	}
	return out, f.err
}

// TestOtherDownloadUsingClientJob_LivenessByState pins which states keep a
// shared job in the client: all of them except failed, and imported when the
// client removes imported downloads anyway.
func TestOtherDownloadUsingClientJob_LivenessByState(t *testing.T) {
	clientID := int64(7)
	ref := "abc"
	self := models.Download{ID: 1, DownloadClientID: &clientID, TorrentID: &ref, Status: models.StateDownloading}
	for _, st := range models.AllStates() {
		for _, removeOnImport := range []bool{false, true} {
			other := models.Download{ID: 2, DownloadClientID: &clientID, TorrentID: &ref, Status: st}
			lister := &fakeJobLister{rows: []models.Download{self, other}}
			client := &models.DownloadClient{ID: clientID, Type: "qbittorrent", RemoveOnImport: removeOnImport}
			got, err := OtherDownloadUsingClientJob(context.Background(), lister, client, &self)
			if err != nil {
				t.Fatal(err)
			}
			want := true
			switch {
			case st == models.StateFailed:
				want = false
			case st == models.StateImported && removeOnImport:
				want = false
			}
			if (got != nil) != want {
				t.Errorf("state %s removeOnImport=%v: other counted as user = %v, want %v", st, removeOnImport, got != nil, want)
			}
		}
	}
}

// TestOtherDownloadUsingClientJob_FieldPerClientType: torrent clients compare
// torrent_id, usenet clients the nzo id, and a row on another client never
// counts.
func TestOtherDownloadUsingClientJob_FieldPerClientType(t *testing.T) {
	clientID, otherClient := int64(7), int64(8)
	for _, typ := range []string{"qbittorrent", "transmission", "deluge", "rtorrent", "nzbget", "sabnzbd"} {
		torrent := typ != "nzbget" && typ != "sabnzbd"
		mk := func(id int64, cid *int64) models.Download {
			r := "ABC"
			d := models.Download{ID: id, DownloadClientID: cid, Status: models.StateDownloading}
			if torrent {
				d.TorrentID = &r
			} else {
				d.SABnzbdNzoID = &r
			}
			return d
		}
		self := mk(1, &clientID)
		client := &models.DownloadClient{ID: clientID, Type: typ}

		lister := &fakeJobLister{rows: []models.Download{self, mk(2, &otherClient)}}
		if got, _ := OtherDownloadUsingClientJob(context.Background(), lister, client, &self); got != nil {
			t.Errorf("%s: a row on another client counted as a user", typ)
		}
		lister = &fakeJobLister{rows: []models.Download{self, mk(2, &clientID)}}
		if got, _ := OtherDownloadUsingClientJob(context.Background(), lister, client, &self); got == nil {
			t.Errorf("%s: a row sharing the id was not found", typ)
		}
		if torrent && (lister.torrentHit != 1 || lister.nzoHit != 0) {
			t.Errorf("%s: should look up torrent_id, hits torrent=%d nzo=%d", typ, lister.torrentHit, lister.nzoHit)
		}
		if !torrent && (lister.nzoHit != 1 || lister.torrentHit != 0) {
			t.Errorf("%s: should look up the nzo id, hits torrent=%d nzo=%d", typ, lister.torrentHit, lister.nzoHit)
		}
	}
}

// TestRemoveDownloadUnlessShared_LookupErrorKeepsTheJob: when the check
// cannot run, the job stays in the client rather than risk removing one
// another row needs. The client here points nowhere, so any call would fail.
func TestRemoveDownloadUnlessShared_LookupErrorKeepsTheJob(t *testing.T) {
	clientID := int64(9)
	ref := "abc"
	dl := &models.Download{ID: 1, DownloadClientID: &clientID, TorrentID: &ref}
	client := &models.DownloadClient{ID: clientID, Type: "qbittorrent", Host: "127.0.0.1", Port: 1}
	removed, err := RemoveDownloadUnlessShared(context.Background(), &fakeJobLister{err: errors.New("db down")}, client, dl, true, "", "test")
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v, want the job left alone with no error", removed, err)
	}
}
