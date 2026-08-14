# Privacy Policy

Last updated: 2026-08-14.

This covers the **telemetry ping** Bindery sends to `api.getbindery.dev`. It is the only data Bindery sends anywhere that you did not configure yourself.

Everything else Bindery talks to — indexers, download clients, metadata providers, notification targets — you configure, and the data goes to services you chose. Bindery ships with none of them and adds no intermediary.

## Who is responsible

Bindery is maintained by a single individual (GitHub: [@vavallee](https://github.com/vavallee)), who operates `api.getbindery.dev` and is the data controller for the telemetry described here. Contact: open an issue at [github.com/vavallee/bindery/issues](https://github.com/vavallee/bindery/issues), or for anything you would rather not post publicly, the process in [SECURITY.md](SECURITY.md).

## What is sent

Once per day, a release build sends:

| Field | Content |
|---|---|
| `install_id` | A random UUID generated on first run. Not derived from anything about you, your machine, or your library. |
| `version`, `os`, `arch`, `deploy` | Binary version, operating system, CPU architecture, and deploy method (kubernetes / docker / binary). |
| `features` | Counts and booleans for which subsystems are configured — number of indexers, download clients, notifications, users; whether Calibre, Audiobookshelf, Grimmory, OIDC, multi-user, or a Hardcover token are in use. Never names, URLs, credentials, or values. |
| `errors` | The number of ERROR/WARN log entries in the last 24 hours, plus the five most frequent error messages. These are the fixed, developer-written message strings only, truncated to 120 characters. Log attributes — titles, paths, URLs, usernames — are stripped and never sent. |

**Not sent:** hostnames, IP addresses in the payload, library contents, author or book names, file paths, indexer or download client names or URLs, credentials, or anything identifying you or your machine.

The response contains the latest published version, which powers the in-app update badge.

## IP addresses

Receiving an HTTP request necessarily reveals the sender's IP address, and under GDPR Article 4 an IP address is personal data. Bindery's telemetry server:

- uses the IP **only** to enforce a per-IP rate limit on the ping endpoint;
- **never stores it** — no database table has an IP column;
- **never logs it**.

It exists in memory for the duration of the request and in the rate limiter's short-lived state. It is not associated with the `install_id` or with anything else.

## Legal basis (GDPR)

The processing relies on **legitimate interest** (Article 6(1)(f)): counting active installations, understanding which platforms and versions are in use, and spotting widespread breakage in a project maintained by one person. The data is pseudonymous by construction — a random UUID with no path back to an individual — and the balancing test favours processing because the impact on any individual is negligible and the alternative is maintaining the project blind.

If you would rather not participate, opting out is unconditional and takes one setting (below).

## How long it is kept

| Data | Retention |
|---|---|
| Install rows (`install_id`, version, os, arch, deploy, last seen) | **60 days** after the last ping, then deleted |
| Daily activity ledger | **400 days** |
| Daily aggregates (totals per day, per version, per feature) | Kept indefinitely — these contain no per-install rows, only counts |
| IP addresses | Not retained |

An install that stops pinging disappears from the install table within 60 days. What remains is counts.

## How to opt out

Either of these disables the entire ping:

- **Settings → General** → turn off telemetry (`telemetry.enabled: false`), or
- set `BINDERY_TELEMETRY_DISABLED=true` in the environment — this works **before first run**, so nothing is ever sent.

Both switches disable everything, including the error counters and the update badge. A disabled ping means the app has no way to learn that a newer version exists.

Non-release builds do not send telemetry at all.

## Your rights

Under GDPR you may request access to, correction of, or erasure of personal data about you, and may object to processing based on legitimate interest.

Practically: the data is pseudonymous, and the maintainer has no way to connect an `install_id` to a person. If you want your install's row removed, opt out and it is deleted within 60 days automatically — or open an issue with your `install_id` (visible in Settings → About) and it will be deleted on request.

## Third parties

Telemetry is not shared with anyone, sold, or used for advertising. There are no analytics SDKs, no trackers, and no third-party services in the telemetry path. The aggregate dashboard at `/stats` is public and contains only counts.

## Verifying this

Every claim here is checkable in the source:

- client — [`internal/telemetry/client.go`](internal/telemetry/client.go)
- server — [`cmd/telemetry-server/main.go`](cmd/telemetry-server/main.go)

The retention windows are the `retentionWindow` and `ledgerRetentionWindow` constants; the IP handling is `realIP` and `limiter.allow` in the ping handler.

If a future release changes any of this, the change lands here before it ships.
