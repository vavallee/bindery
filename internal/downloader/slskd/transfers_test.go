package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/slskd/slskdtest"
)

func fakeClient(t *testing.T) (*Client, *slskdtest.Fake) {
	t.Helper()
	f, srv := slskdtest.New()
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return New(u.Hostname(), port, "test-key", "", false), f
}

func twoFiles() []BatchFile {
	return []BatchFile{{Filename: `@@u\Books\Dune\01.mp3`, Size: 100}, {Filename: `@@u\Books\Dune\02.mp3`, Size: 300}}
}

func TestTransferStateParsing(t *testing.T) {
	cases := []struct {
		in   string
		want Group
	}{
		{`"Requested"`, GroupWaiting},
		{`"Queued, Remotely"`, GroupWaiting},
		{`"Queued, Locally"`, GroupWaiting},
		{`"Initializing"`, GroupStarting},
		{`"InProgress"`, GroupActive},
		{`"Completed, Succeeded"`, GroupSucceeded},
		{`"Completed, Errored"`, GroupFailed},
		{`"Completed, Cancelled"`, GroupFailed},
		{`"Completed, TimedOut"`, GroupFailed},
		{`"Completed, Rejected"`, GroupFailed},
		{`"Completed, Aborted"`, GroupFailed},
		{`"Completed"`, GroupFailed},
		{strconv.Itoa(int(StateCompleted | StateSucceeded)), GroupSucceeded},
		{`"SomethingNewer"`, GroupWaiting},
	}
	for _, c := range cases {
		var s TransferState
		if err := json.Unmarshal([]byte(c.in), &s); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got := s.Group(); got != c.want {
			t.Errorf("%s: group %v, want %v", c.in, got, c.want)
		}
	}
	var s TransferState
	if err := json.Unmarshal([]byte(`{}`), &s); err == nil {
		t.Error("an object is not a state")
	}
}

func TestSummarize(t *testing.T) {
	place := 3
	mk := func(state string, size, done int64, p *int) Transfer {
		var st TransferState
		_ = json.Unmarshal([]byte(strconv.Quote(state)), &st)
		return Transfer{State: st, Size: size, BytesTransferred: done, PlaceInQueue: p}
	}
	if p := Summarize(nil); p.Group != GroupFailed || p.FailureWord == "" {
		t.Errorf("an empty batch must fail, got %+v", p)
	}
	p := Summarize([]Transfer{mk(slskdtest.Succeeded, 100, 100, nil), mk(slskdtest.Succeeded, 300, 300, nil)})
	if p.Group != GroupSucceeded || p.Done != 400 || p.Total != 400 {
		t.Errorf("all succeeded: %+v", p)
	}
	// All requested files must succeed: one still running keeps it unfinished.
	p = Summarize([]Transfer{mk(slskdtest.Succeeded, 100, 100, nil), mk(slskdtest.InProgress, 300, 150, nil)})
	if p.Group != GroupActive || p.Done != 250 {
		t.Errorf("one running: %+v", p)
	}
	// One failed file fails the download even though the other succeeded.
	p = Summarize([]Transfer{mk(slskdtest.Succeeded, 100, 100, nil), mk(slskdtest.TimedOut, 300, 0, nil)})
	if p.Group != GroupFailed || p.Failed != 1 || p.FailureWord != "timed out" {
		t.Errorf("partial failure: %+v", p)
	}
	p = Summarize([]Transfer{mk(slskdtest.Queued, 100, 0, &place), mk(slskdtest.Queued, 100, 0, nil)})
	if p.Group != GroupWaiting || p.QueuePosition == nil || *p.QueuePosition != 3 {
		t.Errorf("queued: %+v", p)
	}
	p = Summarize([]Transfer{mk(slskdtest.Starting, 100, 0, nil), mk(slskdtest.Queued, 100, 0, nil)})
	if p.Group != GroupStarting {
		t.Errorf("starting: %+v", p)
	}
	if got := mk(slskdtest.Rejected, 1, 0, nil).State.FailureWord(); got != "rejected by the peer" {
		t.Errorf("rejected word = %q", got)
	}
}

