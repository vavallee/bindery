//go:build race

package db

// raceEnabled reports whether the race detector is on. It slows queries
// several fold, so wall clock budgets do not hold under it.
const raceEnabled = true
