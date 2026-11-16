package decision_test

import (
	"testing"

	"github.com/vavallee/bindery/internal/decision"
)

func TestDeadTorrentSpec(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name     string
		protocol string
		seeders  *int
		ok       bool
	}{
		{"zero seeders torrent rejected", "torrent", n(0), false},
		{"seeded torrent passes", "torrent", n(1), true},
		{"unknown seeders passes", "torrent", nil, true},
		{"usenet passes", "usenet", n(0), true},
	}
	for _, tc := range cases {
		r := release(withProtocol(tc.protocol))
		r.Seeders = tc.seeders
		ok, reason := decision.DeadTorrentSpec{}.IsSatisfiedBy(r, emptyBook())
		if ok != tc.ok {
			t.Errorf("%s: ok=%v reason=%q", tc.name, ok, reason)
		}
	}
}
