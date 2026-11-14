package main

import "github.com/vavallee/bindery/internal/models"

// downloadStateLabels is the state label set for bindery_queue_items: every
// download state, so each one is reported (at zero when empty) and a value
// outside it collapses to other.
//
// The gauge is collected at scrape time with one indexed COUNT GROUP BY
// rather than kept up to date by the scheduler. A scrape every 15 to 60
// seconds costs one small query, the number is never stale, and no state
// change anywhere in the code has to remember to touch a gauge.
func downloadStateLabels() []string {
	states := models.AllStates()
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}
