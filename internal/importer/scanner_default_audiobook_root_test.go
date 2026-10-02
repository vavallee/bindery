package importer

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The audiobook default root folder setting (#2166). Before it existed the only
// install wide audiobook location was BINDERY_AUDIOBOOK_DIR, which a Windows
// binary user has no comfortable way to set.

const settingDefaultAudiobookRoot = "library.defaultAudiobookRootFolderId"

func scannerWithAudiobookDefaults(t *testing.T) (*Scanner, *db.RootFolderRepo, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	rf := db.NewRootFolderRepo(database)
	settings := db.NewSettingsRepo(database)
	s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), db.NewBookRepo(database),
		db.NewAuthorRepo(database), db.NewHistoryRepo(database), "/env/lib", "/env/audiobooks", "", "", "")
	s.WithRootFolders(rf)
	s.WithSettings(settings)
	return s, rf, settings, context.Background()
}

func TestEffectiveAudiobookDir_DefaultSettingBeatsEnv(t *testing.T) {
	s, rf, settings, ctx := scannerWithAudiobookDefaults(t)
	def, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingDefaultAudiobookRoot, fmt.Sprintf("%d", def.ID)); err != nil {
		t.Fatal(err)
	}

	if got := s.effectiveAudiobookDir(ctx, &models.Author{}); got != def.Path {
		t.Errorf("author without override: want default %q, got %q", def.Path, got)
	}
	if got := s.effectiveAudiobookDir(ctx, nil); got != def.Path {
		t.Errorf("nil author: want default %q, got %q", def.Path, got)
	}
	// Reorganize and the destination preview resolve through proposedPathFor,
	// pruning through rootsForFormat, reconcile through effectiveRootForFormat;
	// all of them must see the default too.
	roots := s.rootsForFormat(ctx, &models.Author{}, models.MediaTypeAudiobook)
	if len(roots) == 0 || roots[0] != def.Path {
		t.Errorf("rootsForFormat: want %q first, got %v", def.Path, roots)
	}
	if got := s.effectiveRootForFormat(ctx, &models.Author{}, models.MediaTypeAudiobook); got != def.Path {
		t.Errorf("reconcile root: want %q, got %q", def.Path, got)
	}
}

func TestEffectiveAudiobookDir_AuthorOverrideBeatsDefault(t *testing.T) {
	s, rf, settings, ctx := scannerWithAudiobookDefaults(t)
	own, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	def, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingDefaultAudiobookRoot, fmt.Sprintf("%d", def.ID)); err != nil {
		t.Fatal(err)
	}
	if got := s.effectiveAudiobookDir(ctx, &models.Author{AudiobookRootFolderID: &own.ID}); got != own.Path {
		t.Errorf("author override: want %q, got %q", own.Path, got)
	}
}

func TestEffectiveAudiobookDir_DeletedDefaultFallsBackToEnv(t *testing.T) {
	s, rf, settings, ctx := scannerWithAudiobookDefaults(t)
	def, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingDefaultAudiobookRoot, fmt.Sprintf("%d", def.ID)); err != nil {
		t.Fatal(err)
	}
	if err := rf.Delete(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.effectiveAudiobookDir(ctx, &models.Author{}); got != "/env/audiobooks" {
		t.Errorf("deleted default: want /env/audiobooks, got %q", got)
	}
}

// The ebook default must never steer audiobooks, the same #421 rule the per
// author columns follow, and the audiobook default must never steer ebooks.
func TestEffectiveAudiobookDir_DefaultsStayInTheirLane(t *testing.T) {
	s, rf, settings, ctx := scannerWithAudiobookDefaults(t)
	ebook, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "library.defaultRootFolderId", fmt.Sprintf("%d", ebook.ID)); err != nil {
		t.Fatal(err)
	}
	if got := s.effectiveAudiobookDir(ctx, &models.Author{}); got != "/env/audiobooks" {
		t.Errorf("ebook default leaked into audiobooks: got %q", got)
	}

	ab, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "library.defaultRootFolderId", ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingDefaultAudiobookRoot, fmt.Sprintf("%d", ab.ID)); err != nil {
		t.Fatal(err)
	}
	if got := s.effectiveLibraryDir(ctx, &models.Author{}); got != "/env/lib" {
		t.Errorf("audiobook default leaked into ebooks: got %q", got)
	}
}
