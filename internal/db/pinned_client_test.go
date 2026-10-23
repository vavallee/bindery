package db

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestResolvePinnedClient_ProtocolOperands isolates the two protocol checks. The
// client against the indexer is reachable on its own when the release protocol
// is empty, and the client against the release on its own when the indexer
// agrees with the client.
func TestResolvePinnedClient_ProtocolOperands(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	clients := NewDownloadClientRepo(database)
	usenet := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, usenet); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		indexerType string
		release     string
		wantReason  string
	}{
		{"client against indexer, release protocol empty", "torznab", "", PinClientWrongProtocol},
		{"client against release, indexer agrees with client", "newznab", "torrent", PinClientWrongProtocol},
		{"everything agrees", "newznab", "usenet", ""},
		{"everything agrees, release protocol empty", "newznab", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := &models.Indexer{Name: "ix", Type: tc.indexerType, DownloadClientID: &usenet.ID}
			c, err := clients.ResolvePinnedClient(ctx, idx, tc.release)
			if tc.wantReason == "" {
				if err != nil || c == nil || c.ID != usenet.ID {
					t.Fatalf("got %v, %v; want the pinned client", c, err)
				}
				return
			}
			var perr *PinnedClientError
			if !errors.As(err, &perr) || perr.Reason != tc.wantReason || c != nil {
				t.Fatalf("got %v, %v; want a %q PinnedClientError", c, err, tc.wantReason)
			}
		})
	}
}

// TestMigrate109_RealRunnerOnADatabaseAtVersion108 builds a database with
// migrations 1 to 108 applied by the real per migration routine, puts an
// indexer row and a download client row in it the way a pre 109 install had
// them, then lets the real runner apply the one pending migration. Existing
// rows must come back unpinned, and the foreign key must be enforced.
func TestMigrate109_RealRunnerOnADatabaseAtVersion108(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if err := setPragmas(database); err != nil {
		t.Fatal(err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, name := range names {
		v, err := migrationVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		if v >= 109 {
			continue
		}
		if name == "025_multiuser.sql" {
			if err := multiuserPreFlight(database); err != nil {
				t.Fatal(err)
			}
		}
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(database, v, name, string(content)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		applied++
	}
	if applied == 0 {
		t.Fatal("no migrations applied")
	}
	var hasCol int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('indexers') WHERE name='download_client_id'`).Scan(&hasCol); err != nil || hasCol != 0 {
		t.Fatalf("the column exists before migration 109 (count %d, err %v)", hasCol, err)
	}

	if _, err := database.Exec(`INSERT INTO download_clients (name, type, host, port, enabled, created_at, updated_at)
		VALUES ('sab', 'sabnzbd', 'h', 1, 1, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO indexers (name, type, url, api_key, categories, priority, enabled, supports_search, created_at, updated_at)
		VALUES ('old', 'newznab', 'http://x', 'k', '[7020]', 25, 1, 1, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert indexer: %v", err)
	}

	if err := migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var v109 int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=109`).Scan(&v109); err != nil || v109 != 1 {
		t.Fatalf("migration 109 not recorded (count %d, err %v)", v109, err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	got, err := NewIndexerRepo(database).List(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v (%d rows)", err, len(got))
	}
	if got[0].Name != "old" || got[0].Priority != 25 || got[0].DownloadClientID != nil {
		t.Errorf("existing indexer changed or came back pinned: %+v", got[0])
	}

	// The foreign key is enforced: no pin to a client that does not exist, and
	// no deleting a client that is pinned.
	if _, err := database.Exec(`UPDATE indexers SET download_client_id=9999 WHERE name='old'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("a pin to a missing client was accepted: %v", err)
	}
	if _, err := database.Exec(`UPDATE indexers SET download_client_id=(SELECT id FROM download_clients LIMIT 1) WHERE name='old'`); err != nil {
		t.Fatalf("pin to a real client: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM download_clients`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("a pinned client was deleted by raw SQL: %v", err)
	}
}
