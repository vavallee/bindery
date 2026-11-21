package abs

import (
	"strings"
	"testing"
	"time"
)

// The shared importrollback clone keeps a time's zone, while ABS has always
// normalised to UTC. These pin that ABS output did not change.

func TestBookSnapshotMetadata_NonUTCTimeStoredAsUTC(t *testing.T) {
	t.Parallel()
	est := time.FixedZone("EST", -5*3600)
	when := time.Date(2020, 6, 7, 8, 9, 10, 0, est)
	snap := &bookRollbackSnapshot{Title: "t", ReleaseDate: cloneTimePtr(&when), LastMetadataRefreshAt: cloneTimePtr(&when)}
	envelope, err := bookSnapshotMetadata(nil, snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(envelope.Snapshot.Before)
	if !strings.Contains(raw, `"releaseDate":"2020-06-07T13:09:10Z"`) || strings.Contains(raw, "-05:00") {
		t.Fatalf("snapshot JSON = %s, want UTC times", raw)
	}
	if envelope.Snapshot.After != nil {
		t.Fatalf("nil after snapshot encoded as %s, want absent", envelope.Snapshot.After)
	}
}

func TestRestoreTimePtr_NormalisesRestoredValueToUTC(t *testing.T) {
	t.Parallel()
	est := time.FixedZone("EST", -5*3600)
	before := time.Date(2001, 2, 3, 4, 5, 6, 0, est)
	after := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

	live, changed := cloneTimePtr(&after), false
	restoreTimePtr(&live, &before, &after, &changed)
	if !changed || !live.Equal(before) || live.Location() != time.UTC {
		t.Fatalf("restored = %v (%v), changed %v, want the original instant in UTC", live, live.Location(), changed)
	}

	// Edited after the run: left exactly as it was, zone included.
	edited := time.Date(2030, 1, 1, 0, 0, 0, 0, est)
	live, changed = &edited, false
	restoreTimePtr(&live, &before, &after, &changed)
	if changed || live != &edited || live.Location() != est {
		t.Fatalf("edited value touched: %v changed %v", live, changed)
	}
}

func TestAuthorSnapshotMetadata_TypedNilSnapshotIsAbsent(t *testing.T) {
	t.Parallel()
	var none *authorRollbackSnapshot
	envelope, err := authorSnapshotMetadata(nil, none, none)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Snapshot.Before != nil || envelope.Snapshot.After != nil {
		t.Fatalf("typed nil encoded as before=%s after=%s, want absent", envelope.Snapshot.Before, envelope.Snapshot.After)
	}
}
