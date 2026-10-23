package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func adminCtx() context.Context { return auth.WithUserRole(context.Background(), "admin") }
func userCtx() context.Context  { return auth.WithUserRole(context.Background(), "user") }

func TestSendFailedText_PinSuffixNamesClientOnlyForAdmins(t *testing.T) {
	err := &sendError{cause: errors.New("connection refused"), pin: &pinTarget{indexer: "TrackerX", client: "sab-private"}}

	admin := sendFailedText(adminCtx(), err)
	if !strings.Contains(admin, `Indexer "TrackerX" is pinned to download client "sab-private", so no other client was tried`) {
		t.Fatalf("admin text = %q", admin)
	}
	user := sendFailedText(userCtx(), err)
	if !strings.Contains(user, `Indexer "TrackerX" is pinned to one download client, so no other client was tried`) {
		t.Fatalf("user text = %q", user)
	}
	if strings.Contains(user, "sab-private") {
		t.Fatalf("non admin text leaks the client name: %q", user)
	}

	plain := sendFailedText(adminCtx(), &sendError{cause: errors.New("connection refused")})
	if strings.Contains(plain, "pinned") {
		t.Fatalf("an unpinned send failure must not mention a pin: %q", plain)
	}
}

func TestQueueItemMessage_PinFailuresRespectRole(t *testing.T) {
	perr := &db.PinnedClientError{IndexerName: "TrackerX", ClientID: 4, ClientName: "sab-private", Reason: db.PinClientDisabled}
	if got := queueItemMessage(adminCtx(), perr); !strings.Contains(got, "sab-private") {
		t.Errorf("admin message = %q, want the client named", got)
	}
	if got := queueItemMessage(userCtx(), perr); strings.Contains(got, "sab-private") || !strings.Contains(got, "TrackerX") {
		t.Errorf("user message = %q, want the indexer and not the client", got)
	}

	aerr := &ambiguousPinError{reason: pinRefuseAmbiguous, pinned: []string{"TrackerX"}}
	if got := queueItemMessage(adminCtx(), aerr); !strings.Contains(got, "(TrackerX)") {
		t.Errorf("admin ambiguous message = %q, want the pinned indexer named", got)
	}
	if got := queueItemMessage(userCtx(), aerr); strings.Contains(got, "TrackerX") {
		t.Errorf("user ambiguous message = %q, must not name pinned indexers", got)
	}
}

