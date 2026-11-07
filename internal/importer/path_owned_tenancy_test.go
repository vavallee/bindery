package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// twoUserImportFixture: alice and bob each own a "Jane Doe" author row
// (distinct foreign ids, as two providers or two adds under tenancy give) and
// share one root for every import, so a book of the same title renders to the
// same destination path for both.
type twoUserImportFixture struct {
	database  *sql.DB
	s         *Scanner
	books     *db.BookRepo
	downloads *db.DownloadRepo
	users     *db.UserRepo
	alice     int64
	bob       int64
	aliceA    *models.Author
	bobA      *models.Author
}

func newTwoUserImportFixture(t *testing.T) *twoUserImportFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	authors := db.NewAuthorRepo(database)
	f := &twoUserImportFixture{
		database:  database,
		books:     db.NewBookRepo(database),
		downloads: db.NewDownloadRepo(database),
		users:     users,
		alice:     alice.ID,
		bob:       bob.ID,
	}
	f.aliceA = &models.Author{ForeignID: "OL-JD-ALICE", Name: "Jane Doe", SortName: "Doe, Jane", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.CreateForUser(ctx, f.aliceA, alice.ID); err != nil {
		t.Fatal(err)
	}
	f.bobA = &models.Author{ForeignID: "hc:JD-BOB", Name: "Jane Doe", SortName: "Doe, Jane", Monitored: true, MetadataProvider: "hardcover"}
	if err := authors.CreateForUser(ctx, f.bobA, bob.ID); err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f.s = NewScanner(f.downloads, db.NewDownloadClientRepo(database), f.books, authors,
		db.NewHistoryRepo(database), root, root, "", "", "")
	f.s.WithSettings(settings)
	f.s.WithRootFolders(db.NewRootFolderRepo(database))
	f.s.WithSeriesRepo(db.NewSeriesRepo(database))
	f.s.WithUsers(users)
	return f
}

