package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate133RepairsEditionProvenance seeds provenance written by the old
// Upsert: some rows carry another edition's id, some are right, one names an
// edition that does not exist. Only the wrong rows with a matching edition move.
func TestMigrate133RepairsEditionProvenance(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-133-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-133-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	eds := NewEditionRepo(database)
	e1 := &models.Edition{ForeignID: "abs:edition:lib:item1:audiobook", BookID: b.ID, Title: "1", Format: "AUDIOBOOK", Monitored: true}
	e2 := &models.Edition{ForeignID: "abs:edition:lib:item2:audiobook", BookID: b.ID, Title: "2", Format: "AUDIOBOOK", Monitored: true}
	for _, e := range []*models.Edition{e1, e2} {
		if err := eds.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	seed := func(ext string, local int64) {
		mustExec(t, database, `INSERT INTO abs_provenance (library_id, entity_type, external_id, local_id) VALUES ('lib', 'edition', ?, ?)`, ext, local)
	}
	seed("item1:audiobook", e2.ID) // wrong, repairable
	seed("item2:audiobook", e2.ID) // already right
	seed("gone:audiobook", 999)    // no such edition, left alone
	mustExec(t, database, `INSERT INTO abs_provenance (library_id, entity_type, external_id, local_id) VALUES ('lib', 'book', 'item1:audiobook', 55)`)

	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`,
		migrationVersionForTest(t, "133_abs_edition_provenance_repair.sql"))
	for i := 0; i < 2; i++ { // the second pass proves it is idempotent
		if err := migrate(database); err != nil {
			t.Fatalf("migrate 133 pass %d: %v", i, err)
		}
		mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`,
			migrationVersionForTest(t, "133_abs_edition_provenance_repair.sql"))
	}

	local := func(typ, ext string) int64 {
		var id int64
		if err := database.QueryRowContext(ctx, `SELECT local_id FROM abs_provenance WHERE entity_type = ? AND external_id = ?`, typ, ext).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := local("edition", "item1:audiobook"); got != e1.ID {
		t.Errorf("wrong row not repaired: got %d want %d", got, e1.ID)
	}
	if got := local("edition", "item2:audiobook"); got != e2.ID {
		t.Errorf("right row changed: got %d want %d", got, e2.ID)
	}
	if got := local("edition", "gone:audiobook"); got != 999 {
		t.Errorf("row with no edition changed: got %d", got)
	}
	if got := local("book", "item1:audiobook"); got != 55 {
		t.Errorf("non edition row changed: got %d", got)
	}
}
