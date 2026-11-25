package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

const logHandlerBufSize = 512

// The drain goroutine writes the channel to SQLite in batches, one transaction
// per batch, instead of one autocommit per line. A batch closes when it holds
// logBatchMaxRows entries or logBatchMaxWait has passed since its first entry,
// whichever comes first. Tradeoff: after a hard crash (SIGKILL, power loss) up
// to logBatchMaxWait of the most recent lines can be missing from the logs
// table. A graceful shutdown flushes through Stop, and the in-memory ring and
// stdout copies are unaffected.
const (
	logBatchMaxRows = 100
	logBatchMaxWait = 250 * time.Millisecond
)

// logStderr is where write failures are reported. A variable so tests can
// capture it.
var logStderr io.Writer = os.Stderr

// LogHandler is a slog.Handler that persists records to the database via a
// buffered channel. If the channel is full the record is silently dropped —
// the handler must never block the request path. If a batch cannot be written
// the entries are retried one by one and the first failure of a run is reported
// once on stderr.
type LogHandler struct {
	repo     *LogRepo
	ch       chan LogEntry
	preAttrs []slog.Attr
	// levelVar is shared (by pointer) between the root handler and all children
	// created via WithAttrs, so a single SetLevel call propagates everywhere.
	levelVar *atomic.Int64
	// wg is non-nil only on the root handler returned by NewLogSlogHandler.
	// Child handlers from WithAttrs share repo+ch but must not own the wg or
	// the close-channel responsibility.
	wg *sync.WaitGroup
	// batches counts transactions the drain goroutine has attempted. Tests read
	// it to prove that a burst of records is not written one commit per line.
	batches atomic.Int64
	// maxRows and maxWait are the batch limits; fields so tests can shrink them.
	maxRows int
	maxWait time.Duration
}

// NewLogSlogHandler returns a non-blocking slog.Handler backed by repo.
// Call Stop to flush in-flight entries and shut the drain goroutine down.
func NewLogSlogHandler(repo *LogRepo, minLevel slog.Level) *LogHandler {
	return newLogHandler(repo, minLevel, logBatchMaxRows, logBatchMaxWait)
}

// newLogHandler is NewLogSlogHandler with explicit batch limits, for tests.
func newLogHandler(repo *LogRepo, minLevel slog.Level, maxRows int, maxWait time.Duration) *LogHandler {
	lv := &atomic.Int64{}
	lv.Store(int64(minLevel))
	h := &LogHandler{
		repo:     repo,
		ch:       make(chan LogEntry, logHandlerBufSize),
		levelVar: lv,
		wg:       &sync.WaitGroup{},
		maxRows:  maxRows,
		maxWait:  maxWait,
	}
	h.wg.Add(1)
	go h.drain()
	return h
}

// SetLevel changes the minimum level recorded to the database. Safe to call
// concurrently; takes effect immediately for both the root handler and any
// child handlers created via WithAttrs.
func (h *LogHandler) SetLevel(l slog.Level) {
	h.levelVar.Store(int64(l))
}

// Level returns the current minimum level.
func (h *LogHandler) Level() slog.Level {
	return slog.Level(h.levelVar.Load())
}

func (h *LogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.Level()
}

func (h *LogHandler) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level < h.Level() {
		return nil
	}

	// Send on a closed channel panics — even with the default branch in the
	// select below (default fires when the channel is full, not when closed).
	// Stop closes h.ch on shutdown, after which any late Handle call would
	// otherwise crash the process. Recover and drop instead.
	defer func() { _ = recover() }()

	component := ""
	fields := map[string]string{}

	for _, a := range h.preAttrs {
		if a.Key == "component" {
			component = fmt.Sprintf("%v", a.Value.Any())
		} else if a.Key != "" {
			fields[a.Key] = fmt.Sprintf("%v", a.Value.Any())
		}
	}

	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "component" {
			component = fmt.Sprintf("%v", a.Value.Any())
		} else if a.Key != "" {
			fields[a.Key] = fmt.Sprintf("%v", a.Value.Any())
		}
		return true
	})

	// Secrets are stripped before the row is written: a logged URL can carry
	// an indexer apikey or a userinfo password, and the logs table is part of
	// every backup. Redaction is idempotent, so the read path may repeat it.
	for k, v := range fields {
		fields[k] = httpsec.RedactSecrets(v)
	}
	e := LogEntry{
		TS:        rec.Time,
		Level:     rec.Level.String(),
		Component: component,
		Message:   httpsec.RedactSecrets(rec.Message),
		Fields:    fields,
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}

	// Non-blocking send — drop if full.
	select {
	case h.ch <- e:
	default:
	}
	return nil
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := append(append([]slog.Attr{}, h.preAttrs...), attrs...)
	// Share levelVar so SetLevel on the root propagates to all children.
	// Intentionally do NOT propagate wg: only the root handler owns shutdown.
	return &LogHandler{repo: h.repo, ch: h.ch, preAttrs: combined, levelVar: h.levelVar, maxRows: h.maxRows, maxWait: h.maxWait}
}

func (h *LogHandler) WithGroup(_ string) slog.Handler {
	return h
}

// drain collects entries into batches and writes each batch in one
// transaction. It blocks on the channel while idle, so there is no timer or
// wakeup when nothing is logged, and it holds no database connection while it
// waits for a batch to fill: the transaction only begins once the batch is
// closed. Entries keep their channel order. When the channel is closed (Stop)
// the batch in hand is written before the goroutine exits.
func (h *LogHandler) drain() {
	if h.wg != nil {
		defer h.wg.Done()
	}
	batch := make([]LogEntry, 0, h.maxRows)
	timer := time.NewTimer(h.maxWait)
	defer timer.Stop()
	failing := false
	for {
		first, ok := <-h.ch
		if !ok {
			return
		}
		batch = append(batch[:0], first)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(h.maxWait)
		closed := false
	collect:
		for len(batch) < h.maxRows {
			select {
			case e, ok := <-h.ch:
				if !ok {
					closed = true
					break collect
				}
				batch = append(batch, e)
			case <-timer.C:
				break collect
			}
		}
		h.flush(batch, &failing)
		if closed {
			return
		}
	}
}

// flush writes batch in one transaction. If the transaction fails, the entries
// are retried one at a time so a single bad row does not take the rest of the
// batch with it. The first failure of a run is reported on stderr (not slog:
// that would feed straight back into this handler); the report repeats only
// after a successful write has ended the run.
func (h *LogHandler) flush(batch []LogEntry, failing *bool) {
	h.batches.Add(1)
	ctx := context.Background()
	err := h.repo.InsertBatch(ctx, batch)
	if err == nil {
		*failing = false
		return
	}
	lost := 0
	var lastErr error
	for _, e := range batch {
		if ierr := h.repo.Insert(ctx, e); ierr != nil {
			lost++
			lastErr = ierr
		}
	}
	if lost == 0 {
		*failing = false
		return
	}
	if !*failing {
		*failing = true
		_, _ = fmt.Fprintf(logStderr, "bindery: log writer dropped %d of %d entries: batch error: %v; last row error: %v\n",
			lost, len(batch), err, lastErr)
	}
}

// Stop closes the log channel and blocks until the drain goroutine has
// flushed any in-flight entries. Safe to call once on the root handler;
// calling on a child returned by WithAttrs is a no-op. ctx bounds the wait;
// if it expires before drain finishes, Stop returns ctx.Err and the
// remaining entries are abandoned.
func (h *LogHandler) Stop(ctx context.Context) error {
	if h.wg == nil {
		// Child handler — no shutdown responsibility.
		return nil
	}
	close(h.ch)
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