func TestClientForGrab_Routing(t *testing.T) {
	f := newPinFixture(t)
	ctx := f.ctx
	id := func(n int64) *int64 { return &n }

	t.Run("no indexer id takes the protocol default", func(t *testing.T) {
		c, pin, err := f.h.clientForGrab(ctx, nil, "usenet", "")
		if err != nil || pin != nil || c == nil || c.ID != f.pubC.ID {
			t.Fatalf("got %v, %v, %v; want the priority 1 client and no pin", c, pin, err)
		}
	})
	t.Run("indexer id zero is not an indexer", func(t *testing.T) {
		f.pin(t, &f.privC.ID)
		c, pin, err := f.h.clientForGrab(ctx, id(0), "usenet", "")
		if err != nil || pin != nil || c.ID != f.pubC.ID {
			t.Fatalf("got %v, %v, %v", c, pin, err)
		}
	})
	t.Run("the release protocol must match the pinned client", func(t *testing.T) {
		f.pin(t, &f.privC.ID)
		c, pin, err := f.h.clientForGrab(ctx, &f.idx.ID, "torrent", "")
		var perr *db.PinnedClientError
		if c != nil || pin != nil || !errors.As(err, &perr) || perr.Reason != db.PinClientWrongProtocol {
			t.Fatalf("usenet pinned indexer, torrent release: got %v, %v, %v", c, pin, err)
		}
		tor := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 1, Enabled: true}
		if err := f.clients.Create(ctx, tor); err != nil {
			t.Fatal(err)
		}
		tix := &models.Indexer{Name: "TorX", Type: "torznab", URL: "http://tor.invalid/api", Enabled: true, DownloadClientID: &tor.ID}
		if err := f.indexers.Create(ctx, tix); err != nil {
			t.Fatal(err)
		}
		c, pin, err = f.h.clientForGrab(ctx, &tix.ID, "usenet", "")
		if c != nil || pin != nil || !errors.As(err, &perr) || perr.Reason != db.PinClientWrongProtocol {
			t.Fatalf("torrent pinned indexer, usenet release: got %v, %v, %v", c, pin, err)
		}
		c, _, err = f.h.clientForGrab(ctx, &tix.ID, "torrent", "")
		if err != nil || c == nil || c.ID != tor.ID {
			t.Fatalf("matching protocol: got %v, %v", c, err)
		}
	})
	t.Run("a pinned indexer gets its client and a pin target", func(t *testing.T) {
		c, pin, err := f.h.clientForGrab(ctx, &f.idx.ID, "usenet", "")
		if err != nil || c == nil || c.ID != f.privC.ID {
			t.Fatalf("got %v, %v", c, err)
		}
		if pin == nil || pin.indexer != "TrackerX" || pin.client != "sab-private" {
			t.Fatalf("pin = %+v", pin)
		}
	})
	t.Run("a pinned indexer never silently uses the default when its client is unusable", func(t *testing.T) {
		f.privC.Enabled = false
		if err := f.clients.Update(ctx, f.privC); err != nil {
			t.Fatal(err)
		}
		c, pin, err := f.h.clientForGrab(ctx, &f.idx.ID, "usenet", "")
		var perr *db.PinnedClientError
		if c != nil || pin != nil || !errors.As(err, &perr) || perr.Reason != db.PinClientDisabled {
			t.Fatalf("got %v, %v, %v", c, pin, err)
		}
	})
	t.Run("an unpinned existing indexer takes the default", func(t *testing.T) {
		f.pin(t, nil)
		c, pin, err := f.h.clientForGrab(ctx, &f.idx.ID, "usenet", "")
		if err != nil || pin != nil || c.ID != f.pubC.ID {
			t.Fatalf("got %v, %v, %v", c, pin, err)
		}
	})
	t.Run("no indexer repo ignores the id", func(t *testing.T) {
		f.pin(t, &f.privC.ID)
		h := *f.h
		h.indexers = nil
		c, pin, err := h.clientForGrab(ctx, &f.idx.ID, "usenet", "")
		if err != nil || pin != nil || c.ID != f.pubC.ID {
			t.Fatalf("got %v, %v, %v", c, pin, err)
		}
	})
	t.Run("an indexer lookup failure is returned, not routed around", func(t *testing.T) {
		broken, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		h := *f.h
		h.indexers = db.NewIndexerRepo(broken)
		broken.Close()
		c, pin, err := h.clientForGrab(ctx, &f.idx.ID, "usenet", "")
		if err == nil || c != nil || pin != nil {
			t.Fatalf("got %v, %v, %v; want only an error", c, pin, err)
		}
		var perr *db.PinnedClientError
		if errors.As(err, &perr) {
			t.Fatalf("a storage fault must not read as a pin failure: %v", err)
		}
	})
}

func TestNoProtocolClientError_NamesTheMismatch(t *testing.T) {
	h, _, _, clients, _, ctx := queueFixture(t)
	torrent := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, torrent); err != nil {
		t.Fatal(err)
	}

	err := h.noProtocolClientError(ctx, "usenet")
	var nce *noClientError
	if !errors.As(err, &nce) {
		t.Fatalf("err = %T", err)
	}
	for _, want := range []string{"usenet (nzb)", "this release is usenet", "only enabled client(s) are torrent", "SABnzbd/NZBGet"} {
		if !strings.Contains(nce.msg, want) {
			t.Errorf("usenet release message %q missing %q", nce.msg, want)
		}
	}

	h2, _, _, clients2, _, ctx2 := queueFixture(t)
	sab := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients2.Create(ctx2, sab); err != nil {
		t.Fatal(err)
	}
	err = h2.noProtocolClientError(ctx2, "torrent")
	if !errors.As(err, &nce) {
		t.Fatalf("err = %T", err)
	}
	for _, want := range []string{"no enabled torrent download client", "this release is torrent", "are usenet", "qBittorrent/Transmission/Deluge/rTorrent"} {
		if !strings.Contains(nce.msg, want) {
			t.Errorf("torrent release message %q missing %q", nce.msg, want)
		}
	}

	h3, _, _, _, _, ctx3 := queueFixture(t)
	err = h3.noProtocolClientError(ctx3, "usenet")
	if !errors.As(err, &nce) || !strings.Contains(nce.msg, "add a download client") || strings.Contains(nce.msg, "this release is") {
		t.Fatalf("with no clients at all got %v", err)
	}
	if !errors.Is(err, errNoDownloadClient) {
		t.Fatal("must unwrap to errNoDownloadClient so the API answers 400 with the setup code")
	}
}

