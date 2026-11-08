package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/telemetry"
)

// countProbes swaps in a probe that counts calls and otherwise behaves like
// the real one.
func countProbes(f *blockedFixture) *int {
	calls := 0
	f.h.blocked.probe = func(path string, write bool) bool { calls++; return dirAccessible(path, write) }
	return &calls
}

// freshInstall drops the import history, so the blocked check never runs and
// only the library folder answer can touch the disk.
func freshInstall(t *testing.T) *blockedFixture {
	t.Helper()
	f := newBlockedFixture(t)
	f.h.blocked.history = nil
	return f
}

func wantLibraryFolder(t *testing.T, s SetupState, want bool) {
	t.Helper()
	if s.HasLibraryFolder == nil {
		t.Fatalf("hasLibraryFolder missing, want %v", want)
	}
	if *s.HasLibraryFolder != want {
		t.Fatalf("hasLibraryFolder = %v, want %v", *s.HasLibraryFolder, want)
	}
}

// No root folder is needed when BINDERY_LIBRARY_DIR is a writable folder: the
// importer falls back to it.
func TestSetupStateLibrary_UsableLibraryDirCounts(t *testing.T) {
	f := freshInstall(t)
	s, _ := f.get(t, "admin")
	wantLibraryFolder(t, s, true)
}

func TestSetupStateLibrary_MissingLibraryDirWithNoRootFolder(t *testing.T) {
	f := freshInstall(t)
	f.cfg.LibraryDir = filepath.Join(f.lib, "never-mounted")
	s, _ := f.get(t, "admin")
	wantLibraryFolder(t, s, false)
}

func TestSetupStateLibrary_EmptyLibraryDirWithNoRootFolder(t *testing.T) {
	f := freshInstall(t)
	f.cfg.LibraryDir = ""
	s, _ := f.get(t, "admin")
	wantLibraryFolder(t, s, false)
}

// A root folder row is enough on its own, and answering from it costs no disk
// access at all.
func TestSetupStateLibrary_RootFolderRowSkipsTheProbe(t *testing.T) {
	f := freshInstall(t)
	f.cfg.LibraryDir = filepath.Join(f.lib, "never-mounted")
	if _, err := f.roots.Create(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	calls := countProbes(f)
	s, _ := f.get(t, "admin")
	wantLibraryFolder(t, s, true)
	if *calls != 0 {
		t.Errorf("probe ran %d times with a root folder present", *calls)
	}
}

// Once setup is complete an import has already landed somewhere, so the
// answer is yes without touching the media mounts on every page load.
func TestSetupStateLibrary_CompleteSetupSkipsTheProbe(t *testing.T) {
	f := freshInstall(t)
	ctx := context.Background()
	for _, k := range []string{telemetry.SettingFirstAuthorAt, telemetry.SettingFirstImportAt} {
		if err := f.h.settings.Set(ctx, k, "2026-11-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg.LibraryDir = filepath.Join(f.lib, "never-mounted")
	calls := countProbes(f)
	s, _ := f.get(t, "admin")
	if !s.Complete {
		t.Fatalf("fixture is not complete: %+v", s)
	}
	wantLibraryFolder(t, s, true)
	if *calls != 0 {
		t.Errorf("probe ran %d times on a complete setup", *calls)
	}
}

// Non admins cannot act on it and are not shown storage state, so the field
// is left out and nothing is probed for them.
func TestSetupStateLibrary_NotForNonAdmins(t *testing.T) {
	f := freshInstall(t)
	calls := countProbes(f)
	s, body := f.get(t, "user")
	if s.HasLibraryFolder != nil || strings.Contains(body, "hasLibraryFolder") {
		t.Errorf("non admin got hasLibraryFolder: %s", body)
	}
	if *calls != 0 {
		t.Errorf("probe ran %d times for a non admin", *calls)
	}
}

// The answer comes from the blocked check's cached probe: repeat loads inside
// the TTL do not touch the disk again.
func TestSetupStateLibrary_UsesTheCachedProbe(t *testing.T) {
	f := freshInstall(t)
	calls := countProbes(f)
	f.get(t, "admin")
	first := *calls
	if first == 0 {
		t.Fatal("probe never ran")
	}
	f.clock = f.clock.Add(storageProbeTTL / 2)
	s, _ := f.get(t, "admin")
	wantLibraryFolder(t, s, true)
	if *calls != first {
		t.Errorf("probe re-ran inside the TTL: %d -> %d", first, *calls)
	}
}

// A hung mount must not hang the request. With no finished probe yet the
// answer is unknown, so the field is left out rather than guessed.
func TestSetupStateLibrary_HungProbeLeavesItOut(t *testing.T) {
	f := freshInstall(t)
	release := make(chan struct{})
	defer close(release)
	f.h.blocked.probe = func(string, bool) bool { <-release; return true }
	f.h.blocked.wait = 50 * time.Millisecond

	done := make(chan SetupState)
	go func() { s, _ := f.get(t, "admin"); done <- s }()
	select {
	case s := <-done:
		if s.HasLibraryFolder != nil {
			t.Errorf("hasLibraryFolder = %v before any probe finished", *s.HasLibraryFolder)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request hung on a stuck probe")
	}
}
