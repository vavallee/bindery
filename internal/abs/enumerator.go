// Package abs provides Audiobookshelf client, normalization, and import logic.
package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

const SettingABSImportCheckpoint = "abs.import_checkpoint"

// Default debounce thresholds for the checkpointer. The enumerator writes a
// per-item checkpoint into the singleton settings row so an interrupted import
// can resume near where it stopped. Writing on every item turned a 10k-book
// library into 10k fsync+WAL appends against one row (finding 13); debouncing
// to "every N items or every K seconds, whichever comes first" cuts that to a
// few hundred writes while keeping the resume window bounded.
//
// After a crash, the import may re-process up to checkpointMinItems items or
// checkpointMinInterval of work that the user already saw progress for. ABS
// imports are idempotent (upsertBook / resolveAuthor key off ForeignID via
// GetByForeignID and either Update or Create, never insert-only audit rows
// keyed by run), so replaying a handful of items is harmless.
const (
	checkpointMinItems    = 100
	checkpointMinInterval = 5 * time.Second
)

type enumerationClient interface {
	ListLibraryItems(ctx context.Context, libraryID string, page, limit int) (*LibraryItemsPage, error)
	GetLibraryItem(ctx context.Context, itemID string) (*LibraryItem, error)
}

type EnumerationStats struct {
	PagesScanned       int `json:"pagesScanned"`
	ItemsSeen          int `json:"itemsSeen"`
	ItemsNormalized    int `json:"itemsNormalized"`
	ItemsDetailFetched int `json:"itemsDetailFetched"`
}

