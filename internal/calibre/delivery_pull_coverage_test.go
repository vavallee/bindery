package calibre

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

func TestDeliverer_NotePullContactIsCopiedOut(t *testing.T) {
	f, _ := newPullFixture(t)
	fixed := time.Date(2026, 10, 1, 9, 30, 0, 0, time.FixedZone("x", 3600))
	f.d.now = func() time.Time { return fixed }

	if got := f.d.PullContact(); got.LastSeen != nil || got.PluginVersion != "" {
		t.Fatalf("contact before any call = %+v, want empty", got)
	}

	caps := []string{"add_format", "cover"}
	f.d.NotePullContact(" 0.7.0\x00\n ", caps, "10.0.0.9:51234")
	caps[0] = "mutated"

	got := f.d.PullContact()
	if got.LastSeen == nil || !got.LastSeen.Equal(fixed) || got.LastSeen.Location() != time.UTC {
		t.Fatalf("LastSeen = %v, want %v in UTC", got.LastSeen, fixed)
	}
	if got.PluginVersion != "0.7.0" || got.RemoteAddr != "10.0.0.9:51234" {
		t.Fatalf("contact = %+v, want the cleaned version and the remote", got)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"add_format", "cover"}) {
		t.Fatalf("capabilities = %v; the caller's slice must not alias the stored one", got.Capabilities)
	}
	got.Capabilities[0] = "changed by caller"
	if again := f.d.PullContact(); again.Capabilities[0] != "add_format" {
		t.Fatal("PullContact must return a copy")
	}
}

