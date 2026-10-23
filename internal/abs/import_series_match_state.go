package abs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const (
	seriesMatchOutcomeMatched = "matched"
	seriesMatchOutcomeMiss    = "miss"

	// seriesMatchMissTTL is how long a recorded miss is trusted. Hardcover
	// gains series and volumes slowly, so a nightly import re-asking once a
	// month is plenty, while retrying on every run is the 3,800 item walk
	// of #2173. Matched items have no expiry: a link, once made, is not
	// something a later search improves on, and a changed item re-matches via
	// the input hash instead.
	seriesMatchMissTTL = 30 * 24 * time.Hour

	// seriesMatchMissJitter spreads expiry across a week. A library imported
	// for the first time records every miss within a few hours, so without
	// it they would all fall due on the same night and rebuild the storm.
	seriesMatchMissJitter = 7 * 24 * time.Hour

	// seriesMatchVersion is folded into the input hash. Bump it when the
	// matching rules change in a way that should revisit recorded outcomes.
	seriesMatchVersion = "1"
)

// seriesMatchInputHash digests everything the Hardcover series match reads for
// one item, so an item whose title, author, series or linked book changed no
// longer agrees with the outcome recorded for it. It deliberately ignores
// fields the match never looks at (description, files, durations).
func seriesMatchInputHash(item NormalizedLibraryItem, queries []hardcoverSeriesQuery, authorName string, bookID int64) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "v%s\x00%d\x00%s\x00%s\x00", seriesMatchVersion, bookID, strings.TrimSpace(authorName), strings.TrimSpace(item.Title))
	for _, q := range queries {
		_, _ = fmt.Fprintf(h, "q\x00%s\x00%s\x00%t\x00", q.Title, q.Sequence, q.FromABS)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (i *Importer) seriesMatchNow() time.Time {
	if i.seriesMatchClock != nil {
		return i.seriesMatchClock()
	}
	return time.Now()
}

// seriesMatchMissExpiry returns when a miss recorded at checkedAt falls due,
// with a per item offset derived from the hash.
func seriesMatchMissExpiry(checkedAt time.Time, inputHash string) time.Time {
	var offset time.Duration
	if raw, err := hex.DecodeString(inputHash); err == nil && len(raw) >= 2 {
		n := int64(binary.BigEndian.Uint16(raw[:2]))
		offset = time.Duration(n) * (seriesMatchMissJitter / 0x10000)
	}
	return checkedAt.Add(seriesMatchMissTTL + offset)
}

// seriesMatchAlreadyDone reports whether this item's Hardcover series match can
// be skipped. Reading is safe in dry runs, which never write outcomes.
func (i *Importer) seriesMatchAlreadyDone(ctx context.Context, cfg ImportConfig, book *models.Book, itemID, inputHash string) bool {
	if i.seriesMatchState == nil || cfg.Full {
		return false
	}
	state, err := i.seriesMatchState.Get(ctx, cfg.SourceID, cfg.LibraryID, itemID)
	if err != nil {
		slog.Warn("abs import: read series match state failed", "itemID", itemID, "error", err)
		return false
	}
	if state == nil {
		return i.seriesMatchBootstrap(ctx, cfg, book, itemID, inputHash)
	}
	if state.InputHash != inputHash {
		return false
	}
	switch state.Outcome {
	case seriesMatchOutcomeMatched:
		return true
	case seriesMatchOutcomeMiss:
		return i.seriesMatchNow().Before(seriesMatchMissExpiry(state.CheckedAt, state.InputHash))
	}
	return false
}

// seriesMatchBootstrap adopts a match made before outcomes were recorded, so
// upgrading does not cost one more full walk. It trusts only evidence about
// this item, and needs both: the item's own book already carries a Hardcover
// identity (a match merges that onto the book), and the item has a series
// membership whose series has a Hardcover link. A brand new volume joining a
// series a sibling already linked has the membership but not the identity, so
// it is not adopted and matches normally. The current hash is recorded, which
// means only changes from here on re-match.
func (i *Importer) seriesMatchBootstrap(ctx context.Context, cfg ImportConfig, book *models.Book, itemID, inputHash string) bool {
	if i.provenance == nil || i.series == nil || book == nil || book.MetadataProvider != providerHardcover {
		return false
	}
	rows, err := i.provenance.ListSeriesByItem(ctx, cfg.SourceID, cfg.LibraryID, itemID)
	if err != nil {
		slog.Warn("abs import: read series provenance failed", "itemID", itemID, "error", err)
		return false
	}
	for _, row := range rows {
		link, err := i.series.GetHardcoverLink(ctx, row.LocalID)
		if err != nil || link == nil {
			continue
		}
		i.recordSeriesMatch(ctx, cfg, itemID, inputHash, seriesMatchOutcomeMatched)
		return true
	}
	return false
}

// recordSeriesMatch persists an outcome. Dry runs never write it.
func (i *Importer) recordSeriesMatch(ctx context.Context, cfg ImportConfig, itemID, inputHash, outcome string) {
	if i.seriesMatchState == nil || cfg.DryRun {
		return
	}
	err := i.seriesMatchState.Upsert(ctx, &models.ABSSeriesMatchState{
		SourceID:  cfg.SourceID,
		LibraryID: cfg.LibraryID,
		ItemID:    itemID,
		InputHash: inputHash,
		Outcome:   outcome,
	}, i.seriesMatchNow())
	if err != nil {
		slog.Warn("abs import: record series match state failed", "itemID", itemID, "error", err)
	}
}

// WithSeriesMatchState enables skipping the Hardcover series match for items
// already matched or recently missed (#2173). Without it every item re-matches.
func (i *Importer) WithSeriesMatchState(repo *db.ABSSeriesMatchRepo) *Importer {
	i.seriesMatchState = repo
	return i
}