func TestEnqueueBatchSendsDestinationAndFiles(t *testing.T) {
	c, f := fakeClient(t)
	res, err := c.EnqueueBatch(context.Background(), "0a1b2c3d-0000-4000-8000-000000000001", "peer1", twoFiles(), DownloadFolder(42))
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch == nil || len(res.Batch.Transfers) != 2 {
		t.Fatalf("batch = %+v", res.Batch)
	}
	b := f.OnlyBatch()
	if b == nil || b.Destination != "bindery/42" || b.Username != "peer1" || len(b.Transfers) != 2 {
		t.Fatalf("fake saw %+v", b)
	}
	for _, k := range f.Keys {
		if k != "test-key" {
			t.Errorf("X-API-Key = %q", k)
		}
	}
	// The same id again is slskd's 409, never a second download.
	if _, err := c.EnqueueBatch(context.Background(), "0a1b2c3d-0000-4000-8000-000000000001", "peer1", twoFiles(), DownloadFolder(42)); err == nil {
		t.Error("a repeated batch id was accepted")
	}
}

func TestEnqueueBatchOfflinePeer(t *testing.T) {
	c, f := fakeClient(t)
	f.Offline = true
	_, err := c.EnqueueBatch(context.Background(), "0a1b2c3d-0000-4000-8000-000000000002", "peer1", twoFiles(), "bindery/1")
	if !errors.Is(err, ErrPeerOffline) {
		t.Fatalf("err = %v", err)
	}
}

// slskd answers 207 when some files were refused. A download holds all its
// files or none, so what did start is taken back.
func TestEnqueueBatchPartialRefusalIsUndone(t *testing.T) {
	c, f := fakeClient(t)
	f.Refuse[`@@u\Books\Dune\02.mp3`] = true
	_, err := c.EnqueueBatch(context.Background(), "0a1b2c3d-0000-4000-8000-000000000003", "peer1", twoFiles(), "bindery/1")
	if err == nil || !strings.Contains(err.Error(), "1 of the 2") {
		t.Fatalf("err = %v", err)
	}
	if b := f.Batch("0a1b2c3d-0000-4000-8000-000000000003"); b == nil || len(b.Transfers) != 0 {
		t.Errorf("transfers left behind: %+v", b)
	}
	if f.Count("DELETE ") != 1 {
		t.Errorf("DELETE calls = %d, want one per started file", f.Count("DELETE "))
	}
}

func TestEnqueueBatchBounds(t *testing.T) {
	c, f := fakeClient(t)
	if _, err := c.EnqueueBatch(context.Background(), "x", "u", nil, "d"); err == nil {
		t.Error("no files accepted")
	}
	many := make([]BatchFile, MaxGrabFiles+1)
	for i := range many {
		many[i] = BatchFile{Filename: "f" + strconv.Itoa(i), Size: 1}
	}
	if _, err := c.EnqueueBatch(context.Background(), "x", "u", many, "d"); err == nil {
		t.Error("too many files accepted")
	}
	if f.Count("POST ") != 0 {
		t.Error("a refused request still reached slskd")
	}
}

func TestListBatchesGroupsAndSkipsForeignTransfers(t *testing.T) {
	c, f := fakeClient(t)
	ctx := context.Background()
	if _, err := c.EnqueueBatch(ctx, "0a1b2c3d-0000-4000-8000-00000000000a", "peer1", twoFiles(), "bindery/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnqueueBatch(ctx, "0a1b2c3d-0000-4000-8000-00000000000b", "peer2", twoFiles()[:1], "bindery/2"); err != nil {
		t.Fatal(err)
	}
	f.SetAll("0a1b2c3d-0000-4000-8000-00000000000a", slskdtest.InProgress, 50)
	got, err := c.ListBatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got["0a1b2c3d-0000-4000-8000-00000000000a"]) != 2 || len(got["0a1b2c3d-0000-4000-8000-00000000000b"]) != 1 {
		t.Fatalf("batches = %v", got)
	}
	if p := Summarize(got["0a1b2c3d-0000-4000-8000-00000000000a"]); p.Group != GroupActive || p.Done != 100 {
		t.Errorf("summary %+v", p)
	}
}

func TestRemoveBatchCancelsEveryTransfer(t *testing.T) {
	c, f := fakeClient(t)
	ctx := context.Background()
	id := "0a1b2c3d-0000-4000-8000-00000000000c"
	if _, err := c.EnqueueBatch(ctx, id, "peer1", twoFiles(), "bindery/1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveBatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Batch(id).Transfers); n != 0 {
		t.Errorf("%d transfers remain", n)
	}
	// A batch slskd no longer has is already removed.
	if err := c.RemoveBatch(ctx, "0a1b2c3d-0000-4000-8000-00000000ffff"); err != nil {
		t.Errorf("a missing batch: %v", err)
	}
}
