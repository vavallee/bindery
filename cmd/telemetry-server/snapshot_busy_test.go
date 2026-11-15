package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newFileTestServer is newTestServer on a real WAL file opened through the
// production DSN. :memory: has a single shared cache and never exhibits the
// cross-connection read-to-write upgrade failure, so it cannot reproduce it.
func newFileTestServer(t *testing.T) *server {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(t.TempDir()+"/ping.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE installs (
		install_id  TEXT PRIMARY KEY,
		version     TEXT NOT NULL,
		os          TEXT NOT NULL,
		arch        TEXT NOT NULL,
		first_seen  DATETIME NOT NULL,
		last_seen   DATETIME NOT NULL,
		deploy      TEXT NOT NULL DEFAULT '',
		features    TEXT
	)`); err != nil {
		t.Fatalf("create installs: %v", err)
	}
	if err := createAggregateTables(context.Background(), db); err != nil {
		t.Fatalf("create aggregate tables: %v", err)
	}
	s := &server{db: db}
	s.setLatest("v1.0.0")
	s.limiter = newRateLimiter(time.Hour, time.Minute)
	return s
}

func postPing(s *server, id string, n int) int {
	code, _ := postPingBody(s, id, n)
	return code
}

// postPingBody is postPing that also returns the response body, so a failing
// test can say what the server answered.
func postPingBody(s *server, id string, n int) (int, string) {
	buf, _ := json.Marshal(pingRequest{InstallID: id, Version: "1.2.3", OS: "linux", Arch: "amd64", Deploy: "docker"})
	req := httptest.NewRequest(http.MethodPost, "/api/ping", bytes.NewReader(buf))
	// The limiter allows one ping per IP per window (an hour in the fixture),
	// so every ping needs an IP no earlier ping used. n is spread over the
	// 10.0.0.0/8 block, which is room for 16M pings.
	req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:1234", n>>16&255, n>>8&255, n&255)
	rec := httptest.NewRecorder()
	s.handlePing(rec, req)
	return rec.Code, rec.Body.String()
}

// TestSnapshotDay_PingCommitsMidSnapshot is the regression test for the
// production "snapshot failed ... database is locked (5/517)" warnings. A
// ping commits after snapshotDay's first read and before its first write.
// A deferred transaction cannot upgrade a stale read snapshot to a writer
// (SQLITE_BUSY / SQLITE_BUSY_SNAPSHOT, returned at once, busy_timeout is not
// consulted), so snapshotDay must take the write lock when it begins. Then
// the ping simply waits its turn and both succeed.
func TestSnapshotDay_PingCommitsMidSnapshot(t *testing.T) {
	s := newFileTestServer(t)
	insertInstall(t, s, uuid('1'), "1.2.3", time.Now().UTC().Add(-time.Hour), time.Now().UTC())

	pingDone := make(chan int, 1)
	snapshotAfterReads = func() {
		go func() { pingDone <- postPing(s, uuid('2'), 2) }()
		// On a deferred snapshot the ping commits straight away. On an
		// immediate one it blocks on the write lock until we commit, so
		// stop waiting after a short while and let the snapshot go on.
		select {
		case code := <-pingDone:
			pingDone <- code
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Cleanup(func() { snapshotAfterReads = nil })

	if err := s.snapshotDay(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("snapshotDay: %v", err)
	}
	select {
	case code := <-pingDone:
		if code != http.StatusOK {
			t.Fatalf("ping status = %d, want 200", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ping never completed")
	}
}

// TestSnapshotDay_ConcurrentPings is the stress shape of the same bug:
// pings arrive every few seconds in production, here every few milliseconds.
func TestSnapshotDay_ConcurrentPings(t *testing.T) {
	s := newFileTestServer(t)
	for i := 0; i < 200; i++ {
		insertInstall(t, s, fmt.Sprintf("%08x-0000-0000-0000-000000000000", i), "1.2.3",
			time.Now().UTC().Add(-time.Hour), time.Now().UTC())
	}
	stop := make(chan struct{})
	type result struct {
		total, bad int
		firstBad   string
	}
	done := make(chan result, 1)
	go func() {
		var r result
		// About 200 pings a second, far above production's one every few
		// seconds, but bounded so the rate does not depend on how fast the
		// runner is.
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- r
				return
			case <-tick.C:
			}
			r.total++
			code, body := postPingBody(s, fmt.Sprintf("%08x-1111-1111-1111-111111111111", i), i+1)
			if code != http.StatusOK {
				if r.bad == 0 {
					r.firstBad = fmt.Sprintf("ping #%d: status %d body %q", i, code, body)
				}
				r.bad++
			}
		}
	}()
	for i := 0; i < 30; i++ {
		if err := s.snapshotDay(context.Background(), time.Now().UTC()); err != nil {
			close(stop)
			<-done
			t.Fatalf("snapshotDay #%d: %v", i, err)
		}
		// Back to back snapshots hold the write lock almost continuously
		// and starve the poster (it sent 1 to 2 pings in 30 runs), which
		// proves nothing. A gap lets pings land before, during and after
		// each snapshot.
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	r := <-done
	t.Logf("sent %d pings", r.total)
	if r.bad != 0 {
		t.Fatalf("%d of %d pings failed, first: %s", r.bad, r.total, r.firstBad)
	}
}

func TestIsBusy(t *testing.T) {
	s := newFileTestServer(t)
	ctx := context.Background()
	// Hold the write lock on one connection, then ask another to start an
	// immediate write with no busy timeout: that returns SQLITE_BUSY.
	c1, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	if _, err := c1.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c1.ExecContext(ctx, `ROLLBACK`) }()
	c2, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := c2.ExecContext(ctx, `PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	_, busyErr := c2.ExecContext(ctx, `BEGIN IMMEDIATE`)
	if busyErr == nil {
		t.Fatal("expected a busy error")
	}
	if !isBusy(fmt.Errorf("wrapped: %w", busyErr)) {
		t.Errorf("isBusy(%v) = false, want true", busyErr)
	}
	if isBusy(errors.New("snapshot: no such table: x")) || isBusy(nil) || isBusy(sql.ErrNoRows) {
		t.Error("isBusy true for a non-busy error")
	}
}

