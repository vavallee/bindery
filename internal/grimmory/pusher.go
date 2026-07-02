package grimmory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// PushConfig is the live settings snapshot for push operations. The api layer
// materialises it from the settings table on every use, so toggling the
// integration or changing credentials takes effect without a restart.
type PushConfig struct {
	Enabled  bool
	BaseURL  string
	APIKey   string
	Username string
	Password string
}

// Ready reports whether the config is complete enough to push, with a
// human-readable reason when it isn't.
func (c PushConfig) Ready() (bool, string) {
	if !c.Enabled {
		return false, "Grimmory integration is disabled"
	}
	if c.BaseURL == "" {
		return false, "Grimmory server URL is not configured"
	}
	if c.APIKey == "" && c.Username == "" {
		return false, "Grimmory credentials are not configured — set a username/password (or API token)"
	}
	return true, ""
}

// PushStore is the subset of *db.GrimmoryPushRepo the pusher needs.
type PushStore interface {
	Has(ctx context.Context, filePath string) (bool, error)
	Record(ctx context.Context, bookID int64, filePath string, grimmoryBookID int64) error
}

// Uploader is the subset of *Client the pusher needs, so tests can inject a
// fake without an HTTP server.
type Uploader interface {
	UploadBookDrop(ctx context.Context, filePath string) (int64, error)
}

// PushOutcome classifies one tracked push attempt.
type PushOutcome int

const (
	// OutcomePushed means the file was uploaded and recorded.
	OutcomePushed PushOutcome = iota
	// OutcomeAlreadyPushed means the file was recorded by a previous push and
	// was skipped (BookDrop has no server-side dedup, so re-uploading would
	// duplicate it over there).
	OutcomeAlreadyPushed
	// OutcomeFailed means the upload errored; the file stays unrecorded so a
	// later run retries it.
	OutcomeFailed
)

// Pusher pushes imported book files into Grimmory's BookDrop. It is shared by
// the importer's post-import hook (best-effort, never blocks an import) and
// the bulk sync job (tracked outcomes).
type Pusher struct {
	loadCfg   func() PushConfig
	store     PushStore
	newClient func(cfg PushConfig) (Uploader, error)

	// One client is cached per config snapshot so the JWT session from a
	// login survives across pushes instead of re-authenticating per book.
	mu        sync.Mutex
	client    Uploader
	clientCfg PushConfig
}

// NewPusher wires a pusher against the live-config loader and push store.
func NewPusher(loadCfg func() PushConfig, store PushStore) *Pusher {
	return &Pusher{
		loadCfg: loadCfg,
		store:   store,
		newClient: func(cfg PushConfig) (Uploader, error) {
			c, err := NewClient(cfg.BaseURL, cfg.APIKey)
			if err != nil {
				return nil, err
			}
			return c.WithCredentials(cfg.Username, cfg.Password), nil
		},
	}
}

// WithClientFactory overrides client construction (tests).
func (p *Pusher) WithClientFactory(fn func(cfg PushConfig) (Uploader, error)) *Pusher {
	p.newClient = fn
	return p
}

func (p *Pusher) clientFor(cfg PushConfig) (Uploader, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil && p.clientCfg == cfg {
		return p.client, nil
	}
	c, err := p.newClient(cfg)
	if err != nil {
		return nil, err
	}
	p.client, p.clientCfg = c, cfg
	return c, nil
}

// PushOnImport mirrors a just-imported ebook into Grimmory. Best-effort by
// contract (#826): a Grimmory failure must never block or fail the underlying
// Bindery import, so every error path logs and returns.
func (p *Pusher) PushOnImport(ctx context.Context, bookID int64, title, filePath string) {
	cfg := p.loadCfg()
	if ok, reason := cfg.Ready(); !ok {
		if cfg.Enabled {
			// Enabled but unusable is an operator error worth surfacing;
			// plain disabled is the normal case and stays quiet.
			slog.Warn("grimmory: push skipped", "reason", reason, "title", title)
		}
		return
	}
	outcome, err := p.pushTracked(ctx, cfg, bookID, filePath)
	switch outcome {
	case OutcomePushed:
		slog.Info("grimmory: book pushed to BookDrop", "title", title, "path", filePath)
	case OutcomeAlreadyPushed:
		slog.Debug("grimmory: file already pushed, skipping", "title", title, "path", filePath)
	case OutcomeFailed:
		slog.Warn("grimmory: push failed (import is unaffected; bulk sync will retry)",
			"title", title, "path", filePath, "error", err)
	}
}

// pushTracked performs one idempotent push: skip if recorded, upload, record.
func (p *Pusher) pushTracked(ctx context.Context, cfg PushConfig, bookID int64, filePath string) (PushOutcome, error) {
	if p.store != nil {
		pushed, err := p.store.Has(ctx, filePath)
		if err != nil {
			return OutcomeFailed, err
		}
		if pushed {
			return OutcomeAlreadyPushed, nil
		}
	}
	client, err := p.clientFor(cfg)
	if err != nil {
		return OutcomeFailed, err
	}
	grimmoryID, err := client.UploadBookDrop(ctx, filePath)
	if err != nil {
		return OutcomeFailed, err
	}
	if p.store != nil {
		if err := p.store.Record(ctx, bookID, filePath, grimmoryID); err != nil {
			// The upload landed; failing to record only risks a duplicate on
			// the next sweep. Log rather than report the push as failed.
			slog.Warn("grimmory: failed to record push", "path", filePath, "error", err)
		}
	}
	return OutcomePushed, nil
}

// ErrSyncAlreadyRunning is returned when a bulk sync is started while a
// previous one is still executing. Maps to 409 Conflict at the API layer.
var ErrSyncAlreadyRunning = errors.New("grimmory sync already running")
