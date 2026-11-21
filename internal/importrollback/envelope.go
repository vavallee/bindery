package importrollback

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Envelope is the JSON stored in an importer's run-entity metadata column.
// Kind and Version are stamped on every record so a reader can reject
// envelopes from another importer or an unknown version; Data holds free-form
// per-outcome breadcrumbs; Snapshot carries the typed before/after payload
// rollback restores from.
type Envelope struct {
	Kind     string            `json:"kind"`
	Version  int               `json:"version"`
	Data     map[string]any    `json:"data,omitempty"`
	Snapshot *SnapshotEnvelope `json:"snapshot,omitempty"`
}

// SnapshotEnvelope carries a before/after snapshot of one entity. Before and
// After are raw JSON whose shape is named by EntityType; a decoder matches on
// EntityType before unmarshaling into a concrete snapshot struct.
type SnapshotEnvelope struct {
	EntityType string          `json:"entityType"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
}

// NewEnvelope builds the envelope for one entity from already encoded
// before/after payloads (either may be nil).
func NewEnvelope(kind string, version int, entityType string, data map[string]any, before, after json.RawMessage) Envelope {
	return Envelope{
		Kind:    kind,
		Version: version,
		Data:    data,
		Snapshot: &SnapshotEnvelope{
			EntityType: entityType,
			Before:     before,
			After:      after,
		},
	}
}

// MarshalPayload encodes a snapshot struct pointer into the envelope's
// RawMessage slot. A nil input, including a typed nil pointer, returns a nil
// payload so the omitempty tag drops the field: json.Marshal on a (*T)(nil)
// encodes "null", which cannot be told apart from an absent snapshot and
// would defeat before/after gating. label names the importer in the error.
func MarshalPayload(label string, v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %s rollback snapshot: %w", label, err)
	}
	return data, nil
}

// ParseEnvelope decodes raw and reports false when it is blank, malformed, or
// carries another kind or version. A nil Data is replaced with an empty map.
func ParseEnvelope(raw, kind string, version int) (Envelope, bool) {
	var envelope Envelope
	if strings.TrimSpace(raw) == "" {
		return envelope, false
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return envelope, false
	}
	if envelope.Kind != kind || envelope.Version != version {
		return envelope, false
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope, true
}

// DecodeSnapshots returns the before and after snapshots of type T from raw
// when it holds an envelope of the given kind and version whose snapshot is
// for entityType and has both sides. Anything else reports false.
func DecodeSnapshots[T any](raw, kind string, version int, entityType string) (*T, *T, bool) {
	envelope, ok := ParseEnvelope(raw, kind, version)
	if !ok || envelope.Snapshot == nil || envelope.Snapshot.EntityType != entityType {
		return nil, nil, false
	}
	if len(envelope.Snapshot.Before) == 0 || len(envelope.Snapshot.After) == 0 {
		return nil, nil, false
	}
	var before, after T
	if err := json.Unmarshal(envelope.Snapshot.Before, &before); err != nil {
		return nil, nil, false
	}
	if err := json.Unmarshal(envelope.Snapshot.After, &after); err != nil {
		return nil, nil, false
	}
	return &before, &after, true
}
