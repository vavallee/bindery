package scheduler

import (
	"sync"
	"testing"
	"time"
)

func TestUpgradeGate_CooldownConcurrency(t *testing.T) {
	s := &Scheduler{}
	now := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 3000; i++ {
				s.noteUpgradeSearched(int64(i), now)
				if i%100 == 0 {
					_ = s.coolingUpgradeBooks(now)
				}
			}
		}(g)
	}
	wg.Wait()
	if n := len(s.coolingUpgradeBooks(now)); n != 3000 {
		t.Fatalf("cooling=%d", n)
	}
	if n := len(s.coolingUpgradeBooks(now.Add(25 * time.Hour))); n != 0 {
		t.Fatalf("cooling after expiry=%d", n)
	}
	if len(s.upgradeSearched) != 0 {
		t.Fatalf("map not pruned: %d", len(s.upgradeSearched))
	}
}