type ImportCheckpoint struct {
	LibraryID  string    `json:"libraryId"`
	Page       int       `json:"page"`
	LastItemID string    `json:"lastItemId,omitempty"`
	PageSize   int       `json:"pageSize"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type Enumerator struct {
	client        enumerationClient
	settings      *db.SettingsRepo
	pageSize      int
	checkpointKey string
	onCheckpoint  func(ImportCheckpoint)
	minItems      int
	minInterval   time.Duration
	now           func() time.Time
}

func NewEnumerator(client enumerationClient, settings *db.SettingsRepo, pageSize int) *Enumerator {
	if pageSize <= 0 {
		pageSize = 50
	}
	return &Enumerator{
		client:        client,
		settings:      settings,
		pageSize:      pageSize,
		checkpointKey: SettingABSImportCheckpoint,
		minItems:      checkpointMinItems,
		minInterval:   checkpointMinInterval,
		now:           time.Now,
	}
}

func (e *Enumerator) WithCheckpointObserver(fn func(ImportCheckpoint)) *Enumerator {
	e.onCheckpoint = fn
	return e
}

// WithCheckpointDebounce overrides the default debounce thresholds. A
// non-positive minItems or minInterval falls back to the package default.
// Intended for tests; production code should rely on the defaults.
func (e *Enumerator) WithCheckpointDebounce(minItems int, minInterval time.Duration) *Enumerator {
	if minItems > 0 {
		e.minItems = minItems
	}
	if minInterval > 0 {
		e.minInterval = minInterval
	}
	return e
}

// WithClock injects a clock for tests. Production callers should not use this.
func (e *Enumerator) WithClock(now func() time.Time) *Enumerator {
	if now != nil {
		e.now = now
	}
	return e
}

// checkpointer debounces calls to writeFn: it writes when at least minItems
// candidates have been offered since the last write OR at least minInterval
// has elapsed, whichever fires first. flush() forces the most recent pending
// candidate to disk regardless of either threshold. A nil pending candidate
// means nothing has been offered since the last write, so flush is a no-op.
type checkpointer struct {
	now             func() time.Time
	minInterval     time.Duration
	minItems        int
	writeFn         func(context.Context, ImportCheckpoint) error
	pending         *ImportCheckpoint
	itemsSinceWrite int
	lastWriteAt     time.Time
}

func newCheckpointer(minItems int, minInterval time.Duration, now func() time.Time, writeFn func(context.Context, ImportCheckpoint) error) *checkpointer {
	if now == nil {
		now = time.Now
	}
	return &checkpointer{
		now:         now,
		minInterval: minInterval,
		minItems:    minItems,
		writeFn:     writeFn,
		lastWriteAt: now(),
	}
}

// offer records a new candidate checkpoint and writes it if either threshold
// is met. It is safe to call once per processed item.
func (c *checkpointer) offer(ctx context.Context, cp ImportCheckpoint) error {
	c.pending = &cp
	c.itemsSinceWrite++
	if c.itemsSinceWrite < c.minItems && c.now().Sub(c.lastWriteAt) < c.minInterval {
		return nil
	}
	return c.flush(ctx)
}

// flush writes the pending candidate (if any) regardless of thresholds and
// resets the counters. Callers MUST call flush when the enumeration ends
// (whether on success or on an error returned by the per-item callback) so the
// final position is durable.
func (c *checkpointer) flush(ctx context.Context) error {
	if c.pending == nil {
		return nil
	}
	cp := *c.pending
	if err := c.writeFn(ctx, cp); err != nil {
		return err
	}
	c.pending = nil
	c.itemsSinceWrite = 0
	c.lastWriteAt = c.now()
	return nil
}

func (e *Enumerator) Enumerate(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
	var stats EnumerationStats
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return stats, errors.New("library_id is required")
	}

	checkpoint, err := e.loadCheckpoint(ctx)
	if err != nil {
		return stats, err
	}
	page := 0
	skipUntilID := ""
	if checkpoint != nil && checkpoint.LibraryID == libraryID {
		page = checkpoint.Page
		skipUntilID = checkpoint.LastItemID
	}

	cp := newCheckpointer(e.minItems, e.minInterval, e.now, e.saveCheckpoint)
	// Always flush the most recent pending checkpoint on return so resume from
	// a crash, a context cancel, or a callback error lands at the last
	// successfully processed item rather than discarding the partial progress
	// captured since the last debounced write.
	flushOnReturn := func() {
		if flushErr := cp.flush(ctx); flushErr != nil {
			slog.Warn("abs enumerate: flush checkpoint failed", "libraryID", libraryID, "error", flushErr)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			flushOnReturn()
			return stats, err
		}

		resp, err := e.client.ListLibraryItems(ctx, libraryID, page, e.pageSize)
		if err != nil {
			flushOnReturn()
			return stats, err
		}
		if err := validateBookLibraryPage(libraryID, resp); err != nil {
			flushOnReturn()
			return stats, err
		}
		stats.PagesScanned++
		slog.Info("abs enumerate page",
			"libraryID", libraryID,
			"page", page,
			"limit", resp.Limit,
			"results", len(resp.Results),
			"total", resp.Total)

		if len(resp.Results) == 0 {
			break
		}

		startIndex := 0
		if skipUntilID != "" {
			found := false
			for idx, item := range resp.Results {
				if item.ID == skipUntilID {
					startIndex = idx + 1
					found = true
					break
				}
			}
			if !found {
				slog.Warn("abs checkpoint item not found on resume; reprocessing page",
					"libraryID", libraryID,
					"page", page,
					"lastItemID", skipUntilID)
			}
			skipUntilID = ""
		}

		for _, item := range resp.Results[startIndex:] {
			stats.ItemsSeen++
			reasons := item.DetailFetchReasons()
			detailFetched := len(reasons) > 0
			if detailFetched {
				slog.Info("abs enumerate detail fetch",
					"libraryID", libraryID,
					"itemID", item.ID,
					"reasons", reasons)
				detail, err := e.client.GetLibraryItem(ctx, item.ID)
				if err != nil {
					flushOnReturn()
					return stats, err
				}
				item = MergeLibraryItem(item, *detail)
				stats.ItemsDetailFetched++
			}

			normalized := NormalizeLibraryItem(item, detailFetched)
			if err := fn(ctx, normalized); err != nil {
				flushOnReturn()
				return stats, err
			}
			stats.ItemsNormalized++
			if err := cp.offer(ctx, ImportCheckpoint{
				LibraryID:  libraryID,
				Page:       page,
				LastItemID: item.ID,
				PageSize:   e.pageSize,
				UpdatedAt:  e.now().UTC(),
			}); err != nil {
				return stats, err
			}
		}

		page++
		// Page boundary: offer a checkpoint that records the next page with no
		// LastItemID so resume picks up at the top of the new page. The
		// debounce still applies here. Final correctness is guaranteed by the
		// flushOnReturn at the end of the function.
		if err := cp.offer(ctx, ImportCheckpoint{
			LibraryID: libraryID,
			Page:      page,
			PageSize:  e.pageSize,
			UpdatedAt: e.now().UTC(),
		}); err != nil {
			return stats, err
		}

		limit := resp.Limit
		if limit <= 0 {
			limit = e.pageSize
		}
		if limit <= 0 || len(resp.Results) < limit || page*limit >= resp.Total {
			break
		}
	}

	if err := e.clearCheckpoint(ctx); err != nil {
		flushOnReturn()
		return stats, err
	}
	// Successful completion: drop the pending checkpoint without writing,
	// since clearCheckpoint already wiped the stored value.
	cp.pending = nil
	slog.Info("abs enumerate complete",
		"libraryID", libraryID,
		"pagesScanned", stats.PagesScanned,
		"itemsSeen", stats.ItemsSeen,
		"itemsNormalized", stats.ItemsNormalized,
		"itemsDetailFetched", stats.ItemsDetailFetched)
	return stats, nil
}

func validateBookLibraryPage(libraryID string, resp *LibraryItemsPage) error {
	if resp == nil {
		return errors.New("abs library items response is empty")
	}
	if mediaType := strings.TrimSpace(resp.MediaType); mediaType != "" && mediaType != "book" {
		return fmt.Errorf("library %q is %q, expected book", libraryID, mediaType)
	}
	for _, item := range resp.Results {
		if mediaType := strings.TrimSpace(item.MediaType); mediaType != "" && mediaType != "book" {
			return fmt.Errorf("library %q contains %q item %q, expected book", libraryID, mediaType, item.ID)
		}
	}
	return nil
}

func (e *Enumerator) loadCheckpoint(ctx context.Context) (*ImportCheckpoint, error) {
	if e.settings == nil {
		return nil, nil
	}
	setting, err := e.settings.Get(ctx, e.checkpointKey)
	if err != nil {
		return nil, err
	}
	if setting == nil || strings.TrimSpace(setting.Value) == "" {
		return nil, nil
	}
	var checkpoint ImportCheckpoint
	if err := json.Unmarshal([]byte(setting.Value), &checkpoint); err != nil {
		return nil, fmt.Errorf("decode abs checkpoint: %w", err)
	}
	return &checkpoint, nil
}

func (e *Enumerator) saveCheckpoint(ctx context.Context, checkpoint ImportCheckpoint) error {
	if e.settings == nil {
		if e.onCheckpoint != nil {
			e.onCheckpoint(checkpoint)
		}
		return nil
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode abs checkpoint: %w", err)
	}
	if err := e.settings.Set(ctx, e.checkpointKey, string(payload)); err != nil {
		return err
	}
	if e.onCheckpoint != nil {
		e.onCheckpoint(checkpoint)
	}
	return nil
}

func (e *Enumerator) clearCheckpoint(ctx context.Context) error {
	if e.settings == nil {
		return nil
	}
	return e.settings.Delete(ctx, e.checkpointKey)
}
