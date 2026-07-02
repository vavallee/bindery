package main

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// The Settings UI writes the ebook naming template under "naming.bookTemplate";
// "naming_template" is the legacy backend-only key. defaultNamingTemplate must
// honour the UI key or every import silently falls back to the built-in
// default layout (#1356).
func TestDefaultNamingTemplate(t *testing.T) {
	ctx := context.Background()
	set := func(t *testing.T, repo *db.SettingsRepo, key, value string) {
		t.Helper()
		if err := repo.Set(ctx, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	tests := []struct {
		name string
		seed map[string]string
		want string
	}{
		{name: "unset falls back to empty", want: ""},
		{
			name: "UI key is honoured",
			seed: map[string]string{"naming.bookTemplate": "{Author}/{Series}/{Title}"},
			want: "{Author}/{Series}/{Title}",
		},
		{
			name: "legacy key still works",
			seed: map[string]string{"naming_template": "{Author}/{Title}"},
			want: "{Author}/{Title}",
		},
		{
			name: "UI key wins over legacy",
			seed: map[string]string{
				"naming.bookTemplate": "{Author}/{Series}/{Title}",
				"naming_template":     "{Author}/{Title}",
			},
			want: "{Author}/{Series}/{Title}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			repo := db.NewSettingsRepo(database)
			for k, v := range tt.seed {
				set(t, repo, k, v)
			}
			if got := defaultNamingTemplate(repo); got != tt.want {
				t.Errorf("defaultNamingTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}
