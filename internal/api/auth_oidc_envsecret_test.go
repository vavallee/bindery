package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #4159: a provider whose secret comes from BINDERY_OIDC_<ID>_CLIENT_SECRET
// never has a secret written by a save. A stored one is left alone.
func TestSetProviders_EnvSecretNeverPersisted(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET", "env-secret-value")
	h, settings := newOIDCSetProvidersFixture(t)

	// New provider, no secret sent: allowed, stored without one.
	body := `[{"id":"keycloak","name":"KC","issuer":"https://kc.example.com","client_id":"abc","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := storedProviders(t, settings); len(got) != 1 || got[0].ClientSecret != "" {
		t.Fatalf("stored = %+v, want empty secret", got)
	}

	// A client that sends a secret anyway (typed, or the env value echoed) is ignored.
	body = `[{"id":"keycloak","name":"KC","issuer":"https://kc.example.com","client_id":"abc","client_secret":"env-secret-value","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, _ := settings.Get(context.Background(), SettingOIDCProviders)
	if strings.Contains(raw.Value, "env-secret-value") {
		t.Fatalf("env secret persisted: %s", raw.Value)
	}
}

func TestSetProviders_EnvSecretLeavesStoredValueAlone(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)
	body := `[{"id":"kc","name":"KC","issuer":"https://kc.example.com","client_id":"abc","client_secret":"old-stored","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d", rec.Code)
	}

	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "env-secret-value")
	// An issuer change with no secret is normally refused; with the env secret it is fine.
	body = `[{"id":"kc","name":"KC","issuer":"https://kc2.example.com","client_id":"abc","client_secret":"typed","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := storedProviders(t, settings)
	if len(got) != 1 || got[0].ClientSecret != "old-stored" || got[0].Issuer != "https://kc2.example.com" {
		t.Fatalf("stored = %+v, want old-stored kept and new issuer", got)
	}
}

// Without the variable the existing write-only rule is unchanged.
func TestSetProviders_NoEnvStillRequiresSecretForNewProvider(t *testing.T) {
	h, _ := newOIDCSetProvidersFixture(t)
	body := `[{"id":"kc","name":"KC","issuer":"https://kc.example.com","client_id":"abc","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// The list says a secret comes from the environment and names the variable, to
// admins only, and never carries the secret value to anyone.
func TestGetProviders_EnvSecretFlagAndRedaction(t *testing.T) {
	t.Setenv("BINDERY_OIDC_CORP_CLIENT_SECRET", "env-only-secret-9f3")
	for _, role := range []string{"", "user", "admin"} {
		rec, got := getProvidersAs(t, role)
		body := rec.Body.String()
		for _, leak := range []string{"env-only-secret-9f3", "s3cr3t-value"} {
			if strings.Contains(body, leak) {
				t.Errorf("role %q: response contains secret %q: %s", role, leak, body)
			}
		}
		if role == "admin" {
			if !got[0].ClientSecretFromEnv || got[0].ClientSecretEnvVar != "BINDERY_OIDC_CORP_CLIENT_SECRET" {
				t.Errorf("admin view: flag/var = %v %q", got[0].ClientSecretFromEnv, got[0].ClientSecretEnvVar)
			}
		} else if strings.Contains(body, "client_secret") {
			t.Errorf("role %q: non admin response mentions client_secret: %s", role, body)
		}
	}
}

func TestGetProviders_AdminFlagFalseWithoutEnv(t *testing.T) {
	_, got := getProvidersAs(t, "admin")
	if got[0].ClientSecretFromEnv {
		t.Fatal("flag must be false when the variable is unset")
	}
}
