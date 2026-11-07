package main

import (
	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
)

// newLibraryRoots builds the strict containment set used by the delete and
// adoption handlers: root folders plus the library env dirs, nothing else.
// It must never include import source folders (#2168).
func newLibraryRoots(cfg *config.Config, rootFolders api.RootLister) *api.LibraryRoots {
	return api.NewLibraryRoots(rootFolders, cfg.LibraryDir, cfg.AudiobookDir)
}

// newImportRoots builds the wider set manual import may read from: the library
// roots, the download dirs (#1373) and the admin configured import source
// folders (#2168). Only this set is given the settings repo.
func newImportRoots(cfg *config.Config, rootFolders api.RootLister, settings *db.SettingsRepo) *api.LibraryRoots {
	return api.NewLibraryRoots(rootFolders, cfg.LibraryDir, cfg.AudiobookDir, cfg.DownloadDir, cfg.AudiobookDownloadDir).
		WithImportSources(settings)
}
