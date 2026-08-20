// Package migrate bulk-imports authors and related records into Bindery
// from external sources — a Readarr SQLite DB or a plain CSV of names.
// Callers drive the work; this package doesn't set up HTTP or CLI surface.
package migrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// catalogueFetchConcurrency bounds how many newly-imported authors' catalogue
// fetches run at once, and catalogueFetchPaceInterval additionally throttles
// how fast new ones start. A bulk CSV import used to fire one unbounded
// goroutine per author (`go onCatalogueFetch(full)` with no cap at all), so a
// 19-author CSV opened 19 simultaneous OpenLibrary (and, until #2075's other
// fixes, Hardcover) fetches, and reliably tipped OpenLibrary into 429s that
// cascaded into timeouts and connection refusals with nothing to back off.
//
// This bound is deliberately lower than the 4 used for other provider fan-out
// (authorAutoSearchConcurrency, searchPaceInterval): each author's own
// catalogue fetch already internally fans out up to
// openlibrary.authorWorkSampleConcurrency (4) concurrent edition-sample
// requests, so the two stack — at 4 authors here that's up to 16 concurrent
// OpenLibrary requests in flight, not 4. 2 keeps the realistic worst case (up
// to 8) meaningfully lower while still importing faster than one author at a
// time.
const catalogueFetchConcurrency = 2

// catalogueFetchPaceInterval is a var, not a const, so tests can zero it out
// (matching internal/api's searchPaceInterval convention) rather than waiting
// out real pacing delays.
var catalogueFetchPaceInterval = 3 * time.Second

// dispatchCatalogueFetch fans newlyAdded out to onFetch — one call per
// author — bounded to catalogueFetchConcurrency in flight and paced
// catalogueFetchPaceInterval apart, via concurrency.RunBoundedPaced. Shared
// by ImportCSVAuthors and importReadarrAuthors so the launch semantics (in
// particular, dispatching before any later error is allowed to skip it — see
// the caller in readarr.go) can't drift between the two independently.
//
// bgCtx uses context.WithoutCancel rather than ctx directly. This is not
// working around anything the old `go onCatalogueFetch(full)`-style code was
// exposed to — onFetch takes no ctx at all (it's ultimately
// AuthorHandler.FetchAuthorBooks(author, false, ""), which manages its own
// lifetime), so the caller's request context never reached it either way.
// It's RunBoundedPaced itself that reads ctx, to gate its own dispatch loop
// (the concurrency semaphore and the pacing wait). Passing the caller's
// request ctx straight in would mean net/http cancelling it — which happens
// shortly after ImportCSVAuthors/importReadarrAuthors return and the handler
// writes its response — stops the loop from *launching* the rest of the
// batch, silently leaving later authors in a large import with no catalogue
// at all.
func dispatchCatalogueFetch(ctx context.Context, newlyAdded []*models.Author, onFetch func(*models.Author)) {
	if onFetch == nil || len(newlyAdded) == 0 {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	go concurrency.RunBoundedPaced(bgCtx, newlyAdded, catalogueFetchConcurrency, catalogueFetchPaceInterval,
		func(_ context.Context, a *models.Author) { onFetch(a) })
}

// Result summarises an import run for UI/CLI display.
type Result struct {
	Requested  int               `json:"requested"`
	Added      int               `json:"added"`
	Skipped    int               `json:"skipped"`
	Errors     int               `json:"errors"`
	AddedNames []string          `json:"addedNames,omitempty"`
	Failures   map[string]string `json:"failures,omitempty"` // name → reason
}

func newResult() *Result {
	return &Result{Failures: map[string]string{}}
}

func (r *Result) fail(name, reason string) {
	r.Errors++
	if r.Failures == nil {
		r.Failures = map[string]string{}
	}
	r.Failures[name] = reason
}

// ImportCSVAuthors bulk-adds authors from a CSV or newline-separated list.
// Input formats accepted, one per row:
//   - Plain name only:          "Andy Weir"
//   - Two-column CSV:           "Andy Weir,true"        (name, monitored)
//
// A third column, if present, is ignored for backward compatibility (#966):
// it was a "searchOnAdd" flag that, after #963, no longer gated anything —
// every author's catalogue is now fetched on add, and the fetch never
// auto-grabs. Legacy three-column files therefore still import unchanged.
//
// Each name is resolved via OpenLibrary SearchAuthors; the top match is
// created. Duplicates (same foreign ID already in DB) are skipped rather
// than errored.
//
// onCatalogueFetch is invoked for EVERY newly-created author so the catalogue
// is always populated (mirrors the Readarr migrate path and the AddAuthor UI).
// The wired callback is FetchAuthorBooks(author, false, "") which fetches the
// catalogue but never auto-grabs, so populating on every row is safe — an
// empty catalogue otherwise leaves the library scan with no book rows to match
// files against, and the user's library looks empty after import.
// settings supplies the install-wide author monitor defaults (#1666). A CSV row
// carries a monitored flag but never a monitor *mode*, and this is the path
// that queued ~1250 books for one user: without the default the author lands in
// mode "all", the catalogue fetch below monitors every work, and the scheduler
// grabs the lot.
func ImportCSVAuthors(
	ctx context.Context,
	reader io.Reader,
	authors *db.AuthorRepo,
	settings *db.SettingsRepo,
	agg *metadata.Aggregator,
	onCatalogueFetch func(author *models.Author),
) (*Result, error) {
	res := newResult()
	if reader == nil {
		return res, errors.New("reader is nil")
	}

	rows, err := parseCSVRows(reader)
	if err != nil {
		return res, err
	}
	res.Requested = len(rows)

	var newlyAdded []*models.Author

	for _, row := range rows {
		name := row.name
		if name == "" {
			continue
		}

		// Resolve via OpenLibrary. Top match wins.
		matches, err := agg.SearchAuthors(ctx, name)
		if err != nil {
			slog.Warn("csv import: search failed", "name", name, "error", err)
			res.fail(name, "metadata lookup failed: "+err.Error())
			continue
		}
		if len(matches) == 0 {
			res.fail(name, "no OpenLibrary match")
			continue
		}
		top := matches[0]

		// Skip if already present.
		existing, _ := authors.GetByAnyForeignID(ctx, top.ForeignID)
		if existing != nil {
			res.Skipped++
			continue
		}

		// Fetch full metadata (description, image) — soft-fail if it errors.
		full, ferr := agg.GetAuthor(ctx, top.ForeignID)
		if ferr != nil || full == nil {
			full = &top
		}
		full.Monitored = row.monitored
		full.MetadataProvider = "openlibrary"
		db.ApplyAuthorMonitorDefaults(ctx, settings, full)

		if err := authors.Create(ctx, full); err != nil {
			if isAuthorCreateConflict(err) {
				if existing, _ := authors.GetByAnyForeignID(ctx, full.ForeignID); existing != nil {
					res.Skipped++
					continue
				}
			}
			res.fail(name, err.Error())
			continue
		}
		res.Added++
		res.AddedNames = append(res.AddedNames, full.Name)
		newlyAdded = append(newlyAdded, full)
	}

	// Always populate the catalogue for every newly-created author — the
	// callback fetches metadata but never auto-grabs (see func doc). An
	// empty catalogue would leave the library scan nothing to match against.
	dispatchCatalogueFetch(ctx, newlyAdded, onCatalogueFetch)

	return res, nil
}

type csvRow struct {
	name      string
	monitored bool
}

// utf8BOM is the UTF-8 encoding of U+FEFF. Excel, Google Sheets and Numbers all
// write it at the head of a CSV they save as UTF-8, and it is not data: left in
// place it becomes part of the first cell, so a header cell reads "\ufeffname"
// and no longer matches the header names we skip on. The header row then gets
// imported as if it were an author, and the resulting metadata search burns a
// provider lookup on a name that does not exist (#2075).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripBOM removes a single leading UTF-8 BOM. Only at the very start, and only
// one: a BOM anywhere else is a legitimate (if odd) character in a cell.
func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, utf8BOM)
}

