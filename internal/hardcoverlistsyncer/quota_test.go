package hardcoverlistsyncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

type quotaDeferredClient struct {
	fakeHardcoverClient
	err error
}

func (c *quotaDeferredClient) GetUserLists(ctx context.Context) ([]hardcover.HCList, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.fakeHardcoverClient.GetUserLists(ctx)
}

func TestQuotaDeferredListResumesWithoutMarkingSuccess(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()
	il := testImportList("Quota list", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatal(err)
	}
	deferred := &hardcover.QuotaDeferredError{Status: hardcover.QuotaStatus{Source: "upstream", Allowance: 50000, Remaining: 0, Deferred: true, NextEligible: time.Now().Add(time.Hour)}}
	client := &quotaDeferredClient{err: deferred, fakeHardcoverClient: fakeHardcoverClient{lists: []hardcover.HCList{{ID: 9, Slug: il.URL}}}}
	s.WithClientFactory(func(string) hardcoverClient { return client })
	if err := s.SyncOne(ctx, il.ID); !errors.Is(err, hardcover.ErrRateLimited) {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, il.ID)
	if err != nil || stored.LastSyncAt != nil {
		t.Fatalf("deferred sync marked successful: %+v %v", stored, err)
	}
	if progress := s.Progress(); progress.Running || !strings.Contains(progress.Message, "deferred") {
		t.Fatalf("progress: %+v", progress)
	}
	client.err = nil
	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetByID(ctx, il.ID)
	if err != nil || stored.LastSyncAt == nil {
		t.Fatalf("resume not completed: %+v %v", stored, err)
	}
}
