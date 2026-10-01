//go:build race

package duplicates

// raceEnabled reports whether the race detector is on. It slows Scan several
// fold, so wall clock budgets scale with it.
const raceEnabled = true
