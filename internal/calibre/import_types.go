package calibre

// Constants for the Calibre run-tracking + rollback layer. Kept in a small
// dedicated file (rather than importer.go) so the rollback code can depend
// on them without dragging in the rest of the importer package surface.

const (
	defaultSourceID          = "default"
	runEntityMetadataKind    = "calibre_run_entity_metadata"
	runEntityMetadataVersion = 1
	entityTypeAuthor         = "author"
	entityTypeBook           = "book"
	entityTypeEdition        = "edition"
	runStatusRunning         = "running"
	runStatusCompleted       = "completed"
	runStatusFailed          = "failed"
	runStatusRolledBack      = "rolled_back"
	outcomeCreated           = "created"
	outcomeUpdated           = "updated"
	outcomeLinked            = "linked"
)
