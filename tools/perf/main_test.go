package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsBadArguments(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage"},
		{[]string{"bake"}, "unknown command"},
		{[]string{"seed"}, "-data is required"},
		{[]string{"run"}, "-data is required"},
		{[]string{"run", "-data", t.TempDir(), "-mode", "enormous"}, "unknown mode"},
		{[]string{"run", "-data", t.TempDir(), "-binary", "/bin/true", "-only", "("}, "-only"},
		{[]string{"seed", "-nope"}, "flag provided but not defined"},
		{[]string{"run", "-nope"}, "flag provided but not defined"},
	} {
		var out bytes.Buffer
		err := run(ctx, tc.args, &out)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%v) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

func TestSeedCommand(t *testing.T) {
	data := t.TempDir()
	var out bytes.Buffer
	if err := run(context.Background(), []string{"seed", "-books", "30", "-data", data, "-untracked"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "seed-30-untracked", manifestName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "library-30-untracked", ".perf-tree")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "30 books") {
		t.Fatalf("seed reports what it wrote, got %q", out.String())
	}
}

func TestModes(t *testing.T) {
	for _, name := range []string{"tiny", "quick", "full"} {
		m, err := modeByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != name || m.Reps < 1 || len(m.ReadSizes) == 0 || len(m.Sweep) == 0 || len(m.Indexers) == 0 || len(m.ScanSizes) == 0 || len(m.ImportSizes) == 0 {
			t.Errorf("mode %s is incomplete: %+v", name, m)
		}
	}
	quick, _ := modeByName("quick")
	full, _ := modeByName("full")
	if quick.Reps != 3 || full.Reps != 3 {
		t.Error("quick and full both run every scenario three times")
	}
	want := []int{1000, 10000, 40000}
	if len(full.ReadSizes) != 3 || full.ReadSizes[0] != want[0] || full.ReadSizes[1] != want[1] || full.ReadSizes[2] != want[2] {
		t.Errorf("full reads %v, want %v", full.ReadSizes, want)
	}
	if full.EndpointDur <= quick.EndpointDur || full.PollDur <= quick.PollDur {
		t.Error("full must measure for longer than quick")
	}
	_ = time.Second
}
