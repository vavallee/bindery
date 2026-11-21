package downloader

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/downloader/slskd/slskdtest"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

func slskdFakeClient(t *testing.T, id int64, remap string) (*models.DownloadClient, *slskdtest.Fake) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	f, srv := slskdtest.New()
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return &models.DownloadClient{ID: id, Type: "slskd", Host: u.Hostname(), Port: port, APIKey: "test-key", PathRemap: remap}, f
}

func grabAddr(t *testing.T) string {
	t.Helper()
	addr, err := slskd.EncodeGrab(slskd.Grab{User: "peer1", Files: []slskd.BatchFile{
		{Filename: `@@peer1\Books\Dune\01.mp3`, Size: 100}, {Filename: `@@peer1\Books\Dune\02.mp3`, Size: 300},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

var guidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The dispatch matrix for slskd: send, poll and remove each reach slskd's own
// API and nothing shaped like another client's.
func TestSlskdSendPollRemoveDispatch(t *testing.T) {
	client, f := slskdFakeClient(t, 913910, "")
	ctx := context.Background()

	res, err := SendDownload(ctx, client, grabAddr(t), "Dune", SendOptions{MediaType: models.MediaTypeAudiobook, DownloadID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if res.Protocol != ProtocolSoulseek || res.UsesTorrentID || !guidRe.MatchString(res.RemoteID) {
		t.Fatalf("result = %+v", res)
	}
	b := f.Batch(res.RemoteID)
	if b == nil || b.Destination != "bindery/42" || b.Username != "peer1" || len(b.Transfers) != 2 {
		t.Fatalf("slskd got %+v", b)
	}
	// The folder name carries the id only, never the title or the peer.
	if strings.Contains(b.Destination, "Dune") || strings.Contains(b.Destination, "peer1") {
		t.Errorf("destination %q leaks title or user", b.Destination)
	}

	live, usesTorrent, err := GetLiveStatuses(ctx, client)
	if err != nil || usesTorrent {
		t.Fatalf("live: %v %v", err, usesTorrent)
	}
	st, ok := live[res.RemoteID]
	if !ok || st.Soulseek == nil || st.Soulseek.State != "waiting" || st.Soulseek.Files != 2 {
		t.Fatalf("live = %+v", live)
	}
	if st.Soulseek.QueuePosition == nil || *st.Soulseek.QueuePosition != 1 {
		t.Errorf("queue position = %v", st.Soulseek.QueuePosition)
	}

	f.SetFile(res.RemoteID, 0, slskdtest.Succeeded, 100)
	f.SetFile(res.RemoteID, 1, slskdtest.InProgress, 150)
	live, _, _ = GetLiveStatuses(ctx, client)
	st = live[res.RemoteID]
	if st.Percentage != "62.5" || st.Soulseek.State != "downloading" || st.Soulseek.FilesDone != 1 || st.SizeLeft != 150 {
		t.Errorf("progress = %+v / %+v", st, st.Soulseek)
	}

	f.SetFile(res.RemoteID, 1, slskdtest.Errored, 150)
	live, _, _ = GetLiveStatuses(ctx, client)
	if got := live[res.RemoteID]; got.Soulseek.State != "failed" || !LiveStatusIsError(got.Status) {
		t.Errorf("failed = %+v", got)
	}

	id := res.RemoteID
	if err := RemoveDownload(ctx, client, &models.Download{ID: 42, SABnzbdNzoID: &id}, false, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Batch(id).Transfers); n != 0 {
		t.Errorf("%d transfers left after RemoveDownload", n)
	}

	for _, r := range f.Requests {
		if !strings.Contains(r, " /api/v0/") {
			t.Errorf("request %q is not an slskd endpoint", r)
		}
	}
}

func TestSlskdSendRefusesBadInput(t *testing.T) {
	client, f := slskdFakeClient(t, 913911, "")
	ctx := context.Background()
	for _, src := range []string{"https://indexer.example/x.nzb", "slskd://!!", ""} {
		if _, err := SendDownload(ctx, client, src, "t", SendOptions{DownloadID: 1}); err == nil {
			t.Errorf("%q was sent", src)
		}
	}
	if _, err := SendDownload(ctx, client, grabAddr(t), "t", SendOptions{}); err == nil {
		t.Error("a send without a download id was accepted")
	}
	if f.Count("POST ") != 0 {
		t.Error("a refused send reached slskd")
	}
}

func TestSlskdSendOfflinePeerFails(t *testing.T) {
	client, f := slskdFakeClient(t, 913912, "")
	f.Offline = true
	_, err := SendDownload(context.Background(), client, grabAddr(t), "t", SendOptions{DownloadID: 1})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v", err)
	}
}

// A retry reuses bindery/<id>. What the first attempt left there is deleted
// before the new request, so it is not imported next to the new files.
func TestSlskdSendClearsAStaleFolder(t *testing.T) {
	root := t.TempDir()
	client, f := slskdFakeClient(t, 913913, "/app/downloads:"+root)
	f.Downloads = "/app/downloads"
	stale := filepath.Join(root, "bindery", "9", "old")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "bindery", "10")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	// The new batch must be requested only after the stale folder is gone.
	staleAtPost := true
	f.OnRequest = func(method, path string) {
		if method == http.MethodPost && strings.HasSuffix(path, "/transfers/downloads/batches") {
			_, err := os.Stat(filepath.Join(root, "bindery", "9"))
			staleAtPost = !os.IsNotExist(err)
		}
	}
	if _, err := SendDownload(context.Background(), client, grabAddr(t), "t", SendOptions{DownloadID: 9}); err != nil {
		t.Fatal(err)
	}
	if staleAtPost {
		t.Error("the batch was requested before the stale folder was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "bindery", "9")); !os.IsNotExist(err) {
		t.Errorf("the stale folder survived: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("another download's folder was touched: %v", err)
	}
}

func TestSlskdRemoveDeletesOnlyThisDownloadsFolder(t *testing.T) {
	root := t.TempDir()
	client, f := slskdFakeClient(t, 913914, "/app/downloads:"+root)
	mine := filepath.Join(root, "bindery", "5")
	other := filepath.Join(root, "bindery", "6")
	for _, d := range []string{mine, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.epub"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := "0a1b2c3d-0000-4000-8000-000000000099"
	_ = f
	// deleteFiles false leaves the files.
	if err := RemoveDownload(context.Background(), client, &models.Download{ID: 5, SABnzbdNzoID: &id}, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("files deleted without being asked: %v", err)
	}
	if err := RemoveDownload(context.Background(), client, &models.Download{ID: 5, SABnzbdNzoID: &id}, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("own folder survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another download's folder was deleted: %v", err)
	}
}

// A folder that is a symlink is removed as the link and never followed.
func TestSlskdRemoveDoesNotFollowASymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bindery"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "bindery", "7")); err != nil {
		t.Skip("symlinks unavailable")
	}
	client, _ := slskdFakeClient(t, 913915, "/app/downloads:"+root)
	id := "0a1b2c3d-0000-4000-8000-000000000098"
	if err := RemoveDownload(context.Background(), client, &models.Download{ID: 7, SABnzbdNzoID: &id}, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.txt")); err != nil {
		t.Errorf("the symlink target was deleted: %v", err)
	}
}

func TestSlskdLocalFolderRefusesAnUnsafeShape(t *testing.T) {
	client, f := slskdFakeClient(t, 913916, "")
	f.Downloads = "relative/path"
	if p, why := SlskdLocalFolder(context.Background(), client, 3, ""); p != "" || why == "" {
		t.Errorf("a relative downloads folder gave %q", p)
	}
	f.Downloads = "/app/downloads"
	if p, _ := SlskdLocalFolder(context.Background(), client, 3, ""); p != "/app/downloads/bindery/3" {
		t.Errorf("folder = %q", p)
	}
}
