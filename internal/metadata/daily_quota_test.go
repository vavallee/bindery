package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type quotaProvider struct {
	*mockProvider
	err error
}

func (p *quotaProvider) CheckQuota(context.Context) error { return p.err }

func TestDailyQuotaAggregator(t *testing.T) {
	ctx := context.Background()
	daily := &DailyQuotaError{ResetAt: time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)}
	primary := &mockProvider{name: "openlibrary"}
	for _, tc := range []struct {
		name string
		agg  *Aggregator
		want error
	}{
		{"nil", nil, nil},
		{"unsupported", NewAggregator(primary), nil},
		{"available", NewAggregator(&quotaProvider{primary, nil}), nil},
		{"primary held", NewAggregator(&quotaProvider{primary, daily}), daily},
		{"enricher held", NewAggregator(primary, &quotaProvider{&mockProvider{name: "hardcover"}, daily}), daily},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.agg.CheckQuota(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if !strings.Contains(daily.Error(), "2026-09-19T01:00:00Z") {
		t.Fatal(daily.Error())
	}
}

func TestDailyQuotaISBNStopsFallback(t *testing.T) {
	daily := &DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	primary := &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", daily)}
	fallback := &mockProvider{name: "openlibrary"}
	book, _, err := NewAggregator(primary, fallback).GetBookByISBNWithOutcome(context.Background(), "9780441172719")
	if book != nil || !errors.Is(err, daily) || fallback.getByISBNCalls != 0 {
		t.Fatalf("book %v err %v fallback calls %d", book, err, fallback.getByISBNCalls)
	}
}
