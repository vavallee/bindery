package config

import "testing"

// TestLoad_ImageCacheMaxMB: 2048 by default, a positive value is taken as
// given, and zero, a negative or an unparseable value falls back to the
// default rather than leaving the cache without a ceiling. A value above
// MaxImageCacheMB is brought down to it, so converting to bytes cannot
// overflow, and one too large to parse at all falls back like any other
// unparseable value.
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
		{"1048576", MaxImageCacheMB},
		{"1048577", MaxImageCacheMB},
		{"2000000000", MaxImageCacheMB},
		{"99999999999999999999999", 2048},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("BINDERY_IMAGE_CACHE_MAX_MB", tc.env)
			if got := Load().ImageCacheMaxMB; got != tc.want {
				t.Errorf("BINDERY_IMAGE_CACHE_MAX_MB=%q gave %d, want %d", tc.env, got, tc.want)
			}
			if b := int64(Load().ImageCacheMaxMB) << 20; b <= 0 {
				t.Errorf("BINDERY_IMAGE_CACHE_MAX_MB=%q gave %d bytes, want a positive number", tc.env, b)
			}
		})
	}
}