func TestValidateDownloadClientPin_Matrix(t *testing.T) {
	f := newPinAPIFixture(t)
	ctx := context.Background()
	idp := func(n int64) *int64 { return &n }
	usenetPin, torrentPin, offPin := idp(f.usenet.ID), idp(f.torrent.ID), idp(f.off.ID)

	cases := []struct {
		name    string
		idx     models.Indexer
		stored  *models.Indexer
		wantErr string
	}{
		{"no pin is always valid", models.Indexer{Type: "newznab"}, nil, ""},
		{"new valid pin", models.Indexer{Type: "torznab", DownloadClientID: torrentPin}, nil, ""},
		{"new pin with wrong protocol", models.Indexer{Type: "newznab", DownloadClientID: torrentPin}, nil, "is a torrent client but this indexer is usenet"},
		{"new pin to disabled client", models.Indexer{Type: "torznab", DownloadClientID: offPin}, nil, "is disabled"},
		{"unchanged pin to a disabled client keeps saving",
			models.Indexer{Type: "torznab", DownloadClientID: offPin},
			&models.Indexer{Type: "torznab", DownloadClientID: offPin}, ""},
		{"pin moved to a disabled client is refused",
			models.Indexer{Type: "torznab", DownloadClientID: offPin},
			&models.Indexer{Type: "torznab", DownloadClientID: torrentPin}, "is disabled"},
		{"first pin on a stored unpinned indexer is checked",
			models.Indexer{Type: "torznab", DownloadClientID: offPin},
			&models.Indexer{Type: "torznab"}, "is disabled"},
		{"type change under an unchanged pin is checked",
			models.Indexer{Type: "newznab", DownloadClientID: torrentPin},
			&models.Indexer{Type: "torznab", DownloadClientID: torrentPin}, "is a torrent client but this indexer is usenet"},
		{"empty type reads as newznab, not a type change",
			models.Indexer{Type: "", DownloadClientID: torrentPin},
			&models.Indexer{Type: "newznab", DownloadClientID: torrentPin}, ""},
		{"empty stored type against newznab is not a type change",
			models.Indexer{Type: "newznab", DownloadClientID: usenetPin},
			&models.Indexer{Type: "", DownloadClientID: usenetPin}, ""},
		{"missing client", models.Indexer{Type: "newznab", DownloadClientID: idp(99999)}, nil, "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.ih.validateDownloadClientPin(ctx, tc.idx, tc.stored)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var bad pinInvalidError
			if !errors.As(err, &bad) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want a pinInvalidError containing %q", err, tc.wantErr)
			}
		})
	}

	t.Run("no client repo refuses rather than storing an unchecked pin", func(t *testing.T) {
		h := &IndexerHandler{}
		err := h.validateDownloadClientPin(ctx, models.Indexer{Type: "torznab", DownloadClientID: torrentPin}, nil)
		var bad pinInvalidError
		if !errors.As(err, &bad) {
			t.Fatalf("err = %v, want a pinInvalidError", err)
		}
	})
}

func TestIndexerPin_StorageFaultIsA500WithoutDetail(t *testing.T) {
	f := newPinAPIFixture(t)
	broken, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	f.ih.clients = db.NewDownloadClientRepo(broken)
	broken.Close()

	rec := f.create(`{"name":"X","url":"http://192.168.1.60:9117/api","type":"newznab","downloadClientId":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a storage fault: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(strings.ToLower(body), "sql") || strings.Contains(body, "closed") {
		t.Fatalf("body leaks the storage error: %s", body)
	}

	rec2 := httptest.NewRecorder()
	writePinError(rec2, httptest.NewRequest(http.MethodPost, "/x", nil), pinInvalidError("bad pin"))
	if rec2.Code != http.StatusBadRequest || !strings.Contains(rec2.Body.String(), "bad pin") {
		t.Fatalf("pinInvalidError = %d %s, want a 400 carrying the text", rec2.Code, rec2.Body.String())
	}
}
