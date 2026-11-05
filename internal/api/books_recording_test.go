package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestAttachRecordingPrecedence(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books, editions := db.NewBookRepo(database), db.NewEditionRepo(database)
	a := &models.Author{ForeignID: "OL-REC-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-REC-B", AuthorID: a.ID, Title: "B", SortTitle: "B", Status: "wanted",
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, Narrator: "Scott Brick, Simon Vance"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := editions.Upsert(ctx, &models.Edition{ForeignID: "abs:rec", BookID: b.ID, Title: "B", Format: "M4B",
		Monitored: true, Narrators: []string{"Scott Brick", "Simon Vance"}, EditionType: models.EditionTypeUnabridged}); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithEditionHydration(editions)

	// In step with the string: the edition's list and type show.
	h.attachRecording(ctx, b)
	if !reflect.DeepEqual(b.Narrators, []string{"Scott Brick", "Simon Vance"}) || b.EditionType != models.EditionTypeUnabridged {
		t.Fatalf("in step: %#v %q", b.Narrators, b.EditionType)
	}

	// A user edit of the string wins over the edition's older list.
	b.Narrator = "Rosamund Pike"
	h.attachRecording(ctx, b)
	if !reflect.DeepEqual(b.Narrators, []string{"Rosamund Pike"}) {
		t.Fatalf("user edit hidden: %#v", b.Narrators)
	}
	if b.EditionType != models.EditionTypeUnabridged {
		t.Fatalf("edition type lost: %q", b.EditionType)
	}
}
