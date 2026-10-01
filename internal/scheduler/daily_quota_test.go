package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type pausedRefreshProvider struct {
	metadata.Provider
	checks int
}

func (p *pausedRefreshProvider) CheckQuota(context.Context) error {
	p.checks++
	return &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
}
func TestDailyQuotaRefreshStopsBeforeProviderCalls(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authors := db.NewAuthorRepo(database)
	for _, name := range []string{"First", "Second"} {
		if err := authors.Create(context.Background(), &models.Author{Name: name, ForeignID: "hc:" + name, Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	p := &pausedRefreshProvider{}
	s := &Scheduler{authors: authors, meta: metadata.NewAggregator(p)}
	s.refreshMetadata()
	if p.checks != 1 {
		t.Fatalf("continued walking authors: %d checks", p.checks)
	}
}