func (f *twoUserImportFixture) book(t *testing.T, author *models.Author, owner int64, foreign string) *models.Book {
	t.Helper()
	rel := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b := &models.Book{
		ForeignID: foreign, AuthorID: author.ID, Title: "Shared Title", SortTitle: "shared title",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: models.MediaTypeEbook,
		MetadataProvider: author.MetadataProvider, ReleaseDate: &rel, OwnerUserID: owner,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *twoUserImportFixture) importEbook(t *testing.T, b *models.Book, owner int64, guid, body string) *models.Download {
	t.Helper()
	return f.importEbookAs(t, b, owner, guid, body, false)
}

// importEbookAs imports one EPUB for b. With fixMatch set the import runs as
// an admin's Fix match of that downloaded file onto b, the way the Reassign
// handler runs it.
func (f *twoUserImportFixture) importEbookAs(t *testing.T, b *models.Book, owner int64, guid, body string, fixMatch bool) *models.Download {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "Shared Title.epub")
	if err := os.WriteFile(src, bookSized(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if fixMatch {
		ctx = WithFixMatch(ctx, FixMatch{SourcePaths: []string{src}})
	}
	dl := &models.Download{BookID: &b.ID, GUID: guid, Title: "Jane.Doe.Shared.Title.epub", Status: models.StateCompleted, Protocol: "torrent", OwnerUserID: owner}
	if err := f.downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	f.s.tryImportInternal(ctx, dl, dir, "qbittorrent", guid, "", nil, nil)
	got, err := f.downloads.GetByID(ctx, dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// importSecondOntoFirst imports the first book, then imports a file for the
// second book, whose destination renders to the same path, and returns the
// first book and the second download. The second import is blocked either
// way; what changes is the reason it gives.
//
// A plain import no longer reaches the block: since #3560 it takes the next
// free " (2)" name instead. A Fix match keeps its rendered destination (it is
// the one import allowed to take a tracked path, and only the reassigned
// file's own path), so an admin's Fix match of a downloaded file onto the
// second book is the reachable path that still blocks and still writes this
// reason onto the second user's download.
func (f *twoUserImportFixture) importSecondOntoFirst(t *testing.T, firstOwner, secondOwner int64, firstAuthor, secondAuthor *models.Author) (*models.Book, *models.Download) {
	t.Helper()
	first := f.book(t, firstAuthor, firstOwner, "OL-ST-FIRST")
	second := f.book(t, secondAuthor, secondOwner, "hc:ST-SECOND")
	if dl := f.importEbook(t, first, firstOwner, "guid-first", "first bytes"); dl.Status != models.StateImported {
		t.Fatalf("first import: %s %s", dl.Status, dl.ErrorMessage)
	}
	dl := f.importEbookAs(t, second, secondOwner, "guid-second", "second bytes", true)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("second import status = %s (%s), want %s", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	return first, dl
}

// TestImport_PathOwnedByOtherUsersBook_ReasonDoesNotNameIt: with tenancy on,
// bob's import that renders onto the path alice's book tracks is still
// blocked, and the reason bob reads in his queue says another book in the
// library uses the path without giving that book's title or id, or pointing
// him at a book he cannot open.
func TestImport_PathOwnedByOtherUsersBook_ReasonDoesNotNameIt(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTwoUserImportFixture(t)
	first, dl := f.importSecondOntoFirst(t, f.alice, f.bob, f.aliceA, f.bobA)

	for _, leak := range []string{fmt.Sprintf("id %d", first.ID), fmt.Sprintf("%q", first.Title), "Fix match"} {
		if strings.Contains(dl.ErrorMessage, leak) {
			t.Errorf("reason %q contains %q", dl.ErrorMessage, leak)
		}
	}
	if !strings.Contains(dl.ErrorMessage, "already used by another book in the library") {
		t.Errorf("reason %q does not say another book uses the path", dl.ErrorMessage)
	}
}

// TestImport_PathOwnedByOwnBookOrNoTenancy_ReasonNamesOwner: the holding book
// is still named, with the Fix match pointer, when tenancy is off (one shared
// library) or when the download's owner also owns that book.
func TestImport_PathOwnedByOwnBookOrNoTenancy_ReasonNamesOwner(t *testing.T) {
	cases := []struct {
		name     string
		tenancy  bool
		sameUser bool
	}{
		{"tenancy off, other user", false, false},
		{"tenancy on, same user", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := newTwoUserImportFixture(t)
			secondOwner, secondAuthor := f.bob, f.bobA
			if tc.sameUser {
				secondOwner = f.alice
			}
			first, dl := f.importSecondOntoFirst(t, f.alice, secondOwner, f.aliceA, secondAuthor)
			// Both download owners are plain users, so the hint sends them to
			// an admin rather than at an action they cannot run (#3560).
			for _, want := range []string{fmt.Sprintf("id %d", first.ID), fmt.Sprintf("%q", first.Title), "ask an admin to use Fix match"} {
				if !strings.Contains(dl.ErrorMessage, want) {
					t.Errorf("reason %q does not contain %q", dl.ErrorMessage, want)
				}
			}
		})
	}
}

// TestPathOwnedReason_HolderUnreadableIsNotNamed: with tenancy on, when the
// book holding the path cannot be read (gone, or the lookup fails) its owner
// is unknown, so the reason must not name it: the title and id on the error
// may be another user's.
func TestPathOwnedReason_HolderUnreadableIsNotNamed(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTwoUserImportFixture(t)
	ctx := context.Background()
	mine := f.book(t, f.bobA, f.bob, "hc:ST-MINE")
	dl := &models.Download{OwnerUserID: f.bob}
	owned := &db.PathOwnedError{Path: "/lib/Jane Doe/x.epub", OwnerBookID: 424242, OwnerTitle: "Secret Title"}

	check := func(name string) {
		t.Helper()
		err := f.s.pathOwnedReason(ctx, dl, mine, owned)
		var po *db.PathOwnedError
		if !errors.As(err, &po) {
			t.Errorf("%s: reason %v does not unwrap to *db.PathOwnedError", name, err)
		}
		for _, leak := range []string{"Secret Title", "424242", "Fix match"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: reason %q contains %q", name, err.Error(), leak)
			}
		}
	}
	check("missing holder")
	_ = f.database.Close()
	check("lookup error")
}

// TestPathOwnedReason_HintMatchesOwnerRole is the second half of #3560: Fix
// match is admin only, so the block reason only tells its reader to run it
// when that reader (the download's owner) is an admin. A plain user is told to
// ask an admin; an owner the importer cannot resolve gets wording that is
// true for either.
func TestPathOwnedReason_HintMatchesOwnerRole(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newTwoUserImportFixture(t)
	ctx := context.Background()
	if err := f.users.SetRoleUnguarded(ctx, f.alice, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	mine := f.book(t, f.bobA, f.bob, "hc:ST-MINE")
	owned := &db.PathOwnedError{Path: "/lib/Jane Doe/x.epub", OwnerBookID: 7, OwnerTitle: "Holder"}

	cases := []struct {
		name      string
		dl        *models.Download
		noUsers   bool
		want      string
		forbidden string
	}{
		{"admin owner", &models.Download{OwnerUserID: f.alice}, false, "; if the file is really \"Shared Title\", use Fix match", "ask an admin"},
		{"plain user owner", &models.Download{OwnerUserID: f.bob}, false, "ask an admin to use Fix match", ""},
		{"no owner recorded", &models.Download{}, false, "an admin can use Fix match", "ask an admin"},
		{"owner row missing", &models.Download{OwnerUserID: 99999}, false, "an admin can use Fix match", "ask an admin"},
		{"no user repository", &models.Download{OwnerUserID: f.bob}, true, "an admin can use Fix match", "ask an admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.s.WithUsers(f.users)
			if tc.noUsers {
				f.s.WithUsers(nil)
			}
			err := f.s.pathOwnedReason(ctx, tc.dl, mine, owned)
			var po *db.PathOwnedError
			if !errors.As(err, &po) {
				t.Fatalf("reason %v does not unwrap to *db.PathOwnedError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not contain %q", err.Error(), tc.want)
			}
			if tc.forbidden != "" && strings.Contains(err.Error(), tc.forbidden) {
				t.Errorf("reason %q contains %q", err.Error(), tc.forbidden)
			}
		})
	}
}

// TestImport_SameTitleTwoUsers_SecondImportGetsSuffix is the #3560 repro: a
// plain user's book renders onto the path another user's book already holds.
// The import used to block for good, pointing at an admin only action. It now
// lands beside the first at "<name> (2).epub" and leaves the first file alone.
func TestImport_SameTitleTwoUsers_SecondImportGetsSuffix(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTwoUserImportFixture(t)
	ctx := context.Background()
	first := f.book(t, f.aliceA, f.alice, "OL-ST-FIRST")
	second := f.book(t, f.bobA, f.bob, "hc:ST-SECOND")
	if dl := f.importEbook(t, first, f.alice, "guid-first", "first bytes"); dl.Status != models.StateImported {
		t.Fatalf("first import: %s %s", dl.Status, dl.ErrorMessage)
	}
	dl := f.importEbook(t, second, f.bob, "guid-second", "second bytes")
	if dl.Status != models.StateImported {
		t.Fatalf("second import status = %s (%s), want %s", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	firstFiles, err := f.books.ListFiles(ctx, first.ID)
	if err != nil || len(firstFiles) != 1 {
		t.Fatalf("first book files = %+v, %v", firstFiles, err)
	}
	secondFiles, err := f.books.ListFiles(ctx, second.ID)
	if err != nil || len(secondFiles) != 1 {
		t.Fatalf("second book files = %+v, %v", secondFiles, err)
	}
	want := strings.TrimSuffix(firstFiles[0].Path, ".epub") + " (2).epub"
	if secondFiles[0].Path != want {
		t.Errorf("second book path = %s, want %s", secondFiles[0].Path, want)
	}
	if got, _ := os.ReadFile(firstFiles[0].Path); !strings.HasPrefix(string(got), "first bytes") { //nolint:gosec // test path under t.TempDir
		t.Errorf("first book's file was replaced: %q", got)
	}
}