// skipBOM wraps a reader so a leading UTF-8 BOM is consumed before the caller
// sees any bytes. For readers parsed as a stream rather than read whole.
func skipBOM(reader io.Reader) io.Reader {
	br := bufio.NewReader(reader)
	if prefix, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(prefix, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	return br
}

func parseCSVRows(reader io.Reader) ([]csvRow, error) {
	// Read everything upfront. bufio.ReadLine returns a slice into its internal
	// buffer; any subsequent read reuses that buffer and would corrupt the slice
	// before we copy it (CVE-style bug found by fuzzer: input whose first line
	// exceeds 4096 bytes triggered exactly this overwrite).
	all, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	// Strip before anything inspects the bytes, including the first-line comma
	// check below, so both the CSV branch and the bare-name branch are covered.
	all = stripBOM(all)
	if len(all) == 0 {
		return nil, nil
	}

	// Inspect the first line to decide between CSV and plain list. If it
	// contains a comma, use encoding/csv (handles quoted names with commas);
	// otherwise treat each line as a bare author name.
	firstNewline := bytes.IndexByte(all, '\n')
	var firstLine []byte
	if firstNewline == -1 {
		firstLine = all
	} else {
		firstLine = all[:firstNewline]
	}
	hasComma := bytes.ContainsRune(firstLine, ',')

	if hasComma {
		records, err := csv.NewReader(bytes.NewReader(all)).ReadAll()
		if err != nil {
			return nil, err
		}
		// Common header field names — if the first record's first cell matches
		// one of these, the row is a column-label header and must be skipped.
		headerFields := map[string]bool{
			"name": true, "author": true, "author name": true,
			"monitored": true, "searchonadd": true,
		}
		out := make([]csvRow, 0, len(records))
		for i, rec := range records {
			if i == 0 && len(rec) > 0 && headerFields[strings.ToLower(strings.TrimSpace(rec[0]))] {
				continue
			}
			out = append(out, rowFromFields(rec))
		}
		return out, nil
	}

	var out []csvRow
	for _, line := range strings.Split(string(all), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, csvRow{name: line, monitored: true})
	}
	return out, nil
}

func rowFromFields(fields []string) csvRow {
	// Only the first two columns (name, monitored) carry meaning. A third
	// column is tolerated but ignored for backward compatibility (#966): it was
	// a "searchOnAdd" flag that no longer gates anything after #963. Leaving the
	// parse lenient means existing users' three-column files keep importing.
	row := csvRow{monitored: true}
	if len(fields) >= 1 {
		row.name = strings.TrimSpace(fields[0])
	}
	if len(fields) >= 2 {
		row.monitored = parseBool(fields[1], true)
	}
	return row
}

func parseBool(s string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y", "t":
		return true
	case "false", "0", "no", "n", "f":
		return false
	}
	return fallback
}
