package api

import (
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The release URL used throughout is on host.example. Indexer ids:
//
//	1  U1  unpinned, on the URL host
//	3  U2  unpinned, on the URL host
//	2  P   pinned, on the URL host
//	10 O   unpinned, on another host
//	20 PX  pinned, on another host
const decisionReleaseURL = "http://host.example/dl/1.nzb"

func decisionIndexers(keyed bool, set string) []models.Indexer {
	pin := int64(7)
	key := ""
	if keyed {
		key = "secret"
	}
	u1 := models.Indexer{ID: 1, Name: "U1", URL: "http://host.example/api", APIKey: key}
	u2 := models.Indexer{ID: 3, Name: "U2", URL: "http://host.example:9696/api2", APIKey: key}
	p := models.Indexer{ID: 2, Name: "P", URL: "http://host.example/api3", APIKey: key, DownloadClientID: &pin}
	o := models.Indexer{ID: 10, Name: "O", URL: "http://other.example/api", APIKey: key}
	px := models.Indexer{ID: 20, Name: "PX", URL: "http://elsewhere.example/api", APIKey: key, DownloadClientID: &pin}
	switch set {
	case "none":
		return []models.Indexer{o, px}
	case "onlyUnpinned":
		return []models.Indexer{u1, o, px}
	case "onlyPinned":
		return []models.Indexer{p, o, px}
	case "both":
		return []models.Indexer{u1, p, o, px}
	case "severalOnePinned":
		return []models.Indexer{u1, u2, p, o, px}
	case "severalNonePinned":
		return []models.Indexer{u1, u2, o, px}
	}
	panic("unknown set " + set)
}

// TestResolveGrabPin is the whole decision matrix: the caller's id (absent,
// pinned, unpinned on the URL host, unpinned elsewhere, unknown) against what the
// URL's host matches (nothing, only unpinned indexers, only a pinned one, both,
// several with one pinned, several none pinned), each for pinned indexers with
// and without a stored key. The key plays no part in the decision, so the two
// must always agree.
func TestResolveGrabPin(t *testing.T) {
	id := func(v int64) *int64 { return &v }
	use := func(v int64) grabPinDecision { return grabPinDecision{Kind: pinUseIndexer, IndexerID: v} }
	noInfo := grabPinDecision{Kind: pinNoInfo}
	ambiguous := grabPinDecision{Kind: pinRefuse, Reason: pinRefuseAmbiguous, Pinned: []string{"P"}}
	contradiction := grabPinDecision{Kind: pinRefuse, Reason: pinRefuseContradiction, Pinned: []string{"P"}}

	cases := []struct {
		name   string
		caller *int64
		set    string
		want   grabPinDecision
	}{
		// No id supplied (rule d).
		{"no id, host matches nothing", nil, "none", noInfo},
		{"no id, only an unpinned indexer on the host", nil, "onlyUnpinned", use(1)},
		{"no id, only a pinned indexer on the host", nil, "onlyPinned", use(2)},
		{"no id, a pinned and an unpinned indexer share the host", nil, "both", ambiguous},
		{"no id, several on the host and one pinned", nil, "severalOnePinned", ambiguous},
		{"no id, several on the host and none pinned", nil, "severalNonePinned", noInfo},

		// A supplied id that exists and is pinned always wins (rule a).
		{"pinned id elsewhere, host matches nothing", id(20), "none", use(20)},
		{"pinned id elsewhere, only unpinned on the host", id(20), "onlyUnpinned", use(20)},
		{"pinned id elsewhere, only a pinned on the host", id(20), "onlyPinned", use(20)},
		{"pinned id elsewhere, both on the host", id(20), "both", use(20)},
		{"pinned id elsewhere, several on the host one pinned", id(20), "severalOnePinned", use(20)},
		{"pinned id elsewhere, several on the host none pinned", id(20), "severalNonePinned", use(20)},
		{"pinned id on the host, shared with unpinned", id(2), "both", use(2)},
		{"pinned id on the host, shared with several", id(2), "severalOnePinned", use(2)},

		// A supplied unpinned id whose host matches the URL is trusted (rule b).
		{"unpinned id on the host, alone", id(1), "onlyUnpinned", use(1)},
		{"unpinned id on the host, sharing it with a pinned", id(1), "both", use(1)},
		{"unpinned id on the host, several one pinned", id(1), "severalOnePinned", use(1)},
		{"unpinned id on the host, several none pinned", id(1), "severalNonePinned", use(1)},

		// A supplied unpinned id elsewhere contradicts the URL (rule b).
		{"unpinned id elsewhere, host matches nothing", id(10), "none", use(10)},
		{"unpinned id elsewhere, only unpinned on the host", id(10), "onlyUnpinned", use(10)},
		{"unpinned id elsewhere, only a pinned on the host", id(10), "onlyPinned", contradiction},
		{"unpinned id elsewhere, both on the host", id(10), "both", contradiction},
		{"unpinned id elsewhere, several one pinned", id(10), "severalOnePinned", contradiction},
		{"unpinned id elsewhere, several none pinned", id(10), "severalNonePinned", use(10)},

		// An id that matches no indexer is no id at all (rule c).
		{"unknown id, host matches nothing", id(999), "none", noInfo},
		{"unknown id, only an unpinned on the host", id(999), "onlyUnpinned", use(1)},
		{"unknown id, only a pinned on the host", id(999), "onlyPinned", use(2)},
		{"unknown id, both on the host", id(999), "both", ambiguous},
		{"unknown id, several one pinned", id(999), "severalOnePinned", ambiguous},
		{"unknown id, several none pinned", id(999), "severalNonePinned", noInfo},
	}
	for _, keyed := range []bool{true, false} {
		for _, tc := range cases {
			name := tc.name + ", keyless"
			if keyed {
				name = tc.name + ", keyed"
			}
			t.Run(name, func(t *testing.T) {
				got := resolveGrabPin(tc.caller, decisionReleaseURL, decisionIndexers(keyed, tc.set))
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("resolveGrabPin = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

// With no indexers at all the decision has nothing to say.
func TestResolveGrabPin_NoIndexers(t *testing.T) {
	one := int64(1)
	for _, caller := range []*int64{nil, &one} {
		if got := resolveGrabPin(caller, decisionReleaseURL, nil); got.Kind != pinNoInfo {
			t.Errorf("caller %v: %+v, want no information", caller, got)
		}
	}
}
