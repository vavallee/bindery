package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestResolvePinnedClient_NotPinned(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	clients := NewDownloadClientRepo(database)
	ctx := context.Background()

	if c, err := clients.ResolvePinnedClient(ctx, nil, "usenet"); c != nil || err != nil {
		t.Fatalf("nil indexer = %v, %v; want nil, nil", c, err)
	}
	if c, err := clients.ResolvePinnedClient(ctx, &models.Indexer{Name: "free", Type: "newznab"}, "usenet"); c != nil || err != nil {
		t.Fatalf("unpinned indexer = %v, %v; want nil, nil so the caller falls back to the default client", c, err)
	}
}

func TestResolvePinnedClient_MissingClientNeverSubstitutes(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	clients := NewDownloadClientRepo(database)
	ctx := context.Background()
	other := &models.DownloadClient{Name: "other", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	gone := other.ID + 100
	c, err := clients.ResolvePinnedClient(ctx, &models.Indexer{Name: "ix", Type: "newznab", DownloadClientID: &gone}, "usenet")
	var perr *PinnedClientError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a *PinnedClientError", err)
	}
	if c != nil {
		t.Fatalf("a missing pinned client must not be replaced by another one, got %v", c)
	}
	if perr.Reason != PinClientMissing || perr.ClientID != gone || perr.ClientName != "" || perr.IndexerName != "ix" {
		t.Fatalf("perr = %+v", perr)
	}
}

func TestResolvePinnedClient_DisabledClient(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	clients := NewDownloadClientRepo(database)
	ctx := context.Background()
	off := &models.DownloadClient{Name: "off", Type: "qbittorrent", Host: "h", Port: 1, Enabled: false}
	if err := clients.Create(ctx, off); err != nil {
		t.Fatal(err)
	}
	c, err := clients.ResolvePinnedClient(ctx, &models.Indexer{Name: "tor", Type: "torznab", DownloadClientID: &off.ID}, "torrent")
	var perr *PinnedClientError
	if !errors.As(err, &perr) || c != nil {
		t.Fatalf("got %v, %v; want a PinnedClientError", c, err)
	}
	if perr.Reason != PinClientDisabled || perr.ClientName != "off" {
		t.Fatalf("perr = %+v", perr)
	}
}

func TestResolvePinnedClient_DatabaseErrorIsNotAPinError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	clients := NewDownloadClientRepo(database)
	id := int64(1)
	database.Close()

	c, err := clients.ResolvePinnedClient(context.Background(), &models.Indexer{Name: "ix", Type: "newznab", DownloadClientID: &id}, "usenet")
	if err == nil || c != nil {
		t.Fatalf("got %v, %v; want a database error", c, err)
	}
	var perr *PinnedClientError
	if errors.As(err, &perr) {
		t.Fatalf("a database failure must not read as a missing client: %v", err)
	}
}

func TestPinnedClientError_MessageRedactsClientForNonAdmins(t *testing.T) {
	cases := []struct {
		name          string
		e             PinnedClientError
		includeClient bool
		want          []string
		notWant       []string
	}{
		{
			name:          "missing, admin",
			e:             PinnedClientError{IndexerName: "ix", ClientID: 7, ClientName: "", Reason: PinClientMissing},
			includeClient: true,
			want:          []string{`indexer "ix" is pinned to a download client (id 7), which no longer exists`, "Nothing was sent"},
		},
		{
			name:          "missing, non admin hides the id",
			e:             PinnedClientError{IndexerName: "ix", ClientID: 7, Reason: PinClientMissing},
			includeClient: false,
			want:          []string{"that no longer exists"},
			notWant:       []string{"id 7", "(id"},
		},
		{
			name:          "disabled, admin names the client",
			e:             PinnedClientError{IndexerName: "ix", ClientID: 7, ClientName: "qbit-main", Reason: PinClientDisabled},
			includeClient: true,
			want:          []string{`download client "qbit-main"`, "which is disabled"},
		},
		{
			name:          "disabled, non admin does not see the name",
			e:             PinnedClientError{IndexerName: "ix", ClientID: 7, ClientName: "qbit-main", Reason: PinClientDisabled},
			includeClient: false,
			want:          []string{`indexer "ix" is pinned to a download client, which is disabled`},
			notWant:       []string{"qbit-main"},
		},
		{
			name:          "wrong protocol",
			e:             PinnedClientError{IndexerName: "ix", ClientID: 7, ClientName: "sab", Reason: PinClientWrongProtocol, ClientProtocol: "usenet", WantProtocol: "torrent"},
			includeClient: true,
			want:          []string{"which is a usenet client, but the indexer is torrent"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.e.Message(tc.includeClient)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q missing %q", got, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("message %q must not contain %q", got, nw)
				}
			}
		})
	}

	e := &PinnedClientError{IndexerName: "ix", ClientID: 7, ClientName: "sab", Reason: PinClientDisabled}
	if e.Error() != e.Message(true) {
		t.Fatalf("Error() must be the admin rendering, got %q", e.Error())
	}
}

func TestDownloadClientDelete_PinnedClientListsEveryIndexerAndKeepsTheClient(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	clients := NewDownloadClientRepo(database)
	indexers := NewIndexerRepo(database)
	c := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		idx := &models.Indexer{Name: name, Type: "newznab", URL: "http://x", Enabled: true, DownloadClientID: &c.ID}
		if err := indexers.Create(ctx, idx); err != nil {
			t.Fatal(err)
		}
	}
	// A third indexer on a different client must not be named.
	idx := &models.Indexer{Name: "unrelated", Type: "newznab", URL: "http://x", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}

	err = clients.Delete(ctx, c.ID)
	var pinned *ClientPinnedError
	if !errors.As(err, &pinned) {
		t.Fatalf("Delete = %v, want *ClientPinnedError", err)
	}
	if strings.Join(pinned.Indexers, ",") != "alpha,zeta" || pinned.ClientID != c.ID {
		t.Fatalf("pinned = %+v, want alpha,zeta sorted", pinned)
	}
	msg := pinned.Error()
	if !strings.Contains(msg, "2 indexer(s)") || !strings.Contains(msg, "alpha, zeta") || strings.Contains(msg, "unrelated") {
		t.Fatalf("message = %q", msg)
	}
	if got, err := clients.GetByID(ctx, c.ID); err != nil || got == nil {
		t.Fatalf("a refused delete must leave the client in place, got %v, %v", got, err)
	}
}

func TestDownloadClientDelete_UnpinnedAndUnknown(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	clients := NewDownloadClientRepo(database)
	c := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := clients.Delete(ctx, c.ID); err != nil {
		t.Fatalf("deleting an unpinned client: %v", err)
	}
	if got, err := clients.GetByID(ctx, c.ID); err != nil || got != nil {
		t.Fatalf("GetByID after delete = %v, %v; want nil, nil", got, err)
	}
	if err := clients.Delete(ctx, 9999); err != nil {
		t.Fatalf("deleting an unknown id is a no-op, got %v", err)
	}
}

func TestDownloadClientDelete_ReleasedAfterIndexerUnpinned(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	clients := NewDownloadClientRepo(database)
	indexers := NewIndexerRepo(database)
	c := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{Name: "ix", Type: "newznab", URL: "http://x", Enabled: true, DownloadClientID: &c.ID}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	idx.DownloadClientID = nil
	if err := indexers.Update(ctx, idx); err != nil {
		t.Fatal(err)
	}
	if err := clients.Delete(ctx, c.ID); err != nil {
		t.Fatalf("delete after clearing the pin: %v", err)
	}
}
