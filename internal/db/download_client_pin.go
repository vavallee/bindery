package db

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// ClientPinnedError is what DownloadClientRepo.Delete returns while one or more
// indexers are pinned to the client (#3080). Indexers holds their names.
type ClientPinnedError struct {
	ClientID int64
	Indexers []string
}

func (e *ClientPinnedError) Error() string {
	return fmt.Sprintf("this download client is pinned by %d indexer(s): %s. Choose another download client for them, or clear it, before deleting this one",
		len(e.Indexers), strings.Join(e.Indexers, ", "))
}

// pinnedError returns a *ClientPinnedError naming every indexer pinned to the
// client, or nil when none is.
func (r *DownloadClientRepo) pinnedError(ctx context.Context, clientID int64) error {
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM indexers WHERE download_client_id=? ORDER BY name`, clientID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	return &ClientPinnedError{ClientID: clientID, Indexers: names}
}

// Why a pinned client cannot take a grab.
const (
	PinClientMissing       = "missing"
	PinClientDisabled      = "disabled"
	PinClientWrongProtocol = "wrong protocol"
)

// PinnedClientError says a grab from a pinned indexer cannot go to the client
// it is pinned to. The grab fails and is never sent anywhere else (#3080).
type PinnedClientError struct {
	IndexerName    string
	ClientID       int64
	ClientName     string // empty when the client no longer exists
	Reason         string
	ClientProtocol string
	WantProtocol   string
}

const pinNoFallback = "Nothing was sent and no other client was tried. Change the indexer's download client in Settings, Indexers."

func (e *PinnedClientError) Error() string { return e.Message(true) }

// Message renders the error. Indexers and clients are admin managed, so a
// caller that is not an admin gets the indexer (already in their search
// results) but not the client's name.
func (e *PinnedClientError) Message(includeClient bool) string {
	target := "a download client"
	if includeClient && e.ClientName != "" {
		target = fmt.Sprintf("download client %q", e.ClientName)
	}
	head := fmt.Sprintf("indexer %q is pinned to %s", e.IndexerName, target)
	switch e.Reason {
	case PinClientMissing:
		if includeClient {
			return fmt.Sprintf("%s (id %d), which no longer exists. %s", head, e.ClientID, pinNoFallback)
		}
		return head + " that no longer exists. " + pinNoFallback
	case PinClientDisabled:
		return head + ", which is disabled. " + pinNoFallback
	default:
		return fmt.Sprintf("%s, which is a %s client, but the indexer is %s. %s", head, e.ClientProtocol, e.WantProtocol, pinNoFallback)
	}
}

// ClientProtocol reports "torrent" or "usenet" for a download client type.
func ClientProtocol(clientType string) string {
	if slices.Contains(torrentClientTypes, clientType) {
		return "torrent"
	}
	return "usenet"
}

// ResolvePinnedClient returns the client idx is pinned to, (nil, nil) when it
// is not pinned, or a *PinnedClientError when the pinned client is missing,
// disabled or of the wrong protocol. It never substitutes another client.
// releaseProtocol, when set, is the protocol of the release being sent and
// must match too, so a usenet release cannot reach a torrent client.
func (r *DownloadClientRepo) ResolvePinnedClient(ctx context.Context, idx *models.Indexer, releaseProtocol string) (*models.DownloadClient, error) {
	if idx == nil || idx.DownloadClientID == nil {
		return nil, nil
	}
	perr := &PinnedClientError{IndexerName: idx.Name, ClientID: *idx.DownloadClientID, WantProtocol: idx.Protocol()}
	c, err := r.GetByID(ctx, *idx.DownloadClientID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		perr.Reason = PinClientMissing
		return nil, perr
	}
	perr.ClientName = c.Name
	perr.ClientProtocol = ClientProtocol(c.Type)
	if !c.Enabled {
		perr.Reason = PinClientDisabled
		return nil, perr
	}
	if perr.ClientProtocol != perr.WantProtocol || (releaseProtocol != "" && perr.ClientProtocol != releaseProtocol) {
		perr.Reason = PinClientWrongProtocol
		return nil, perr
	}
	return c, nil
}
