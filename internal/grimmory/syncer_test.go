package grimmory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type fakeBookLister struct {
	books []models.Book
	err   error
}

func (f *fakeBookLister) ListByStatus(context.Context, string) ([]models.Book, error) {
	return f.books, f.err
}

func waitForSync(t *testing.T, s *Syncer) SyncProgress {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p := s.Progress()
		if !p.Running && p.FinishedAt != nil {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("sync did not finish in time")
	return SyncProgress{}
}

func TestSyncer_PushesEligibleBooks(t *testing.T) {
	store := &fakeStore{has: map[string]bool{"/lib/b.epub": true}}
	up := &fakeUploader{}
	pusher := newTestPusher(store, up)
	books := &fakeBookLister{books: []models.Book{
		{ID: 1, Title: "A", EbookFilePath: "/lib/a.epub"},
		{ID: 2, Title: "B", FilePath: "/lib/b.epub"}, // legacy path column, already pushed
		{ID: 3, Title: "C"},                          // no file — not eligible
	}}
	s := NewSyncer(books, pusher)

	if err := s.Start(context.Background(), enabledCfg()); err != nil {
		t.Fatal(err)
	}
	p := waitForSync(t, s)

	if p.Stats.Total != 2 || p.Stats.Pushed != 1 || p.Stats.AlreadyPushed != 1 || p.Stats.Failed != 0 {
		t.Fatalf("stats = %+v, want total=2 pushed=1 alreadyPushed=1", p.Stats)
	}
	if len(up.calls) != 1 || up.calls[0] != "/lib/a.epub" {
		t.Fatalf("uploads = %v, want just /lib/a.epub", up.calls)
	}
}

func TestSyncer_SecondStartWhileRunningConflicts(t *testing.T) {
	block := make(chan struct{})
	up := &fakeUploader{}
	pusher := NewPusher(enabledCfg, &fakeStore{}).WithClientFactory(func(PushConfig) (Uploader, error) {
		<-block // hold the run in-flight
		return up, nil
	})
	books := &fakeBookLister{books: []models.Book{{ID: 1, Title: "A", EbookFilePath: "/lib/a.epub"}}}
	s := NewSyncer(books, pusher)

	if err := s.Start(context.Background(), enabledCfg()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), enabledCfg()); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrSyncAlreadyRunning", err)
	}
	close(block)
	waitForSync(t, s)
}

func TestSyncer_RecordsFailures(t *testing.T) {
	up := &fakeUploader{err: errors.New("bookdrop rejected")}
	pusher := newTestPusher(&fakeStore{}, up)
	books := &fakeBookLister{books: []models.Book{
		{ID: 1, Title: "A", EbookFilePath: "/lib/a.epub"},
		{ID: 2, Title: "B", EbookFilePath: "/lib/b.epub"},
	}}
	s := NewSyncer(books, pusher)

	if err := s.Start(context.Background(), enabledCfg()); err != nil {
		t.Fatal(err)
	}
	p := waitForSync(t, s)

	if p.Stats.Failed != 2 {
		t.Fatalf("failed = %d, want 2", p.Stats.Failed)
	}
	if len(p.Errors) != 2 || p.Errors[0].Reason != "bookdrop rejected" {
		t.Fatalf("errors = %+v, want two entries with the upload reason", p.Errors)
	}
}
