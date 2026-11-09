package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type exportFixture struct {
	database *sql.DB
	h        *LibraryExportHandler
	books    *db.BookRepo
	files    *db.BookFileRepo
	editions *db.EditionRepo
	series   *db.SeriesRepo
	author   *models.Author
}

func newExportFixture(t *testing.T, database *sql.DB) *exportFixture {
	t.Helper()
	f := &exportFixture{
		database: database,
		books:    db.NewBookRepo(database),
		files:    db.NewBookFileRepo(database),
		editions: db.NewEditionRepo(database),
		series:   db.NewSeriesRepo(database),
	}
	f.h = NewLibraryExportHandler(f.books, f.files, f.editions, f.series)
	f.author = &models.Author{ForeignID: "OL-EXP-A", Name: "Export Author", SortName: "Author, Export", MetadataProvider: "openlibrary", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(context.Background(), f.author); err != nil {
		t.Fatal(err)
	}
	return f
}

func newExportFixtureMem(t *testing.T) *exportFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return newExportFixture(t, database)
}

func (f *exportFixture) book(t *testing.T, n int) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: fmt.Sprintf("OL-EXP-B%d", n), AuthorID: f.author.ID, Title: fmt.Sprintf("Book %03d", n),
		SortTitle: fmt.Sprintf("book %03d", n), Status: models.BookStatusWanted, Monitored: true,
		MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *exportFixture) get(t *testing.T, query string, uid int64, role string) (*httptest.ResponseRecorder, libraryExportResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/library/export"+query, nil)
	req = req.WithContext(withAuthCtx(req.Context(), uid, role))
	rec := httptest.NewRecorder()
	f.h.Export(rec, req)
	var out libraryExportResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestLibraryExport_EmptyLibrary(t *testing.T) {
	f := newExportFixtureMem(t)
	rec, out := f.get(t, "", 0, auth.RoleAdmin)
	if rec.Code != http.StatusOK || out.Version != 1 || out.NextCursor != "" || len(out.Items) != 0 {
		t.Fatalf("got %d %+v", rec.Code, out)
	}
	// items must be [] and not null for a client that iterates it.
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("items must serialise as an empty array: %s", rec.Body.String())
	}
}

func TestLibraryExport_PagesWithAStableCursor(t *testing.T) {
	f := newExportFixtureMem(t)
	var ids []int64
	for i := 1; i <= 7; i++ {
		ids = append(ids, f.book(t, i).ID)
	}

	var seen []int64
	cursor := ""
	pages := 0
	for {
		q := "?limit=3"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		_, out := f.get(t, q, 0, auth.RoleAdmin)
		pages++
		for _, it := range out.Items {
			seen = append(seen, it.ID)
		}
		if pages == 1 {
			// A book added mid walk, and one removed, must not shift the next page.
			f.book(t, 99)
			if err := f.books.Delete(context.Background(), ids[1]); err != nil {
				t.Fatal(err)
			}
		}
		if out.NextCursor == "" {
			break
		}
		if len(out.Items) != 3 {
			t.Fatalf("a page with a next cursor must be full, got %d", len(out.Items))
		}
		cursor = out.NextCursor
		if pages > 10 {
			t.Fatal("cursor never ended")
		}
	}
	want := []int64{ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6]}
	// ids[1] was already served on page one, so it stays; the book added later has a higher id and is reached.
	seenSet := map[int64]int{}
	for _, id := range seen {
		seenSet[id]++
		if seenSet[id] > 1 {
			t.Fatalf("book %d served twice: %v", id, seen)
		}
	}
	for _, id := range want {
		if id == ids[1] {
			continue
		}
		if seenSet[id] != 1 {
			t.Fatalf("book %d never served: %v", id, seen)
		}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("pages must be in ascending id order: %v", seen)
		}
	}
}

func TestLibraryExport_LastPageHasEmptyCursor(t *testing.T) {
	f := newExportFixtureMem(t)
	for i := 1; i <= 3; i++ {
		f.book(t, i)
	}
	_, out := f.get(t, "?limit=3", 0, auth.RoleAdmin)
	if len(out.Items) != 3 || out.NextCursor != "" {
		t.Fatalf("exactly one full page must not advertise a next page: %+v", out)
	}
}

