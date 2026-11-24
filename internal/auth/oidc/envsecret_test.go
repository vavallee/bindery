package oidc

import "testing"

func TestEnvClientSecretVar(t *testing.T) {
	cases := map[string]string{
		"keycloak":     "BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET",
		"my-idp":       "BINDERY_OIDC_MY_IDP_CLIENT_SECRET",
		"my_idp":       "BINDERY_OIDC_MY_IDP_CLIENT_SECRET",
		"Auth.entik 2": "BINDERY_OIDC_AUTH_ENTIK_2_CLIENT_SECRET",
		"idp9":         "BINDERY_OIDC_IDP9_CLIENT_SECRET",
		"é-idp":        "BINDERY_OIDC___IDP_CLIENT_SECRET",
		"":             "BINDERY_OIDC__CLIENT_SECRET",
		// Cannot collide with the other BINDERY_OIDC_* settings: the suffix
		// is always _CLIENT_SECRET.
		"email_link": "BINDERY_OIDC_EMAIL_LINK_CLIENT_SECRET",
	}
	for id, want := range cases {
		if got := EnvClientSecretVar(id); got != want {
			t.Errorf("EnvClientSecretVar(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestEffectiveClientSecret_EnvWinsWhenNonEmpty(t *testing.T) {
	if got := EffectiveClientSecret("kc", "stored"); got != "stored" {
		t.Fatalf("unset env: got %q, want stored", got)
	}
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "")
	if got := EffectiveClientSecret("kc", "stored"); got != "stored" {
		t.Fatalf("empty env must not override: got %q", got)
	}
	if ClientSecretFromEnv("kc") {
		t.Fatal("empty env must not count as set")
	}
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "from-env")
	if got := EffectiveClientSecret("kc", "stored"); got != "from-env" {
		t.Fatalf("env set: got %q, want from-env", got)
	}
	if !ClientSecretFromEnv("kc") {
		t.Fatal("expected ClientSecretFromEnv true")
	}
}

// The secret handed to the OAuth2 client (login redirect and code exchange
// both build it here) is the environment value, not the stored one.
func TestOAuth2Config_UsesEnvSecret(t *testing.T) {
	e := &entry{cfg: ProviderConfig{ID: "kc", ClientID: "c", ClientSecret: "stored"}}
	if got := e.oauth2Config("https://b.example").ClientSecret; got != "stored" {
		t.Fatalf("no env: got %q", got)
	}
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "from-env")
	if got := e.oauth2Config("https://b.example").ClientSecret; got != "from-env" {
		t.Fatalf("env set: got %q, want from-env", got)
	}
	// A provider with no stored secret at all still gets the env value.
	e2 := &entry{cfg: ProviderConfig{ID: "kc", ClientID: "c"}}
	if got := e2.oauth2Config("https://b.example").ClientSecret; got != "from-env" {
		t.Fatalf("empty stored: got %q", got)
	}
}

func TestEnvClientSecretVar_NonASCIIFoldingDoesNotCollide(t *testing.T) {
	if EnvClientSecretVar("k\u0131dp") == EnvClientSecretVar("kidp") {
		t.Fatal("dotless i must not map onto i")
	}
	if EnvClientSecretVar("\u017fso") == EnvClientSecretVar("sso") {
		t.Fatal("long s must not map onto s")
	}
}

func TestEnvClientSecretVarUsable(t *testing.T) {
	for id, want := range map[string]bool{"kc": true, "-1-": true, "-": false, "__": false, "": false, "\u00e9": false} {
		if got := EnvClientSecretVarUsable(id); got != want {
			t.Errorf("usable(%q) = %v, want %v", id, got, want)
		}
	}
}
