# Security Policy

Bindery is an open-source automation tool that holds API keys, reaches LAN
services, and writes to the local filesystem. We take reports seriously.

## Supported versions

Only the latest minor release receives security fixes. Older minors do not.

| Version | Supported |
| ------- | --------- |
| 0.12.x  | Yes       |
| < 0.12  | No        |

## Reporting a vulnerability

**Do not open a public issue.** Use one of:

1. **GitHub Security Advisory** (preferred) —
   [github.com/vavallee/bindery/security/advisories/new](https://github.com/vavallee/bindery/security/advisories/new).
   This creates a private thread with the maintainers.
2. Email the maintainer listed in the project `AUTHORS` / commit metadata.

Please include:

- A description of the issue and its impact.
- Steps to reproduce (PoC welcome).
- The commit SHA or release tag you tested.
- Any relevant configuration (Kubernetes / Docker / bare metal).

## Disclosure timeline

- **Acknowledgement**: within 7 days.
- **Initial assessment**: within 14 days.
- **Fix target**: 90-day coordinated disclosure window. If the bug is actively
  exploited or trivially weaponized, we may cut the window shorter.
- **Credit**: by default we credit reporters in the release notes. If you
  prefer to remain anonymous, say so in the initial report.

There is no bug bounty. What we can offer is credit and a timely fix.

## Scope

**In scope:**

- Authentication / authorization bypass.
- SSRF, SQL injection, path traversal, RCE, XSS, CSRF.
- Information disclosure from the API, logs, or container image.
- Privilege escalation inside the container / pod.
- Supply-chain concerns in the published Docker image, Helm chart, or
  release binaries.

**Out of scope:**

- Attacks requiring a compromised host, reverse proxy, or upstream metadata
  provider (OpenLibrary / Google Books / Hardcover).
- DoS via resource exhaustion against a single homelab instance (defenses
  live at the ingress layer, not the app).
- Self-XSS or attacks that require the victim to paste attacker-controlled
  content into their own admin UI.
- Social engineering, phishing, or physical attacks.
- Findings that only affect code paths behind feature flags disabled by
  default.

See the
[wiki Security page](https://github.com/vavallee/bindery/wiki/Security)
for the full threat model and a summary of built-in controls (SSRF guards,
CSP, cookie Secure auto-detect, container hardening, CI scans).

## Security controls currently in place

- **Authentication**: API key + signed session cookie + bcrypt passwords +
  per-IP login rate limit.
- **SSRF**: outbound URLs for webhooks, indexers, and download clients pass
  through `internal/httpsec.ValidateOutboundURL` with policy-based blocking
  of loopback, link-local, cloud-metadata endpoints, and (for webhooks)
  RFC1918 ranges. DNS results are verified to defeat rebinding.
- **Transport**: session cookies auto-flip to `Secure` behind TLS / the
  `X-Forwarded-Proto: https` header. HSTS is emitted only when TLS is
  actually in play.
- **Headers**: CSP (no `unsafe-eval`, no remote script origins),
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: strict-origin-when-cross-origin`.
- **Container**: distroless base, non-root UID, read-only root filesystem,
  all Linux capabilities dropped, RuntimeDefault seccomp, digest-pinned
  base images.
- **Kubernetes**: dedicated ServiceAccount with token automount disabled,
  API key sourced from a Secret, optional NetworkPolicy.
- **CI**: gosec, govulncheck, semgrep, gitleaks, ESLint security, Trivy,
  Grype, Dockle, Syft SBOM, ZAP baseline, OpenSSF Scorecard, helm-unittest
  — SARIF uploaded to the Security tab; weekly rerun on a cron.
- **Supply chain**: SLSA build provenance via
  `actions/attest-build-provenance`, verifiable with
  `gh attestation verify`.

## Secrets

Never file a public issue containing a session cookie, API key, or bcrypt
hash from a real deployment. If you need to share one to reproduce a bug,
use the private advisory channel.
