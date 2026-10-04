package notifier

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Every webhook Bindery can point at that keeps its secret in the URL path:
// Home Assistant's webhook id, a Teams connector, an Apprise config key, and
// an ntfy topic (on ntfy.sh or self hosted), which is the only thing guarding
// a topic without an access token.
func TestSend_FailureDoesNotLeakPathSecrets(t *testing.T) {
	addr := closedAddr(t)
	for name, u := range map[string]string{
		"home assistant": "http://" + addr + "/api/webhook/S3CRETTOKEN-hook",
		"teams":          "http://" + addr + "/webhookb2/11111111-2222@33333333-4444/IncomingWebhook/S3CRETTOKEN/55555555",
		"apprise key":    "http://" + addr + "/notify/S3CRETTOKEN",
		"ntfy topic":     "http://" + addr + "/S3CRETTOKEN",
	} {
		t.Run(name, func(t *testing.T) {
			n := testNotifier(&http.Client{})
			err := n.Test(context.Background(), &models.Notification{URL: u})
			if err == nil {
				t.Fatal("expected the send to fail")
			}
			if strings.Contains(err.Error(), "S3CRETTOKEN") {
				t.Fatalf("notifier error leaks the webhook secret: %v", err)
			}
			if !strings.Contains(err.Error(), addr) {
				t.Fatalf("the host should stay for diagnosis: %v", err)
			}
		})
	}
}
