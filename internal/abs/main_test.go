package abs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

// TestMain keeps this package's tests off the network.
//
// metadata.NewAggregator always builds a real audnex client, so any test
// that imports an item with an ASIN through an aggregator, without stubbing
// audnex, asks api.audnex.us. Where egress is blocked (CI runs behind
// harden-runner) that connect hangs for the client's 15 second timeout and
// is retried with backoff, about two minutes per lookup. That is what
// pushed the race job past its 25 minute timeout (TestCovImpRollback_*,
// TestImporter_Rollback*, TestImporter_HardcoverSeries*). Where egress is
// open the tests instead depend on whatever the live service answers.
//
// Refusing every non-loopback dial on the shared default transport, which
// the metadata clients clone, makes such a lookup fail at once the same way
// on every machine. httptest servers listen on loopback and are unaffected,
// and clients with their own guarded dialer (GuardedTransport) keep it.
// The shortened backoff stops the refused lookup's retries costing real
// seconds.
func TestMain(m *testing.M) {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.DialContext = loopbackOnlyDial(t.DialContext)
	}
	restore := providerhttp.SetBaseDelay(time.Millisecond)
	code := m.Run()
	restore()
	os.Exit(code)
}

func loopbackOnlyDial(next func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if next == nil {
		next = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("internal/abs tests do not reach the network: refused dial to %s", addr)
		}
		return next(ctx, network, addr)
	}
}
