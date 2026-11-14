package main

import (
	"net/http"
)

func (s *server) handleHome(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    display: flex; align-items: center; justify-content: center;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  }
  .card {
    text-align: center;
    padding: 3rem 2rem;
    max-width: 520px;
  }
  img { width: 96px; height: 96px; margin-bottom: 1.5rem; }
  h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.02em; margin-bottom: .5rem; }
  p  { color: #94a3b8; line-height: 1.7; margin-bottom: 2rem; }
  .features {
    display: flex; flex-direction: column; gap: .5rem;
    margin-bottom: 2rem;
    text-align: left;
  }
  .feature {
    display: flex; align-items: flex-start; gap: .65rem;
    color: #94a3b8; font-size: .9rem; line-height: 1.5;
  }
  .feature-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: #10b981; flex-shrink: 0; margin-top: .45rem;
  }
  .links { display: flex; flex-wrap: wrap; gap: .75rem; justify-content: center; }
  a {
    display: inline-flex; align-items: center; gap: .5rem;
    padding: .65rem 1.4rem;
    border-radius: 8px; text-decoration: none;
    font-weight: 600; font-size: .9rem;
    transition: background .15s, color .15s;
  }
  a.primary { background: #10b981; color: #fff; }
  a.primary:hover { background: #059669; }
  a.secondary {
    background: transparent; color: #94a3b8;
    border: 1px solid #334155;
  }
  a.secondary:hover { background: #1e293b; color: #e2e8f0; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .85em; background: #1e293b; color: #cbd5e1; padding: .1em .35em; border-radius: 4px; }
</style>
</head>
<body>
<div class="card">
  <img src="https://raw.githubusercontent.com/vavallee/bindery/main/.github/assets/logo.png" alt="Bindery logo">
  <h1>Bindery</h1>
  <p><strong style="color:#e2e8f0">The Readarr replacement built to outlive its metadata sources.</strong><br>
     Automated ebook &amp; audiobook management for Usenet &amp; torrents — monitor
     authors, search indexers, download, and organize.</p>
  <div class="features">
    <div class="feature"><div class="feature-dot"></div><span>No single point of failure for metadata — OpenLibrary, Google Books, Hardcover, DNB, Audnex, and Audible. Documented public APIs, zero scraping.</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Bring your dead Readarr install with you — import <code>readarr.db</code> and your authors, indexers, download clients, and blocklist come across in one step.</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Ebooks and audiobooks in independent slots — separate roots, narrator metadata, and multi-part audiobook handling.</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Usenet and torrents — SABnzbd, NZBGet, qBittorrent, Transmission, Deluge, and rTorrent.</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Boring to run, by design — a single Go binary, SQLite, distroless image, Helm chart, ARM down to a Pi Zero.</span></div>
  </div>
  <div class="links">
    <a class="primary" href="https://github.com/vavallee/bindery">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor">
        <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z"/>
      </svg>
      View on GitHub
    </a>
    <a class="secondary" href="https://github.com/vavallee/bindery-plugins">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/>
        <path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/>
      </svg>
      Calibre Plugin
    </a>
    <a class="secondary" href="/stats">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <line x1="18" y1="20" x2="18" y2="10"/>
        <line x1="12" y1="20" x2="12" y2="4"/>
        <line x1="6" y1="20" x2="6" y2="14"/>
      </svg>
      Telemetry
    </a>
  </div>
</div>
</body>
</html>`))
}

// handleTelemetryFields renders the public schema doc: every field a
// Bindery install can send, with a one-line explanation of why we ask.
// This is the trust artifact users link to when deciding whether to leave
// telemetry on. The page lists fields by category (core, features, future)
// and includes the opt-out instructions in one place.
func (s *server) handleTelemetryFields(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(telemetryFieldsHTML))
}

// telemetryFieldsHTML is the static HTML for /telemetry-fields. Inline so
// the binary has no asset dependencies; the page is small and updates
// infrequently (only when ping schema gains a field).
const telemetryFieldsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bindery telemetry: what we send</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { min-height: 100svh; background: #0f1117; color: #e2e8f0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; padding: 3rem 1.5rem; }
  .container { max-width: 760px; margin: 0 auto; }
  a.back { color: #94a3b8; font-size: .85rem; text-decoration: none; }
  a.back:hover { color: #e2e8f0; }
  h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.02em; margin: .75rem 0 .5rem; }
  h2 { font-size: 1.1rem; font-weight: 600; color: #cbd5e1; margin: 2rem 0 .75rem; }
  p, li { line-height: 1.6; color: #cbd5e1; }
  p { margin-bottom: 1rem; }
  ul { padding-left: 1.5rem; margin-bottom: 1rem; }
  li { margin-bottom: .5rem; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; background: #1e293b; padding: .15rem .4rem; border-radius: 3px; font-size: .9em; color: #f1f5f9; }
  .field { margin-bottom: 1rem; padding: .85rem 1rem; background: #1e293b; border: 1px solid #334155; border-radius: 8px; }
  .field code { background: #0f1117; }
  .field .why { color: #94a3b8; font-size: .9rem; margin-top: .35rem; }
  footer { margin-top: 3rem; color: #64748b; font-size: .75rem; text-align: center; }
  .opt-out { background: #1e293b; border-left: 3px solid #10b981; padding: 1rem 1.25rem; border-radius: 4px; margin: 1.5rem 0; }
  .opt-out code { background: #0f1117; display: inline-block; margin: .25rem 0; }
</style>
</head>
<body>
<div class="container">
  <p><a class="back" href="/">&larr; Bindery</a></p>
  <h1>Telemetry: what we send</h1>

  <p>Every Bindery install sends one anonymous ping per day to <code>api.getbindery.dev/api/ping</code>. The full schema is below. Nothing else leaves your install via telemetry: no library contents, no book titles, no author names, no IPs, no hostnames, no usernames.</p>

  <p>This page is the source of truth. If a future release adds a field, it appears here first.</p>

  <h2>Core fields (always sent)</h2>

  <div class="field">
    <code>install_id</code>
    <div class="why">Random UUID generated on first run and stored in your Bindery database. Lets us count distinct installs without tracking who you are. Resetting Bindery's DB resets the UUID; we have no other way to link two installs together.</div>
  </div>

  <div class="field">
    <code>version</code>
    <div class="why">The Bindery binary's release tag (e.g. <code>1.15.2</code>). Lets us see which versions are actually deployed so we know when an old release can be deprecated. Non-release builds (dev / sha-XXXXX / commits past a tag) are dropped client-side and never sent.</div>
  </div>

  <div class="field">
    <code>os</code> / <code>arch</code>
    <div class="why">Output of Go's <code>runtime.GOOS</code> and <code>runtime.GOARCH</code> (e.g. <code>linux</code> / <code>amd64</code>). Lets us prioritise fixes for the OS/arch combos that are actually in use.</div>
  </div>

  <div class="field">
    <code>deploy</code>
    <div class="why">How Bindery is being run: <code>kubernetes</code> (inside a pod), <code>docker</code> (inside any other container), <code>binary</code> (bare metal), <code>helm</code> (Helm chart, when the chart sets <code>BINDERY_DEPLOY_METHOD=helm</code>), <code>unraid</code> (a container started by Unraid). Detected from <code>HOST_OS</code>, <code>KUBERNETES_SERVICE_HOST</code> and the presence of <code>/.dockerenv</code>. Helps us know which deployment paths are worth supporting.</div>
  </div>

  <h2>Features (sent starting v1.15.3+, optional)</h2>

  <p>Counts and booleans summarising which subsystems are configured. No names, IDs, URLs, or values are ever sent &mdash; just numbers and on/off flags. Tells the maintainer which features users actually use so support time goes to the parts of Bindery people rely on.</p>

  <div class="field">
    <code>features.indexers</code> / <code>features.download_clients</code> / <code>features.notifications</code> / <code>features.users</code>
    <div class="why">Integer count of enabled configuration rows. The user count tells us whether multi-user is in real use; the others tell us how many integrations a typical install has.</div>
  </div>

  <div class="field">
    <code>features.calibre_enabled</code> / <code>features.abs_enabled</code> / <code>features.grimmory_enabled</code>
    <div class="why">Boolean: whether the Calibre, Audiobookshelf, or Grimmory integration is turned on. No URLs, paths, or credentials.</div>
  </div>

  <div class="field">
    <code>features.hardcover_token</code>
    <div class="why">Boolean: whether a Hardcover API token is configured (presence, not the value).</div>
  </div>

  <div class="field">
    <code>features.oidc_enabled</code> / <code>features.multi_user</code>
    <div class="why">Booleans: whether OIDC sign-in is configured, and whether there is more than one user account in the local DB.</div>
  </div>

  <div class="field">
    <code>features.setup_indexer_day</code> / <code>features.setup_client_day</code> / <code>features.first_author_day</code> / <code>features.first_grab_day</code> / <code>features.first_import_day</code>
    <div class="why">Integers (v1.30.1+): whole days between install creation and the first time each milestone happened — first indexer added, first download client added, first author added, first grab, first import. 0 means "the same day"; a field is absent until its milestone happens. Never timestamps, dates, or times of day — only a day count. These exist because most installs that never finish initial setup are abandoned within a day, and a day-count is the least data that shows where setup stalls.</div>
  </div>

  <h2>What we don't collect</h2>

  <ul>
    <li>Your IP address (the server rate-limits by IP but never stores it).</li>
    <li>Your hostname, server name, domain, or any DNS-resolvable identifier.</li>
    <li>Anything about your library: book titles, author names, ISBNs, ratings, file paths.</li>
    <li>Anything about your network: indexer URLs, download client URLs, notification webhook URLs.</li>
    <li>Anything about your users: usernames, email addresses, OIDC subject IDs.</li>
    <li>API keys, tokens, passwords, session secrets.</li>
    <li>Request logs, error traces, or stack traces.</li>
  </ul>

  <h2>How to opt out</h2>

  <div class="opt-out">
    <p>Two ways to disable telemetry entirely. Either is sufficient; the env var wins if both are set.</p>
    <p><strong>Env var (recommended for fresh installs):</strong><br>
    <code>BINDERY_TELEMETRY_DISABLED=true</code></p>
    <p><strong>Settings DB (for running installs):</strong><br>
    Set the <code>telemetry.enabled</code> setting to <code>false</code>. Survives restarts; takes effect on the next scheduled ping (within 24 hours).</p>
  </div>

  <h2>How the data is stored</h2>

  <p>Pings land in a small SQLite database on a single VM in Hetzner Cloud (Falkenstein, Germany). Per-install rows whose <code>last_seen</code> is older than 60 days are deleted nightly. The dashboard's long-running charts (monthly new installs, version adoption over time) are computed from daily aggregate tables that hold only counts per day: how many new installs, how many were active, how many were on each version, how many had each subsystem enabled. The aggregate rows never contain individual install IDs, so they're kept indefinitely. The database itself is backed up daily to an off-site object store; backups follow the same 60-day retention on per-install rows.</p>

  <p>The aggregated dashboard at <a href="/stats">/stats</a> is rendered live from the database with no caching. The full source of both the bindery client and the telemetry server lives at <a href="https://github.com/vavallee/bindery">github.com/vavallee/bindery</a> &mdash; the client at <code>internal/telemetry/client.go</code>, the server at <code>cmd/telemetry-server/main.go</code>.</p>

  <footer>
    Last updated 2026-05-29. If a future release changes any of this, the change lands on this page before it ships.
  </footer>
</div>
</body>
</html>`
