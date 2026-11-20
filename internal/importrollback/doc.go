// Package importrollback holds the parts of import rollback that the
// Audiobookshelf (internal/abs) and Calibre (internal/calibre) importers
// share: the snapshot envelope stored in each importer's run-entity table,
// the "restore a field only if it still holds the post-import value" helpers,
// and author identifier capture and diffing.
//
// What stays in each importer: the snapshot structs themselves (the set of
// fields an importer mutates is importer specific), the repositories, and the
// local and external ID spaces. The stored JSON format is unchanged.
package importrollback
