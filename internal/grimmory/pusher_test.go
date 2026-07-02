package grimmory

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct {
	has      map[string]bool
	recorded []string
	hasErr   error
}

func (f *fakeStore) Has(_ context.Context, path string) (bool, error) {
	return f.has[path], f.hasErr
}

func (f *fakeStore) Record(_ context.Context, _ int64, path string, _ int64) error {
	f.recorded = append(f.recorded, path)
	if f.has == nil {
		f.has = map[string]bool{}
	}
	f.has[path] = true
	return nil
}

type fakeUploader struct {
	calls []string
	err   error
	id    int64
}

func (f *fakeUploader) UploadBookDrop(_ context.Context, path string) (int64, error) {
	f.calls = append(f.calls, path)
	return f.id, f.err
}

func enabledCfg() PushConfig {
	return PushConfig{Enabled: true, BaseURL: "http://grimmory:6060", Username: "bindery", Password: "s3cret"}
}

func newTestPusher(store *fakeStore, up *fakeUploader) *Pusher {
	return NewPusher(enabledCfg, store).WithClientFactory(func(PushConfig) (Uploader, error) {
		return up, nil
	})
}

func TestPushConfigReady(t *testing.T) {
	tests := []struct {
		name string
		cfg  PushConfig
		want bool
	}{
		{"disabled", PushConfig{}, false},
		{"no url", PushConfig{Enabled: true}, false},
		{"no creds", PushConfig{Enabled: true, BaseURL: "http://x"}, false},
		{"username auth", PushConfig{Enabled: true, BaseURL: "http://x", Username: "u", Password: "p"}, true},
		{"api key auth", PushConfig{Enabled: true, BaseURL: "http://x", APIKey: "tok"}, true},
	}
	for _, tt := range tests {
		if got, reason := tt.cfg.Ready(); got != tt.want {
			t.Errorf("%s: Ready() = %v (%s), want %v", tt.name, got, reason, tt.want)
		}
	}
}

func TestPushOnImport_PushesAndRecords(t *testing.T) {
	store := &fakeStore{}
	up := &fakeUploader{id: 42}
	p := newTestPusher(store, up)

	p.PushOnImport(context.Background(), 7, "Night Flights", "/books/x.epub")

	if len(up.calls) != 1 || up.calls[0] != "/books/x.epub" {
		t.Fatalf("uploader calls = %v, want one push of /books/x.epub", up.calls)
	}
	if len(store.recorded) != 1 {
		t.Fatalf("recorded = %v, want the pushed path", store.recorded)
	}
}

func TestPushOnImport_SkipsWhenAlreadyPushed(t *testing.T) {
	store := &fakeStore{has: map[string]bool{"/books/x.epub": true}}
	up := &fakeUploader{}
	p := newTestPusher(store, up)

	p.PushOnImport(context.Background(), 7, "Night Flights", "/books/x.epub")

	if len(up.calls) != 0 {
		t.Fatalf("uploader calls = %v, want none (idempotency)", up.calls)
	}
}

func TestPushOnImport_DisabledDoesNothing(t *testing.T) {
	up := &fakeUploader{}
	p := NewPusher(func() PushConfig { return PushConfig{} }, &fakeStore{}).
		WithClientFactory(func(PushConfig) (Uploader, error) { return up, nil })

	p.PushOnImport(context.Background(), 7, "Night Flights", "/books/x.epub")

	if len(up.calls) != 0 {
		t.Fatalf("uploader calls = %v, want none when disabled", up.calls)
	}
}

func TestPushOnImport_FailureIsNotRecorded(t *testing.T) {
	store := &fakeStore{}
	up := &fakeUploader{err: errors.New("boom")}
	p := newTestPusher(store, up)

	// Must not panic or propagate: the import is unaffected by contract.
	p.PushOnImport(context.Background(), 7, "Night Flights", "/books/x.epub")

	if len(store.recorded) != 0 {
		t.Fatalf("recorded = %v, want none on failure so a later sync retries", store.recorded)
	}
}

func TestPusher_CachesClientAcrossPushes(t *testing.T) {
	store := &fakeStore{}
	up := &fakeUploader{}
	factoryCalls := 0
	p := NewPusher(enabledCfg, store).WithClientFactory(func(PushConfig) (Uploader, error) {
		factoryCalls++
		return up, nil
	})

	p.PushOnImport(context.Background(), 1, "A", "/books/a.epub")
	p.PushOnImport(context.Background(), 2, "B", "/books/b.epub")

	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1 (client cached so the JWT session survives)", factoryCalls)
	}
}