func TestLibraryExport_LimitIsCappedAndValidated(t *testing.T) {
	f := newExportFixtureMem(t)
	for i := 1; i <= 205; i++ {
		f.book(t, i)
	}
	_, out := f.get(t, "?limit=100000", 0, auth.RoleAdmin)
	if len(out.Items) != 200 || out.NextCursor == "" {
		t.Fatalf("limit must cap at 200, got %d items, cursor %q", len(out.Items), out.NextCursor)
	}
	_, def := f.get(t, "", 0, auth.RoleAdmin)
	if len(def.Items) != 100 {
		t.Fatalf("default limit is 100, got %d", len(def.Items))
	}
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=abc", "?cursor=abc", "?cursor=-5"} {
		if rec, _ := f.get(t, q, 0, auth.RoleAdmin); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rec.Code)
		}
	}
}

func TestLibraryExport_ItemShape(t *testing.T) {
	f := newExportFixtureMem(t)
	ctx := context.Background()
	b := f.book(t, 1)
	if err := f.files.Add(ctx, b.ID, "ebook", "/lib/a/Book.EPUB"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE book_files SET size_bytes = 1234 WHERE book_id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	isbn := "9780000000002"
	if err := f.editions.Upsert(ctx, &models.Edition{ForeignID: "OL-ED-1", BookID: b.ID, Title: "Book 001", ISBN13: &isbn, Format: "Paperback", Language: "eng", IsEbook: false}); err != nil {
		t.Fatal(err)
	}
	s1 := &models.Series{ForeignID: "S1", Title: "First Series"}
	s2 := &models.Series{ForeignID: "S2", Title: "Second Series"}
	for _, s := range []*models.Series{s1, s2} {
		if err := f.series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.series.LinkBook(ctx, s1.ID, b.ID, "2", false); err != nil {
		t.Fatal(err)
	}
	if err := f.series.LinkBook(ctx, s2.ID, b.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	bare := f.book(t, 2)

	rec, out := f.get(t, "", 0, auth.RoleAdmin)
	if rec.Code != http.StatusOK || len(out.Items) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	it := out.Items[0]
	if it.ID != b.ID || it.Title != "Book 001" || it.AuthorID != f.author.ID || it.AuthorName != "Export Author" {
		t.Fatalf("book fields: %+v", it)
	}
	if len(it.Files) != 1 || it.Files[0] != (libraryExportFile{Path: "/lib/a/Book.EPUB", Format: "epub", Size: 1234, MediaType: "ebook"}) {
		t.Fatalf("files: %+v", it.Files)
	}
	if len(it.Editions) != 1 || it.Editions[0].ISBN13 != isbn || it.Editions[0].ISBN10 != "" || it.Editions[0].ForeignID != "OL-ED-1" || it.Editions[0].Language != "eng" {
		t.Fatalf("editions: %+v", it.Editions)
	}
	if len(it.Series) != 2 || it.Series[0] != (libraryExportSeries{ID: s2.ID, Title: "Second Series", Position: "1", Primary: true}) ||
		it.Series[1] != (libraryExportSeries{ID: s1.ID, Title: "First Series", Position: "2", Primary: false}) {
		t.Fatalf("series (primary first): %+v", it.Series)
	}
	// A book with nothing attached still has all three arrays, never null.
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"files", "editions", "series"} {
		if string(raw.Items[1][k]) != "[]" {
			t.Errorf("book %d %s = %s, want []", bare.ID, k, raw.Items[1][k])
		}
	}
	for _, k := range []string{"id", "title", "authorId", "authorName", "files", "editions", "series"} {
		if _, ok := raw.Items[0][k]; !ok {
			t.Errorf("contract key %q missing", k)
		}
	}
}

func TestLibraryExport_ExcludedBooksAreLeftOut(t *testing.T) {
	f := newExportFixtureMem(t)
	keep, drop := f.book(t, 1), f.book(t, 2)
	if _, err := f.database.Exec(`UPDATE books SET excluded = 1 WHERE id = ?`, drop.ID); err != nil {
		t.Fatal(err)
	}
	_, out := f.get(t, "", 0, auth.RoleAdmin)
	if len(out.Items) != 1 || out.Items[0].ID != keep.ID {
		t.Fatalf("got %+v", out.Items)
	}
}

func TestLibraryExport_TenancyScopesLikeTheBookList(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newExportFixtureMem(t)
	users := db.NewUserRepo(f.database)
	alice, err := users.Create(context.Background(), "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(context.Background(), "bob", "h")
	if err != nil {
		t.Fatal(err)
	}
	a, b, shared := f.book(t, 1), f.book(t, 2), f.book(t, 3)
	setOwner(t, f.database, "books", a.ID, alice.ID)
	setOwner(t, f.database, "books", b.ID, bob.ID)
	if _, err := f.database.Exec(`UPDATE books SET owner_user_id = NULL WHERE id = ?`, shared.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(o libraryExportResponse) []int64 {
		var r []int64
		for _, it := range o.Items {
			r = append(r, it.ID)
		}
		return r
	}
	_, outA := f.get(t, "", alice.ID, auth.RoleUser)
	if got := ids(outA); len(got) != 2 || got[0] != a.ID || got[1] != shared.ID {
		t.Fatalf("alice sees her own and unowned books only, got %v", got)
	}
	_, outB := f.get(t, "", bob.ID, auth.RoleUser)
	if got := ids(outB); len(got) != 2 || got[0] != b.ID || got[1] != shared.ID {
		t.Fatalf("bob sees his own and unowned books only, got %v", got)
	}
	_, outAdmin := f.get(t, "", 99, auth.RoleAdmin)
	if len(outAdmin.Items) != 3 {
		t.Fatalf("admin sees all, got %v", ids(outAdmin))
	}
	// A cursor cannot be used to reach past the scope.
	_, paged := f.get(t, "?limit=1&cursor="+strconv.FormatInt(a.ID, 10), alice.ID, auth.RoleUser)
	if got := ids(paged); len(got) != 1 || got[0] != shared.ID {
		t.Fatalf("alice paging past her book must skip bob's, got %v", got)
	}
}

// --- query count --------------------------------------------------------

type exportQueryLog struct {
	mu sync.Mutex
	n  int
}

func (l *exportQueryLog) add()   { l.mu.Lock(); l.n++; l.mu.Unlock() }
func (l *exportQueryLog) reset() { l.mu.Lock(); l.n = 0; l.mu.Unlock() }
func (l *exportQueryLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

type exportCountingDriver struct {
	inner driver.Driver
	log   *exportQueryLog
}

func (d exportCountingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &exportCountingConn{Conn: c, log: d.log}, nil
}

type exportCountingConn struct {
	driver.Conn
	log *exportQueryLog
}

func (c *exportCountingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.log.add()
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *exportCountingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.log.add()
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *exportCountingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, q)
	}
	return c.Prepare(q)
}

func (c *exportCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bc, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bc.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx
}

var exportDriverSeq atomic.Int64

// A page costs the same number of queries whether it holds 2 books or 150:
// files, editions and series are each loaded once for the whole page.
func TestLibraryExport_BatchLoadingDoesNotScaleWithPageSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.db")
	seed, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	counted := &exportQueryLog{}
	name := "sqlite-export-counting-" + strconv.FormatInt(exportDriverSeq.Add(1), 10)
	sql.Register(name, exportCountingDriver{inner: &sqlite.Driver{}, log: counted})
	database, err := sql.Open(name, path)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })

	f := newExportFixture(t, database)
	ctx := context.Background()
	sr := &models.Series{ForeignID: "S-CNT", Title: "Counted"}
	if err := f.series.Create(ctx, sr); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 150; i++ {
		b := f.book(t, i)
		if err := f.files.Add(ctx, b.ID, "ebook", fmt.Sprintf("/lib/%d.epub", i)); err != nil {
			t.Fatal(err)
		}
		if err := f.editions.Upsert(ctx, &models.Edition{ForeignID: fmt.Sprintf("OL-CNT-%d", i), BookID: b.ID, Title: b.Title}); err != nil {
			t.Fatal(err)
		}
		if err := f.series.LinkBook(ctx, sr.ID, b.ID, strconv.Itoa(i), true); err != nil {
			t.Fatal(err)
		}
	}

	counted.reset()
	_, small := f.get(t, "?limit=2", 0, auth.RoleAdmin)
	nSmall := counted.count()
	counted.reset()
	_, big := f.get(t, "?limit=150", 0, auth.RoleAdmin)
	nBig := counted.count()

	if len(small.Items) != 2 || len(big.Items) != 150 {
		t.Fatalf("pages: %d and %d items", len(small.Items), len(big.Items))
	}
	if len(big.Items[149].Files) != 1 || len(big.Items[149].Editions) != 1 || len(big.Items[149].Series) != 1 {
		t.Fatalf("attachments missing on the last item: %+v", big.Items[149])
	}
	if nSmall == 0 || nBig == 0 {
		t.Fatalf("the counting driver saw no queries (%d and %d), so the comparison below proves nothing", nSmall, nBig)
	}
	if nSmall != nBig {
		t.Fatalf("queries scale with page size: %d for 2 books, %d for 150", nBig, nSmall)
	}
	if nBig > 4 {
		t.Fatalf("a page should cost four queries, got %d", nBig)
	}
}
