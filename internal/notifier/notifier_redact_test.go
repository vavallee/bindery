package notifier

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// closedAddr returns a loopback host:port nothing listens on, so a request to
// it fails in the transport and comes back as a *url.Error carrying the URL.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// Discord and Telegram put the credential in the URL path. A failed send
// returns the transport error, which Send logs and the test endpoint shows,
// and that error embeds the full URL.
func TestSend_FailureDoesNotLeakWebhookCredential(t *testing.T) {
	addr := closedAddr(t)
	for name, u := range map[string]string{
		"discord":  "http://" + addr + "/api/webhooks/123456789012345678/S3CRETTOKEN-abc_DEF",
		"telegram": "http://" + addr + "/bot123456:S3CRETTOKEN-x_y/sendMessage",
		"query":    "http://" + addr + "/message?token=S3CRETTOKEN",
	} {
		t.Run(name, func(t *testing.T) {
			n := testNotifier(&http.Client{})
			err := n.Test(context.Background(), &models.Notification{URL: u})
			if err == nil {
				t.Fatal("expected the send to fail")
			}
			if strings.Contains(err.Error(), "S3CRETTOKEN") {
				t.Fatalf("notifier error leaks the webhook credential: %v", err)
			}
		})
	}
}

// A URL that does not parse is echoed back inside the parse error.
func TestSend_UnparseableURLDoesNotLeakWebhookCredential(t *testing.T) {
	n := testNotifier(&http.Client{})
	err := n.Test(context.Background(), &models.Notification{URL: "http://discord.com/api/webhooks/1/S3CRETTOKEN\x7f"})
	if err == nil {
		t.Fatal("expected an error for a URL with a control character")
	}
	if strings.Contains(err.Error(), "S3CRETTOKEN") {
		t.Fatalf("notifier error leaks the webhook credential: %v", err)
	}
}
