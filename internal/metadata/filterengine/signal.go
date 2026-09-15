package filterengine

import "fmt"

// Registry is a fixed, ordered set of signals with no duplicate IDs. Order
// matters for exactly one reason at v1: internal/api/authors.go's
// pre-#2235 boolean chain was first-drop-wins, and a candidate tripping more
// than one veto is now attributed (for Skipped* counter purposes) to its
// strongest-magnitude observation — with every v1 signal at equal
// (vetoWeight) magnitude, ties are broken by registry order, so registry
// order is kept identical to the original chain's check order (media type,
// junk title, language, part book, missing date, missing ISBN, min pages).
// That is what makes the golden parity test an exact comparison rather than
// an approximate one.
type Registry struct {
	signals []Signal
}

// NewRegistry builds a Registry from signals, rejecting a duplicate ID
// outright — two signals racing to emit under the same ID would make an
// observation's Signal field ambiguous evidence, silently. This is a
// programming error (the registry is a fixed, code-defined list, never
// user input), so callers that know their registry is a compile-time
// constant should wrap this in MustRegistry rather than propagate the
// error.
func NewRegistry(signals ...Signal) (*Registry, error) {
	seen := make(map[string]struct{}, len(signals))
	for _, s := range signals {
		id := s.ID()
		if id == "" {
			return nil, fmt.Errorf("filterengine: signal at index %d has an empty ID", len(seen))
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("filterengine: duplicate signal id %q", id)
		}
		seen[id] = struct{}{}
	}
	return &Registry{signals: append([]Signal(nil), signals...)}, nil
}

// MustRegistry is NewRegistry for a registry callers know is a fixed,
// code-defined list — analogous to regexp.MustCompile. Panics on the same
// conditions NewRegistry would error on.
func MustRegistry(signals ...Signal) *Registry {
	r, err := NewRegistry(signals...)
	if err != nil {
		panic(err)
	}
	return r
}

// Signals returns the registry's signals in registration order.
func (r *Registry) Signals() []Signal {
	return r.signals
}

// Decide runs every signal in the registry against c under ctx, in
// registration order, and bands the result — the batched-pass equivalent of
// calling the package-level Decide with the registry's full signal list.
func (r *Registry) Decide(c Candidate, ctx *Context) Result {
	return Decide(c, ctx, r.signals...)
}
