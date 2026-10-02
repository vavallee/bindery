package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate097AddsSeedTimeColumns covers the #2206 schema: the two limits
// are nullable INTEGER columns and the provenance column defaults to unset, so
// a row that existed before the migration reads back as "no override" and is
// still eligible for Prowlarr to fill.
func TestMigrate097AddsSeedTimeColumns(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	type column struct {
		typ     string
		notNull int
		dflt    *string
	}
	cols := map[string]column{}
	rows, err := database.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value FROM pragma_table_info('indexers')`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var c column
		if err := rows.Scan(&name, &c.typ, &c.notNull, &c.dflt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	for _, name := range []string{"seed_time_minutes", "inactive_seed_time_minutes"} {
		c, ok := cols[name]
		if !ok {
			t.Fatalf("column %s missing", name)
		}
		if c.typ != "INTEGER" || c.notNull != 0 {
			t.Errorf("%s: type %q notnull %d, want nullable INTEGER", name, c.typ, c.notNull)
		}
	}
	src, ok := cols["seed_time_source"]
	if !ok {
		t.Fatal("column seed_time_source missing")
	}
	if src.notNull != 1 || src.dflt == nil || *src.dflt != "''" {
		t.Errorf("seed_time_source: notnull %d default %v, want NOT NULL DEFAULT ''", src.notNull, src.dflt)
	}

	// A row written without the new columns, the shape every pre-097 row has.
	mustExec(t, database, `INSERT INTO indexers (name, type, url, api_key, categories) VALUES ('Old', 'torznab', 'https://old.example/api', 'k', '[7020]')`)
	list, err := NewIndexerRepo(database).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %d rows, want 1", len(list))
	}
	got := list[0]
	if got.SeedTimeMinutes != nil || got.InactiveSeedTimeMinutes != nil {
		t.Errorf("pre-existing row: SeedTimeMinutes=%v InactiveSeedTimeMinutes=%v, want both nil", got.SeedTimeMinutes, got.InactiveSeedTimeMinutes)
	}
	if got.SeedTimeSource != models.SeedRatioSourceUnset {
		t.Errorf("pre-existing row: SeedTimeSource=%q, want unset", got.SeedTimeSource)
	}
}

// TestIndexerRepo_SeedTimeLimits round-trips both seed time limits (#2206) and
// the seed time provenance through Create, Update and GetByID: unset, a value,
// and a clear back to NULL.
func TestIndexerRepo_SeedTimeLimits(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewIndexerRepo(database)

	seed, inactive := 1440, 120
	idx := &models.Indexer{
		Name: "Timed", Type: "torznab", URL: "https://example.com/api",
		APIKey: "k", Categories: []int{7020}, Priority: 1, Enabled: true,
		SeedTimeMinutes: &seed, SeedTimeSource: models.SeedRatioSourceProwlarr,
		InactiveSeedTimeMinutes: &inactive,
	}
	if err := repo.Create(ctx, idx); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByID(ctx, idx.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SeedTimeMinutes == nil || *got.SeedTimeMinutes != 1440 {
		t.Errorf("SeedTimeMinutes after create = %v, want 1440", got.SeedTimeMinutes)
	}
	if got.InactiveSeedTimeMinutes == nil || *got.InactiveSeedTimeMinutes != 120 {
		t.Errorf("InactiveSeedTimeMinutes after create = %v, want 120", got.InactiveSeedTimeMinutes)
	}
	if got.SeedTimeSource != models.SeedRatioSourceProwlarr {
		t.Errorf("SeedTimeSource after create = %q, want prowlarr", got.SeedTimeSource)
	}

	newSeed := 30
	got.SeedTimeMinutes = &newSeed
	got.SeedTimeSource = models.SeedRatioSourceUser
	got.InactiveSeedTimeMinutes = nil
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, idx.ID)
	if got.SeedTimeMinutes == nil || *got.SeedTimeMinutes != 30 {
		t.Errorf("SeedTimeMinutes after update = %v, want 30", got.SeedTimeMinutes)
	}
	if got.InactiveSeedTimeMinutes != nil {
		t.Errorf("InactiveSeedTimeMinutes after clear = %v, want nil", *got.InactiveSeedTimeMinutes)
	}
	if got.SeedTimeSource != models.SeedRatioSourceUser {
		t.Errorf("SeedTimeSource after update = %q, want user", got.SeedTimeSource)
	}

	// The other read paths scan the same columns.
	inst := &models.ProwlarrInstance{Name: "P", URL: "http://p:9696", APIKey: "k"}
	if err := NewProwlarrRepo(database).Create(ctx, inst); err != nil {
		t.Fatalf("Create prowlarr instance: %v", err)
	}
	instID := inst.ID
	pid := 3
	got.ProwlarrInstanceID, got.ProwlarrIndexerID = &instID, &pid
	p := *got
	p.URL = "https://example.com/p"
	if err := repo.Create(ctx, &p); err != nil {
		t.Fatalf("Create prowlarr row: %v", err)
	}
	byInst, err := repo.ListByProwlarrInstance(ctx, instID)
	if err != nil || len(byInst) != 1 {
		t.Fatalf("ListByProwlarrInstance = %d rows, %v", len(byInst), err)
	}
	if byInst[0].SeedTimeMinutes == nil || *byInst[0].SeedTimeMinutes != 30 || byInst[0].SeedTimeSource != models.SeedRatioSourceUser {
		t.Errorf("ListByProwlarrInstance lost the seed time: %v %q", byInst[0].SeedTimeMinutes, byInst[0].SeedTimeSource)
	}
}
