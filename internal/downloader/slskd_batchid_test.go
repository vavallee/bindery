package downloader

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Every attempt gets its own batch id, so a retry after a cancelled batch can
// never meet slskd's 409 for a reused id, even when slskd still holds the old
// batch.
func TestSlskdEveryAttemptUsesANewBatchID(t *testing.T) {
	client, f := slskdFakeClient(t, 913920, "")
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		res, err := SendDownload(ctx, client, grabAddr(t), "t", SendOptions{DownloadID: 3})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if seen[res.RemoteID] {
			t.Fatalf("batch id %s reused", res.RemoteID)
		}
		seen[res.RemoteID] = true
		if i%2 == 0 {
			id := res.RemoteID
			if err := RemoveDownload(ctx, client, &models.Download{ID: 3, SABnzbdNzoID: &id}, false, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(f.Batches) != 5 {
		t.Errorf("slskd holds %d batches", len(f.Batches))
	}
}
