package indexer

import (
	"context"
	"errors"
	"time"

	"github.com/vavallee/bindery/internal/metrics"
	"github.com/vavallee/bindery/internal/models"
)

// observeSearch records one search against one indexer in
// bindery_indexer_requests_total and its duration histogram (#3851). The
// label is the indexer's configured type, never its user given name or URL.
// Running out of the daily query budget is result quota, a deadline is
// timeout, any other error is error.
func observeSearch(idx models.Indexer, start time.Time, err error) {
	metrics.ObserveIndexerRequest(idx.Type, searchResult(err), time.Since(start))
}

func searchResult(err error) string {
	switch {
	case err == nil:
		return metrics.ResultOK
	case budgetExhausted(err):
		return metrics.ResultQuota
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return metrics.ResultTimeout
	default:
		return metrics.ResultError
	}
}

// isTimeout reports whether err is a network timeout, which is how an
// http.Client Timeout surfaces.
func isTimeout(err error) bool {
	var te interface{ Timeout() bool }
	return errors.As(err, &te) && te.Timeout()
}
