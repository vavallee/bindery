package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

type statsResponse struct {
	Active30d  int            `json:"active_30d"`
	Total      int            `json:"total"`
	Cumulative int            `json:"cumulative"`
	Versions   map[string]int `json:"versions"`
	BuildSHA   string         `json:"build_sha"`
}

// statsJSON is the response shape for the public /stats.json endpoint. It
// surfaces the numbers the Discord stats bot (and any future scraper) needs
// without requiring the full /api/stats token-gated payload.
type statsJSON struct {
	Active     int    `json:"active"`     // 30-day active install count
	Total      int    `json:"total"`      // rolling 60-day distinct count (bounded by retention)
	Cumulative int    `json:"cumulative"` // all-time installs ever (sum of daily new_installs)
	Latest     string `json:"latest"`     // latest released version, e.g. "v1.9.5"
}

// statsBucket pairs a label (version / OS / arch) with its install count.
type statsBucket struct {
	Label string
	Count int
}

// dailyBucket holds the count of installs whose last_seen falls within a
// single day, used for the 30-day activity sparkline.
type dailyBucket struct {
	Day   time.Time
	Count int
}

// versionTrendDay holds per-version active counts for one day, used in the
// stacked version-adoption chart.
type versionTrendDay struct {
	Day      time.Time
	Versions map[string]int
	Total    int
}

// statsData is the complete aggregated telemetry view used by both the
// auth-gated JSON API and the public HTML dashboard.
type statsData struct {
	Active7d  int
	Active30d int
	Total     int
	// Cumulative is the true all-time install count: the sum of new_installs
	// across every day in daily_global. Unlike Total (a rolling 60-day count
	// bounded by the retention sweep), this is monotonic — it counts every
	// distinct install_id ever seen, including those long since evicted from
	// the installs table. Backed by the perpetual daily_global aggregate.
	Cumulative int
	Versions   []statsBucket
	// VersionsRecent is the same set of buckets restricted to installs that
	// pinged in the last 7 days. Dashboard renders both alongside each other
	// so dormant installs (e.g. v1.8.1 pinged once and never again) stop
	// inflating the headline.
	VersionsRecent   []statsBucket
	OS               []statsBucket
	Arch             []statsBucket
	Deploy           []statsBucket
	Daily            []dailyBucket
	DailyNew         []dailyBucket // new installs per day (first_seen), 30 days
	Longevity        []statsBucket // age buckets for 30d-active installs
	LongevityYoungDB bool          // true when DB span < 30d, so higher buckets cannot fire
	// LedgerEpoch is the YYYY-MM-DD the activity ledger started collecting
	// live pings ("" before the migration has run). Chart days before it
	// were seeded from last_seen and undercount; the dashboard footnotes
	// the two ledger-fed charts while the 30-day window still spans it.
	LedgerEpoch  string
	Monthly      []statsBucket     // new installs per calendar month, last 12 mo
	VersionTrend []versionTrendDay // per-day per-version active counts, 30 days
	TopVersions  []string          // top-N versions for VersionTrend legend

	// Features (last 7d): per-subsystem adoption counts among installs that
	// Setup funnel: how fast new installs (first_seen in the last 30 days,
	// on a funnel-capable client version) reach each first milestone.
	// Filled by computeSetupFunnel; empty slice renders a "no data yet"
	// note during the rollout window.
	Funnel []funnelStage

	// have reported a features payload in the last 7 days. FeaturesReporting
	// is the denominator; each bucket count is the numerator. Older clients
	// without the features field don't contribute to either, so the
	// percentage is "of the installs we have data for."
	FeaturesReporting int
	Features          []statsBucket
}

