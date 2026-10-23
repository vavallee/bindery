package models

import "testing"

func TestIndexerProtocol(t *testing.T) {
	cases := map[string]string{
		"torznab":  "torrent",
		"newznab":  "usenet",
		"":         "usenet",
		"whatever": "usenet",
	}
	for typ, want := range cases {
		if got := (Indexer{Type: typ}).Protocol(); got != want {
			t.Errorf("Indexer{Type: %q}.Protocol() = %q, want %q", typ, got, want)
		}
	}
}

func TestIndexerNeedsAttention(t *testing.T) {
	msg := "refused"
	code := func(n int) *int { return &n }
	cases := []struct {
		name string
		idx  Indexer
		want bool
	}{
		{"no error", Indexer{}, false},
		{"error without a code is a transport failure", Indexer{LastError: &msg}, false},
		{"code without an error text", Indexer{LastErrorCode: code(100)}, false},
		{"99 is below the human range", Indexer{LastError: &msg, LastErrorCode: code(99)}, false},
		{"100 bad credentials", Indexer{LastError: &msg, LastErrorCode: code(100)}, true},
		{"102 forbidden", Indexer{LastError: &msg, LastErrorCode: code(102)}, true},
		{"199 is the top of the range", Indexer{LastError: &msg, LastErrorCode: code(199)}, true},
		{"200 is outside it", Indexer{LastError: &msg, LastErrorCode: code(200)}, false},
		{"500 rate limit clears on its own", Indexer{LastError: &msg, LastErrorCode: code(500)}, false},
	}
	for _, tc := range cases {
		if got := tc.idx.NeedsAttention(); got != tc.want {
			t.Errorf("%s: NeedsAttention() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
