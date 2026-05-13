package importer

import "github.com/vavallee/bindery/internal/pathmap"

// Remapper is kept as a compatibility alias for importer tests and callers.
type Remapper = pathmap.Remapper

// ParseRemap is kept as a compatibility wrapper for existing importer callers.
var ParseRemap = pathmap.Parse