func TestDeliverer_PullPending(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")

	got, err := f.d.PullPending(f.ctx, row.ID)
	if err != nil || got == nil || got.ID != row.ID {
		t.Fatalf("pending row = %+v, %v", got, err)
	}
	if _, err := f.d.PullPending(f.ctx, row.ID+999); !errors.Is(err, ErrPullNotFound) {
		t.Fatalf("missing row: %v, want ErrPullNotFound", err)
	}
	if err := f.d.PullAck(f.ctx, row.ID, PullAckRequest{CalibreID: 3, Outcome: DeliveryOutcomeAdded}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.PullPending(f.ctx, row.ID); !errors.Is(err, ErrPullNotFound) {
		t.Fatalf("delivered row: %v, want ErrPullNotFound (only pending rows are served)", err)
	}

	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.d.PullPending(ctx, row.ID); err == nil || errors.Is(err, ErrPullNotFound) {
		t.Fatalf("cancelled lookup: %v, want the store error", err)
	}
}

func TestDeliverer_PullFileMissingSkipsTheRow(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")
	f.d.PullFileMissing(f.ctx, &row)

	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliverySkipped || got.Outcome != deliverySkipNoFile {
		t.Fatalf("row = %+v, want skipped with %q", got, deliverySkipNoFile)
	}
	// A row that vanished is not an error either: the skip just has nothing
	// to write.
	gone := row
	gone.ID = row.ID + 999
	f.d.PullFileMissing(f.ctx, &gone)
}

func TestDeliverer_PullCoverPath(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")

	// No cover on the book: nothing to serve.
	if p, err := f.d.PullCoverPath(f.ctx, &row); err != nil || p != "" {
		t.Fatalf("no cover: %q, %v", p, err)
	}

	// A stored cover resolves to its file.
	store := covers.NewStore(filepath.Join(t.TempDir(), "covers"))
	src := filepath.Join(t.TempDir(), "c.jpg")
	if err := os.WriteFile(src, testJPEG(), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := store.Resolve(ref)
	f.d.WithCovers(CoverSource{Store: store, CacheDir: t.TempDir()})
	if err := f.books.SetImageURL(f.ctx, f.book.ID, ref); err != nil {
		t.Fatal(err)
	}
	if p, err := f.d.PullCoverPath(f.ctx, &row); err != nil || p != want {
		t.Fatalf("stored cover: %q, %v; want %q", p, err, want)
	}

	// A book that is gone has no cover and no error.
	orphan := row
	orphan.BookID = f.book.ID + 999
	if p, err := f.d.PullCoverPath(f.ctx, &orphan); err != nil || p != "" {
		t.Fatalf("missing book: %q, %v", p, err)
	}

	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.d.PullCoverPath(ctx, &row); err == nil {
		t.Fatal("cancelled lookup: want the store error")
	}
}

func TestCoverSource_Available(t *testing.T) {
	store := covers.NewStore(filepath.Join(t.TempDir(), "covers"))
	src := filepath.Join(t.TempDir(), "c.jpg")
	if err := os.WriteFile(src, testJPEG(), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	missingRef := covers.Scheme + strings.Repeat("0", 64) + ".jpg"

	withAll := CoverSource{Store: store, CacheDir: t.TempDir()}
	cases := []struct {
		name string
		src  CoverSource
		url  string
		want bool
	}{
		{"blank", withAll, "  ", false},
		{"stored ref", withAll, ref, true},
		{"absent ref", withAll, missingRef, false},
		{"ref without a store", CoverSource{CacheDir: t.TempDir()}, ref, false},
		{"https with a cache", withAll, "HTTPS://example.com/c.jpg", true},
		{"http with a cache", withAll, "http://example.com/c.jpg", true},
		{"remote without a cache", CoverSource{Store: store}, "https://example.com/c.jpg", false},
		{"other scheme", withAll, "ftp://example.com/c.jpg", false},
		{"host path", withAll, "/library/Author/Book/cover.jpg", false},
	}
	for _, tc := range cases {
		if got := tc.src.Available(tc.url); got != tc.want {
			t.Errorf("%s: Available(%q) = %v, want %v", tc.name, tc.url, got, tc.want)
		}
	}
}

func TestPullList_RejectsABadCursorAndClampsTheLimit(t *testing.T) {
	f, _ := newPullFixture(t)
	for _, c := range []string{"abc", "-4"} {
		if _, err := f.d.PullList(f.ctx, PullCaps{}, c, 10); !errors.Is(err, ErrPullInvalid) {
			t.Errorf("cursor %q: %v, want ErrPullInvalid", c, err)
		}
	}
	for i := 0; i < 3; i++ {
		f.addBook(t, fmt.Sprintf("Book %d", i), "b.epub")
	}
	for _, limit := range []int{0, -1, PullMaxBatch + 100} {
		page, err := f.d.PullList(f.ctx, PullCaps{}, "", limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Deliveries) != 3 || page.NextCursor != "" || page.Pending != 3 {
			t.Errorf("limit %d: %d items, cursor %q, pending %d; want all 3 on one page", limit, len(page.Deliveries), page.NextCursor, page.Pending)
		}
	}

	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.d.PullList(ctx, PullCaps{AddFormat: true}, "", 5); err == nil {
		t.Fatal("cancelled listing: want an error")
	}
}

// The listing offers a cover only when one could be served, using the
// cache-free check rather than a download.
func TestPullList_HasCoverFollowsTheCoverSource(t *testing.T) {
	f, _ := newPullFixture(t)
	cacheDir := t.TempDir()
	f.d.WithCovers(CoverSource{CacheDir: cacheDir})
	imageURL := "https://93.184.216.34/c.jpg"
	sum := sha256.Sum256([]byte(imageURL))
	if err := os.WriteFile(filepath.Join(cacheDir, fmt.Sprintf("%x.jpg", sum)), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.SetImageURL(f.ctx, f.book.ID, imageURL); err != nil {
		t.Fatal(err)
	}
	row := f.addFile(t, "a.epub")
	page, err := f.d.PullList(f.ctx, PullCaps{}, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Deliveries) != 1 || page.Deliveries[0].ID != row.ID || !page.Deliveries[0].HasCover {
		t.Fatalf("page = %+v, want the row with hasCover", page.Deliveries)
	}
}

func TestPullAck_Validation(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")

	invalid := []PullAckRequest{
		{CalibreID: 1, Outcome: "maybe"},
		{CalibreID: 0, Outcome: DeliveryOutcomeAdded},
		{CalibreID: 1, Outcome: DeliveryOutcomeAdded, Library: strings.Repeat("x", pullMaxLibraryLen+1)},
	}
	for _, req := range invalid {
		if err := f.d.PullAck(f.ctx, row.ID, req); !errors.Is(err, ErrPullInvalid) {
			t.Errorf("ack %+v: %v, want ErrPullInvalid", req, err)
		}
	}
	if err := f.d.PullAck(f.ctx, row.ID+999, PullAckRequest{CalibreID: 1, Outcome: DeliveryOutcomeAdded}); !errors.Is(err, ErrPullNotFound) {
		t.Errorf("missing row: %v, want ErrPullNotFound", err)
	}

	// A skipped row cannot be acknowledged.
	f.d.PullFileMissing(f.ctx, &row)
	if err := f.d.PullAck(f.ctx, row.ID, PullAckRequest{CalibreID: 1, Outcome: DeliveryOutcomeAdded}); !errors.Is(err, ErrPullNotPending) {
		t.Errorf("skipped row: %v, want ErrPullNotPending", err)
	}

	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := f.d.PullAck(ctx, row.ID, PullAckRequest{CalibreID: 1, Outcome: DeliveryOutcomeAdded}); err == nil {
		t.Error("cancelled ack: want the store error")
	}
}

// "already" records the delivery and, once a library is named, the contact
// reports it.
func TestPullAck_AlreadyRecordsTheLibrary(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")
	if err := f.d.PullAck(f.ctx, row.ID, PullAckRequest{CalibreID: 12, Outcome: DeliveryOutcomeAlready, Library: " /calibre "}); err != nil {
		t.Fatal(err)
	}
	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliveryDelivered || got.Outcome != DeliveryOutcomeAlready {
		t.Fatalf("row = %+v, want delivered/already", got)
	}
	if lib := f.d.PullContact().Library; lib != "/calibre" {
		t.Fatalf("contact library = %q, want /calibre", lib)
	}
	// A blank library on a later ack keeps the one already known.
	f.d.notePullLibrary("   ")
	if lib := f.d.PullContact().Library; lib != "/calibre" {
		t.Fatalf("blank library replaced the known one: %q", lib)
	}
}

func TestPullNack_ValidationAndMessage(t *testing.T) {
	f, _ := newPullFixture(t)
	row := f.addFile(t, "a.epub")

	if err := f.d.PullNack(f.ctx, row.ID, PullNackRequest{Code: ""}); !errors.Is(err, ErrPullInvalid) {
		t.Errorf("empty code: %v, want ErrPullInvalid", err)
	}
	if err := f.d.PullNack(f.ctx, row.ID, PullNackRequest{Code: strings.Repeat("a", bridgeMaxToken+1)}); !errors.Is(err, ErrPullInvalid) {
		t.Errorf("long code: %v, want ErrPullInvalid", err)
	}
	if err := f.d.PullNack(f.ctx, row.ID+999, PullNackRequest{Code: "busy"}); !errors.Is(err, ErrPullNotFound) {
		t.Errorf("missing row: %v, want ErrPullNotFound", err)
	}

	// A long message is cut to the cap.
	if err := f.d.PullNack(f.ctx, row.ID, PullNackRequest{Code: "Busy", Error: strings.Repeat("e", pullMaxErrorLen+50), Retryable: true}); err != nil {
		t.Fatal(err)
	}
	got := f.row(t, row.ID)
	if got.LastErrorCode != "busy" || len(got.LastError) != pullMaxErrorLen {
		t.Fatalf("row code %q, error length %d; want busy and %d", got.LastErrorCode, len(got.LastError), pullMaxErrorLen)
	}

	// With no message, the code stands in for one.
	other := f.addFile(t, "b.epub")
	if err := f.d.PullNack(f.ctx, other.ID, PullNackRequest{Code: "locked", Retryable: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.row(t, other.ID); got.LastError != "locked" {
		t.Fatalf("LastError = %q, want the code", got.LastError)
	}

	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := f.d.PullNack(ctx, other.ID, PullNackRequest{Code: "busy"}); err == nil {
		t.Error("cancelled nack: want the store error")
	}
}

func TestParseBridgeCapabilities_CapsTheList(t *testing.T) {
	parts := make([]string, 0, bridgeMaxCapabilities+10)
	for i := 0; i < bridgeMaxCapabilities+10; i++ {
		parts = append(parts, fmt.Sprintf("cap_%d", i))
	}
	got := ParseBridgeCapabilities(strings.Join(parts, ","))
	if len(got) != bridgeMaxCapabilities || got[0] != "cap_0" {
		t.Fatalf("kept %d capabilities (first %q), want %d starting at cap_0", len(got), got[0], bridgeMaxCapabilities)
	}
}

func TestCleanBridgeVersion_TruncatesAndStripsControlCharacters(t *testing.T) {
	if got := CleanBridgeVersion(" 0.7.0\t-rc 1 "); got != "0.7.0-rc1" {
		t.Errorf("CleanBridgeVersion = %q, want 0.7.0-rc1", got)
	}
	long := strings.Repeat("9", bridgeMaxToken*2)
	if got := CleanBridgeVersion(long); len(got) != bridgeMaxToken {
		t.Errorf("long version kept %d bytes, want %d", len(got), bridgeMaxToken)
	}
}
