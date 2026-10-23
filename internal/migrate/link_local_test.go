package migrate

import (
	"strings"
	"testing"
)

// The migration check follows the same rule as the forms it feeds (#2036).
func TestValidateMigratedURL_LinkLocalRule(t *testing.T) {
	const env = "BINDERY_ALLOW_LINK_LOCAL_SERVICES"
	t.Setenv(env, "")
	if err := validateMigratedURL("http://169.254.1.2:8989"); err == nil || !strings.Contains(err.Error(), env) {
		t.Errorf("option off: err = %v, want a refusal naming %s", err, env)
	}
	if err := validateMigratedURL("http://169.254.169.254/"); err == nil {
		t.Error("option off: metadata accepted")
	}
	t.Setenv(env, "1")
	if err := validateMigratedURL("http://169.254.1.2:8989"); err != nil {
		t.Errorf("option on: link-local refused: %v", err)
	}
	if err := validateMigratedURL("http://169.254.169.254/"); err == nil {
		t.Error("option on: metadata accepted")
	}
}
