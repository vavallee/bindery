package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/logbuf"
)

const (
	// logListDefaultLimit / logListMaxLimit bound a single List page.
	logListDefaultLimit = 200
	logListMaxLimit     = 1000

	// logExportMaxRows caps one export so a multi-million-row log table can't
	// be turned into an unbounded download. The cap is surfaced in the file
	// footer and in the UI hint — an export is never silently truncated.
	logExportMaxRows = 50000
	// logExportPageSize is how many rows are pulled from the store per query.
	// The export streams page by page so memory stays flat regardless of how
	// many rows match.
	logExportPageSize = 1000
)

// LogHandler exposes the log store over HTTP. When a LogRepo is attached it
// queries the persistent database; otherwise it falls back to the ring buffer.
type LogHandler struct {
	ring  *logbuf.Ring
	logs  *db.LogRepo    // optional persistent store
	dblog *db.LogHandler // optional; kept in sync with ring level
}

func NewLogHandler(ring *logbuf.Ring) *LogHandler {
	return &LogHandler{ring: ring}
}

// WithLogRepo attaches a persistent log repository so the handler queries the
// database when date/component/search filters are supplied.
func (h *LogHandler) WithLogRepo(logs *db.LogRepo) *LogHandler {
	h.logs = logs
	return h
}

// WithDBLogHandler stores a reference to the DB slog handler so that
// SetLevel propagates to it alongside the ring buffer.
func (h *LogHandler) WithDBLogHandler(dblog *db.LogHandler) *LogHandler {
	h.dblog = dblog
	return h
}

// parseLogFilter turns the shared /system/logs query string into a
// db.LogFilter. List and Export both call it so a downloaded export always
// matches the rows the on-screen table was showing — a second, hand-rolled
// copy of this parsing is exactly how the two drift apart.
//
// Query params:
//
//	level     — minimum level: debug | info | warn | error (default: info)
//	component — filter by component name
//	from      — RFC3339 start timestamp (inclusive)
//	to        — RFC3339 end timestamp (inclusive)
//	q         — full-text search in message + fields
//	limit     — max entries (default/ceiling supplied by the caller)
//	offset    — pagination offset (default: 0)
//
// Unparseable values fall back to the default rather than erroring, matching
// the endpoint's long-standing lenient behaviour.
func parseLogFilter(q url.Values, defaultLimit, maxLimit int) db.LogFilter {
	f := db.LogFilter{Level: slog.LevelInfo, Limit: defaultLimit}

	if l := q.Get("level"); l != "" {
		f.Level = parseLevel(l)
		f.HasLevel = true
	}
	if ls := q.Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			if n > maxLimit {
				n = maxLimit
			}
			f.Limit = n
		}
	}
	if os := q.Get("offset"); os != "" {
		if n, err := strconv.Atoi(os); err == nil && n >= 0 {
			f.Offset = n
		}
	}

	f.Component = q.Get("component")
	f.Q = q.Get("q")
	if s := q.Get("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.FromTS = t
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.ToTS = t
		}
	}
	return f
}

