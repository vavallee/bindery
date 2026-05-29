package telemetry

import (
	"context"
	"encoding/json"
	"testing"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		// Release tags — both with and without the leading "v" since the
		// docker image build keeps "v1.7.0" while goreleaser strips it.
		{"v1.7.0", true},
		{"1.7.0", true},
		{"v0.0.1", true},
		{"v10.20.30", true},

		// Non-release labels CI emits today.
		{"dev", false},               // go run / go build with no -ldflags
		{"dev-abc1234", false},       // development branch interim image
		{"sha-abc1234", false},       // non-release branch interim image
		{"v1.7.0-3-gabc1234", false}, // git describe form (commits past a tag)
		{"v1.7.0-rc.1", false},       // pre-release (not currently used; safe to revisit)
		{"", false},                  // misconfigured / empty
		{"latest", false},            // image-tag style, not a real version
		{"v1.7", false},              // truncated
		{"1.7.0.1", false},           // four-component, not semver
		{"v1.7.0 ", false},           // whitespace-padded
		{"main", false},              // branch name accidentally injected
	}
	for _, tc := range cases {
		if got := isReleaseVersion(tc.version); got != tc.want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestPingPayloadWireShape verifies the JSON shape of a ping payload both
// with and without a features section. Two cohorts care about the shape:
// older telemetry-server versions that don't know about features (must
// accept the message without the field), and the current server which
// expects the nested features object verbatim.
func TestPingPayloadWireShape(t *testing.T) {
	t.Run("no features field when gatherer is nil", func(t *testing.T) {
		body := pingPayload{
			InstallID: "11111111-1111-1111-1111-111111111111",
			Version:   "1.15.2",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded["features"]; present {
			t.Errorf("features field must be omitted when nil; got: %s", raw)
		}
	})

	t.Run("features field embeds when gatherer is set", func(t *testing.T) {
		f := Features{
			Indexers:        2,
			DownloadClients: 1,
			Notifications:   3,
			CalibreEnabled:  true,
			MultiUser:       true,
		}
		body := pingPayload{
			InstallID: "22222222-2222-2222-2222-222222222222",
			Version:   "1.15.3",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
			Features:  &f,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded struct {
			Features map[string]any `json:"features"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := decoded.Features["indexers"]; got != float64(2) {
			t.Errorf("features.indexers = %v, want 2", got)
		}
		if got := decoded.Features["calibre_enabled"]; got != true {
			t.Errorf("features.calibre_enabled = %v, want true", got)
		}
		// Zero-valued fields must be omitted, otherwise the payload bloats
		// for installs that have nothing configured.
		if _, present := decoded.Features["abs_enabled"]; present {
			t.Errorf("features.abs_enabled must be omitted when false; got: %v", decoded.Features)
		}
		if _, present := decoded.Features["users"]; present {
			t.Errorf("features.users must be omitted when zero; got: %v", decoded.Features)
		}
	})
}

// TestWithGathererIsOptional verifies that calling WithGatherer is optional
// and that omitting it leaves the gatherer nil so Ping falls back to the
// legacy payload shape (covered above by the no-features-field test).
func TestWithGathererIsOptional(t *testing.T) {
	c := &Client{}
	if c.gatherer != nil {
		t.Error("zero-value Client should have nil gatherer")
	}
	c2 := (&Client{}).WithGatherer(func(context.Context) Features {
		return Features{Indexers: 1}
	})
	if c2.gatherer == nil {
		t.Error("WithGatherer should set the gatherer")
	}
	got := c2.gatherer(context.Background())
	if got.Indexers != 1 {
		t.Errorf("gatherer returned %+v, want Indexers=1", got)
	}
}
