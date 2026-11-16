package api

import (
	"bytes"
	"encoding/json"
)

// nullableInt64 is a JSON request field that tells "left out" apart from an
// explicit null. A bare *int64 cannot: both decode to nil. That matters for a
// field where null is a real choice, such as an author's quality profile,
// where null means "no profile" and leaving it out means "do not change it"
// on an update or "use the default" on a create (#3831).
type nullableInt64 struct {
	// Set is true when the field was present in the request, null or not.
	Set bool
	// Value is the number sent, or nil for an explicit null.
	Value *int64
}

func (n *nullableInt64) UnmarshalJSON(data []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		n.Value = nil
		return nil
	}
	var v int64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}
