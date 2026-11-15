package config

import "testing"

// TestLoad_ImageCacheMaxMB: 2048 by default, a positive value is taken as
// given, and zero, a negative or an unparseable value falls back to the
// default rather than leaving the cache without a ceiling.
func TestLoad_ImageCacheMaxMB(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 2048},
		{"512", 512},
		{" 8192 ", 8192},
		{"0", 2048},
		{"-1", 2048},
		{"lots", 2048},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("BINDERY_IMAGE_CACHE_MAX_MB", tc.env)
			if got := Load().ImageCacheMaxMB; got != tc.want {
				t.Errorf("BINDERY_IMAGE_CACHE_MAX_MB=%q gave %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}