// List handles GET /api/v1/system/logs. See parseLogFilter for the query
// params. When no date range is given the DB-backed path defaults to the last
// hour; without a LogRepo attached it falls back to the in-memory ring buffer.
func (h *LogHandler) List(w http.ResponseWriter, r *http.Request) {
	f := parseLogFilter(r.URL.Query(), logListDefaultLimit, logListMaxLimit)

	if h.logs != nil {
		// Default to the last hour when no explicit date range is supplied.
		if f.FromTS.IsZero() && f.ToTS.IsZero() {
			f.FromTS = time.Now().UTC().Add(-time.Hour)
		}

		entries, err := h.logs.Query(r.Context(), f)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		if entries == nil {
			entries = []db.LogEntry{}
		}
		writeJSON(w, http.StatusOK, entries)
		return
	}

	// Ring buffer fallback (no DB attached).
	entries := h.ring.Snapshot(f.Level, f.Limit)
	if entries == nil {
		entries = []logbuf.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// Export handles GET /api/v1/system/logs/export.
//
// It accepts exactly the same filter params as List (see parseLogFilter) and
// streams the matching entries as a plain-text attachment. Plain text — not
// JSON — because the point of this endpoint is a file a user can paste into a
// GitHub issue (#1903); the line format mirrors what the Logs tab renders, so
// what you download is what you were looking at.
//
// Admin-only, mounted alongside List: the log stream is app-wide and carries
// other users' book titles, OIDC usernames and download names. Values are run
// through httpsec.RedactSecrets on the way out so a URL that captured an
// indexer apikey/token in a message or attr doesn't ride along into an issue
// attachment.
//
// The response is streamed page by page (logExportPageSize rows per query) and
// capped at logExportMaxRows so neither the server nor the client has to hold
// the whole log table in memory. Reaching the cap is reported in the footer,
// never silently.
func (h *LogHandler) Export(w http.ResponseWriter, r *http.Request) {
	f := parseLogFilter(r.URL.Query(), logExportMaxRows, logExportMaxRows)
	maxRows := f.Limit

	filename := "bindery-logs-" + time.Now().UTC().Format("20060102T150405Z") + ".txt"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	bw := &exportWriter{w: bufio.NewWriter(w)}
	defer func() { _ = bw.flush() }()

	if h.logs == nil {
		// Ring-buffer fallback (no persistent store attached). Ring.Snapshot
		// filters on level and limit ONLY — component, q, from and to are not
		// applied — so the header must describe what actually happened rather
		// than echoing the request. This file is meant to be pasted into public
		// issues; a header claiming "component=downloader" over a dump of the
		// whole buffer would send a maintainer chasing the wrong process.
		entries := h.ring.Snapshot(f.Level, maxRows)
		bw.header(ringFilter(f), maxRows, "in-memory ring buffer (no persistent log store)")
		if ignored := ignoredRingFilters(f); ignored != "" {
			bw.printf("# NOT applied (unsupported without a persistent log store): %s\n", ignored)
		}
		bw.str("# order: oldest first\n")
		bw.str("#\n")
		for _, e := range entries {
			bw.line(e.Time, e.Level, "", e.Msg, e.Attrs)
		}
		bw.printf("# %d entries\n", len(entries))
		if len(entries) >= maxRows {
			bw.printf("# hit the %d-entry limit — older buffered entries were not written\n", maxRows)
		}
		return
	}

	// Pin the upper bound of the window before paging so a line written
	// mid-export can't enter the result set halfway through the walk.
	explicitRange := !f.FromTS.IsZero() || !f.ToTS.IsZero()
	if f.ToTS.IsZero() {
		f.ToTS = time.Now().UTC()
	}
	// Same last-hour default as List — and on the same condition: only when
	// NEITHER bound was given. List leaves a to-only range unbounded below, so
	// deriving from=to-1h here produced a file covering one hour of the window
	// the table on screen was showing in full (#1903).
	if !explicitRange {
		f.FromTS = f.ToTS.Add(-time.Hour)
	}
	bw.header(f, maxRows, "")

	flusher, _ := w.(http.Flusher)
	page := f
	written := 0
	for written < maxRows {
		page.Limit = min(logExportPageSize, maxRows-written)

		entries, err := h.logs.Query(r.Context(), page)
		if err != nil {
			// Status and headers are already on the wire, so the only honest
			// signal left is an in-band marker: better a short file that says
			// it is short than a silently truncated one.
			bw.printf("# export failed after %d entries: %v\n", written, err)
			return
		}
		for _, e := range entries {
			bw.line(e.TS, e.Level, e.Component, e.Message, e.Fields)
		}
		written += len(entries)
		if len(entries) < page.Limit {
			break
		}
		// Walk by keyset, not OFFSET: the DB log writer is asynchronous, so a
		// record stamped inside the window can be INSERTed after an earlier page
		// was read and shift every later offset, repeating a row; pruning
		// mid-export skips rows the same way. Resuming from the last row read
		// is immune to both, and it is a linear index walk rather than
		// re-scanning and discarding `written` rows per page.
		last := entries[len(entries)-1]
		page.BeforeTS = last.TS
		page.BeforeID = last.ID
		page.Offset = 0
		if err := bw.flush(); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	bw.printf("# %d entries\n", written)
	if written >= maxRows {
		if r.URL.Query().Get("limit") != "" {
			// The ceiling was the caller's own limit param, so don't dress it
			// up as a system cap the way the server-imposed one is.
			bw.printf("# stopped at the requested limit of %d entries — there may be more\n", maxRows)
		} else {
			bw.printf("# reached the %d-entry export cap — there may be more; narrow the level, component, search or date range to reach them\n", maxRows)
		}
	}
}

// ringFilter strips the filter fields Ring.Snapshot does not implement, so the
// export header describes the file that was actually produced.
func ringFilter(f db.LogFilter) db.LogFilter {
	return db.LogFilter{Level: f.Level, HasLevel: f.HasLevel, Limit: f.Limit}
}

// ignoredRingFilters names the requested filters the ring-buffer path silently
// drops, for the warning line above the entries.
func ignoredRingFilters(f db.LogFilter) string {
	var parts []string
	if f.Component != "" {
		parts = append(parts, "component")
	}
	if f.Q != "" {
		parts = append(parts, "q")
	}
	if !f.FromTS.IsZero() {
		parts = append(parts, "from")
	}
	if !f.ToTS.IsZero() {
		parts = append(parts, "to")
	}
	return strings.Join(parts, ", ")
}

// exportWriter is the plain-text sink for Export. Per-write errors are dropped
// deliberately: bufio.Writer latches the first failure and every later call
// becomes a no-op, so a client that closed the connection mid-download surfaces
// once on flush instead of at every line, and there is no way to report it
// anyway once the 200 is on the wire.
type exportWriter struct{ w *bufio.Writer }

func (e *exportWriter) str(s string) { _, _ = e.w.WriteString(s) }
func (e *exportWriter) b(c byte)     { _ = e.w.WriteByte(c) }
func (e *exportWriter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.w, format, args...)
}
func (e *exportWriter) flush() error { return e.w.Flush() }

// header writes the comment block at the top of an export: when it was
// produced and exactly which filters produced it, so a file pasted into an
// issue is self-describing.
func (e *exportWriter) header(f db.LogFilter, maxRows int, source string) {
	e.str("# Bindery log export\n")
	e.printf("# generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	if source != "" {
		e.printf("# source: %s\n", source)
	}
	e.printf("# filters: %s\n", describeLogFilter(f))
	e.printf("# max entries: %d\n", maxRows)
	e.str("#\n")
}

// describeLogFilter renders the applied filters as a one-line summary. The
// operator-supplied values (component, search) go through exportValue like any
// log value: an admin who pasted a URL with an apikey into the search box would
// otherwise put it in the header of a file meant for a public issue, and a
// newline in either would break the comment block into forged log lines.
func describeLogFilter(f db.LogFilter) string {
	var parts []string
	if f.HasLevel {
		parts = append(parts, "level>="+f.Level.String())
	}
	if f.Component != "" {
		parts = append(parts, "component="+exportValue(f.Component))
	}
	if !f.FromTS.IsZero() {
		parts = append(parts, "from="+f.FromTS.UTC().Format(time.RFC3339))
	}
	if !f.ToTS.IsZero() {
		parts = append(parts, "to="+f.ToTS.UTC().Format(time.RFC3339))
	}
	if f.Q != "" {
		parts = append(parts, "q="+exportValue(f.Q))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// line renders one entry as
//
//	2026-08-12T14:03:22.123Z ERROR [importer] message key=value other="two words"
//
// which is the same shape the Logs tab shows on screen. Newlines are escaped so
// one entry is always one line (greppable, and a multi-line stack trace can't
// forge a fake record), and attrs are sorted so two exports of the same rows
// diff cleanly.
//
// EVERY part of the line goes through exportValue — level, component, attr keys
// and attr values, not just the message. All of them are code-supplied today
// (see db.LogHandler), so this is not an attacker-reachable escape; it is the
// promise the endpoint makes. A component carrying a newline would otherwise
// forge log lines in a file destined for a public issue, and an unquoted key
// containing a space or '=' would break the key=value parse for whoever greps
// it.
func (e *exportWriter) line(ts time.Time, level, component, msg string, fields map[string]string) {
	e.str(ts.UTC().Format("2006-01-02T15:04:05.000Z"))
	e.b(' ')
	e.str(exportValue(level))
	if component != "" {
		e.str(" [" + exportValue(component) + "]")
	}
	e.b(' ')
	e.str(exportValue(msg))

	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e.b(' ')
			e.str(exportToken(exportValue(k)))
			e.b('=')
			e.str(exportToken(exportValue(fields[k])))
		}
	}
	e.b('\n')
}

// exportToken quotes a key or value that would otherwise break the key=value
// shape of a log line.
func exportToken(s string) string {
	if s == "" || strings.ContainsAny(s, " \t=\"") {
		return strconv.Quote(s)
	}
	return s
}

// exportValue prepares a message or attr value for the text export: secrets
// stripped (an exported file is meant to be attached to a public issue, and a
// logged URL can carry an indexer apikey — see httpsec.RedactSecrets), and
// line breaks escaped so the one-entry-per-line contract holds.
func exportValue(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\n")
	return httpsec.RedactSecrets(s)
}

// SetLevel handles PUT /api/v1/system/loglevel
func (h *LogHandler) SetLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Level == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "level required"})
		return
	}
	level := parseLevel(req.Level)
	h.ring.SetLevel(level)
	if h.dblog != nil {
		h.dblog.SetLevel(level)
	}
	writeJSON(w, http.StatusOK, map[string]string{"level": level.String()})
}

// GetLevel handles GET /api/v1/system/loglevel
func (h *LogHandler) GetLevel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"level": h.ring.Level().String()})
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
