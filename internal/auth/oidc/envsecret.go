package oidc

import (
	"os"
	"strings"
)

// EnvClientSecretVar returns the environment variable that supplies a
// provider's client secret: BINDERY_OIDC_<ID>_CLIENT_SECRET, where <ID> is the
// provider id upper-cased with every character outside A-Z and 0-9 replaced
// by an underscore. Two ids that normalise to the same name (for example
// "my-idp" and "my_idp") would share one variable, so SetProviders refuses to
// save such a set; otherwise a provider pointed at another issuer could be
// handed this provider's secret. The _CLIENT_SECRET suffix keeps these names clear of the other
// BINDERY_OIDC_* settings such as BINDERY_OIDC_EMAIL_LINK.
func EnvClientSecretVar(providerID string) string {
	var b strings.Builder
	b.WriteString("BINDERY_OIDC_")
	// ASCII only on purpose: strings.ToUpper folds some non-ASCII letters to
	// ASCII (for example the dotless i), which would let two different ids
	// share a variable.
	for _, r := range providerID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_CLIENT_SECRET")
	return b.String()
}

// EnvClientSecret returns the client secret supplied through the environment
// for the provider id, or "" when the variable is unset or empty.
func EnvClientSecret(providerID string) string {
	return os.Getenv(EnvClientSecretVar(providerID))
}

// ClientSecretFromEnv reports whether the provider's secret comes from the
// environment.
func ClientSecretFromEnv(providerID string) bool {
	return EnvClientSecret(providerID) != ""
}

// EffectiveClientSecret returns the secret to use for a provider: the
// environment value when non-empty, otherwise the stored one.
func EffectiveClientSecret(providerID, stored string) string {
	if v := EnvClientSecret(providerID); v != "" {
		return v
	}
	return stored
}

// EnvClientSecretVarUsable reports whether the id has at least one A-Z, a-z or
// 0-9 character, so its variable name is not the degenerate empty one.
func EnvClientSecretVarUsable(providerID string) bool {
	for _, r := range providerID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return true
		}
	}
	return false
}
