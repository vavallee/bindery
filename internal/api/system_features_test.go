package api

import (
	"encoding/json"
	"testing"
)

// GET /system/status advertises forUser under features.requestsForUser, the
// exact name an integration checks before sending the field (#3337). A rename
// would silently turn the feature off for every client that detects it.
func TestCurrentSystemFeatures_AdvertisesRequestsForUser(t *testing.T) {
	raw, err := json.Marshal(CurrentSystemFeatures())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"requestsForUser":true}`; got != want {
		t.Fatalf("features = %s, want %s", got, want)
	}
}
