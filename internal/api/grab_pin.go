package api

import (
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// grabPinKind is the outcome of resolveGrabPin.
type grabPinKind int

const (
	// pinNoInfo: nothing identifies an indexer and nothing on the host calls for
	// action. The grab is unpinned and routed as it always was. Signing may still
	// attribute an indexer afterwards, for the seed limits.
	pinNoInfo grabPinKind = iota
	// pinUseIndexer: the grab is attributed to grabPinDecision.IndexerID, and that
	// indexer's pin, or lack of one, decides the client. Nothing downstream may
	// replace it.
	pinUseIndexer
	// pinRefuse: the grab cannot safely be routed and is refused.
	pinRefuse
)

// Reasons for a refusal.
const (
	pinRefuseAmbiguous     = "ambiguous"
	pinRefuseContradiction = "contradiction"
)

// grabPinDecision is the one answer to "which indexer does this grab belong to,
// and may it be sent".
type grabPinDecision struct {
	Kind      grabPinKind
	IndexerID int64    // pinUseIndexer
	Reason    string   // pinRefuse
	Pinned    []string // pinRefuse: the pinned indexers that made it unsafe
}

// resolveGrabPin decides, from the caller's indexer id (nil when none), the
// release URL and the full indexer list, which indexer a grab belongs to for the
// purpose of a download client pin (#3080). It does no signing and no IO. The
// rules, in order:
//
//	a. A supplied id that exists and is pinned wins, whatever the host.
//	b. A supplied id that exists and is unpinned is trusted when its host
//	   matches the release URL's host. When it does not match (a contradiction)
//	   and any pinned indexer does match the URL's host, the grab is refused.
//	   Otherwise the supplied indexer stands.
//	c. A supplied id that matches no indexer is treated as no id.
//	d. With no usable id, the indexers whose host matches the URL decide: none
//	   gives no information; exactly one is the release's indexer; several are
//	   refused when any of them is pinned (the release cannot be told apart, and
//	   signing's pick is a guess) and otherwise give no information.
//
// Host matching is newznab.DownloadHostMatchesIndexer: case, one trailing dot
// and the port are ignored.
func resolveGrabPin(callerID *int64, releaseURL string, idxs []models.Indexer) grabPinDecision {
	matches := func(idx models.Indexer) bool {
		return newznab.DownloadHostMatchesIndexer(releaseURL, idx.URL)
	}
	var matching, pinnedOnHost []models.Indexer
	var supplied *models.Indexer
	for i := range idxs {
		if callerID != nil && idxs[i].ID == *callerID {
			supplied = &idxs[i]
		}
		if matches(idxs[i]) {
			matching = append(matching, idxs[i])
			if idxs[i].DownloadClientID != nil {
				pinnedOnHost = append(pinnedOnHost, idxs[i])
			}
		}
	}
	names := func(list []models.Indexer) []string {
		out := make([]string, 0, len(list))
		for _, idx := range list {
			out = append(out, idx.Name)
		}
		return out
	}

	if supplied != nil {
		if supplied.DownloadClientID != nil || matches(*supplied) || len(pinnedOnHost) == 0 {
			return grabPinDecision{Kind: pinUseIndexer, IndexerID: supplied.ID}
		}
		return grabPinDecision{Kind: pinRefuse, Reason: pinRefuseContradiction, Pinned: names(pinnedOnHost)}
	}

	switch {
	case len(matching) == 0:
		return grabPinDecision{Kind: pinNoInfo}
	case len(matching) == 1:
		return grabPinDecision{Kind: pinUseIndexer, IndexerID: matching[0].ID}
	case len(pinnedOnHost) > 0:
		return grabPinDecision{Kind: pinRefuse, Reason: pinRefuseAmbiguous, Pinned: names(pinnedOnHost)}
	default:
		return grabPinDecision{Kind: pinNoInfo}
	}
}

// ambiguousPinError refuses a grab that cannot safely be routed because of a
// pinned indexer (#3080). Sending it to the default client could put a private
// tracker's release on the public client, so it is not sent.
type ambiguousPinError struct {
	reason string
	pinned []string
}

func (e *ambiguousPinError) Error() string { return e.Message(true) }

// Message names the pinned indexers only for an admin.
func (e *ambiguousPinError) Message(includeNames bool) string {
	names := ""
	if includeNames && len(e.pinned) > 0 {
		names = " (" + strings.Join(e.pinned, ", ") + ")"
	}
	if e.reason == pinRefuseContradiction {
		return "the indexer named on this grab does not match the release's host, and an indexer on that host is pinned to a download client" + names +
			". It was not sent to any client. Grab it from the search results so the right indexer is known."
	}
	return "this release could not be tied to one indexer, and an indexer on its host is pinned to a download client" + names +
		". It was not sent to any client. Grab it from the search results so the indexer is known."
}
