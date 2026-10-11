package hardcoverlistsyncer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

func TestDailyQuotaListSyncStopsBeforeFetch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	token := "example-only"
	// Example-only install secret so the test can compute the fingerprint.
	secret := strings.Repeat("ab", 32)
	if err := settings.Set(ctx, "auth.hardcover_daily_hold_secret", secret); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	if err := settings.Set(ctx, "auth.hardcover_daily_holds", fmt.Sprintf(`{"%x":%q}`, mac.Sum(nil), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	s := New(db.NewImportListRepo(database), db.NewAuthorRepo(database), db.NewBookRepo(database)).WithDailyQuota(hardcover.NewDailyQuota(settings))
	il := testImportList("Held", "hardcover", true)
	il.APIKey = token
	// The real factory inherits the hold, so the list fetch stops before any
	// network call and nothing is written.
	var daily *metadata.DailyQuotaError
	if err := s.syncList(ctx, il); !errors.As(err, &daily) {
		t.Fatalf("factory hold: %v", err)
	}
	books, err := s.books.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 || len(authors) != 0 {
		t.Fatalf("wrote %d books, %d authors", len(books), len(authors))
	}
}
