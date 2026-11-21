package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Every torrent poller records the first sighting of a torrent, which is what
// separates a torrent the client lost (clientRemoved, never counted) from one
// it never received (neverArrived, counted). qBittorrent has its own test in
// scanner_client_removed_test.go; these cover Transmission, Deluge and
// rTorrent, so a poller that stops calling noteClientSeen fails here.

type seenPoller struct {
	name    string
	handler func(listed *atomic.Bool) http.HandlerFunc
	poll    func(s *Scanner, ctx context.Context, c *models.DownloadClient)
}

func seenPollers() []seenPoller {
	return []seenPoller{
		{
			name: "qbittorrent",
			handler: func(listed *atomic.Bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v2/auth/login":
						_, _ = w.Write([]byte("Ok."))
					case "/api/v2/torrents/info":
						if !listed.Load() {
							_, _ = w.Write([]byte("[]"))
							return
						}
						_ = json.NewEncoder(w).Encode([]map[string]any{{
							"hash": clientErrHash, "name": "Errored Torrent", "state": "downloading",
							"progress": 0.4, "amount_left": 100, "size": 200, "category": "",
							"save_path": "/tmp/x", "content_path": "/tmp/x/Errored Torrent",
						}})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}
			},
			poll: (*Scanner).checkQbittorrentDownloads,
		},
		{
			name: "transmission",
			handler: func(listed *atomic.Bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					torrents := []map[string]any{}
					if listed.Load() {
						torrents = append(torrents, map[string]any{
							"id": 1, "hashString": clientErrHash, "name": "Errored Torrent",
							"status": 4, "percentDone": 0.4, "downloadDir": "/downloads",
							"addedDate": time.Now().Unix(),
						})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"result": "success", "arguments": map[string]any{"torrents": torrents},
					})
				}
			},
			poll: (*Scanner).checkTransmissionDownloads,
		},
		{
			name: "deluge",
			handler: func(listed *atomic.Bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Method string `json:"method"`
						ID     int64  `json:"id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&req)
					var result any
					switch req.Method {
					case "auth.login", "auth.check_session", "web.connected":
						result = true
					case "core.get_torrents_status":
						status := map[string]any{}
						if listed.Load() {
							status[clientErrHash] = map[string]any{
								"name": "Errored Torrent", "hash": clientErrHash,
								"progress": 40.0, "state": "Downloading",
								"total_size": 200, "total_done": 80,
								"download_location": "/downloads",
							}
						}
						result = status
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
				}
			},
			poll: (*Scanner).checkDelugeDownloads,
		},
		{
			name: "rtorrent",
			handler: func(listed *atomic.Bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "text/xml")
					if !strings.Contains(string(body), "d.multicall2") {
						_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
						return
					}
					rows := ""
					if listed.Load() {
						// name, hash, base_path, directory, custom1, size_bytes,
						// left_bytes, down.rate, complete, is_active, is_open, message
						rows = fmt.Sprintf(`<value><array><data>
<value><string>Errored Torrent</string></value><value><string>%s</string></value>
<value><string>/downloads/x</string></value><value><string>/downloads/x</string></value>
<value><string></string></value><value><i8>200</i8></value><value><i8>120</i8></value>
<value><i8>10</i8></value><value><i8>0</i8></value><value><i8>1</i8></value>
<value><i8>1</i8></value><value><string></string></value>
</data></array></value>`, strings.ToUpper(clientErrHash))
					}
					_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><array><data>`+rows+`</data></array></value></param></params></methodResponse>`)
				}
			},
			poll: (*Scanner).checkRtorrentDownloads,
		},
	}
}

