package abs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

type heldProvider struct {
	metadata.Provider
	checks    int
	stopAfter int
}

func (p *heldProvider) CheckQuota(context.Context) error {
	p.checks++
	if p.checks > p.stopAfter {
		return &metadata.DailyQuotaError{ResetAt: time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)}
	}
	return nil
}

func TestDailyQuotaStopsABSBeforeCheckpointingItem(t *testing.T) {
	for _, after := range []int{0, 1} {
		t.Run(string(rune('0'+after)), func(t *testing.T) {
			importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			p := &heldProvider{stopAfter: after}
			importer.WithMetadata(metadata.NewAggregator(p))
			accepted := 0
			importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
				for range 3 {
					if err := fn(ctx, sampleABSItem()); err != nil {
						return EnumerationStats{}, err
					}
					accepted++
				}
				return EnumerationStats{}, nil
			}
			_, err := importer.Run(context.Background(), ImportConfig{SourceID: DefaultSourceID, BaseURL: "https://abs.example.com", APIKey: "example-only", LibraryID: "lib-books", Enabled: true, DryRun: true})
			if err == nil || !strings.Contains(err.Error(), "2026-09-19T01:00:00Z") || accepted != 0 {
				t.Fatalf("accepted %d err %v", accepted, err)
			}
		})
	}
}
