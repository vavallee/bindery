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
	// Same issuer: the stored value is left alone and the typed one ignored.
	body = `[{"id":"kc","name":"KC2","issuer":"https://kc.example.com","client_id":"abc","client_secret":"typed","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := storedProviders(t, settings)
	if len(got) != 1 || got[0].ClientSecret != "old-stored" {
		t.Fatalf("stored = %+v, want old-stored kept", got)
	}
}

// The stored secret belongs to the old issuer. With the variable set the
// issuer guard is waived, so the stored secret must be cleared instead of
// surviving to be used against the new issuer once the variable is removed.
func TestSetProviders_EnvSecretIssuerChangeClearsStoredSecret(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)
	body := `[{"id":"kc","name":"KC","issuer":"https://kc.example.com","client_id":"abc","client_secret":"old-stored","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d", rec.Code)
	}

	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "env-secret-value")
	body = `[{"id":"kc","name":"KC","issuer":"https://kc2.example.com","client_id":"abc","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := storedProviders(t, settings)
	if len(got) != 1 || got[0].ClientSecret != "" || got[0].Issuer != "https://kc2.example.com" {
		t.Fatalf("stored = %+v, want empty secret and new issuer", got)
	}
}

// Ids that map to one variable must never be saved together, or a look-alike
// provider pointed at another issuer would be sent the real secret.
func TestSetProviders_RejectsIDsSharingAnEnvVariable(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET", "env-secret-value")
	cases := map[string]string{
		"case":      `[{"id":"keycloak","name":"A","issuer":"https://kc.example.com","client_id":"a","client_secret":"s","scopes":["openid"]},{"id":"KEYCLOAK","name":"B","issuer":"https://evil.example","client_id":"b","scopes":["openid"]}]`,
		"separator": `[{"id":"my-idp","name":"A","issuer":"https://a.example.com","client_id":"a","client_secret":"s","scopes":["openid"]},{"id":"my_idp","name":"B","issuer":"https://b.example.com","client_id":"b","client_secret":"s","scopes":["openid"]}]`,
		"dotless-i": `[{"id":"kidp","name":"A","issuer":"https://a.example.com","client_id":"a","client_secret":"s","scopes":["openid"]},{"id":"kıdp","name":"B","issuer":"https://b.example.com","client_id":"b","client_secret":"s","scopes":["openid"]}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h, settings := newOIDCSetProvidersFixture(t)
			rec := httptest.NewRecorder()
			h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			// "case" and "dotless-i" are stopped by the id rule, "separator"
			// is a true variable collision between two valid ids.
			if name == "separator" && !strings.Contains(rec.Body.String(), "share") {
				t.Fatalf("want a shared variable error, got %s", rec.Body.String())
			}
			if got := storedProviders(t, settings); len(got) != 0 {
				t.Fatalf("nothing may be stored, got %+v", got)
			}
		})
	}
}

func TestSetProviders_RejectsIDWithNoLettersOrDigits(t *testing.T) {
	h, _ := newOIDCSetProvidersFixture(t)
	body := `[{"id":"--","name":"A","issuer":"https://a.example.com","client_id":"a","client_secret":"s","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "letter or digit") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
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
