package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func addSeriesAlias(t *testing.T, repo *SeriesRepo, foreignID string, seriesID int64) {
	t.Helper()
	if _, err := repo.db.Exec(`INSERT INTO series_aliases (foreign_id, series_id) VALUES (?, ?)`, foreignID, seriesID); err != nil {
		t.Fatal(err)
	}
}

// A provider id that was merged away resolves to the series it was merged
// into, through both lookups, and is never created again (#2554).
func TestSeriesAliasResolution(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	if err := repo.CreateOrGet(ctx, target); err != nil {
		t.Fatal(err)
	}
	addSeriesAlias(t, repo, "nb-series:1:serien-om-fjellet", target.ID)

	got, err := repo.GetByForeignID(ctx, "nb-series:1:serien-om-fjellet")
	if err != nil || got == nil || got.ID != target.ID {
		t.Fatalf("GetByForeignID(alias) = %+v, %v; want series %d", got, err, target.ID)
	}
	if got, err := repo.GetByForeignID(ctx, "hc-series:1"); err != nil || got == nil || got.ID != target.ID {
		t.Fatalf("GetByForeignID(own id) = %+v, %v", got, err)
	}
	if got, err := repo.GetByForeignID(ctx, "nb-series:1:unknown"); err != nil || got != nil {
		t.Fatalf("GetByForeignID(unknown) = %+v, %v; want nil, nil", got, err)
	}

	again := &models.Series{ForeignID: "nb-series:1:serien-om-fjellet", Title: "Serien om fjellet"}
	if err := repo.CreateOrGet(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != target.ID {
		t.Errorf("CreateOrGet(alias) = series %d, want the merge target %d", again.ID, target.ID)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series`).Scan(&n); err != nil || n != 1 {
		t.Errorf("series rows = %d (%v), want 1: an aliased id must not be recreated", n, err)
	}
}

// An alias id cannot become another series' own id, by creation or by a
// foreign-id change, or the two would name different series.
func TestSeriesAliasRefusedAsForeignID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	other := &models.Series{ForeignID: "hc-series:2", Title: "Havserien"}
	for _, s := range []*models.Series{target, other} {
		if err := repo.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	addSeriesAlias(t, repo, "nb-series:1:old", target.ID)

	if err := repo.Create(ctx, &models.Series{ForeignID: "nb-series:1:old", Title: "x"}); !errors.Is(err, ErrSeriesAlias) {
		t.Errorf("Create(alias) err = %v, want ErrSeriesAlias", err)
	}
	if err := repo.UpdateForeignID(ctx, other.ID, "nb-series:1:old"); !errors.Is(err, ErrSeriesAlias) {
		t.Errorf("UpdateForeignID(alias) err = %v, want ErrSeriesAlias", err)
	}
}

// Deleting the target drops its aliases: the user deleted the series, so a
// provider that still reports the old id creates it anew.
func TestSeriesAliasDroppedWithTarget(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	if err := repo.CreateOrGet(ctx, target); err != nil {
		t.Fatal(err)
	}
	addSeriesAlias(t, repo, "nb-series:1:old", target.ID)
	if err := repo.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "nb-series:1:old", Title: "Old"}
	if err := repo.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.ID == 0 || s.ID == target.ID {
		t.Errorf("CreateOrGet after the target's delete = %d, want a new series", s.ID)
	}
}

// The condition for #2554: a series is only ever looked up by provider
// foreign id through the alias-aware resolver. Any other query of that shape
// would find nothing for a merged-away id and let its caller create the series
// again. So every SQL string in this package that touches the series table and
// compares foreign_id, in a WHERE, a JOIN, an IN or an UPDATE, must also
// consult series_aliases.
func TestSeriesForeignIDLookupsGoThroughResolver(t *testing.T) {
	literal := regexp.MustCompile("(?s)`[^`]*`|\"(?:[^\"\\\\]|\\\\.)*\"")
	seriesTable := regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO)\s+series(?:[\s),]|$)`)
	assignment := regexp.MustCompile(`(?i)\bSET\s+foreign_id\s*=`)
	compares := regexp.MustCompile(`(?i)\bforeign_id\s*(?:=|!=|<>|\bIN\b|\bLIKE\b|\bIS\b)`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, loc := range literal.FindAllStringIndex(src, -1) {
			sql := src[loc[0]:loc[1]]
			if !seriesTable.MatchString(sql) || !compares.MatchString(assignment.ReplaceAllString(sql, "")) {
				continue
			}
			checked++
			if !strings.Contains(sql, "series_aliases") {
				t.Errorf("%s:%d compares series foreign_id without series_aliases: %s",
					f, 1+strings.Count(src[:loc[0]], "\n"), strings.Join(strings.Fields(sql), " "))
			}
		}
	}
	if checked == 0 {
		t.Fatal("matched no series foreign_id query at all; the patterns no longer find the resolver")
	}
}
