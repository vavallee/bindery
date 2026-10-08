package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func sharedJobFixture(t *testing.T) (*DownloadRepo, *DownloadClientRepo, context.Context) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewDownloadRepo(database), NewDownloadClientRepo(database), context.Background()
}

func createSharedJobClient(t *testing.T, clients *DownloadClientRepo, ctx context.Context, c models.DownloadClient) *models.DownloadClient {
	t.Helper()
	if c.Name == "" {
		c.Name = c.Type + "-" + c.Host
	}
	c.Enabled = true
	if err := clients.Create(ctx, &c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return &c
}

func createSharedJobDownload(t *testing.T, downloads *DownloadRepo, ctx context.Context, guid string, clientID int64, torrentID, nzoID *string) *models.Download {
	t.Helper()
	dl := &models.Download{
		GUID: guid, Title: guid, DownloadClientID: &clientID,
		Status: models.StateDownloading, Protocol: "torrent",
		TorrentID: torrentID, SABnzbdNzoID: nzoID,
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	return dl
}

func sp(s string) *string { return &s }

// TestListByClientJob_BlankIDsNeverMatch: an empty, whitespace only or NULL
// id must never pair two rows as sharing a job, from either side. Rows with no
// id yet are common (between Create and SetTorrentID), and treating them as
// one job would keep a torrent in the client for good.
func TestListByClientJob_BlankIDsNeverMatch(t *testing.T) {
	downloads, clients, ctx := sharedJobFixture(t)
	qb := createSharedJobClient(t, clients, ctx, models.DownloadClient{Type: "qbittorrent", Host: "qb", Port: 8080})
	sab := createSharedJobClient(t, clients, ctx, models.DownloadClient{Type: "sabnzbd", Host: "sab", Port: 8085})

	createSharedJobDownload(t, downloads, ctx, "t-null", qb.ID, nil, nil)
	createSharedJobDownload(t, downloads, ctx, "t-empty", qb.ID, sp(""), sp(""))
	createSharedJobDownload(t, downloads, ctx, "t-space", qb.ID, sp("   "), sp("   "))
	createSharedJobDownload(t, downloads, ctx, "n-null", sab.ID, nil, nil)
	createSharedJobDownload(t, downloads, ctx, "n-empty", sab.ID, sp(""), sp(""))
	createSharedJobDownload(t, downloads, ctx, "n-space", sab.ID, sp("   "), sp("   "))

	for _, id := range []string{"", " ", "   ", "\t"} {
		got, err := downloads.ListByClientTorrentID(ctx, qb, id)
		if err != nil || len(got) != 0 {
			t.Errorf("ListByClientTorrentID(%q) = %d rows, err %v; want none", id, len(got), err)
		}
		got, err = downloads.ListByClientNzoID(ctx, sab, id)
		if err != nil || len(got) != 0 {
			t.Errorf("ListByClientNzoID(%q) = %d rows, err %v; want none", id, len(got), err)
		}
	}
	// A real id must not pick up the blank rows either.
	if got, _ := downloads.ListByClientTorrentID(ctx, qb, "abc"); len(got) != 0 {
		t.Errorf("a real torrent id matched %d blank rows", len(got))
	}
	if got, _ := downloads.ListByClientNzoID(ctx, sab, "SABnzbd_nzo_x"); len(got) != 0 {
		t.Errorf("a real nzo id matched %d blank rows", len(got))
	}
}

// TestListByClientJob_SameDaemonAcrossClientEntries: two entries configured
// against one daemon (an ebook and an audiobook entry on the same
// qBittorrent) adopt torrents across each other, so a row on either counts.
// An entry on another daemon, or of another type, does not.
func TestListByClientJob_SameDaemonAcrossClientEntries(t *testing.T) {
	downloads, clients, ctx := sharedJobFixture(t)
	ebook := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "qb-ebook", Type: "qbittorrent", Host: "QB.lan", Port: 8080, URLBase: "qbit/"})
	audio := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "qb-audio", Type: "qbittorrent", Host: "http://qb.lan/", Port: 8080, URLBase: "/qbit"})
	otherPort := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "qb-other-port", Type: "qbittorrent", Host: "qb.lan", Port: 9090, URLBase: "/qbit"})
	otherSSL := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "qb-ssl", Type: "qbittorrent", Host: "qb.lan", Port: 8080, URLBase: "/qbit", UseSSL: true})
	otherBase := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "qb-other-base", Type: "qbittorrent", Host: "qb.lan", Port: 8080})
	otherType := createSharedJobClient(t, clients, ctx, models.DownloadClient{Name: "deluge", Type: "deluge", Host: "qb.lan", Port: 8080, URLBase: "/qbit"})

	hash := "0123456789abcdef0123456789abcdef01234567"
	a := createSharedJobDownload(t, downloads, ctx, "on-ebook", ebook.ID, sp(hash), nil)
	b := createSharedJobDownload(t, downloads, ctx, "on-audio", audio.ID, sp(hash), nil)
	for _, c := range []*models.DownloadClient{otherPort, otherSSL, otherBase, otherType} {
		createSharedJobDownload(t, downloads, ctx, "on-"+c.Name, c.ID, sp(hash), nil)
	}

	for _, c := range []*models.DownloadClient{ebook, audio} {
		got, err := downloads.ListByClientTorrentID(ctx, c, hash)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[int64]bool{}
		for _, d := range got {
			ids[d.ID] = true
		}
		if len(got) != 2 || !ids[a.ID] || !ids[b.ID] {
			t.Errorf("from %s: got %d rows %v, want exactly the ebook and audiobook entries' rows", c.Name, len(got), ids)
		}
	}
}
