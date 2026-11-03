package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// A download grabbed without a book (the free text Search page) is matched to
// a book after it completes. Under tenancy that match may only pick a book the
// download's owner can see: their own books and the unowned ones. An admin's
// download, an ownerless download and any download with tenancy off still
// match against the whole library.

type unboundFix struct {
	database  *sql.DB
	s         *Scanner
	downloads *db.DownloadRepo
	books     *db.BookRepo
	authors   *db.AuthorRepo
	admin     int64
	userA     int64
	userB     int64
	ctx       context.Context
}

func newUnboundFix(t *testing.T) unboundFix {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	admin, err := users.CreateFirstAdmin(ctx, "admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	a, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	s := NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors, history, t.TempDir(), "", "", "", "")
	s.WithSettings(settings)
	s.WithUnmatchedUnits(db.NewUnmatchedUnitRepo(database))
	s.WithUsers(users)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	return unboundFix{database, s, downloads, books, authors, admin.ID, a.ID, b.ID, ctx}
}

// seedBook adds "Pandora's Star" by Peter F. Hamilton owned by owner (0 for
// an unowned book and author).
func (f unboundFix) seedBook(t *testing.T, owner int64) *models.Book {
	t.Helper()
	author := &models.Author{ForeignID: "OL-PFH", Name: "Peter F. Hamilton", SortName: "Hamilton, Peter F.", Monitored: true, MetadataProvider: "openlibrary", OwnerUserID: owner}
	if err := f.authors.Create(f.ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-PS", AuthorID: author.ID, Title: "Pandora's Star", SortTitle: "Pandora's Star",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", OwnerUserID: owner,
	}
	if err := f.books.Create(f.ctx, book); err != nil {
		t.Fatal(err)
	}
	return book
}

// unboundTiers are the three signals an unbound download is matched by, each
// set up so it is the only one that can find the book.
var unboundTiers = []struct {
	name    string
	release string
	write   func(t *testing.T, dir string)
}{
	{
		name:    "embedded_metadata",
		release: "grab 7f3a",
		write: func(t *testing.T, dir string) {
			writeEpubAt(t, filepath.Join(dir, "x9q.epub"), "Pandora's Star", "Peter F. Hamilton", "9780345472199")
		},
	},
	{
		name:    "file_name",
		release: "grab 7f3a",
		write: func(t *testing.T, dir string) {
			writeUnboundPDF(t, filepath.Join(dir, "Pandora's Star - Peter F. Hamilton.pdf"))
		},
	},
	{
		name:    "release_title",
		release: "Peter F Hamilton - Pandora's Star (pdf)",
		write: func(t *testing.T, dir string) {
			writeUnboundPDF(t, filepath.Join(dir, "x9q.pdf"))
		},
	},
}

func writeUnboundPDF(t *testing.T, path string) {
	t.Helper()
	body := make([]byte, MinPlausibleEbookBytes+1024)
	copy(body, "%PDF-1.4\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// importUnbound runs one completed download with no book, owned by owner,
// through the importer and returns the book it ended up bound to (nil when it
// stayed unmatched).
func (f unboundFix) importUnbound(t *testing.T, owner int64, release string, write func(*testing.T, string)) *int64 {
	t.Helper()
	dir := t.TempDir()
	write(t, dir)
	dl := &models.Download{GUID: "guid-unbound", Title: release, Status: models.StateCompleted, NZBURL: "fake://url", OwnerUserID: owner}
	if err := f.downloads.Create(f.ctx, dl); err != nil {
		t.Fatal(err)
	}
	f.s.tryImportInternal(f.ctx, dl, dir, "", "", "", nil, nil)
	got, err := f.downloads.GetByGUID(f.ctx, "guid-unbound")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	return got.BookID
}

func TestUnboundDownload_TenancyDoesNotBindToAnotherUsersBook(t *testing.T) {
	for _, tier := range unboundTiers {
		t.Run(tier.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newUnboundFix(t)
			aliceBook := f.seedBook(t, f.userA)

			if got := f.importUnbound(t, f.userB, tier.release, tier.write); got != nil {
				t.Fatalf("bob's download bound to book %d (alice's book is %d); want it left unmatched", *got, aliceBook.ID)
			}
			b, err := f.books.GetByID(f.ctx, aliceBook.ID)
			if err != nil {
				t.Fatal(err)
			}
			if b.EbookFilePath != "" || b.Status != models.BookStatusWanted {
				t.Fatalf("alice's book changed: status=%s ebookFile=%q", b.Status, b.EbookFilePath)
			}
		})
	}
}

func TestUnboundDownload_TenancyBindsToOwnersOwnBook(t *testing.T) {
	for _, tier := range unboundTiers {
		t.Run(tier.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newUnboundFix(t)
			bobBook := f.seedBook(t, f.userB)

			got := f.importUnbound(t, f.userB, tier.release, tier.write)
			if got == nil || *got != bobBook.ID {
				t.Fatalf("bob's download bound to %v, want his own book %d", got, bobBook.ID)
			}
		})
	}
}

func TestUnboundDownload_TenancyBindsToUnownedBook(t *testing.T) {
	for _, tier := range unboundTiers {
		t.Run(tier.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newUnboundFix(t)
			shared := f.seedBook(t, 0)

			got := f.importUnbound(t, f.userB, tier.release, tier.write)
			if got == nil || *got != shared.ID {
				t.Fatalf("bob's download bound to %v, want the unowned book %d", got, shared.ID)
			}
		})
	}
}