func TestTorrentPollers_SeenThenMissingIsClientRemoved(t *testing.T) {
	for _, p := range seenPollers() {
		t.Run(p.name, func(t *testing.T) {
			var listed atomic.Bool
			listed.Store(true)
			s, dlRepo, blocklist, history, client, again := clientErrorFixture(t, p.name, p.handler(&listed))
			ctx := context.Background()
			dl, err := dlRepo.GetByGUID(ctx, "guid-client-error")
			if err != nil || dl == nil {
				t.Fatal(err)
			}
			p.poll(s, ctx, client)
			if got, _ := dlRepo.GetByGUID(ctx, "guid-client-error"); got == nil || got.Status != models.StateDownloading {
				t.Fatalf("the listed torrent's row changed state: %+v", got)
			}
			if seen, _ := dlRepo.ClientSeen(ctx, dl.ID); !seen {
				t.Fatal("a poll that found the torrent did not record the sighting")
			}

			listed.Store(false)
			if _, err := again.database.Exec(`UPDATE downloads SET added_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC(), dl.ID); err != nil {
				t.Fatal(err)
			}
			p.poll(s, ctx, client)

			got, err := dlRepo.GetByGUID(ctx, "guid-client-error")
			if err != nil || got == nil {
				t.Fatal(err)
			}
			if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "removed from the download client") {
				t.Fatalf("status %q, message %q: want failed as removed from the client", got.Status, got.ErrorMessage)
			}
			if entries, _ := blocklist.List(ctx); len(entries) != 0 {
				t.Fatalf("blocklist = %+v, want empty", entries)
			}
			events, _ := history.ListByType(ctx, models.HistoryEventDownloadFailed)
			if len(events) != 1 || !strings.Contains(events[0].Data, `"class":"clientRemoved"`) {
				t.Fatalf("history = %+v, want one clientRemoved event", events)
			}
		})
	}
}

func TestTorrentPollers_NeverListedStaysNeverArrived(t *testing.T) {
	for _, p := range seenPollers() {
		t.Run(p.name, func(t *testing.T) {
			var listed atomic.Bool // never listed
			s, dlRepo, _, history, client, again := clientErrorFixture(t, p.name, p.handler(&listed))
			ctx := context.Background()
			dl, err := dlRepo.GetByGUID(ctx, "guid-client-error")
			if err != nil || dl == nil {
				t.Fatal(err)
			}
			if _, err := again.database.Exec(`UPDATE downloads SET added_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC(), dl.ID); err != nil {
				t.Fatal(err)
			}
			p.poll(s, ctx, client)

			got, err := dlRepo.GetByGUID(ctx, "guid-client-error")
			if err != nil || got == nil {
				t.Fatal(err)
			}
			if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "never reached the download client") {
				t.Fatalf("status %q, message %q: want failed as never arrived", got.Status, got.ErrorMessage)
			}
			events, _ := history.ListByType(ctx, models.HistoryEventDownloadFailed)
			if len(events) != 1 || !strings.Contains(events[0].Data, `"class":"neverArrived"`) {
				t.Fatalf("history = %+v, want one neverArrived event", events)
			}
		})
	}
}

// A row that is already imported or failed can never reach
// failDownloadThatNeverArrived, so polling it writes nothing. After a restart
// the first poll would otherwise issue one UPDATE per historical torrent still
// listed in the client.
func TestTorrentPollers_FinishedRowsAreNotStamped(t *testing.T) {
	for _, p := range seenPollers() {
		for _, status := range []models.DownloadState{models.StateImported, models.StateFailed} {
			t.Run(p.name+"/"+string(status), func(t *testing.T) {
				var listed atomic.Bool
				listed.Store(true)
				s, dlRepo, _, _, client, again := clientErrorFixture(t, p.name, p.handler(&listed))
				ctx := context.Background()
				dl, err := dlRepo.GetByGUID(ctx, "guid-client-error")
				if err != nil || dl == nil {
					t.Fatal(err)
				}
				if _, err := again.database.Exec(`UPDATE downloads SET status = ? WHERE id = ?`, string(status), dl.ID); err != nil {
					t.Fatal(err)
				}
				p.poll(s, ctx, client)
				if seen, _ := dlRepo.ClientSeen(ctx, dl.ID); seen {
					t.Fatalf("a %s row still listed in the client was stamped as seen", status)
				}
			})
		}
	}
}
