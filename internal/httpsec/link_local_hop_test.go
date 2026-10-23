package httpsec

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A redirect hop never gets the link-local allowance, at validation or at the
// dial that follows it, even for a hostname that only turns link-local at
// connect time.
func TestLinkLocalServices_RedirectHopLosesAllowance(t *testing.T) {
	linkLocalOn(t)
	for _, target := range []string{
		urlFor("169.254.1.2"), urlFor("169.254.169.253"), urlFor("fe80::1"), urlFor("169.254.169.254"),
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := RedirectGuard(PolicyService)(req, nil); err == nil {
			t.Errorf("redirect hop to %s allowed", target)
		}
	}
	// A LAN hop is still followed, and the marker rides on the request.
	req, _ := http.NewRequest(http.MethodGet, "http://10.1.2.3:9696/x", nil)
	if err := RedirectGuard(PolicyService)(req, nil); err != nil {
		t.Fatalf("LAN hop refused: %v", err)
	}
	stubResolver(t, "169.254.1.2")
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()
	_, err := NewDialContext(PolicyService)(ctx, "tcp", "rebind.example.com:9696")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("hop dial resolving to link-local: err = %v, want a policy refusal", err)
	}
}
