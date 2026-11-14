package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// previewPageTemplate is the HTML/CSS/JS for /stats/preview. Filters render
// the <option selected> attribute server-side; Chart.js receives its data via
// a JSON <script type="application/json"> tag the inline JS reads on load.
//
// Token placeholders (replaced via strings.NewReplacer in handlePreviewPage):
//
//	{{HERO}}        — hero stats row HTML
//	{{COHORTS}}     — cohort table HTML
//	{{FILTERS}}     — filter form HTML
//	{{JSON}}        — JSON payload (already script-tag-safe)
//	{{GENERATED}}   — generated-at footer string
//	{{RANGELABEL}}  — human-readable range label, HTML-escaped
//
// Token-substitution avoids fighting fmt.Sprintf with all the `%` characters
// in the embedded CSS (width:100%) and JS (palette[i % palette.length]).
const previewPageTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery — Telemetry Preview</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    padding: 2.5rem 1.5rem;
  }
  .container { max-width: 1080px; margin: 0 auto; }
  header { margin-bottom: 1.75rem; display: flex; align-items: flex-end; justify-content: space-between; flex-wrap: wrap; gap: 1rem; }
  header .titleblock h1 { font-size: 1.85rem; font-weight: 700; letter-spacing: -0.02em; margin: .35rem 0 .15rem; display: flex; align-items: center; gap: .65rem; }
  header .badge { display: inline-block; font-size: .65rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase; padding: .2rem .5rem; border-radius: 999px; background: #1e293b; color: #10b981; border: 1px solid #334155; }
  header a.back { color: #94a3b8; font-size: .85rem; text-decoration: none; }
  header a.back:hover { color: #e2e8f0; }
  header .compare { color: #94a3b8; font-size: .8rem; }
  header .compare a { color: #10b981; text-decoration: none; }
  header .compare a:hover { text-decoration: underline; }

  form.filters { display: flex; gap: .75rem; flex-wrap: wrap; margin-bottom: 2rem; background: #1e293b; border: 1px solid #334155; padding: .85rem 1rem; border-radius: 10px; align-items: center; }
  form.filters label { color: #94a3b8; font-size: .78rem; text-transform: uppercase; letter-spacing: .08em; margin-right: .3rem; }
  form.filters select { background: #0f1117; color: #e2e8f0; border: 1px solid #334155; padding: .4rem .65rem; border-radius: 6px; font-size: .85rem; }
  form.filters .field { display: flex; align-items: center; gap: .35rem; }
  form.filters noscript button { background: #10b981; color: #fff; border: 0; padding: .4rem .85rem; border-radius: 6px; font-weight: 600; cursor: pointer; }

  .hero { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 2rem; }
  .hero .stat { padding: 1.1rem 1.3rem; background: #1e293b; border: 1px solid #334155; border-radius: 10px; }
  .hero .stat .num { font-size: 2rem; font-weight: 700; color: #10b981; line-height: 1; font-variant-numeric: tabular-nums; }
  .hero .stat .label { color: #94a3b8; font-size: .8rem; margin-top: .4rem; text-transform: uppercase; letter-spacing: .06em; }
  .hero .stat.delta .num.up { color: #10b981; }
  .hero .stat.delta .num.down { color: #ef4444; }
  .hero .stat.delta .sub { color: #94a3b8; font-size: .8rem; margin-top: .25rem; }

  section { margin-bottom: 2rem; }
  section > h2 { font-size: .78rem; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; color: #94a3b8; margin-bottom: .75rem; }
  .panel { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 1rem 1.1rem; }
  .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; }
  .grid-3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1rem; }

  canvas { width: 100% !important; height: auto !important; }
  .canvas-wrap { position: relative; height: 260px; }
  .canvas-wrap.tall { height: 320px; }
  .canvas-wrap.short { height: 220px; }

  table.cohorts { width: 100%; border-collapse: collapse; font-size: .85rem; }
  table.cohorts th { text-align: left; color: #94a3b8; font-weight: 600; padding: .45rem .65rem; border-bottom: 1px solid #334155; font-size: .72rem; text-transform: uppercase; letter-spacing: .08em; }
  table.cohorts td { padding: .45rem .65rem; color: #cbd5e1; font-variant-numeric: tabular-nums; border-bottom: 1px solid #1e293b; }
  table.cohorts td.pct { text-align: right; border-radius: 4px; font-weight: 600; color: #0f1117; }
  table.cohorts td.empty { color: #475569; text-align: right; }

  footer { margin-top: 2.5rem; color: #64748b; font-size: .75rem; text-align: center; line-height: 1.5; }

  @media (max-width: 720px) {
    .hero { grid-template-columns: 1fr 1fr; }
    .grid-2, .grid-3 { grid-template-columns: 1fr; }
    header { align-items: flex-start; }
  }
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="titleblock">
      <a class="back" href="/">← Bindery</a>
      <h1>Telemetry · Preview <span class="badge">beta</span></h1>
      <p class="compare">Richer charts and filters. <a href="/stats">Old dashboard →</a></p>
    </div>
  </header>

  {{FILTERS}}

  {{HERO}}

  <section>
    <h2>Daily activity ({{RANGELABEL}})</h2>
    <div class="panel"><div class="canvas-wrap tall"><canvas id="chart-active"></canvas></div></div>
  </section>

  <section>
    <h2>New installs per day</h2>
    <div class="panel"><div class="canvas-wrap"><canvas id="chart-new"></canvas></div></div>
  </section>

  <section>
    <h2>Version adoption (active installs per version, stacked)</h2>
    <div class="panel"><div class="canvas-wrap tall"><canvas id="chart-versions"></canvas></div></div>
  </section>

  <section>
    <h2>Distribution</h2>
    <div class="grid-3">
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-os"></canvas></div>
      </div>
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-arch"></canvas></div>
      </div>
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-deploy"></canvas></div>
      </div>
    </div>
  </section>

  <section>
    <h2>Install longevity</h2>
    <div class="panel"><div class="canvas-wrap short"><canvas id="chart-longevity"></canvas></div></div>
  </section>

  <section>
    <h2>Retention cohorts (% of week-N installs still pinging)</h2>
    <div class="panel">{{COHORTS}}</div>
  </section>

  <footer>
    Generated {{GENERATED}}. Data refreshes on every page load.<br>
    Anonymous install counts only — opaque install UUID, version, OS/arch, deploy method. No identifying information collected.
  </footer>
</div>

<script type="application/json" id="telemetry-data">{{JSON}}</script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.6/dist/chart.umd.min.js" integrity="sha384-Sse/HDqcypGpyTDpvZOJNnG0TT3feGQUkF9H+mnRvic+LjR+K1NhTt8f51KIQ3v3" crossorigin="anonymous"></script>
<script>
(function() {
  var raw = document.getElementById('telemetry-data').textContent;
  var data = JSON.parse(raw);
  var palette = ['#10b981','#3b82f6','#f59e0b','#a855f7','#ef4444','#06b6d4','#ec4899','#84cc16'];
  var otherColour = '#475569';
  var gridColour = '#334155';
  var tickColour = '#94a3b8';

  Chart.defaults.color = tickColour;
  Chart.defaults.borderColor = gridColour;
  Chart.defaults.font.family = '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';

  var axes = {
    x: { grid: { color: gridColour, drawOnChartArea: false }, ticks: { color: tickColour, maxRotation: 0, autoSkip: true, maxTicksLimit: 12 } },
    y: { grid: { color: gridColour }, ticks: { color: tickColour, precision: 0 }, beginAtZero: true }
  };

  // Daily active line.
  new Chart(document.getElementById('chart-active'), {
    type: 'line',
    data: {
      labels: data.labels,
      datasets: [{ label: 'Active', data: data.active, borderColor: '#10b981', backgroundColor: 'rgba(16,185,129,0.15)', fill: true, tension: 0.25, pointRadius: 0, borderWidth: 2 }]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });

  // New installs bar.
  new Chart(document.getElementById('chart-new'), {
    type: 'bar',
    data: { labels: data.labels, datasets: [{ label: 'New installs', data: data.new, backgroundColor: '#3b82f6' }] },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });

  // Stacked area for version adoption.
  var versionDatasets = [];
  data.top_versions.forEach(function(ver, i) {
    versionDatasets.push({
      label: ver,
      data: data.version_series[ver] || [],
      borderColor: palette[i % palette.length],
      backgroundColor: palette[i % palette.length],
      fill: true,
      pointRadius: 0,
      tension: 0.2,
      borderWidth: 1
    });
  });
  if (data.version_series['(other)']) {
    versionDatasets.push({
      label: '(other)',
      data: data.version_series['(other)'],
      borderColor: otherColour,
      backgroundColor: otherColour,
      fill: true,
      pointRadius: 0,
      tension: 0.2,
      borderWidth: 1
    });
  }
  new Chart(document.getElementById('chart-versions'), {
    type: 'line',
    data: { labels: data.labels, datasets: versionDatasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: { legend: { position: 'bottom', labels: { color: tickColour, boxWidth: 10 } } },
      scales: {
        x: axes.x,
        y: Object.assign({}, axes.y, { stacked: true })
      },
      interaction: { mode: 'index', intersect: false }
    }
  });

  function doughnut(canvasId, items) {
    if (!items || items.length === 0) return;
    var labels = items.map(function(x) { return x.label; });
    var counts = items.map(function(x) { return x.count; });
    var colours = items.map(function(_, i) { return palette[i % palette.length]; });
    new Chart(document.getElementById(canvasId), {
      type: 'doughnut',
      data: { labels: labels, datasets: [{ data: counts, backgroundColor: colours, borderColor: '#0f1117', borderWidth: 2 }] },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { position: 'bottom', labels: { color: tickColour, boxWidth: 10, padding: 8, font: { size: 11 } } }, title: { display: true, text: canvasId.replace('chart-', '').toUpperCase(), color: tickColour, font: { size: 11, weight: '600' } } }
      }
    });
  }
  doughnut('chart-os', data.os);
  doughnut('chart-arch', data.arch);
  doughnut('chart-deploy', data.deploy);

  // Longevity bar.
  new Chart(document.getElementById('chart-longevity'), {
    type: 'bar',
    data: {
      labels: data.longevity.map(function(x) { return x.label; }),
      datasets: [{ data: data.longevity.map(function(x) { return x.count; }), backgroundColor: ['#ef4444', '#f59e0b', '#3b82f6', '#10b981'] }]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });
})();
</script>
</body>
</html>`

// renderPreviewHero builds the 4 (or 3 when range=all) stat cards.
func renderPreviewHero(d *previewData) string {
	var sb strings.Builder
	sb.WriteString(`<section class="hero-row"><div class="hero">`)
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">Active (%s)</div></div>`,
		d.ActiveRange, html.EscapeString(d.RangeLabel))
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">Total installs</div></div>`,
		d.Total)
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">New (%s)</div></div>`,
		d.NewRange, html.EscapeString(d.RangeLabel))
	if d.ShowDelta {
		dir := "up"
		sign := "+"
		arrow := "↑"
		if d.DeltaActive < 0 {
			dir = "down"
			sign = "−"
			arrow = "↓"
		}
		// Use absolute values for display so the sign is rendered exactly once.
		abs := d.DeltaActive
		if abs < 0 {
			abs = -abs
		}
		absPct := d.DeltaActivePct
		if absPct < 0 {
			absPct = -absPct
		}
		fmt.Fprintf(&sb,
			`<div class="stat delta"><div class="num %s">%s%d</div><div class="sub">%s %.1f%% vs. previous %s</div></div>`,
			dir, sign, abs, arrow, absPct, html.EscapeString(d.RangeLabel))
	}
	sb.WriteString(`</div></section>`)
	return sb.String()
}

// renderPreviewFilters renders the three-dropdown filter form. Selected option
// is set from f. Form submits via GET so query params drive the page; a tiny
// noscript fallback lets users without JS still apply filters (everything is
// server-side anyway).
func renderPreviewFilters(f previewFilters) string {
	opt := func(value, label, selected string) string {
		sel := ""
		if value == selected {
			sel = " selected"
		}
		return fmt.Sprintf(`<option value="%s"%s>%s</option>`,
			html.EscapeString(value), sel, html.EscapeString(label))
	}
	var sb strings.Builder
	sb.WriteString(`<form class="filters" method="get" action="/stats/preview" onchange="this.submit()">`)
	sb.WriteString(`<div class="field"><label>Range</label><select name="range">`)
	sb.WriteString(opt("7d", "Last 7 days", f.Range))
	sb.WriteString(opt("30d", "Last 30 days", f.Range))
	sb.WriteString(opt("90d", "Last 90 days", f.Range))
	sb.WriteString(opt("all", "All time", f.Range))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<div class="field"><label>OS</label><select name="os">`)
	sb.WriteString(opt("all", "All", f.OS))
	sb.WriteString(opt("linux", "Linux", f.OS))
	sb.WriteString(opt("darwin", "macOS", f.OS))
	sb.WriteString(opt("windows", "Windows", f.OS))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<div class="field"><label>Deploy</label><select name="deploy">`)
	sb.WriteString(opt("all", "All", f.Deploy))
	sb.WriteString(opt("docker", "Docker", f.Deploy))
	sb.WriteString(opt("binary", "Binary", f.Deploy))
	sb.WriteString(opt("kubernetes", "Kubernetes", f.Deploy))
	sb.WriteString(opt("helm", "Helm", f.Deploy))
	sb.WriteString(opt("unraid", "Unraid", f.Deploy))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<noscript><button type="submit">Apply</button></noscript>`)
	sb.WriteString(`</form>`)
	return sb.String()
}

// renderPreviewCohorts renders the retention-cohort table. Each percentage
// cell is given an inline background colour scaled from red (0%) to green
// (100%) so the table doubles as a quick-read heatmap.
func renderPreviewCohorts(cohorts []retentionCohort) string {
	if len(cohorts) == 0 {
		return `<p style="color:#94a3b8;font-size:.85rem;">No cohort data yet — need at least 30 days of pings to populate the heatmap.</p>`
	}
	var sb strings.Builder
	sb.WriteString(`<table class="cohorts"><thead><tr>`)
	sb.WriteString(`<th>Cohort week</th><th>Size</th><th>Day 7</th><th>Day 14</th><th>Day 30</th>`)
	sb.WriteString(`</tr></thead><tbody>`)
	for _, c := range cohorts {
		var r7p, r14p, r30p float64
		if c.Size > 0 {
			r7p = float64(c.R7) / float64(c.Size) * 100
			r14p = float64(c.R14) / float64(c.Size) * 100
			r30p = float64(c.R30) / float64(c.Size) * 100
		}
		fmt.Fprintf(&sb,
			`<tr><td>%s</td><td>%d</td>%s%s%s</tr>`,
			c.WeekStart.Format("Jan 2"), c.Size,
			cohortCell(c.R7, r7p, c.Size),
			cohortCell(c.R14, r14p, c.Size),
			cohortCell(c.R30, r30p, c.Size),
		)
	}
	sb.WriteString(`</tbody></table>`)
	return sb.String()
}

// cohortCell renders one retention table cell. Empty cohorts (size 0) get a
// muted dash; populated cells get a heatmap background interpolating between
// red (#ef4444) at 0% and green (#10b981) at 100%.
func cohortCell(count int, pct float64, size int) string {
	if size == 0 {
		return `<td class="empty">—</td>`
	}
	// Interpolate red → amber → green. Simple two-segment lerp.
	var bg string
	switch {
	case pct >= 50:
		bg = lerpHex("#f59e0b", "#10b981", (pct-50)/50)
	default:
		bg = lerpHex("#ef4444", "#f59e0b", pct/50)
	}
	return fmt.Sprintf(`<td class="pct" style="background:%s">%.0f%% <small style="font-weight:500;opacity:.7">(%d)</small></td>`,
		bg, pct, count)
}

// lerpHex linearly interpolates between two `#rrggbb` colour strings. Fraction
// is clamped to [0, 1].
func lerpHex(a, b string, fraction float64) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	ar, ag, ab := hexRGB(a)
	br, bg, bb := hexRGB(b)
	r := int(float64(ar) + (float64(br)-float64(ar))*fraction)
	g := int(float64(ag) + (float64(bg)-float64(ag))*fraction)
	bl := int(float64(ab) + (float64(bb)-float64(ab))*fraction)
	return fmt.Sprintf("#%02x%02x%02x", r, g, bl)
}

func hexRGB(s string) (int, int, int) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0
	}
	var r, g, b int
	_, _ = fmt.Sscanf(s[1:], "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

// handlePreviewPage renders /stats/preview. Reads filters from query string,
// runs computePreviewData, marshals the JSON payload, and writes the template
// with both the server-rendered HTML pieces (hero/cohorts/filters) and the
// JSON blob that Chart.js consumes client-side.
func (s *server) handlePreviewPage(w http.ResponseWriter, r *http.Request) {
	filters := parsePreviewFilters(r.URL.Query())
	d, err := s.computePreviewData(r.Context(), filters)
	if err != nil {
		slog.Error("preview page: compute", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	payload := buildPreviewPayload(d)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Error("preview page: marshal", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Defensive: escape `</script>` so a future label embedded in JSON can't
	// break out of the <script type="application/json"> island. json.Marshal
	// does not escape forward slashes by default.
	safeJSON := strings.ReplaceAll(string(payloadJSON), "</", `<\/`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The site-wide CSP set in secureHeaders blocks all scripts. The preview
	// page is intentionally the only route that loads a CDN script + inline
	// setup, so we widen the policy *only* for this response. The middleware
	// runs first; this Set replaces its value before WriteHeader is called.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src https:; style-src 'unsafe-inline'; "+
			"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
			"connect-src 'self'")

	page := strings.NewReplacer(
		"{{HERO}}", renderPreviewHero(d),
		"{{COHORTS}}", renderPreviewCohorts(d.Cohorts),
		"{{FILTERS}}", renderPreviewFilters(filters),
		"{{JSON}}", safeJSON,
		"{{GENERATED}}", time.Now().UTC().Format("2006-01-02 15:04 MST"),
		"{{RANGELABEL}}", html.EscapeString(d.RangeLabel),
	).Replace(previewPageTemplate)
	if _, err := io.WriteString(w, page); err != nil {
		slog.Warn("preview page: write response", "error", err)
	}
}
