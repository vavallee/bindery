package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A qBittorrent client keeps a password and a proxy password next to the API
// key column. Moving the host without replacements drops all of them, and a
// replacement password for the new host is kept.
func TestDownloadClientUpdate_HostChangeDropsPasswordsQBittorrent(t *testing.T) {
	ctx := context.Background()
	h, clients := downloadClientFixture(t)
	seeded := seedDownloadClient(t, clients, models.DownloadClient{
		Name: "qb", Type: "qbittorrent", Host: "10.10.10.10", Port: 8080, Username: "admin", Password: "qb-pass",
		ProxyUsername: "px", ProxyPassword: "qb-proxy", Category: "books", Enabled: true,
	})
	stored := func() *models.DownloadClient {
		got, _ := clients.GetByID(ctx, seeded.ID)
		return got
	}

	rec := updateDownloadClient(t, h, seeded.ID, `{"name":"qb","host":"10.10.10.10","port":8080,"type":"qbittorrent","category":"books"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op update = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.Password != "qb-pass" || s.ProxyPassword != "qb-proxy" {
		t.Fatalf("passwords lost on a no-op edit: %+v", s)
	}

	moved := `{"name":"qb","host":"10.10.10.11","port":8080,"type":"qbittorrent","category":"books","proxyUsername":"px"`
	rec = updateDownloadClient(t, h, seeded.ID, moved+`,"password":"new-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move with password = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.Password != "new-pass" || s.ProxyPassword != "" {
		t.Fatalf("after move with a password: %+v", s)
	}

	// Move again with nothing new: the password goes too.
	rec = updateDownloadClient(t, h, seeded.ID, `{"name":"qb","host":"10.10.10.12","port":`+strconv.Itoa(8080)+`,"type":"qbittorrent","category":"books"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move without credentials = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.Password != "" || s.APIKey != "" || s.ProxyPassword != "" {
		t.Fatalf("credentials survived the move: %+v", s)
	}
}
