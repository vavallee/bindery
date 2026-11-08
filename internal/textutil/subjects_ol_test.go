package textutil

import "testing"

func TestIsOpenLibraryMachineTag(t *testing.T) {
	tests := []struct {
		subject string
		want    bool
	}{
		{"nyt:hardcover-fiction=2015-01-25", true},
		{"place:Mars", true},
		{"person:Darrow", true},
		{"time:Future", true},
		{"series:Red_Rising", true},
		{"seriesRed Rising Trilogy", true},
		{"collectionID:greatbooks", true},
		{"Time: Future", false},
		{"Series: Discworld", false},
		{"E=mc2", false},
		{"Series fiction", false},
		{"Science fiction", false},
	}
	for _, tt := range tests {
		if got := IsOpenLibraryMachineTag(tt.subject); got != tt.want {
			t.Errorf("IsOpenLibraryMachineTag(%q) = %v, want %v", tt.subject, got, tt.want)
		}
	}
}
