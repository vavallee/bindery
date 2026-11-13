package db

import (
	"context"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestApplyAuthorCreateDefaults_QualityProfile(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	settings := NewSettingsRepo(database)
	profiles := NewQualityProfileRepo(database)
	ctx := context.Background()

	epub := &models.QualityProfile{Name: "Epub only", Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
	gone := &models.QualityProfile{Name: "Deleted", Items: []models.QualityItem{{Quality: "pdf", Allowed: true}}}
	for _, p := range []*models.QualityProfile{epub, gone} {
		if err := profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := profiles.Delete(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	other := int64(4242)

	cases := []struct {
		name    string
		value   string
		set     bool
		preset  *int64
		wantNil bool
		want    int64
	}{
		{name: "unset leaves none", wantNil: true},
		{name: "empty leaves none", set: true, value: "", wantNil: true},
		{name: "default is stamped", set: true, value: strconv.FormatInt(epub.ID, 10), want: epub.ID},
		{name: "deleted profile is ignored", set: true, value: strconv.FormatInt(gone.ID, 10), wantNil: true},
		{name: "garbage is ignored", set: true, value: "epub", wantNil: true},
		{name: "a profile already on the author wins", set: true, value: strconv.FormatInt(epub.ID, 10), preset: &other, want: other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := settings.Delete(ctx, SettingAuthorDefaultQualityProfileID); err != nil {
				t.Fatal(err)
			}
			if tc.set {
				if err := settings.Set(ctx, SettingAuthorDefaultQualityProfileID, tc.value); err != nil {
					t.Fatal(err)
				}
			}
			a := &models.Author{QualityProfileID: tc.preset}
			ApplyAuthorCreateDefaults(ctx, settings, a)
			if tc.wantNil {
				if a.QualityProfileID != nil {
					t.Fatalf("quality profile = %d, want none", *a.QualityProfileID)
				}
				return
			}
			if a.QualityProfileID == nil || *a.QualityProfileID != tc.want {
				t.Fatalf("quality profile = %v, want %d", a.QualityProfileID, tc.want)
			}
			if a.MonitorMode == "" {
				t.Error("ApplyAuthorCreateDefaults skipped the monitor defaults")
			}
		})
	}

	// A nil settings repo, which tests and unwired callers pass, is no default.
	a := &models.Author{}
	ApplyAuthorCreateDefaults(ctx, nil, a)
	if a.QualityProfileID != nil {
		t.Errorf("nil settings stamped quality profile %d", *a.QualityProfileID)
	}
}
