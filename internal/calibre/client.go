// Package calibre wraps Calibre's `calibredb` command-line utility so the
// importer can mirror Bindery's library into a user-configured Calibre
// library. The calibredb binary is the stable contract Calibre exposes for
// scripted access; shelling out keeps Bindery CGO-free and avoids binding
// to Calibre's internal Python APIs.
package calibre

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ErrDisabled is returned by Add / Test when the integration has not been
// enabled in settings. Callers treat it as a soft-skip — the importer logs
// and moves on rather than surfacing it as a failed import.
var ErrDisabled = errors.New("calibre integration disabled")

// Config is a snapshot of the user-facing Calibre settings. It is built
// fresh from the settings repo at the start of each import so toggling the
// flag in the UI takes effect without a restart.
type Config struct {
	Enabled     bool
	BinaryPath  string // path to `calibredb`; empty means resolve via $PATH
	LibraryPath string // target Calibre library directory
}

// runner is the shape of exec.CommandContext, abstracted for tests.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Client calls calibredb. The zero value is not usable; construct with New.
type Client struct {
	cfg Config
	run runner
}

// New builds a Client against cfg. Passing an empty BinaryPath is fine —
// the client will resolve `calibredb` on PATH at call time.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, run: defaultRunner}
}

// Enabled reports whether the client should be invoked at all.
func (c *Client) Enabled() bool { return c != nil && c.cfg.Enabled }

// binary returns the calibredb path the client will invoke. Falling back to
// the bare name lets operators rely on $PATH instead of pinning the path in
// settings, which is the common case on a distro install.
func (c *Client) binary() string {
	if c.cfg.BinaryPath != "" {
		return c.cfg.BinaryPath
	}
	return "calibredb"
}

// Add runs `calibredb add --with-library <lib> <file>` and returns the
// Calibre book id assigned to the newly ingested file. A non-zero id is
// always returned on success so callers can persist the mapping.
//
// calibredb's add output is unstructured plaintext — it does not offer a
// machine-readable format for add — so we regex the "Added book ids: N"
// line that has been stable across Calibre releases since ~2.x.
func (c *Client) Add(ctx context.Context, filePath string) (int64, error) {
	if !c.Enabled() {
		return 0, ErrDisabled
	}
	if c.cfg.LibraryPath == "" {
		return 0, errors.New("calibre library_path is not configured")
	}
	out, err := c.run(ctx, c.binary(), "add", "--with-library", c.cfg.LibraryPath, filePath)
	if err != nil {
		return 0, fmt.Errorf("calibredb add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	id, err := parseAddedID(out)
	if err != nil {
		return 0, fmt.Errorf("calibredb add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return id, nil
}

// Test probes the configured binary by asking for its version. A successful
// call returns the version string printed on stdout; any failure surfaces as
// a wrapped error suitable for display in the settings UI.
func (c *Client) Test(ctx context.Context) (string, error) {
	if !c.cfg.Enabled {
		return "", ErrDisabled
	}
	if c.cfg.LibraryPath != "" {
		if info, err := os.Stat(c.cfg.LibraryPath); err != nil {
			return "", fmt.Errorf("library_path %q: %w", c.cfg.LibraryPath, err)
		} else if !info.IsDir() {
			return "", fmt.Errorf("library_path %q is not a directory", c.cfg.LibraryPath)
		}
	}
	out, err := c.run(ctx, c.binary(), "--version")
	if err != nil {
		return "", fmt.Errorf("calibredb --version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

var addedIDRe = regexp.MustCompile(`Added book ids:\s*([0-9, ]+)`)

// parseAddedID extracts the first numeric id from calibredb add's
// "Added book ids: 12" / "Added book ids: 12, 13" lines. Multi-id output
// happens when a single archive yields several books; we return the first
// because Bindery maps one book row to one imported file.
func parseAddedID(out []byte) (int64, error) {
	m := addedIDRe.FindSubmatch(out)
	if len(m) < 2 {
		return 0, errors.New("could not find \"Added book ids\" in calibredb output")
	}
	parts := strings.Split(string(m[1]), ",")
	first := strings.TrimSpace(parts[0])
	id, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse calibre id %q: %w", first, err)
	}
	return id, nil
}
