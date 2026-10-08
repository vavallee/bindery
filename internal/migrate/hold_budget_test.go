package migrate

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

// The run's total waiting is bounded too, so a primary that refuses each row
// once, which never builds a streak, cannot keep a large import waiting
// without end. Once the budget is spent a refused lookup is not waited out
// and counts toward the streak.
func TestImportCSVAuthors_HoldWaitBudgetIsBounded(t *testing.T) {
	shortenHoldWait(t, 30*time.Millisecond)
	prev := primaryHoldBudget
	primaryHoldBudget = 50 * time.Millisecond
	t.Cleanup(func() { primaryHoldBudget = prev })

	var mu sync.Mutex
	seen := map[string]bool{}
	var calls atomic.Int32
	agg := metadata.NewAggregator(refusingPrimary(&calls, func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		first := !seen[name]
		seen[name] = true
		return first
	}))
	repo := db.NewAuthorRepo(newTestDB(t))
	names := heldAuthorNames(20)

	start := time.Now()
	res, err := ImportCSVAuthors(context.Background(), strings.NewReader(strings.Join(names, "\n")), repo, nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("import took %v, want the 50ms wait budget to bound it", elapsed)
	}
	// The first row waits once and is added; the budget is then spent, so
	// the next refused rows count straight away and trip the breaker.
	if res.Added != 1 {
		t.Errorf("Added=%d, want 1: only the row waited out before the budget ran out", res.Added)
	}
	if got := calls.Load(); got > int32(2+primaryOutageThreshold) {
		t.Errorf("primary asked %d times, want at most %d once the budget was spent", got, 2+primaryOutageThreshold)
	}
}
