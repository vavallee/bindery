// Package migrate bulk-imports authors and related records into Bindery
// from external sources — a Readarr SQLite DB or a plain CSV of names.
// Callers drive the work; this package doesn't set up HTTP or CLI surface.
package migrate

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

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
//   - Two-column CSV:           "Andy Weir,true"
//   - Three-column CSV:         "Andy Weir,true,true"   (monitored, searchOnAdd)
//
// Each name is resolved via OpenLibrary SearchAuthors; the top match is
// created. Duplicates (same foreign ID already in DB) are skipped rather
// than errored. searchOnAdd=true triggers the same async book-fetch as
// the AddAuthor UI, via onSearchOnAdd.
func ImportCSVAuthors(
	ctx context.Context,
	reader io.Reader,
	authors *db.AuthorRepo,
	agg *metadata.Aggregator,
	onSearchOnAdd func(author *models.Author),
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
		existing, _ := authors.GetByForeignID(ctx, top.ForeignID)
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

		if err := authors.Create(ctx, full); err != nil {
			res.fail(name, err.Error())
			continue
		}
		res.Added++
		res.AddedNames = append(res.AddedNames, full.Name)

		if row.searchOnAdd && onSearchOnAdd != nil {
			go onSearchOnAdd(full)
		}
	}

	return res, nil
}

type csvRow struct {
	name        string
	monitored   bool
	searchOnAdd bool
}

func parseCSVRows(reader io.Reader) ([]csvRow, error) {
	// Peek the first non-empty line to decide between CSV and plain list.
	// If it contains a comma, use encoding/csv (handles quoted names with
	// commas); otherwise treat each line as a bare author name.
	buf := bufio.NewReader(reader)
	first, _, err := buf.ReadLine()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	hasComma := strings.Contains(string(first), ",")

	// Push the first line back in front of the rest.
	remaining, _ := io.ReadAll(buf)
	combined := append(append([]byte{}, first...), '\n')
	combined = append(combined, remaining...)

	if hasComma {
		records, err := csv.NewReader(strings.NewReader(string(combined))).ReadAll()
		if err != nil {
			return nil, err
		}
		out := make([]csvRow, 0, len(records))
		for _, rec := range records {
			out = append(out, rowFromFields(rec))
		}
		return out, nil
	}

	var out []csvRow
	for _, line := range strings.Split(string(combined), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, csvRow{name: line, monitored: true, searchOnAdd: false})
	}
	return out, nil
}

func rowFromFields(fields []string) csvRow {
	// Default searchOnAdd to false — bulk imports should be safe by default.
	// Users can opt in per-row by passing a third column "true".
	row := csvRow{monitored: true, searchOnAdd: false}
	if len(fields) >= 1 {
		row.name = strings.TrimSpace(fields[0])
	}
	if len(fields) >= 2 {
		row.monitored = parseBool(fields[1], true)
	}
	if len(fields) >= 3 {
		row.searchOnAdd = parseBool(fields[2], true)
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
