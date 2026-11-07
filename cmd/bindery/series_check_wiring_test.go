package main

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func TestNewSeriesChecker_Outcomes(t *testing.T) {
	cases := []struct {
		name                                string
		res                                 api.SeriesCheckResult
		err                                 error
		skipped, backoff, busy, unavailable bool
	}{
		{name: "ok", res: api.SeriesCheckResult{Status: models.SeriesCheckOK, Created: 2}},
		{name: "skipped", res: api.SeriesCheckResult{}, skipped: true},
		{name: "rate limited", res: api.SeriesCheckResult{Status: models.SeriesCheckFailed}, err: metadata.ErrRateLimited, backoff: true},
		{name: "busy", err: api.ErrSeriesCheckRunning, busy: true},
		{name: "plain error is about the series", res: api.SeriesCheckResult{Status: models.SeriesCheckFailed}, err: errors.New("bad record")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSeriesChecker(func(context.Context, *models.Series) (api.SeriesCheckResult, error) {
				return tc.res, tc.err
			}, nil)
			out := c.CheckSeries(context.Background(), &models.Series{ID: 1})
			if out.Skipped != (tc.skipped && tc.err == nil) || out.Backoff != tc.backoff || out.Busy != tc.busy || out.Unavailable != tc.unavailable {
				t.Errorf("outcome = %+v", out)
			}
			if out.Created != tc.res.Created {
				t.Errorf("created = %d, want %d", out.Created, tc.res.Created)
			}
		})
	}
	if newSeriesChecker(nil, nil).BulkRefreshRunning() {
		t.Error("nil bulk check reported a running refresh")
	}
}
