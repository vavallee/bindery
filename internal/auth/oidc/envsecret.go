package oidc

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// envName returns BINDERY_OIDC_<ID>_<suffix>, where <ID> is the provider id
// upper-cased with every character outside A-Z and 0-9 replaced by an
// underscore. ASCII only on purpose: strings.ToUpper folds some non-ASCII
// letters to ASCII (for example the dotless i), which would let two different
// ids share a variable.
func envName(providerID, suffix string) string {
	var b strings.Builder
	b.WriteString("BINDERY_OIDC_")
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
	b.WriteString(suffix)
	return b.String()
}

// EnvClientSecretVar returns the environment variable that supplies a
// provider's client secret: BINDERY_OIDC_<ID>_CLIENT_SECRET. Two ids that
// normalise to the same name (for example "my-idp" and "my_idp") would share
// one variable, so SetProviders refuses to save such a set; otherwise a
// provider pointed at another issuer could be handed this provider's secret.
// The _CLIENT_SECRET suffix keeps these names clear of the other
// BINDERY_OIDC_* settings such as BINDERY_OIDC_EMAIL_LINK.
func EnvClientSecretVar(providerID string) string {
	return envName(providerID, "_CLIENT_SECRET")
}

// EnvIssuerVar returns the optional variable that binds the environment
// secret to one issuer: BINDERY_OIDC_<ID>_ISSUER.
func EnvIssuerVar(providerID string) string {
	return envName(providerID, "_ISSUER")
}

// EnvBoundIssuer returns the issuer the environment secret is bound to, or ""
// when the binding variable is unset or blank.
func EnvBoundIssuer(providerID string) string {
	return strings.TrimSpace(os.Getenv(EnvIssuerVar(providerID)))
}

// IssuerBound reports whether the provider's secret is bound to an issuer.
func IssuerBound(providerID string) bool {
	return EnvBoundIssuer(providerID) != ""
}

// normalizeIssuer trims whitespace and one trailing slash. The rest of the
// comparison is exact and case sensitive.
func normalizeIssuer(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), "/")
}

// IssuerMatchesBound reports whether issuer equals the issuer the provider's
// secret is bound to. When no binding is set every issuer matches.
func IssuerMatchesBound(providerID, issuer string) bool {
	bound := EnvBoundIssuer(providerID)
	if bound == "" {
		return true
	}
	return normalizeIssuer(issuer) == normalizeIssuer(bound)
}

// EnvClientSecret returns the client secret supplied through the environment
// for the provider id, or "" when the variable is unset or empty. It does not
// check the issuer binding; see EffectiveClientSecret.
func EnvClientSecret(providerID string) string {
	return os.Getenv(EnvClientSecretVar(providerID))
}

// ClientSecretFromEnv reports whether the provider's secret variable is set.
func ClientSecretFromEnv(providerID string) bool {
	return EnvClientSecret(providerID) != ""
}

var warnedMismatch sync.Map

// EffectiveClientSecret returns the secret to use for a provider: the
// environment value when it is non-empty and, if the provider's secret is
// bound to an issuer, the configured issuer matches it; otherwise the stored
// one. A mismatch is logged once per provider and variable (names only).
func EffectiveClientSecret(providerID, issuer, stored string) string {
	v := EnvClientSecret(providerID)
	if v == "" {
		return stored
	}
	if !IssuerMatchesBound(providerID, issuer) {
		key := providerID + "\x00" + EnvIssuerVar(providerID)
		if _, seen := warnedMismatch.LoadOrStore(key, true); !seen {
			slog.Warn("oidc: provider issuer does not match the bound issuer variable, so the client secret from the environment is not used",
				"provider", providerID, "variable", EnvIssuerVar(providerID), "secret_variable", EnvClientSecretVar(providerID))
		}
		return stored
	}
	return v
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