func TestUnboundDownload_AdminOwnedBindsToAnyBook(t *testing.T) {
	for _, tier := range unboundTiers {
		t.Run(tier.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newUnboundFix(t)
			aliceBook := f.seedBook(t, f.userA)

			got := f.importUnbound(t, f.admin, tier.release, tier.write)
			if got == nil || *got != aliceBook.ID {
				t.Fatalf("admin's download bound to %v, want alice's book %d", got, aliceBook.ID)
			}
		})
	}
}

func TestUnboundDownload_OwnerlessBindsToAnyBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newUnboundFix(t)
	aliceBook := f.seedBook(t, f.userA)

	got := f.importUnbound(t, 0, unboundTiers[0].release, unboundTiers[0].write)
	if got == nil || *got != aliceBook.ID {
		t.Fatalf("ownerless download bound to %v, want alice's book %d", got, aliceBook.ID)
	}
}

func TestUnboundDownload_TenancyOffBindsToAnyBook(t *testing.T) {
	for _, tier := range unboundTiers {
		t.Run(tier.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, false)
			f := newUnboundFix(t)
			aliceBook := f.seedBook(t, f.userA)

			got := f.importUnbound(t, f.userB, tier.release, tier.write)
			if got == nil || *got != aliceBook.ID {
				t.Fatalf("with tenancy off bob's download bound to %v, want alice's book %d", got, aliceBook.ID)
			}
		})
	}
}

// An owner whose role cannot be established is scoped to their own books, not
// treated as an admin: neither a user row that has gone nor a scanner with no
// user repository wired lets the download reach another user's book.
func TestUnboundDownload_UnknownOwnerRoleStaysScoped(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f unboundFix)
	}{
		{
			name: "owner_user_row_missing",
			setup: func(t *testing.T, f unboundFix) {
				// The downloads FK would refuse the orphan; the memory database
				// is one connection, so the pragma holds for the rest of the test.
				if _, err := f.database.Exec("PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.database.Exec("DELETE FROM users WHERE id = ?", f.userB); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "no_user_repo",
			setup: func(_ *testing.T, f unboundFix) { f.s.users = nil },
		},
	}
	for _, c := range cases {
		for _, tier := range unboundTiers {
			t.Run(c.name+"/"+tier.name, func(t *testing.T) {
				auth.SetEnforceTenancyForTests(t, true)
				f := newUnboundFix(t)
				aliceBook := f.seedBook(t, f.userA)
				c.setup(t, f)

				if got := f.importUnbound(t, f.userB, tier.release, tier.write); got != nil {
					t.Fatalf("download of an owner with no known role bound to book %d (alice's book is %d); want it left unmatched", *got, aliceBook.ID)
				}
			})
		}
	}
}
