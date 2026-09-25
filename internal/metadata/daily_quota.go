package metadata

import (
	"context"
	"fmt"
	"time"
)

// DailyQuotaError is a long-lived provider pause, distinct from short throttling.
// Callers should stop bulk work and surface ResetAt rather than retry each row.
type DailyQuotaError struct{ ResetAt time.Time }

func (e *DailyQuotaError) Error() string {
	return fmt.Sprintf("Hardcover daily quota exhausted; paused until %s; retry the job after this time", e.ResetAt.UTC().Format(time.RFC3339))
}

// CheckQuota lets bulk jobs stop even when optional enrichment swallowed a
// provider error. No provider requests are made by this check.
func (a *Aggregator) CheckQuota(ctx context.Context) error {
	if a == nil {
		return nil
	}
	for _, p := range a.providers() {
		if quota, ok := p.(interface{ CheckQuota(context.Context) error }); ok {
			if err := quota.CheckQuota(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
