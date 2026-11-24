package oidc

import "testing"

func TestEnvIssuerVar(t *testing.T) {
	if got := EnvIssuerVar("my-idp"); got != "BINDERY_OIDC_MY_IDP_ISSUER" {
		t.Fatalf("got %q", got)
	}
	if EnvIssuerVar("kıdp") == EnvIssuerVar("kidp") {
		t.Fatal("non ASCII folding must not collide")
	}
}

// The env secret is only handed to the issuer it is bound to.
func TestOAuth2Config_EnvSecretBoundToIssuer(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "from-env")
	t.Setenv("BINDERY_OIDC_KC_ISSUER", "https://kc.example.com/realms/home")

	cfg := func(issuer, stored string) string {
		e := &entry{cfg: ProviderConfig{ID: "kc", ClientID: "c", Issuer: issuer, ClientSecret: stored}}
		return e.oauth2Config("https://b.example").ClientSecret
	}
	cases := []struct{ name, issuer, want string }{
		{"exact", "https://kc.example.com/realms/home", "from-env"},
		{"trailing slash on config", "https://kc.example.com/realms/home/", "from-env"},
		{"whitespace on config", "  https://kc.example.com/realms/home ", "from-env"},
		{"other issuer", "https://evil.example.com", ""},
		{"case differs", "https://KC.example.com/realms/home", ""},
		{"empty issuer", "", ""},
	}
	for _, c := range cases {
		if got := cfg(c.issuer, ""); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// On mismatch the stored value is what remains, never the env value.
	if got := cfg("https://evil.example.com", "stored"); got != "stored" {
		t.Errorf("mismatch with stored: got %q", got)
	}
}

func TestOAuth2Config_BoundWithTrailingSlashVariable(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "from-env")
	t.Setenv("BINDERY_OIDC_KC_ISSUER", " https://kc.example.com/ ")
	e := &entry{cfg: ProviderConfig{ID: "kc", Issuer: "https://kc.example.com"}}
	if got := e.oauth2Config("https://b.example").ClientSecret; got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

// Without the variable nothing is bound: any issuer gets the env secret.
func TestOAuth2Config_UnboundKeepsEnvSecretForAnyIssuer(t *testing.T) {
	t.Setenv("BINDERY_OIDC_KC_CLIENT_SECRET", "from-env")
	e := &entry{cfg: ProviderConfig{ID: "kc", Issuer: "https://anything.example"}}
	if got := e.oauth2Config("https://b.example").ClientSecret; got != "from-env" {
		t.Fatalf("got %q", got)
	}
	if IssuerBound("kc") {
		t.Fatal("must not be bound")
	}
}
