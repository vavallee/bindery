package googlebooks

import (
	"os"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

// TestMain shortens the shared retry backoff so the tests that exercise a
// refusal or an outage do not spend real seconds waiting it out.
func TestMain(m *testing.M) {
	restore := providerhttp.SetBaseDelay(time.Millisecond)
	code := m.Run()
	restore()
	os.Exit(code)
}
