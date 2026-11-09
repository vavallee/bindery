package scheduler

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/robfig/cron/v3"
)

// A bad cron spec must be logged with the job name and must not panic or
// register anything; a good spec registers the job.
func TestAddJob_BadSpecLogsAndDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := &Scheduler{cron: cron.New(cron.WithSeconds())}
	s.addJob("not a cron spec", "broken-job", func() {})

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "broken-job") {
		t.Fatalf("expected an error log naming the job, got %q", out)
	}
	if n := len(s.cron.Entries()); n != 0 {
		t.Fatalf("entries = %d, want 0 after a bad spec", n)
	}

	buf.Reset()
	s.addJob("@every 1h", "good-job", func() {})
	if n := len(s.cron.Entries()); n != 1 || buf.Len() != 0 {
		t.Fatalf("good spec: entries=%d log=%q", n, buf.String())
	}
}
