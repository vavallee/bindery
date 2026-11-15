package main

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// /stats/preview — richer interactive dashboard.
//
// Parallel to /stats. Charts are rendered client-side by Chart.js (pinned to
// 4.4.6 via jsdelivr). Filters (range / os / deploy) are server-side: changing
// a <select> submits the form, the page re-runs every query with the narrowed
// WHERE clause, and the new JSON payload is embedded in the page.
//
// previewData is intentionally kept separate from statsData so the preview's
// shape can evolve without dragging the existing /stats page along.
// ---------------------------------------------------------------------------

// previewFilters carries the validated query-string filters for /stats/preview.
// All four fields default to "all" (no narrowing) on invalid or missing input;
// any value rendered back into the page is from this canonical set, never from
// raw user input.
type previewFilters struct {
	Range  string // 7d, 30d, 90d, all
	OS     string // linux, windows, darwin, all
	Deploy string // docker, binary, kubernetes, helm, unraid, all
}

// validPreviewRanges enumerates the accepted values for the range filter.
// Map → seconds so callers can resolve a cutoff without a switch.
var validPreviewRanges = map[string]time.Duration{
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

var validPreviewOS = map[string]bool{
	"linux":   true,
	"windows": true,
	"darwin":  true,
}

// parsePreviewFilters extracts and validates the three filter query params,
// substituting "all" / "30d" for anything unrecognised. Whitelist-only, so the
// caller can safely interpolate the OS/deploy values into SQL by way of bind
// parameters (they're never spliced into a string).
func parsePreviewFilters(q map[string][]string) previewFilters {
	f := previewFilters{Range: "30d", OS: "all", Deploy: "all"}
	if v := first(q["range"]); v != "" {
		if _, ok := validPreviewRanges[v]; ok || v == "all" {
			f.Range = v
		}
	}
	if v := first(q["os"]); v != "" {
		if validPreviewOS[v] || v == "all" {
			f.OS = v
		}
	}
	if v := first(q["deploy"]); v != "" {
		if validDeploys[v] || v == "all" {
			f.Deploy = v
		}
	}
	return f
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// cutoff returns the time threshold for the current range, plus a bool
// indicating whether a cutoff applies at all (range=all → false). The zero
// time is returned when no cutoff applies so callers can use a single sentinel.
func (f previewFilters) cutoff(now time.Time) (time.Time, bool) {
	d, ok := validPreviewRanges[f.Range]
	if !ok {
		return time.Time{}, false
	}
	return now.Add(-d), true
}

// whereClause builds the OS/deploy portion of a WHERE clause and the matching
// bind parameter slice. Returns ("", nil) when no extra filter is needed.
// Callers prepend whatever cutoff/range filter they need and combine the
// result with the existing query template.
func (f previewFilters) whereClause() (string, []any) {
	var parts []string
	var args []any
	if f.OS != "all" {
		parts = append(parts, "os = ?")
		args = append(args, f.OS)
	}
	if f.Deploy != "all" {
		parts = append(parts, "deploy = ?")
		args = append(args, f.Deploy)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}

// retentionCohort holds one weekly cohort's retention rates. The percentages
// are computed in Go from the raw counts (`Size`, `R7`, `R14`, `R30`).
type retentionCohort struct {
	WeekStart time.Time // Monday of the cohort week (first ping in this week)
	Size      int       // installs first seen in this week (W₀)
	R7        int       // still pinging ≥7 days after first_seen
	R14       int       // still pinging ≥14 days after first_seen
	R30       int       // still pinging ≥30 days after first_seen
}

// previewData is the dashboard payload for /stats/preview. Fields map 1:1 onto
// the charts on the page; everything is precomputed server-side so the
// client-side Chart.js setup is just `new Chart(ctx, {data})`.
type previewData struct {
	Filters previewFilters

	// Hero stats row.
	ActiveRange    int     // active installs within the current range
	Total          int     // all-time total, ignores range filter
	NewRange       int     // new installs (first_seen) within the current range
	ActivePrev     int     // active count in the equal-length window ending at cutoff
	DeltaActive    int     // ActiveRange − ActivePrev (omit card when range=all)
	DeltaActivePct float64 // percentage change as a float (positive or negative)
	ShowDelta      bool    // false when range=all
	RangeLabel     string  // human label, e.g. "last 30 days"

	Daily       []dailyBucket // line chart: active per day across window
	DailyNew    []dailyBucket // bar chart: new installs per day across window
	OS          []statsBucket // doughnut: OS share
	Arch        []statsBucket // doughnut: arch share
	Deploy      []statsBucket // doughnut: deploy share
	Longevity   []statsBucket // bar: age buckets
	TopVersions []string      // legend order for the stacked area
	VersionDays []versionTrendDay

	Cohorts []retentionCohort
}

// rangeLabel returns a short human-readable summary of the active range.
func (f previewFilters) rangeLabel() string {
	switch f.Range {
	case "7d":
		return "last 7 days"
	case "30d":
		return "last 30 days"
	case "90d":
		return "last 90 days"
	default:
		return "all time"
	}
}

// rangeDays returns the inclusive number of days the window covers. The
// daily-bucket loops use this to size the x-axis. range=all is treated as
// 90 days for the daily charts (more would get too crowded).
func (f previewFilters) rangeDays() int {
	switch f.Range {
	case "7d":
		return 7
	case "30d":
		return 30
	case "90d":
		return 90
	default:
		return 90
	}
}

// computePreviewData runs every preview-dashboard query under the supplied
// filters and returns one assembled snapshot. Most queries mirror the existing
// statsData ones but with an additional OS/deploy WHERE fragment and a
// caller-controlled cutoff.
func (s *server) computePreviewData(ctx context.Context, f previewFilters) (*previewData, error) {
	now := time.Now().UTC()
	d := &previewData{Filters: f, RangeLabel: f.rangeLabel()}
	cutoff, hasCutoff := f.cutoff(now)
	extraWhere, extraArgs := f.whereClause()

	// Active in range. When range=all we count anyone who ever pinged.
	{
		var args []any
		q := `SELECT COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- extraWhere is built by previewFilters.whereClause() from a static set of "<col> = ?" fragments; user values are bound via extraArgs.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.ActiveRange); err != nil {
			return nil, err
		}
	}

	// All-time total. Range is intentionally ignored — that's the headline
	// "how many installs have we ever seen?" number. OS/deploy filters still
	// apply so the card matches the rest of the page.
	{
		q := `SELECT COUNT(*) FROM installs WHERE 1=1` + extraWhere
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, extraArgs...).Scan(&d.Total); err != nil {
			return nil, err
		}
	}

	// New installs in range.
	{
		var args []any
		q := `SELECT COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND first_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.NewRange); err != nil {
			return nil, err
		}
	}

	// Δ vs. previous equal-length window. Skip for range=all (no notion of
	// "previous" — the active count is monotonic over all-time).
	if hasCutoff {
		d.ShowDelta = true
		span := validPreviewRanges[f.Range]
		prevStart := cutoff.Add(-span)
		prevEnd := cutoff
		args := []any{prevStart, prevEnd}
		q := `SELECT COUNT(*) FROM installs WHERE last_seen >= ? AND last_seen < ?` + extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.ActivePrev); err != nil {
			return nil, err
		}
		d.DeltaActive = d.ActiveRange - d.ActivePrev
		if d.ActivePrev > 0 {
			d.DeltaActivePct = float64(d.DeltaActive) / float64(d.ActivePrev) * 100
		}
	}

	// Histograms (OS/arch/deploy/longevity) — each ranged + filtered.
	queryBuckets := func(col string) ([]statsBucket, error) {
		var args []any
		// #nosec G202 — col is a hard-coded literal from the caller.
		q := `SELECT ` + col + `, COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []statsBucket
		for rows.Next() {
			var b statsBucket
			if err := rows.Scan(&b.Label, &b.Count); err != nil {
				return nil, err
			}
			if b.Label == "" {
				b.Label = "(unknown)"
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}

	versions, err := queryBuckets("version")
	if err != nil {
		return nil, err
	}
	if d.OS, err = queryBuckets("os"); err != nil {
		return nil, err
	}
	if d.Arch, err = queryBuckets("arch"); err != nil {
		return nil, err
	}
	if d.Deploy, err = queryBuckets("deploy"); err != nil {
		return nil, err
	}

	// Longevity buckets — reuse the same CASE expression as /stats, only
	// adapted to honour the preview's optional cutoff and OS/deploy filter.
	{
		var args []any
		q := `SELECT
			  CASE
			    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 7
			         THEN '< 1 week'
			    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 30
			         THEN '1–4 weeks'
			    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 90
			         THEN '1–3 months'
			    ELSE '3+ months'
			  END AS bucket,
			  COUNT(*) AS n
			FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		q += extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY bucket`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		lonMap := make(map[string]int)
		for rows.Next() {
			var bucket string
			var count int
			if err := rows.Scan(&bucket, &count); err != nil {
				return nil, err
			}
			lonMap[bucket] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, label := range []string{"< 1 week", "1–4 weeks", "1–3 months", "3+ months"} {
			d.Longevity = append(d.Longevity, statsBucket{Label: label, Count: lonMap[label]})
		}
	}

	// Daily activity over the range. Fill zero days so the line chart has a
	// continuous axis. When range=all we still cap the window at 90 days for
	// the chart — drawing 800 daily bars on a 600px-wide canvas is illegible.
	days := f.rangeDays()
	today := now.Truncate(24 * time.Hour)
	chartCutoff := today.AddDate(0, 0, -(days - 1))
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(last_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE last_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		dayCount := make(map[string]int)
		for rows.Next() {
			var day string
			var count int
			if err := rows.Scan(&day, &count); err != nil {
				return nil, err
			}
			dayCount[day] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			d.Daily = append(d.Daily, dailyBucket{Day: day, Count: dayCount[key]})
		}
	}

	// New installs per day over the same window.
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(first_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE first_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		dayCount := make(map[string]int)
		for rows.Next() {
			var day string
			var count int
			if err := rows.Scan(&day, &count); err != nil {
				return nil, err
			}
			dayCount[day] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			d.DailyNew = append(d.DailyNew, dailyBucket{Day: day, Count: dayCount[key]})
		}
	}

	// Top-6 versions for the stacked area, ordered by overall count within the
	// active window (already sorted by queryBuckets).
	for i, v := range versions {
		if i >= 6 {
			break
		}
		d.TopVersions = append(d.TopVersions, v.Label)
	}

	// Per-day per-version active counts over the chart window. Reuses the
	// existing query shape but with the range-aware cutoff + OS/deploy filter.
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(last_seen, 1, 10) AS day, version, COUNT(*) FROM installs WHERE last_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day, version ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		vtMap := make(map[string]map[string]int)
		for rows.Next() {
			var day, ver string
			var count int
			if err := rows.Scan(&day, &ver, &count); err != nil {
				return nil, err
			}
			if vtMap[day] == nil {
				vtMap[day] = make(map[string]int)
			}
			vtMap[day][ver] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			vd := versionTrendDay{Day: day, Versions: vtMap[key]}
			if vd.Versions == nil {
				vd.Versions = make(map[string]int)
			}
			for _, cnt := range vd.Versions {
				vd.Total += cnt
			}
			d.VersionDays = append(d.VersionDays, vd)
		}
	}

	// Retention cohorts.
	cohorts, err := s.computeRetentionCohorts(ctx, f, now)
	if err != nil {
		return nil, err
	}
	d.Cohorts = cohorts

	return d, nil
}

// computeRetentionCohorts groups installs by ISO week of first_seen and counts
// how many were still pinging (last_seen ≥ first_seen + N days) at N ∈ {7,14,30}.
// Done in Go so the bucketing logic and "skip cohorts younger than 30 days"
// guard stays readable — one SQL query, all the math in memory.
//
// Cohort window: the same range as the rest of the page when a cutoff is set;
// when range=all, the trailing 26 weeks (≈6 months). Cohorts whose week ends
// less than 30 days ago are dropped because they can't have a 30-day data
// point yet, which would otherwise make the heatmap look broken.
func (s *server) computeRetentionCohorts(ctx context.Context, f previewFilters, now time.Time) ([]retentionCohort, error) {
	var cohortCutoff time.Time
	if c, ok := f.cutoff(now); ok {
		cohortCutoff = c
	} else {
		cohortCutoff = now.AddDate(0, 0, -26*7)
	}
	extraWhere, extraArgs := f.whereClause()

	args := []any{cohortCutoff}
	args = append(args, extraArgs...)
	// Pull every install whose first_seen is within the cohort window.
	// SQLite's substr() is fine for stable last_seen/first_seen formatting.
	// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
	q := `SELECT substr(first_seen, 1, 10), substr(last_seen, 1, 10) FROM installs WHERE first_seen >= ?` + extraWhere
	// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Bucket by ISO Monday of first_seen.
	type bucket struct {
		Size, R7, R14, R30 int
	}
	buckets := make(map[time.Time]*bucket)
	for rows.Next() {
		var firstStr, lastStr string
		if err := rows.Scan(&firstStr, &lastStr); err != nil {
			return nil, err
		}
		first, err1 := time.Parse("2006-01-02", firstStr)
		last, err2 := time.Parse("2006-01-02", lastStr)
		if err1 != nil || err2 != nil {
			continue
		}
		week := mondayOf(first)
		b := buckets[week]
		if b == nil {
			b = &bucket{}
			buckets[week] = b
		}
		b.Size++
		age := int(last.Sub(first).Hours() / 24)
		if age >= 7 {
			b.R7++
		}
		if age >= 14 {
			b.R14++
		}
		if age >= 30 {
			b.R30++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Drop cohorts whose week+30d hasn't passed yet — their R30 is structurally
	// 0 because no install in that cohort has had time to reach the milestone.
	maxWeek := now.AddDate(0, 0, -30)
	weeks := make([]time.Time, 0, len(buckets))
	for w := range buckets {
		if w.AddDate(0, 0, 6).Before(maxWeek) {
			weeks = append(weeks, w)
		}
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Before(weeks[j]) })

	out := make([]retentionCohort, 0, len(weeks))
	for _, w := range weeks {
		b := buckets[w]
		out = append(out, retentionCohort{
			WeekStart: w,
			Size:      b.Size,
			R7:        b.R7,
			R14:       b.R14,
			R30:       b.R30,
		})
	}
	return out, nil
}

// mondayOf returns the Monday (UTC midnight) of the ISO week containing t.
// time.Weekday() treats Sunday as 0; remap so Monday is 0 and subtract.
func mondayOf(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := int(t.Weekday()) // Sun=0..Sat=6
	if wd == 0 {
		wd = 7
	}
	return t.AddDate(0, 0, -(wd - 1))
}

// previewPayload is the JSON shape embedded in the page for Chart.js to read.
// Keeping it separate from previewData lets us strip server-side concerns
// (the *time.Time values, raw struct names) before serialisation.
type previewPayload struct {
	Labels        []string         `json:"labels"` // daily labels (Jan 2)
	Active        []int            `json:"active"` // line: daily active
	New           []int            `json:"new"`    // bar:  daily new
	OS            []labelCount     `json:"os"`
	Arch          []labelCount     `json:"arch"`
	Deploy        []labelCount     `json:"deploy"`
	Longevity     []labelCount     `json:"longevity"`
	TopVersions   []string         `json:"top_versions"`
	VersionSeries map[string][]int `json:"version_series"` // version → daily active across labels
	Cohorts       []cohortPayload  `json:"cohorts"`
}

type labelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type cohortPayload struct {
	WeekStart string  `json:"week_start"` // ISO date of Monday
	Size      int     `json:"size"`
	R7        int     `json:"r7"`
	R14       int     `json:"r14"`
	R30       int     `json:"r30"`
	R7Pct     float64 `json:"r7_pct"`
	R14Pct    float64 `json:"r14_pct"`
	R30Pct    float64 `json:"r30_pct"`
}

// buildPreviewPayload reshapes previewData into the JSON payload embedded in
// the rendered page. Done as a separate step so the SQL layer stays free of
// presentation concerns.
func buildPreviewPayload(d *previewData) previewPayload {
	p := previewPayload{
		TopVersions:   d.TopVersions,
		VersionSeries: make(map[string][]int, len(d.TopVersions)+1),
	}
	for _, day := range d.Daily {
		p.Labels = append(p.Labels, day.Day.Format("Jan 2"))
		p.Active = append(p.Active, day.Count)
	}
	for _, day := range d.DailyNew {
		p.New = append(p.New, day.Count)
	}
	for _, b := range d.OS {
		p.OS = append(p.OS, labelCount(b))
	}
	for _, b := range d.Arch {
		p.Arch = append(p.Arch, labelCount(b))
	}
	for _, b := range d.Deploy {
		p.Deploy = append(p.Deploy, labelCount(b))
	}
	for _, b := range d.Longevity {
		p.Longevity = append(p.Longevity, labelCount(b))
	}

	// Version-mix stacked area: one series per top version + "(other)".
	for _, v := range d.TopVersions {
		p.VersionSeries[v] = make([]int, 0, len(d.VersionDays))
	}
	otherKey := "(other)"
	p.VersionSeries[otherKey] = make([]int, 0, len(d.VersionDays))
	topSet := make(map[string]struct{}, len(d.TopVersions))
	for _, v := range d.TopVersions {
		topSet[v] = struct{}{}
	}
	for _, day := range d.VersionDays {
		other := 0
		for _, v := range d.TopVersions {
			p.VersionSeries[v] = append(p.VersionSeries[v], day.Versions[v])
		}
		for ver, cnt := range day.Versions {
			if _, ok := topSet[ver]; !ok {
				other += cnt
			}
		}
		p.VersionSeries[otherKey] = append(p.VersionSeries[otherKey], other)
	}

	for _, c := range d.Cohorts {
		var r7p, r14p, r30p float64
		if c.Size > 0 {
			r7p = float64(c.R7) / float64(c.Size) * 100
			r14p = float64(c.R14) / float64(c.Size) * 100
			r30p = float64(c.R30) / float64(c.Size) * 100
		}
		p.Cohorts = append(p.Cohorts, cohortPayload{
			WeekStart: c.WeekStart.Format("2006-01-02"),
			Size:      c.Size,
			R7:        c.R7,
			R14:       c.R14,
			R30:       c.R30,
			R7Pct:     r7p,
			R14Pct:    r14p,
			R30Pct:    r30p,
		})
	}

	return p
}
