package importer

import "testing"

// TestTransmissionCompletion pins the Transmission RPC status enum so the
// status-3-is-queued / status-6-is-seeding mapping can't silently invert again
// (the original bug: a torrent queued to download at 0% was classed complete,
// imported, failed, and got terminally blocked).
func TestTransmissionCompletion(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		percentDone  float64
		wantComplete bool
		wantStopped  bool
	}{
		{"stopped, no data", 0, 0.0, false, true},
		{"stopped but fully downloaded", 0, 1.0, true, true},
		{"queued to check", 1, 0.0, false, false},
		{"checking", 2, 0.5, false, false},
		{"queued to download at 0% is NOT complete", 3, 0.0, false, false},
		{"downloading mid-flight", 4, 0.5, false, false},
		{"queued to seed is complete", 5, 1.0, true, false},
		{"seeding is complete", 6, 1.0, true, false},
		{"downloading at 100% is complete", 4, 1.0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotComplete, gotStopped := transmissionCompletion(tc.status, tc.percentDone)
			if gotComplete != tc.wantComplete {
				t.Errorf("complete = %v, want %v (status=%d pct=%.2f)", gotComplete, tc.wantComplete, tc.status, tc.percentDone)
			}
			if gotStopped != tc.wantStopped {
				t.Errorf("stopped = %v, want %v (status=%d pct=%.2f)", gotStopped, tc.wantStopped, tc.status, tc.percentDone)
			}
		})
	}
}
