package hardcover

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

// NewAuthenticated must honor BINDERY_OUTBOUND_PROXY like New() does, so the
// list syncer and import-list browse (its callers) do not dial hardcover.app
// directly (#1847). With a proxy configured, a request the client sends to the
// Hardcover API reaches the proxy, as a CONNECT for the TLS endpoint. The
// request goes through the client's own http.Client rather than a query
// method, so the package's shared throttle never sees the refused tunnel.
func TestNewAuthenticated_RoutesThroughOutboundProxy(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	hit := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- r.Method + " " + r.Host:
		default:
		}
		w.WriteHeader(http.StatusBadGateway) // refuse the tunnel; only the attempt matters
	}))
	defer proxy.Close()
	if _, err := httpsec.ConfigureOutboundProxy(proxy.URL, "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy: %v", err)
	}

	c := NewAuthenticated("tok")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, graphqlURL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}

	select {
	case got := <-hit:
		if got != "CONNECT api.hardcover.app:443" {
			t.Errorf("proxy saw %q, want a CONNECT to the Hardcover API", got)
		}
	default:
		t.Error("the request did not go through the configured proxy")
	}
}