func TestSnapshotDayRetry(t *testing.T) {
	old := snapshotRetryBackoff
	snapshotRetryBackoff = time.Millisecond
	t.Cleanup(func() { snapshotRetryBackoff = old })
	busy := fmt.Errorf("snapshot: %w", errBusyForTest(t))

	t.Run("busy then ok retries", func(t *testing.T) {
		calls := 0
		err := retryBusy(context.Background(), func() error {
			calls++
			if calls < 3 {
				return busy
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("err=%v calls=%d, want nil and 3", err, calls)
		}
	})
	t.Run("busy forever is bounded", func(t *testing.T) {
		calls := 0
		err := retryBusy(context.Background(), func() error { calls++; return busy })
		if err == nil || calls != snapshotMaxAttempts {
			t.Fatalf("err=%v calls=%d, want error and %d", err, calls, snapshotMaxAttempts)
		}
	})
	t.Run("other errors are not retried", func(t *testing.T) {
		calls := 0
		want := errors.New("snapshot: no such table")
		err := retryBusy(context.Background(), func() error { calls++; return want })
		if !errors.Is(err, want) || calls != 1 {
			t.Fatalf("err=%v calls=%d, want the error after 1 call", err, calls)
		}
	})
}

// errBusyForTest produces a genuine driver SQLITE_BUSY error.
func errBusyForTest(t *testing.T) error {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(t.TempDir()+"/b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c1, _ := db.Conn(ctx)
	defer c1.Close()
	if _, err := c1.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	c2, _ := db.Conn(ctx)
	defer c2.Close()
	_, _ = c2.ExecContext(ctx, `PRAGMA busy_timeout = 0`)
	_, e := c2.ExecContext(ctx, `BEGIN IMMEDIATE`)
	if e == nil {
		t.Fatal("expected busy")
	}
	return e
}