// computeStats runs every dashboard query and returns one assembled snapshot.
// All counts are scoped to the active-30-day window except Total and the
// recent-7d cohort.
func (s *server) computeStats(ctx context.Context) (*statsData, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	cutoff7d := now.Add(-7 * 24 * time.Hour)
	d := &statsData{}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ?`, cutoff7d,
	).Scan(&d.Active7d); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ?`, cutoff,
	).Scan(&d.Active30d); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs`,
	).Scan(&d.Total); err != nil {
		return nil, err
	}
	// Cumulative all-time installs from the perpetual daily aggregate. Survives
	// the 60-day retention sweep on raw rows and, thanks to the MAX-guarded
	// backfill, only ever grows.
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(new_installs), 0) FROM daily_global`,
	).Scan(&d.Cumulative); err != nil {
		return nil, err
	}

	queryBuckets := func(col string, since time.Time) ([]statsBucket, error) {
		// #nosec G202 -- col is a literal from the caller, not user input.
		q := `SELECT ` + col + `, COUNT(*) FROM installs WHERE last_seen >= ? GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`
		rows, err := s.db.QueryContext(ctx, q, since)
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

	var err error
	if d.Versions, err = queryBuckets("version", cutoff); err != nil {
		return nil, err
	}
	if d.VersionsRecent, err = queryBuckets("version", cutoff7d); err != nil {
		return nil, err
	}
	if d.OS, err = queryBuckets("os", cutoff); err != nil {
		return nil, err
	}
	if d.Arch, err = queryBuckets("arch", cutoff); err != nil {
		return nil, err
	}
	if d.Deploy, err = queryBuckets("deploy", cutoff); err != nil {
		return nil, err
	}

	// Daily activity for the last 30 days, from the per-day ledger. The
	// previous source — GROUP BY substr(last_seen,1,10) over installs —
	// counted each install only toward its most recent ping day, so
	// historical days showed "installs that went dormant that day" (~5%
	// of the truth) while the newest 1–2 days absorbed everyone else and
	// rendered as spurious spikes. The ledger records every (day, install)
	// pair at ping time, so each day keeps its true distinct-active count.
	cutoff30d := time.Now().UTC().AddDate(0, 0, -29).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, COUNT(*) FROM daily_activity WHERE day >= ? GROUP BY day ORDER BY day`, cutoff30d)
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
	// Ledger epoch for the pre-ledger-undercount footnote. A missing meta
	// row (fresh DB, seed not yet run) leaves it "" — no footnote.
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(value), '') FROM meta WHERE key = ?`, ledgerEpochKey,
	).Scan(&d.LedgerEpoch); err != nil {
		return nil, err
	}

	// Fill in zero-count days so the sparkline has a continuous 30-day axis.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		d.Daily = append(d.Daily, dailyBucket{Day: day, Count: dayCount[key]})
	}

	// New installs per day (first_seen, last 30 days).
	rowsNew, err := s.db.QueryContext(ctx,
		`SELECT substr(first_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE first_seen >= ? GROUP BY day ORDER BY day`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rowsNew.Close()
	newDayCount := make(map[string]int)
	for rowsNew.Next() {
		var day string
		var count int
		if err := rowsNew.Scan(&day, &count); err != nil {
			return nil, err
		}
		newDayCount[day] = count
	}
	if err := rowsNew.Err(); err != nil {
		return nil, err
	}
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		d.DailyNew = append(d.DailyNew, dailyBucket{Day: day, Count: newDayCount[key]})
	}

	// Install longevity: bucket 30d-active installs by age (last_seen − first_seen).
	rowsLon, err := s.db.QueryContext(ctx, `
		SELECT
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
		FROM installs WHERE last_seen >= ?
		GROUP BY bucket`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rowsLon.Close()
	lonMap := make(map[string]int)
	for rowsLon.Next() {
		var bucket string
		var count int
		if err := rowsLon.Scan(&bucket, &count); err != nil {
			return nil, err
		}
		lonMap[bucket] = count
	}
	if err := rowsLon.Err(); err != nil {
		return nil, err
	}
	for _, label := range []string{"< 1 week", "1–4 weeks", "1–3 months", "3+ months"} {
		if cnt, ok := lonMap[label]; ok {
			d.Longevity = append(d.Longevity, statsBucket{Label: label, Count: cnt})
		}
	}

	// If the DB itself hasn't been collecting for 30 days, the "1 to 3 months"
	// and "3+ months" buckets are structurally empty: max(last_seen - first_seen)
	// is bounded by the data span. Flag this so the dashboard can render a
	// footnote instead of silently showing only the lower buckets, which a
	// reader would otherwise interpret as "no install has been alive that long."
	var earliestStr string
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(substr(first_seen, 1, 19)), '')
		   FROM installs`,
	).Scan(&earliestStr); err != nil {
		return nil, err
	}
	if earliestStr != "" {
		if earliest, perr := time.Parse("2006-01-02 15:04:05", earliestStr); perr == nil {
			if now.Sub(earliest) < 30*24*time.Hour {
				d.LongevityYoungDB = true
			}
		}
	}

	// Features adoption among 7d-active installs that reported a payload.
	// Older clients send features = NULL and don't appear in either the
	// numerator or the denominator, which gives an accurate "% of installs
	// we have data for" rather than a misleading "% of all installs."
	if err := s.computeSetupFunnel(ctx, time.Now().UTC().AddDate(0, 0, -30), d); err != nil {
		return nil, err
	}

	if err := s.computeFeatureAdoption(ctx, cutoff7d, d); err != nil {
		return nil, err
	}

	// Monthly new installs for the last 12 calendar months. Reads from
	// daily_global rather than installs so the chart survives the 60-day
	// retention sweep: once a row's first_seen drops out of installs it's
	// already accumulated into the daily aggregate for that day.
	monthlyCutoff := time.Now().UTC().AddDate(0, -12, 0).Format("2006-01-02")
	rowsMon, err := s.db.QueryContext(ctx, `
		SELECT substr(day, 1, 7) AS month, SUM(new_installs)
		  FROM daily_global
		 WHERE day >= ?
		 GROUP BY month
		 ORDER BY month
	`, monthlyCutoff)
	if err != nil {
		return nil, err
	}
	defer rowsMon.Close()
	for rowsMon.Next() {
		var month string
		var count int
		if err := rowsMon.Scan(&month, &count); err != nil {
			return nil, err
		}
		label := month
		if t, err := time.Parse("2006-01", month); err == nil {
			label = t.Format("Jan '06")
		}
		d.Monthly = append(d.Monthly, statsBucket{Label: label, Count: count})
	}
	if err := rowsMon.Err(); err != nil {
		return nil, err
	}

	// Top versions for the version-trend legend (max 4).
	for i, v := range d.Versions {
		if i >= 4 {
			break
		}
		d.TopVersions = append(d.TopVersions, v.Label)
	}

	// Version mix per day for the last 30 days (stacked trend chart), from
	// the ledger — same rationale as the daily-activity chart above.
	rowsVer, err := s.db.QueryContext(ctx,
		`SELECT day, version, COUNT(*) FROM daily_activity WHERE day >= ? GROUP BY day, version ORDER BY day`,
		cutoff30d)
	if err != nil {
		return nil, err
	}
	defer rowsVer.Close()
	vtMap := make(map[string]map[string]int) // day -> version -> count
	for rowsVer.Next() {
		var day, ver string
		var count int
		if err := rowsVer.Scan(&day, &ver, &count); err != nil {
			return nil, err
		}
		if vtMap[day] == nil {
			vtMap[day] = make(map[string]int)
		}
		vtMap[day][ver] = count
	}
	if err := rowsVer.Err(); err != nil {
		return nil, err
	}
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		vd := versionTrendDay{Day: day, Versions: vtMap[key]}
		if vd.Versions == nil {
			vd.Versions = make(map[string]int)
		}
		for _, cnt := range vd.Versions {
			vd.Total += cnt
		}
		d.VersionTrend = append(d.VersionTrend, vd)
	}

	return d, nil
}

// statsTokenValid reports whether the request carries the stats bearer token.
// The comparison is constant time so the response latency does not reveal how
// many leading bytes of a guess were right, which is what makes a token
// recoverable one byte at a time. Matches how the main binary compares every
// other secret (internal/auth/password.go, internal/auth/middleware.go,
// internal/api/opds_auth.go). Callers must reject an empty s.statsToken before
// calling this; both current callers do (#2138).
func (s *server) statsTokenValid(r *http.Request) bool {
	if s.statsToken == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.statsToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.statsToken == "" {
		http.Error(w, "stats endpoint not configured", http.StatusForbidden)
		return
	}
	if !s.statsTokenValid(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	d, err := s.computeStats(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	versions := make(map[string]int, len(d.Versions))
	for _, b := range d.Versions {
		versions[b.Label] = b.Count
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statsResponse{
		Active30d:  d.Active30d,
		Total:      d.Total,
		Cumulative: d.Cumulative,
		Versions:   versions,
		BuildSHA:   buildSHA,
	})
}

// featureField describes one row in the Features section: the JSON key
// inside the stored features blob, the kind of bucket (count vs boolean),
// and the human label rendered on the dashboard. Count fields are summed
// across reporting installs (e.g. "12 indexers per install on average")
// and would render differently; today we just count installs with a
// non-zero value, matching the booleans, because adoption is more
// actionable than mean configuration size at this scale.
type featureField struct {
	JSONKey string
	Label   string
}

var featureFields = []featureField{
	{"indexers", "Indexers configured"},
	{"download_clients", "Download clients configured"},
	{"notifications", "Notifications configured"},
	{"calibre_enabled", "Calibre integration"},
	{"abs_enabled", "Audiobookshelf integration"},
	{"grimmory_enabled", "Grimmory integration"},
	{"hardcover_token", "Hardcover enhanced"},
	{"oidc_enabled", "OIDC auth"},
	{"multi_user", "Multi-user"},
}

// funnelMinVersion is the first client release that reports the setup-funnel
// day offsets. Installs on older versions can't be told apart from installs
// that stalled before their first milestone (both send no funnel fields), so
// the funnel cohort is gated on version, not on field presence.
var funnelMinVersion = [3]int{1, 30, 1}

// funnelCapable reports whether a version string is >= funnelMinVersion.
//
// The leading "v" is optional because both spellings are in the wild: the
// GoReleaser binaries build with {{ .Version }}, which strips it, while the
// Docker image builds with `git describe --tags --match 'v*'` and keeps it
// (.github/workflows/ci.yml, Dockerfile's VERSION arg). The telemetry client's
// own releaseVersionPattern is `^v?\d+\.\d+\.\d+$`, so it accepts and sends
// both. Requiring the bare form here silently dropped every Docker install out
// of the funnel cohort — the dominant deployment method — which looked
// identical to an install that stalled before its first milestone, the exact
// ambiguity the version gate exists to remove.
func funnelCapable(version string) bool {
	m := regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(version)
	if m == nil {
		return false
	}
	for i := range 3 {
		n, _ := strconv.Atoi(m[i+1])
		if n != funnelMinVersion[i] {
			return n > funnelMinVersion[i]
		}
	}
	return true
}

// funnelStage is one milestone row in the setup-funnel table. Numerators
// and denominators are kept per column because the windows differ: an
// install two days old cannot yet have reached anything "by day 7", so it
// belongs in the D+1 denominator but not the D+7 one.
type funnelStage struct {
	Label  string
	D1Num  int // reached with day offset <= 1
	D1Den  int // installs at least 1 day old
	D7Num  int
	D7Den  int // installs at least 7 days old
	Ever   int
	Cohort int // all funnel-capable installs in the window
}

func pct(num, den int) string {
	if den == 0 {
		return "–"
	}
	return fmt.Sprintf("%d%%", int(100*float64(num)/float64(den)+0.5))
}

// computeSetupFunnel fills d.Funnel from installs first seen inside the
// window on funnel-capable versions. Row volume is small (new installs per
// month is in the hundreds), so version filtering happens in Go where the
// semver compare is honest, not approximated in SQL.
func (s *server) computeSetupFunnel(ctx context.Context, since time.Time, d *statsData) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, substr(first_seen, 1, 10), COALESCE(features, '')
		  FROM installs
		 WHERE first_seen >= ?`, since)
	if err != nil {
		return err
	}
	defer rows.Close()

	stages := []funnelStage{
		{Label: "Indexer configured"},
		{Label: "Download client configured"},
		{Label: "Author added"},
		{Label: "First grab"},
		{Label: "First import"},
	}
	today := time.Now().UTC()
	for rows.Next() {
		var version, firstDay, featJSON string
		if err := rows.Scan(&version, &firstDay, &featJSON); err != nil {
			return err
		}
		if !funnelCapable(version) {
			continue
		}
		var f featuresPayload
		if featJSON != "" {
			_ = json.Unmarshal([]byte(featJSON), &f)
		}
		fd, err := time.Parse("2006-01-02", firstDay)
		if err != nil {
			continue
		}
		age := int(today.Sub(fd).Hours() / 24)
		for i, day := range []*int{f.SetupIndexerDay, f.SetupClientDay, f.FirstAuthorDay, f.FirstGrabDay, f.FirstImportDay} {
			st := &stages[i]
			st.Cohort++
			if age >= 1 {
				st.D1Den++
				if day != nil && *day <= 1 {
					st.D1Num++
				}
			}
			if age >= 7 {
				st.D7Den++
				if day != nil && *day <= 7 {
					st.D7Num++
				}
			}
			if day != nil {
				st.Ever++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if stages[0].Cohort > 0 {
		d.Funnel = stages
	}
	return nil
}

// computeFeatureAdoption fills d.FeaturesReporting and d.Features from
// installs that pinged in the last 7 days and include a non-null features
// payload. Uses SQLite's JSON1 extension (modernc.org/sqlite ships with
// it). Booleans count any truthy JSON value; counts count any non-zero
// numeric value. Both reduce to "this install has the subsystem in use,"
// which is the question the dashboard answers.
func (s *server) computeFeatureAdoption(ctx context.Context, since time.Time, d *statsData) error {
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ? AND features IS NOT NULL`,
		since,
	).Scan(&d.FeaturesReporting); err != nil {
		return err
	}
	if d.FeaturesReporting == 0 {
		return nil
	}
	for _, f := range featureFields {
		var n int
		// json_extract returns NULL when the key is missing; the count
		// then excludes the row. For booleans, true → 1, false → 0; the
		// > 0 filter catches both true booleans and non-zero counts.
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			  FROM installs
			 WHERE last_seen >= ?
			   AND features IS NOT NULL
			   AND json_extract(features, '$.'||?) > 0
		`, since, f.JSONKey).Scan(&n); err != nil {
			// Best-effort; a single bad field shouldn't sink the page.
			slog.Warn("feature adoption query", "field", f.JSONKey, "error", err)
			continue
		}
		d.Features = append(d.Features, statsBucket{Label: f.Label, Count: n})
	}
	return nil
}

// handleStatsJSON returns a small public JSON payload with active install
// count, total install count, and the configured latest version. It does NOT
// run the full computeStats pipeline (versions, OS, arch, longevity, etc.)
// because callers like the Discord stats bot only need three numbers and hit
// the endpoint every 10 minutes. The two COUNT(*) queries mirror the head of
// computeStats — keeping them inline avoids dragging the heavy aggregation
// path onto this hot, unauthenticated endpoint.
func (s *server) handleStatsJSON(w http.ResponseWriter, r *http.Request) {
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	var resp statsJSON
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ?`, cutoff,
	).Scan(&resp.Active); err != nil {
		slog.Error("stats.json: active count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM installs`,
	).Scan(&resp.Total); err != nil {
		slog.Error("stats.json: total count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(new_installs), 0) FROM daily_global`,
	).Scan(&resp.Cumulative); err != nil {
		slog.Error("stats.json: cumulative count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.Latest = s.latest()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_ = json.NewEncoder(w).Encode(resp)
}
