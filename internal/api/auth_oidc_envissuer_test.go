package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func putProviders(t *testing.T, h *OIDCHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))
	return rec
}

const boundIssuer = "https://kc.example.com/realms/home"

func seedKC(t *testing.T, h *OIDCHandler) {
	t.Helper()
	body := `[{"id":"kc","name":"KC","issuer":"` + boundIssuer + `","client_id":"abc","scopes":["openid"]}]`
	if rec := putProviders(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetProviders_BoundIssuerAcceptsTheBoundIssuer(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "env-secret-value")
	t.Setenv("BINDERY_OIDC_KC_ISSUER", boundIssuer)
	h, settings := newOIDCSetProvidersFixture(t)
	seedKC(t, h)
	// A trailing slash is the same issuer.
	body := `[{"id":"kc","name":"KC","issuer":"` + boundIssuer + `/","client_id":"abc","scopes":["openid"]}]`
	if rec := putProviders(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := storedProviders(t, settings); len(got) != 1 {
		t.Fatalf("stored %+v", got)
	}
}

func TestSetProviders_BoundIssuerRejectsOtherIssuers(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "env-secret-value")
	t.Setenv("BINDERY_OIDC_KC_ISSUER", boundIssuer)
	evil := "https://evil.example.com"

	t.Run("issuer edit", func(t *testing.T) {
		h, settings := newOIDCSetProvidersFixture(t)
		seedKC(t, h)
		body := `[{"id":"kc","name":"KC","issuer":"` + evil + `","client_id":"abc","scopes":["openid"]}]`
		rec := putProviders(t, h, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "BINDERY_OIDC_KC_ISSUER") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := storedProviders(t, settings); len(got) != 1 || got[0].Issuer != boundIssuer {
			t.Fatalf("stored %+v", got)
		}
	})

	t.Run("delete then add with another issuer", func(t *testing.T) {
		h, settings := newOIDCSetProvidersFixture(t)
		seedKC(t, h)
		if rec := putProviders(t, h, `[]`); rec.Code != http.StatusOK {
			t.Fatalf("delete status=%d", rec.Code)
		}
		body := `[{"id":"kc","name":"KC","issuer":"` + evil + `","client_id":"abc","scopes":["openid"]}]`
		rec := putProviders(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := storedProviders(t, settings); len(got) != 0 {
			t.Fatalf("stored %+v", got)
		}
	})

	t.Run("new provider with the bound id", func(t *testing.T) {
		h, _ := newOIDCSetProvidersFixture(t)
		body := `[{"id":"kc","name":"KC","issuer":"` + evil + `","client_id":"abc","scopes":["openid"]}]`
		if rec := putProviders(t, h, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

// With no ISSUER variable the secret is not bound, so behaviour is unchanged.
func TestSetProviders_UnboundIssuerChangeStillAllowed(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "env-secret-value")
	h, _ := newOIDCSetProvidersFixture(t)
	seedKC(t, h)
	body := `[{"id":"kc","name":"KC","issuer":"https://other.example.com","client_id":"abc","scopes":["openid"]}]`
	if rec := putProviders(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Response flags: admin sees them, the public and non admin views carry
// neither the flags nor any variable name or secret.
func TestGetProviders_IssuerBindingFlags(t *testing.T) {
	t.Setenv("BINDERY_OIDC_CORP_CLIENT_SECRET", "env-only-secret-9f3")
	t.Setenv("BINDERY_OIDC_CORP_ISSUER", "https://idp.internal.invalid")
	for _, role := range []string{"", "user", "admin"} {
		rec, got := getProvidersAs(t, role)
		body := rec.Body.String()
		if strings.Contains(body, "env-only-secret-9f3") {
			t.Errorf("role %q leaked the secret: %s", role, body)
		}
		if role == "admin" {
			if !got[0].ClientSecretIssuerBound || got[0].ClientSecretIssuerEnvVar != "BINDERY_OIDC_CORP_ISSUER" {
				t.Errorf("admin flags = %v %q", got[0].ClientSecretIssuerBound, got[0].ClientSecretIssuerEnvVar)
			}
			continue
		}
		for _, leak := range []string{"issuer_bound", "issuer_env_var", "BINDERY_OIDC_CORP"} {
			if strings.Contains(body, leak) {
				t.Errorf("role %q: response contains %q: %s", role, leak, body)
			}
		}
	}
}

func TestGetProviders_IssuerNotBoundByDefault(t *testing.T) {
	_, got := getProvidersAs(t, "admin")
	if got[0].ClientSecretIssuerBound {
		t.Fatal("must not be bound without the variable")
	}
}
