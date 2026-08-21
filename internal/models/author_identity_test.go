package models

import "testing"

// TestProviderIdentity covers how an author's link to a specific upstream
// author is resolved (#1734). A relink moves the primary foreign id to the new
// provider and keeps the old one as an identifier, so an author can hold
// several at once and both places have to be checked.
func TestProviderIdentity(t *testing.T) {
	cases := []struct {
		name     string
		author   Author
		provider string
		want     string
		wantOK   bool
	}{
		{
			name:     "primary foreign id belongs to the provider",
			author:   Author{ForeignID: "hc:terry-bisson"},
			provider: "hardcover",
			want:     "hc:terry-bisson", wantOK: true,
		},
		{
			name: "identifier row when the primary belongs to someone else",
			author: Author{
				ForeignID:           "OL123A",
				ProviderIdentifiers: map[string]string{"hardcover": "hc:terry-bisson"},
			},
			provider: "hardcover",
			want:     "hc:terry-bisson", wantOK: true,
		},
		{
			name:     "no identity for that provider",
			author:   Author{ForeignID: "OL123A"},
			provider: "hardcover",
			wantOK:   false,
		},
		{
			name: "provider name is matched case insensitively",
			author: Author{
				ForeignID:           "OL123A",
				ProviderIdentifiers: map[string]string{"Hardcover": "hc:x"},
			},
			provider: "HARDCOVER",
			want:     "hc:x", wantOK: true,
		},
		{
			name:     "empty provider asks nothing",
			author:   Author{ForeignID: "hc:x"},
			provider: "",
			wantOK:   false,
		},
		{
			name: "blank identifier is not an identity",
			author: Author{
				ForeignID:           "OL123A",
				ProviderIdentifiers: map[string]string{"hardcover": "   "},
			},
			provider: "hardcover",
			wantOK:   false,
		},
		{
			name:     "openlibrary is the unprefixed default",
			author:   Author{ForeignID: "OL123A"},
			provider: "openlibrary",
			want:     "OL123A", wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.author.ProviderIdentity(tc.provider)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("identity = %q, want %q", got, tc.want)
			}
		})
	}
}
