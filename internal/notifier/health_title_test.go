package notifier

import "testing"

// EventHealth now covers both download clients (#849) and indexers (#1935).
// The title has to say which, since "Download Client Unhealthy" on a suspended
// indexer sends the reader to the wrong settings page.
func TestNormalizeEventPayload_HealthTitleNamesTheSubject(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
		want    string
	}{
		{
			name:    "download client keeps its title",
			payload: map[string]interface{}{"clientId": int64(3), "status": "error", "message": "path not visible"},
			want:    "Download Client Unhealthy",
		},
		{
			name:    "named indexer",
			payload: map[string]interface{}{"indexerId": int64(7), "indexerName": "NZBgeek", "status": "error", "message": "indexer error 101: Account suspended"},
			want:    "Indexer Unhealthy: NZBgeek",
		},
		{
			name:    "indexer with no name",
			payload: map[string]interface{}{"indexerId": int64(7), "status": "error", "message": "boom"},
			want:    "Indexer Unhealthy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeEventPayload(EventHealth, tc.payload)
			if got := out["title"]; got != tc.want {
				t.Errorf("title = %v, want %q", got, tc.want)
			}
			if out["message"] == "" || out["message"] == nil {
				t.Error("message is empty")
			}
		})
	}
}
