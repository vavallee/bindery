package oidc

import (
	"log/slog"
	"os"
	"strings"
)

// EnvClientSecretVar returns the environment variable that supplies a
// provider's client secret: BINDERY_OIDC_<ID>_CLIENT_SECRET, where <ID> is the
// provider id upper-cased with every character outside A-Z and 0-9 replaced
// by an underscore. Two ids that normalise to the same name (for example
// "my-idp" and "my_idp") share one variable; WarnEnvSecretCollisions reports
// that. The _CLIENT_SECRET suffix keeps these names clear of the other
// BINDERY_OIDC_* settings such as BINDERY_OIDC_EMAIL_LINK.
func EnvClientSecretVar(providerID string) string {
	var b strings.Builder
	b.WriteString("BINDERY_OIDC_")
	for _, r := range strings.ToUpper(providerID) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
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

// WarnEnvSecretCollisions logs a warning for every group of provider ids that
// map to the same environment variable name. It does not pick a winner: the
// ids simply share the variable.
func WarnEnvSecretCollisions(cfgs []ProviderConfig) {
	byVar := make(map[string][]string, len(cfgs))
	for _, c := range cfgs {
		v := EnvClientSecretVar(c.ID)
		byVar[v] = append(byVar[v], c.ID)
	}
	for v, ids := range byVar {
		if len(ids) > 1 {
			slog.Warn("oidc: provider ids map to the same client secret environment variable; they share it",
				"variable", v, "providers", ids)
		}
	}
}
