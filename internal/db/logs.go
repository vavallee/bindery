package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LogEntry is a single persisted log record.
type LogEntry struct {
	ID        int64             `json:"id"`
	TS        time.Time         `json:"ts"`
	Level     string            `json:"level"`
	Component string            `json:"component"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields"`
}

// LogFilter controls which rows are returned by LogRepo.Query.
type LogFilter struct {
	Level     slog.Level
	HasLevel  bool
	Component string
	FromTS    time.Time
	ToTS      time.Time
	Q         string
	Limit     int
	Offset    int
}

// LogRepo reads and writes the logs table.
type LogRepo struct {
	db *sql.DB
}

func NewLogRepo(db *sql.DB) *LogRepo {
	return &LogRepo{db: db}
}

// Insert persists a single log entry.
func (r *LogRepo) Insert(ctx context.Context, e LogEntry) error {
	fields := e.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	blob, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal log fields: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO logs (ts, level, component, message, fields) VALUES (?, ?, ?, ?, ?)`,
		e.TS.UTC(), e.Level, e.Component, e.Message, string(blob),
	)
	return err
}

// Query returns log entries matching the filter, newest-first by default.
func (r *LogRepo) Query(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	var conds []string
	var args []any

	if f.HasLevel {
		conds = append(conds, "level IN ("+levelPlaceholders(f.Level)+")")
		args = append(args, levelNames(f.Level)...)
	}
	if f.Component != "" {
		conds = append(conds, "component = ?")
		args = append(args, f.Component)
	}
	if !f.FromTS.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.FromTS.UTC())
	}
	if !f.ToTS.IsZero() {
		conds = append(conds, "ts <= ?")
		args = append(args, f.ToTS.UTC())
	}
	if f.Q != "" {
		conds = append(conds, "(message LIKE ? OR fields LIKE ?)")
		pattern := "%" + strings.ReplaceAll(f.Q, "%", "\\%") + "%"
		args = append(args, pattern, pattern)
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit, f.Offset)

	//nolint:gosec // where clause is built from static strings + parameterised placeholders
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, level, component, message, fields FROM logs`+where+
			` ORDER BY ts DESC LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var tsStr string
		var fieldsJSON string
		if err := rows.Scan(&e.ID, &tsStr, &e.Level, &e.Component, &e.Message, &fieldsJSON); err != nil {
			return nil, fmt.Errorf("scan log row: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			e.TS = t
		} else if t, err := time.Parse("2006-01-02T15:04:05Z", tsStr); err == nil {
			e.TS = t
		} else if t, err := time.Parse("2006-01-02 15:04:05", tsStr); err == nil {
			e.TS = t
		} else {
			e.TS = time.Now()
		}
		if fieldsJSON != "" {
			_ = json.Unmarshal([]byte(fieldsJSON), &e.Fields)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Trim deletes entries older than cutoff.
func (r *LogRepo) Trim(ctx context.Context, cutoff time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM logs WHERE ts < ?`, cutoff.UTC())
	return err
}

// levelPlaceholders returns a SQL IN-list placeholder string like "(?,?,?)".
func levelPlaceholders(minLevel slog.Level) string {
	names := levelNames(minLevel)
	return "?" + strings.Repeat(",?", len(names)-1)
}

// levelNames returns the level name strings for levels >= minLevel.
func levelNames(minLevel slog.Level) []any {
	all := []struct {
		level slog.Level
		name  string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
	}
	var out []any
	for _, v := range all {
		if v.level >= minLevel {
			out = append(out, v.name)
		}
	}
	return out
}
