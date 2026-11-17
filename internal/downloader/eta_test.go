package downloader

import "testing"

func TestEtaToTimeLeft(t *testing.T) {
	cases := []struct {
		name string
		eta  int64
		want string
	}{
		{"qbittorrent infinity sentinel", 8640000, ""},
		{"above the sentinel", 9000000, ""},
		{"just under the sentinel", 8639999, "2399h 59m"},
		{"zero", 0, ""},
		{"transmission unknown", -2, ""},
		{"transmission not available", -1, ""},
		{"seconds", 45, "45s"},
		{"minutes", 125, "2m 05s"},
		{"hours", 3661, "1h 01m"},
	}
	for _, c := range cases {
		if got := etaToTimeLeft(c.eta); got != c.want {
			t.Errorf("%s: etaToTimeLeft(%d) = %q, want %q", c.name, c.eta, got, c.want)
		}
	}
}

func TestLiveTimeLeft_CompleteHasNone(t *testing.T) {
	if got := liveTimeLeft(3600, 0); got != "" {
		t.Errorf("complete download got %q, want empty", got)
	}
	if got := liveTimeLeft(3600, 100); got != "1h 00m" {
		t.Errorf("incomplete download got %q, want 1h 00m", got)
	}
}
