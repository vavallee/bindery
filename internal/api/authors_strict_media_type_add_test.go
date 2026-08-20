package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// strictMediaTypeFixture builds an AuthorHandler with a settings repo, which is
// all strictMediaTypeBypassed reads.
func strictMediaTypeFixture(t *testing.T) (*AuthorHandler, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	h := NewAuthorHandler(db.NewAuthorRepo(database), nil, db.NewBookRepo(database), nil, nil, settings, db.NewMetadataProfileRepo(database), nil)
	return h, settings, context.Background()
}

// TestLogStrictMediaTypeBypass_FiresOnlyWhenThePolicyWouldHaveExcluded pins
// #1759's decision: an explicit add is honoured whatever the strict media-type
// policy says, and the bypass is announced rather than silent.
//
// The behaviour was already correct on both add paths (the direct insert never
// consults the policy, and the single-work catalogue fallback is exempt under
// #1612). What was missing was any sign it had happened, so what this test
// guards is the condition, not the create: firing on every add would be noise,
// and firing on none would be the original complaint.
func TestLogStrictMediaTypeBypass_FiresOnlyWhenThePolicyWouldHaveExcluded(t *testing.T) {
	cases := []struct {
		name      string
		strict    string
		def       string
		bookMedia string
		want      bool
	}{
		{"strict on, ebook default, audiobook added", "true", models.MediaTypeEbook, models.MediaTypeAudiobook, true},
		{"strict on, ebook default, both added", "true", models.MediaTypeEbook, models.MediaTypeBoth, true},
		{"strict on, audiobook default, ebook added", "true", models.MediaTypeAudiobook, models.MediaTypeEbook, true},
		// The added format is the one the policy wants, so nothing was bypassed.
		{"strict on, ebook default, ebook added", "true", models.MediaTypeEbook, models.MediaTypeEbook, false},
		// A "both" default clamps nothing, so there is nothing to bypass.
		{"strict on, both default", "true", models.MediaTypeBoth, models.MediaTypeAudiobook, false},
		// Policy off: the catalogue sync would have created this row too.
		{"strict off", "false", models.MediaTypeEbook, models.MediaTypeAudiobook, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, settings, ctx := strictMediaTypeFixture(t)
			if err := settings.Set(ctx, SettingDefaultMediaTypeStrict, tc.strict); err != nil {
				t.Fatalf("set strict: %v", err)
			}
			if err := settings.Set(ctx, SettingDefaultMediaType, tc.def); err != nil {
				t.Fatalf("set default: %v", err)
			}
			got := h.strictMediaTypeBypassed(ctx, &models.Book{Title: "T", MediaType: tc.bookMedia})
			if got != tc.want {
				t.Errorf("bypassed = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLogStrictMediaTypeBypass_NilBook guards the defensive branch: AddBook
// only calls this once the book is non-nil, but the helper must not panic if
// that ever changes.
func TestLogStrictMediaTypeBypass_NilBook(t *testing.T) {
	h, settings, ctx := strictMediaTypeFixture(t)
	if err := settings.Set(ctx, SettingDefaultMediaTypeStrict, "true"); err != nil {
		t.Fatalf("set strict: %v", err)
	}
	if h.strictMediaTypeBypassed(ctx, nil) {
		t.Error("nil book reported as a bypass")
	}
	h.logStrictMediaTypeBypass(context.Background(), nil)
}
