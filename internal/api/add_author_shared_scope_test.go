package api

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// sharedScopeCases are the two callers who see every author in the library
// list: any account with tenancy off, and an admin with tenancy on.
var sharedScopeCases = []struct {
	name    string
	tenancy bool
	admin   bool
}{
	{"tenancy off", false, false},
	{"admin under tenancy", true, true},
}

func (f tenancyFixture) callerCtx(admin bool) context.Context {
	if admin {
		return f.adminCtx()
	}
	return f.userCtx(f.alice)
}

func (f tenancyFixture) seedBobsHerbert(t *testing.T, foreignID string) *models.Author {
	t.Helper()
	a := &models.Author{
		ForeignID: foreignID, Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary",
	}
	if err := f.authors.CreateForUser(context.Background(), a, f.bob); err != nil {
		t.Fatal(err)
	}
	return a
}

// Add Book must file the book under an author another account already added
// when the caller can see that author in the library list. It used to look the
// author up by the caller's own id only, hit the unique foreign id, and answer
// "author already exists".
func TestAddBook_FilesUnderAuthorAddedByAnotherAccountWhenSharedScope(t *testing.T) {
	for _, tc := range sharedScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, duneAudiobookProvider())
			bobs := f.seedBobsHerbert(t, "OL-SHARED-A")

			res, err := f.h.addBookCore(f.callerCtx(tc.admin), duneAddParams)
			if err != nil {
				t.Fatalf("add book: %v", err)
			}
			if res.Book == nil || res.Book.AuthorID != bobs.ID {
				t.Fatalf("book = %+v, want it filed under the existing author %d", res.Book, bobs.ID)
			}
		})
	}
}

// Add Author with another provider's id but the same name must offer the
// existing author as the match, not create a second Frank Herbert.
func TestCreateAuthor_SameNameFindsAuthorAddedByAnotherAccountWhenSharedScope(t *testing.T) {
	for _, tc := range sharedScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubMetaProvider{author: &models.Author{
				ForeignID: "HC-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank",
				MetadataProvider: "hardcover",
			}}
			f := newTenancyFixture(t, tc.tenancy, provider)
			bobs := f.seedBobsHerbert(t, "OL-HERBERT")

			_, err := f.h.createAuthorCore(f.callerCtx(tc.admin), createAuthorParams{
				ForeignID: "HC-HERBERT", Name: "Frank Herbert", SkipCatalogueSync: true,
			})
			var conflict *authorConflictError
			if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != bobs.ID {
				t.Fatalf("create = %v, want a conflict naming the existing author %d", err, bobs.ID)
			}
			all, _ := f.authors.ListByUser(context.Background(), 0)
			if len(all) != 1 {
				t.Fatalf("library has %d authors, want 1", len(all))
			}
		})
	}
}

// Add Author with the provider id another account used must answer the
// conflict with that author attached, so the UI can point at it.
func TestCreateAuthor_SameForeignIDNamesAuthorAddedByAnotherAccountWhenSharedScope(t *testing.T) {
	for _, tc := range sharedScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, &stubMetaProvider{})
			bobs := f.seedBobsHerbert(t, "OL-HERBERT")

			_, err := f.h.createAuthorCore(f.callerCtx(tc.admin), createAuthorParams{
				ForeignID: "OL-HERBERT", Name: "Frank Herbert", SkipCatalogueSync: true,
			})
			var conflict *authorConflictError
			if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != bobs.ID {
				t.Fatalf("create = %v, want a conflict naming the existing author %d", err, bobs.ID)
			}
		})
	}
}
