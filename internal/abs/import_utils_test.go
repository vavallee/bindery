package abs

import (
	"strings"
	"testing"
)

func TestNormalizeLibraryIDsPrependsLegacyPrimary(t *testing.T) {
	t.Parallel()

	got := normalizeLibraryIDs(" lib-legacy ", []string{" lib-books ", "lib-audio", "lib-books", ""})
	if got, want := strings.Join(got, ","), "lib-legacy,lib-books,lib-audio"; got != want {
		t.Fatalf("library ids = %q, want %q", got, want)
	}
}
