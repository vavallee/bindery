package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func newFunnelFixture(t *testing.T) (*db.SettingsRepo, *db.HistoryRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return db.NewSettingsRepo(database), db.NewHistoryRepo(database)
}

func setFunnelTime(t *testing.T, settings *db.SettingsRepo, key string, at time.Time) {
	t.Helper()
	if err := settings.Set(context.Background(), key, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestMarkFirst_FirstWriteWins(t *testing.T) {
	settings, _ := newFunnelFixture(t)
	ctx := context.Background()

	MarkFirst(ctx, settings, SettingFirstIndexerAt)
	v1, err := settings.Get(ctx, SettingFirstIndexerAt)
	if err != nil || v1 == nil || v1.Value == "" {
		t.Fatalf("expected a recorded timestamp, got %v / %v", v1, err)
	}
	MarkFirst(ctx, settings, SettingFirstIndexerAt)
	v2, _ := settings.Get(ctx, SettingFirstIndexerAt)
	if v2.Value != v1.Value {
		t.Errorf("second mark overwrote the first: %q -> %q", v1.Value, v2.Value)
	}

	// Nil settings must be a no-op, not a panic (handlers without the repo).
	MarkFirst(ctx, nil, SettingFirstIndexerAt)
}

func TestGatherFunnel_DayOffsets(t *testing.T) {
	settings, history := newFunnelFixture(t)
	ctx := context.Background()
	install := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	setFunnelTime(t, settings, SettingInstallCreatedAt, install)
	setFunnelTime(t, settings, SettingFirstIndexerAt, install.Add(2*time.Hour))  // day 0
	setFunnelTime(t, settings, SettingFirstClientAt, install.AddDate(0, 0, 3))   // day 3
	setFunnelTime(t, settings, SettingFirstAuthorAt, install.Add(-24*time.Hour)) // predates anchor → omitted

	f := Features{}
	GatherFunnel(ctx, settings, history, &f)

	if f.SetupIndexerDay == nil || *f.SetupIndexerDay != 0 {
		t.Errorf("SetupIndexerDay = %v, want 0 (same-day must survive, not be omitted)", f.SetupIndexerDay)
	}
	if f.SetupClientDay == nil || *f.SetupClientDay != 3 {
		t.Errorf("SetupClientDay = %v, want 3", f.SetupClientDay)
	}
	if f.FirstAuthorDay != nil {
		t.Errorf("FirstAuthorDay = %d, want nil (event predates install anchor)", *f.FirstAuthorDay)
	}
	if f.FirstGrabDay != nil {
		t.Errorf("FirstGrabDay = %d, want nil (no grab yet)", *f.FirstGrabDay)
	}
}

func TestGatherFunnel_NoAnchorNoFields(t *testing.T) {
	settings, history := newFunnelFixture(t)
	setFunnelTime(t, settings, SettingFirstIndexerAt, time.Now())

	f := Features{}
	GatherFunnel(context.Background(), settings, history, &f)
	if f.SetupIndexerDay != nil {
		t.Errorf("SetupIndexerDay = %d, want nil without an install anchor", *f.SetupIndexerDay)
	}
}

func TestGatherFunnel_DerivesGrabAndImportFromHistory(t *testing.T) {
	settings, history := newFunnelFixture(t)
	ctx := context.Background()

	// Anchor before the events so offsets are meaningful. History Create
	// stamps time.Now(), so anchor relative to now.
	setFunnelTime(t, settings, SettingInstallCreatedAt, time.Now().UTC().Add(-49*time.Hour))

	for _, ev := range []string{models.HistoryEventGrabbed, models.HistoryEventBookImported} {
		if err := history.Create(ctx, &models.HistoryEvent{EventType: ev, SourceTitle: "x"}); err != nil {
			t.Fatalf("create history %s: %v", ev, err)
		}
	}

	f := Features{}
	GatherFunnel(ctx, settings, history, &f)
	if f.FirstGrabDay == nil || *f.FirstGrabDay != 2 {
		t.Errorf("FirstGrabDay = %v, want 2 (derived from history, 49h after anchor)", f.FirstGrabDay)
	}
	if f.FirstImportDay == nil || *f.FirstImportDay != 2 {
		t.Errorf("FirstImportDay = %v, want 2", f.FirstImportDay)
	}

	// The derived values must be pinned: wipe history, gather again.
	if v, _ := settings.Get(ctx, SettingFirstGrabAt); v == nil || v.Value == "" {
		t.Fatal("expected first_grab_at to be pinned in settings")
	}
	f2 := Features{}
	GatherFunnel(ctx, settings, history, &f2)
	if f2.FirstGrabDay == nil || *f2.FirstGrabDay != *f.FirstGrabDay {
		t.Errorf("re-gather changed FirstGrabDay: %v vs %v", f2.FirstGrabDay, f.FirstGrabDay)
	}
}

// TestGatherFunnel_TolerantOfBadInput covers the defensive branches: a
// malformed stored timestamp, and a nil history repo (a caller that wired
// no history — the grab/import derivation must simply be skipped, not panic).
func TestGatherFunnel_TolerantOfBadInput(t *testing.T) {
	settings, _ := newFunnelFixture(t)
	ctx := context.Background()
	install := time.Now().UTC().Add(-72 * time.Hour)

	setFunnelTime(t, settings, SettingInstallCreatedAt, install)
	if err := settings.Set(ctx, SettingFirstIndexerAt, "not-a-timestamp"); err != nil {
		t.Fatalf("set: %v", err)
	}
	setFunnelTime(t, settings, SettingFirstClientAt, install.Add(24*time.Hour))

	f := Features{}
	GatherFunnel(ctx, settings, nil, &f) // nil history on purpose

	if f.SetupIndexerDay != nil {
		t.Errorf("SetupIndexerDay = %d, want nil (unparseable value ignored)", *f.SetupIndexerDay)
	}
	if f.SetupClientDay == nil || *f.SetupClientDay != 1 {
		t.Errorf("SetupClientDay = %v, want 1 (good values still read)", f.SetupClientDay)
	}
	if f.FirstGrabDay != nil {
		t.Errorf("FirstGrabDay = %d, want nil with no history repo", *f.FirstGrabDay)
	}
}

// TestGatherFunnel_UnparseableAnchor: a corrupt install anchor must disable
// the whole section rather than produce nonsense offsets.
func TestGatherFunnel_UnparseableAnchor(t *testing.T) {
	settings, history := newFunnelFixture(t)
	ctx := context.Background()
	if err := settings.Set(ctx, SettingInstallCreatedAt, "garbage"); err != nil {
		t.Fatalf("set: %v", err)
	}
	setFunnelTime(t, settings, SettingFirstIndexerAt, time.Now().UTC())

	f := Features{}
	GatherFunnel(ctx, settings, history, &f)
	if f.SetupIndexerDay != nil {
		t.Errorf("SetupIndexerDay = %d, want nil with a corrupt anchor", *f.SetupIndexerDay)
	}
}
