package main

import (
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// chartPalette cycles through these colours for chart bars and legend swatches.
var chartPalette = []string{
	"#10b981", "#3b82f6", "#f59e0b", "#a855f7",
	"#ef4444", "#06b6d4", "#ec4899", "#84cc16",
}

func paletteColor(i int) string { return chartPalette[i%len(chartPalette)] }

// renderBarChart returns inline SVG for a horizontal bar chart with a legend.
// Each bucket gets its own colour from chartPalette so the swatch in the
// legend column matches the bar.
//
// When pinLabel is non-empty and matches a bucket that would otherwise fall
// into the "(other)" tail, the pinned bucket is swapped into the last visible
// slot so a freshly cut release stays visible before it organically reaches
// top-N. The pinned row's legend is suffixed with " (latest)". Buckets with
// pinLabel already in the visible region are annotated but not reordered.
func renderBarChart(buckets []statsBucket, maxBars int, pinLabel string) string {
	if len(buckets) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	if maxBars > 0 && len(buckets) > maxBars {
		// Copy so we can mutate without surprising the caller (other charts
		// reuse the same slices indirectly via stable-sort in handleStatsPage).
		work := make([]statsBucket, len(buckets))
		copy(work, buckets)
		buckets = work

		if pinLabel != "" {
			pinIdx := -1
			for i := maxBars; i < len(buckets); i++ {
				if buckets[i].Label == pinLabel {
					pinIdx = i
					break
				}
			}
			if pinIdx >= 0 {
				buckets[maxBars-1], buckets[pinIdx] = buckets[pinIdx], buckets[maxBars-1]
			}
		}

		// Collapse the long tail into an "(other)" bucket so the chart
		// doesn't grow unbounded with every release sha.
		head := buckets[:maxBars]
		tail := 0
		for _, b := range buckets[maxBars:] {
			tail += b.Count
		}
		head = append(head, statsBucket{Label: "(other)", Count: tail})
		buckets = head
	}
	max := buckets[0].Count
	for _, b := range buckets {
		if b.Count > max {
			max = b.Count
		}
	}

	var sb strings.Builder
	sb.WriteString(`<div class="chart">`)
	sb.WriteString(`<table class="bars" role="presentation">`)
	for i, b := range buckets {
		colour := paletteColor(i)
		pct := 0
		if max > 0 {
			pct = b.Count * 100 / max
		}
		label := b.Label
		if pinLabel != "" && b.Label == pinLabel {
			label = b.Label + " (latest)"
		}
		fmt.Fprintf(&sb,
			`<tr><td class="legend-cell"><span class="swatch" style="background:%s"></span>%s</td>`+
				`<td class="bar-cell"><div class="bar" style="width:%d%%;background:%s"></div></td>`+
				`<td class="count-cell">%d</td></tr>`,
			colour, html.EscapeString(label), pct, colour, b.Count)
	}
	sb.WriteString(`</table></div>`)
	return sb.String()
}

// renderVersionsTable extends renderBarChart for the Versions section by
// surfacing two cohorts side-by-side: the 30d window (the bar widths are
// scaled to this, matching the rest of the page) and the 7d window in a
// second count column. Versions present in the 30d set but not in the 7d
// set still appear with a 7d count of zero so a dormant version (e.g. an
// older release whose installs pinged once weeks ago and never returned)
// is visible rather than silently dropping out of the table.
func renderVersionsTable(buckets30d, buckets7d []statsBucket, maxBars int, pinLabel string) string {
	if len(buckets30d) == 0 {
		return `<p class="empty">No data yet.</p>`
	}

	recent := make(map[string]int, len(buckets7d))
	for _, b := range buckets7d {
		recent[b.Label] = b.Count
	}

	// Collapse the long tail the same way renderBarChart does so the
	// table stays at most maxBars+1 rows.
	display := buckets30d
	if maxBars > 0 && len(display) > maxBars {
		work := make([]statsBucket, len(display))
		copy(work, display)

		if pinLabel != "" {
			pinIdx := -1
			for i := maxBars; i < len(work); i++ {
				if work[i].Label == pinLabel {
					pinIdx = i
					break
				}
			}
			if pinIdx >= 0 {
				work[maxBars-1], work[pinIdx] = work[pinIdx], work[maxBars-1]
			}
		}

		head := work[:maxBars]
		tail30d := 0
		tail7d := 0
		for _, b := range work[maxBars:] {
			tail30d += b.Count
			tail7d += recent[b.Label]
		}
		head = append(head, statsBucket{Label: "(other)", Count: tail30d})
		recent["(other)"] = tail7d
		display = head
	}

	max := display[0].Count
	for _, b := range display {
		if b.Count > max {
			max = b.Count
		}
	}

	var sb strings.Builder
	sb.WriteString(`<div class="chart">`)
	sb.WriteString(`<table class="bars bars-2col" role="presentation">`)
	sb.WriteString(`<thead><tr>` +
		`<th class="legend-cell"></th>` +
		`<th class="bar-cell"></th>` +
		`<th class="count-cell count-header">7d</th>` +
		`<th class="count-cell count-header">30d</th>` +
		`</tr></thead><tbody>`)
	for i, b := range display {
		colour := paletteColor(i)
		pct := 0
		if max > 0 {
			pct = b.Count * 100 / max
		}
		label := b.Label
		if pinLabel != "" && b.Label == pinLabel {
			label = b.Label + " (latest)"
		}
		fmt.Fprintf(&sb,
			`<tr><td class="legend-cell"><span class="swatch" style="background:%s"></span>%s</td>`+
				`<td class="bar-cell"><div class="bar" style="width:%d%%;background:%s"></div></td>`+
				`<td class="count-cell">%d</td>`+
				`<td class="count-cell">%d</td></tr>`,
			colour, html.EscapeString(label), pct, colour, recent[b.Label], b.Count)
	}
	sb.WriteString(`</tbody></table></div>`)
	return sb.String()
}

// renderLedgerFootnote returns a footnote for the ledger-fed charts (daily
// activity, version mix) while their 30-day window still reaches back past
// the ledger epoch. Days before the epoch were seeded from last_seen —
// each install attributed only to its single most recent day — so those
// bars undercount. Returns "" once the window has aged past the epoch (or
// before the epoch exists at all).
func renderLedgerFootnote(epoch string) string {
	if epoch == "" {
		return ""
	}
	windowStart := time.Now().UTC().AddDate(0, 0, -29).Format("2006-01-02")
	if epoch <= windowStart {
		return ""
	}
	return `<p class="empty" style="margin-top:.5rem">Days before ` +
		html.EscapeString(epoch) + ` predate per-day activity tracking ` +
		`and undercount (each install was attributed only to its most ` +
		`recent ping day).</p>`
}

// renderLongevity wraps renderBarChart with a footnote that fires when the
// DB has been collecting for less than 30 days. In that case the "1 to 3
// months" and "3+ months" buckets cannot exist no matter how many installs
// there are, and the table looks misleadingly small. The footnote tells a
// reader the lower buckets are empty by construction, not by population.
func renderLongevity(buckets []statsBucket, youngDB bool) string {
	chart := renderBarChart(buckets, 0, "")
	if !youngDB {
		return chart
	}
	return chart + `<p class="empty" style="margin-top:.5rem">` +
		`Telemetry DB has been collecting for less than 30 days; the ` +
		`"1 to 3 months" and "3+ months" buckets cannot populate yet ` +
		`(every install's age is bounded by the data span).</p>`
}

// renderFeatures renders the per-subsystem adoption table. Bar widths and
// the count column are denominated in installs (out of reporting); the
// header notes the denominator so a reader can convert to percent. When no
// 7d-active install has reported a features payload yet (older clients
// only), the section renders an explanatory message instead of an empty
// chart with confusing zero counts.
// renderFunnel renders the setup-funnel milestone table: for each first
// milestone, the share of new funnel-capable installs that reached it
// within 1 day, within 7 days, and at all. Denominators differ per column
// (see funnelStage); each cell shows the honest cohort for its window.
func renderFunnel(stages []funnelStage) string {
	if len(stages) == 0 {
		return `<div class="chart"><p class="empty">No funnel data yet. ` +
			`Clients report setup-funnel day offsets starting with v1.30.1; ` +
			`this section populates as new installs arrive on it.</p></div>`
	}
	var b strings.Builder
	fmt.Fprintf(&b,
		`<p class="empty" style="margin:0 0 .5rem 0">Out of %d new install%s on funnel-capable versions. `+
			`"By D+1/D+7" count only installs old enough for that window.</p>`,
		stages[0].Cohort, pluralS(stages[0].Cohort))
	b.WriteString(`<div class="chart"><table class="bars"><tr>` +
		`<td class="legend-cell"></td>` +
		`<td class="count-cell">by D+1</td>` +
		`<td class="count-cell">by D+7</td>` +
		`<td class="count-cell">ever</td></tr>`)
	for _, st := range stages {
		b.WriteString(`<tr><td class="legend-cell">` + html.EscapeString(st.Label) + `</td>` +
			`<td class="count-cell">` + pct(st.D1Num, st.D1Den) + `</td>` +
			`<td class="count-cell">` + pct(st.D7Num, st.D7Den) + `</td>` +
			`<td class="count-cell">` + pct(st.Ever, st.Cohort) + `</td></tr>`)
	}
	b.WriteString(`</table></div>`)
	return b.String()
}

func renderFeatures(buckets []statsBucket, reporting int) string {
	if reporting == 0 {
		return `<div class="chart"><p class="empty">No features data yet. ` +
			`Older clients (pre-v1.15.3) don't include a features payload; ` +
			`this section populates as installs upgrade.</p></div>`
	}
	header := fmt.Sprintf(
		`<p class="empty" style="margin:0 0 .5rem 0">Out of %d install%s reporting features in the last 7 days.</p>`,
		reporting,
		pluralS(reporting),
	)
	// reuse renderBarChart for the bar/count rows; bar widths scale to
	// the largest reported count so the row with the highest adoption
	// pegs at 100 percent and the rest scale down.
	return header + renderBarChart(buckets, 0, "")
}

// pluralS returns "" or "s" for English plural agreement based on count.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderSparkline returns inline SVG for a 30-day daily-activity bar chart.
func renderSparkline(daily []dailyBucket) string {
	if len(daily) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	max := 0
	for _, d := range daily {
		if d.Count > max {
			max = d.Count
		}
	}
	const w, h, gap = 600, 80, 2
	barW := (w - gap*(len(daily)-1)) / len(daily)
	if barW < 1 {
		barW = 1
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Daily active installs over the last 30 days">`, w, h)
	for i, d := range daily {
		barH := 0
		if max > 0 {
			barH = d.Count * h / max
		}
		if barH < 1 && d.Count > 0 {
			barH = 1
		}
		x := i * (barW + gap)
		y := h - barH
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" fill="#10b981"><title>%s: %d</title></rect>`,
			x, y, barW, barH, d.Day.Format("Jan 2"), d.Count)
	}
	sb.WriteString(`</svg>`)
	// Axis labels: first and last day below the chart.
	first := daily[0].Day.Format("Jan 2")
	last := daily[len(daily)-1].Day.Format("Jan 2")
	fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`, first, last)
	return sb.String()
}

// renderMonthlyChart renders a bar sparkline for monthly new-install counts.
// Each bar is labelled via its title attribute; axis labels show first/last month.
func renderMonthlyChart(points []statsBucket) string {
	if len(points) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	max := 0
	for _, p := range points {
		if p.Count > max {
			max = p.Count
		}
	}
	const w, h, gap = 600, 80, 4
	barW := (w - gap*(len(points)-1)) / len(points)
	if barW < 1 {
		barW = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="New installs per month over the last 12 months">`, w, h)
	for i, p := range points {
		barH := 0
		if max > 0 {
			barH = p.Count * h / max
		}
		if barH < 1 && p.Count > 0 {
			barH = 1
		}
		x := i * (barW + gap)
		y := h - barH
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" fill="#3b82f6"><title>%s: %d new</title></rect>`,
			x, y, barW, barH, html.EscapeString(p.Label), p.Count)
	}
	sb.WriteString(`</svg>`)
	if len(points) >= 2 {
		fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`,
			html.EscapeString(points[0].Label), html.EscapeString(points[len(points)-1].Label))
	}
	return sb.String()
}

// renderVersionTrend renders a stacked-proportion day chart: each day's bar is
// divided by version share, so you can see version N-1 declining as N rises.
// topVersions controls colour assignment; any version not in the list is
// collapsed into an "(other)" dark-grey segment.
func renderVersionTrend(days []versionTrendDay, topVersions []string) string {
	if len(days) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	const w, h, gap = 600, 80, 2
	barW := (w - gap*(len(days)-1)) / len(days)
	if barW < 1 {
		barW = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Version distribution over the last 30 days">`, w, h)
	for i, d := range days {
		if d.Total == 0 {
			continue
		}
		x := i * (barW + gap)
		y := 0
		for vi, ver := range topVersions {
			cnt := d.Versions[ver]
			if cnt == 0 {
				continue
			}
			segH := cnt * h / d.Total
			if segH < 1 {
				segH = 1
			}
			fmt.Fprintf(&sb,
				`<rect x="%d" y="%d" width="%d" height="%d" fill="%s"><title>%s %s: %d</title></rect>`,
				x, y, barW, segH, paletteColor(vi), d.Day.Format("Jan 2"), html.EscapeString(ver), cnt)
			y += segH
		}
		// Remaining versions collapsed into "(other)".
		other := d.Total
		for _, ver := range topVersions {
			other -= d.Versions[ver]
		}
		if other > 0 {
			segH := other * h / d.Total
			if segH < 1 {
				segH = 1
			}
			fmt.Fprintf(&sb,
				`<rect x="%d" y="%d" width="%d" height="%d" fill="#475569"><title>%s (other): %d</title></rect>`,
				x, y, barW, segH, d.Day.Format("Jan 2"), other)
		}
	}
	sb.WriteString(`</svg>`)
	first := days[0].Day.Format("Jan 2")
	last := days[len(days)-1].Day.Format("Jan 2")
	fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`, first, last)
	// Colour legend.
	sb.WriteString(`<div class="trend-legend">`)
	for vi, ver := range topVersions {
		fmt.Fprintf(&sb, `<span class="trend-key"><span class="swatch" style="background:%s"></span>%s</span>`,
			paletteColor(vi), html.EscapeString(ver))
	}
	if len(topVersions) > 0 {
		sb.WriteString(`<span class="trend-key"><span class="swatch" style="background:#475569"></span>(other)</span>`)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// handleStatsPage renders a public dashboard with charts and a legend.
// The data is the same aggregate counts surfaced by /api/stats; nothing
// install-identifying is exposed.
func (s *server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.computeStats(r.Context())
	if err != nil {
		slog.Error("stats page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stable order for legend-rendering: queryBuckets already sorts by count
	// desc; ties are broken alphabetically here so the page is deterministic.
	stable := func(bs []statsBucket) {
		sort.SliceStable(bs, func(i, j int) bool {
			if bs[i].Count != bs[j].Count {
				return bs[i].Count > bs[j].Count
			}
			return bs[i].Label < bs[j].Label
		})
	}
	stable(d.Versions)
	stable(d.OS)
	stable(d.Arch)
	stable(d.Deploy)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery — Telemetry</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    padding: 3rem 1.5rem;
  }
  .container { max-width: 720px; margin: 0 auto; }
  header { margin-bottom: 2.5rem; }
  header a.back { color: #94a3b8; font-size: .85rem; text-decoration: none; }
  header a.back:hover { color: #e2e8f0; }
  h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.02em; margin: .75rem 0 .5rem; }
  header p { color: #94a3b8; font-size: .9rem; line-height: 1.6; }
  .summary { display: flex; gap: 1rem; margin: 2rem 0; flex-wrap: wrap; }
  .summary .stat {
    flex: 1 1 200px;
    padding: 1.25rem 1.5rem;
    background: #1e293b;
    border: 1px solid #334155;
    border-radius: 10px;
  }
  .summary .stat .num { font-size: 2.25rem; font-weight: 700; color: #10b981; line-height: 1; }
  .summary .stat .label { color: #94a3b8; font-size: .85rem; margin-top: .35rem; }
  section { margin-bottom: 2.5rem; }
  section > h2 {
    font-size: .8rem; font-weight: 700; text-transform: uppercase;
    letter-spacing: .08em; color: #94a3b8; margin-bottom: .85rem;
  }
  .chart { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 1rem 1.25rem; }
  table.bars { width: 100%%; border-collapse: collapse; font-size: .9rem; }
  table.bars td { padding: .35rem .5rem; vertical-align: middle; }
  td.legend-cell { width: 35%%; color: #cbd5e1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  td.bar-cell { width: 50%%; }
  td.count-cell { width: 15%%; text-align: right; color: #94a3b8; font-variant-numeric: tabular-nums; }
  table.bars-2col td.legend-cell { width: 30%%; }
  table.bars-2col td.bar-cell    { width: 40%%; }
  table.bars-2col td.count-cell  { width: 15%%; }
  table.bars-2col th { text-align: right; color: #64748b; font-size: .7rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; padding: .25rem .5rem .5rem; }
  table.bars-2col th.count-header { color: #94a3b8; }
  .swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: .5rem; vertical-align: middle; }
  .bar { height: 14px; border-radius: 2px; min-width: 2px; }
  .sparkline { width: 100%%; height: 80px; display: block; }
  .sparkline-axis {
    display: flex; justify-content: space-between;
    color: #94a3b8; font-size: .75rem; margin-top: .35rem;
  }
  .empty { color: #94a3b8; font-size: .85rem; padding: .25rem 0; }
  .trend-legend { display: flex; flex-wrap: wrap; gap: .5rem 1.25rem; margin-top: .75rem; font-size: .8rem; color: #cbd5e1; }
  .trend-key { display: flex; align-items: center; gap: .4rem; }
  footer { margin-top: 3rem; color: #64748b; font-size: .75rem; text-align: center; }
</style>
</head>
<body>
<div class="container">
  <header>
    <a class="back" href="/">← Bindery</a>
    <h1>Telemetry</h1>
    <p>Anonymous install counts from instances that opted in to update checks. No identifying information is collected, only an opaque install UUID, the version, the OS/arch reported by Go's runtime, and (on v1.15.3+) per-subsystem adoption counts. "Active 7d" is the count of installs that pinged in the last seven days (the closest proxy we have for "running right now"); "Active 30d" is the wider 30-day window and includes dormant installs that have not pinged in a while. "Cumulative installs" is every distinct install ever seen — it only ever grows and counts each install UUID once, so an install that recreates its database (a fresh volume, a wiped config) is counted again. Full schema and opt-out instructions: <a href="/telemetry-fields" style="color:#10b981">/telemetry-fields</a>.</p>
  </header>

  <div class="summary">
    <div class="stat"><div class="num">%d</div><div class="label">Active installs (7d)</div></div>
    <div class="stat"><div class="num">%d</div><div class="label">Active installs (30d)</div></div>
    <div class="stat"><div class="num">%d</div><div class="label">Cumulative installs</div></div>
  </div>

  <section>
    <h2>Versions</h2>
    %s
  </section>

  <section>
    <h2>Operating system</h2>
    %s
  </section>

  <section>
    <h2>Architecture</h2>
    %s
  </section>

  <section>
    <h2>Deployment method</h2>
    %s
  </section>

  <section>
    <h2>Daily activity (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>New installs per day (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>New installs per month (last 12 months)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>Install longevity (active installs by age)</h2>
    %s
  </section>

  <section>
    <h2>Setup funnel (installs from the last 30 days)</h2>
    %s
  </section>

  <section>
    <h2>Feature adoption (last 7 days)</h2>
    %s
  </section>

  <section>
    <h2>Version mix (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <footer>
    Generated %s. Data refreshes on every page load.
  </footer>
</div>
</body>
</html>`,
		d.Active7d, d.Active30d, d.Cumulative,
		renderVersionsTable(d.Versions, d.VersionsRecent, 8, normalizeVersion(s.latest())),
		renderBarChart(d.OS, 0, ""),
		renderBarChart(d.Arch, 0, ""),
		renderBarChart(d.Deploy, 0, ""),
		renderSparkline(d.Daily)+renderLedgerFootnote(d.LedgerEpoch),
		renderSparkline(d.DailyNew),
		renderMonthlyChart(d.Monthly),
		renderLongevity(d.Longevity, d.LongevityYoungDB),
		renderFunnel(d.Funnel),
		renderFeatures(d.Features, d.FeaturesReporting),
		renderVersionTrend(d.VersionTrend, d.TopVersions)+renderLedgerFootnote(d.LedgerEpoch),
		time.Now().UTC().Format("2006-01-02 15:04 MST"),
	); err != nil {
		slog.Warn("stats: write response", "error", err)
	}
}
