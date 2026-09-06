package api

import (
	"sort"

	"github.com/vavallee/bindery/internal/abs"
)

// SettingType is the shape of a stored setting value. Every value in the
// settings table is a string on the wire and in SQLite, so the type is what a
// client needs to render the right control and what the operator needs to know
// before typing into it.
type SettingType string

const (
	// SettingTypeString is free text with no further structure.
	SettingTypeString SettingType = "string"
	// SettingTypeBool stores the literal "true" or "false". Most boolean keys
	// also accept the empty string, which reads back as the default.
	SettingTypeBool SettingType = "bool"
	// SettingTypeInt stores a decimal integer, bounded by Min and Max when set.
	SettingTypeInt SettingType = "int"
	// SettingTypeDuration stores a Go duration string such as "12h", bounded by
	// Min and Max when set.
	SettingTypeDuration SettingType = "duration"
	// SettingTypeEnum stores one of the values listed in Values.
	SettingTypeEnum SettingType = "enum"
)

// SettingState says what Bindery actually does with a stored value. It exists
// because "the key is accepted" and "the key does something" are two different
// facts, and conflating them is how this project ends up with settings that
// look configured and change nothing.
type SettingState string

const (
	// SettingStateActive means some code path reads the value and behaves
	// differently because of it. This is the only state a client should offer
	// as an editable control.
	SettingStateActive SettingState = "active"

	// SettingStateInternal means Bindery writes the value itself and reads it
	// back as its own bookkeeping: resume checkpoints, last run timestamps,
	// one shot guards, cached job state. The value is real and load bearing,
	// but it is not a knob, and hand editing it corrupts whatever wrote it.
	SettingStateInternal SettingState = "internal"

	// SettingStateInert means the key is still accepted and still stored, and
	// nothing reads it. It is kept so an existing row does not turn into an
	// unknown key on upgrade, and so a client that has always sent it keeps
	// working, but setting it has no effect on anything.
	//
	// This state is the whole reason the registry carries a state field rather
	// than assuming every known key is live. PR #2421 pulled the quality
	// profile Cutoff and UpgradeAllowed controls out of the UI because no code
	// path read them, while the model, the SELECT and the wire format all kept
	// the fields for compatibility. A registry without an inert state invites
	// the next generic settings screen to put exactly those controls back.
	//
	// Anything marked inert here MUST NOT be rendered as an editable control.
	// Show it, label it as doing nothing, and let the operator delete the row.
	SettingStateInert SettingState = "inert"
)

// SettingDescriptor describes one settings key: what it holds, what it defaults
// to, what values it accepts, whether a change needs a restart, and whether
// Bindery reads it at all.
//
// Secret, AdminOnly and Writable are not declared per key. They are filled in
// from isSecretSetting, isAdminOnlySetting and isWritableSecretSetting when the
// registry is served, so a descriptor can never claim a gate that the handler
// does not enforce.
type SettingDescriptor struct {
	Key  string      `json:"key"`
	Type SettingType `json:"type"`
	// Default is the value Bindery behaves as if the key held when the row is
	// absent or empty, written in the same string form the key is stored in.
	Default string `json:"default"`
	// Values enumerates the accepted values of an enum key, in the order a UI
	// should present them.
	Values []string `json:"values,omitempty"`
	// Min and Max bound an int or duration key, in the same string form as the
	// value. Empty means unbounded on that side.
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
	// Description is one line of plain text explaining what the key does.
	Description string `json:"description"`
	// RestartRequired is true when Bindery reads the key once at startup, so a
	// change is stored immediately and changes nothing until the next restart.
	RestartRequired bool         `json:"restartRequired"`
	State           SettingState `json:"state"`

	// Secret is true when the value never travels back over the settings API,
	// not even to an admin.
	Secret bool `json:"secret"`
	// AdminOnly is true when only an admin may read the value.
	AdminOnly bool `json:"adminOnly"`
	// Writable is true when PUT /api/v1/setting/{key} accepts the key. Secrets
	// that have a dedicated settings screen of their own are refused there.
	Writable bool `json:"writable"`
}

// settingDescriptors is the registry: every settings key Bindery knows about.
//
// Keeping this list complete is what lets PUT /api/v1/setting/{key} refuse a
// key it does not recognise instead of storing a typo forever, so a new
// settings key is not finished until it has an entry here. The tests check that
// every Setting* constant in this package is listed and that every key the web
// UI writes resolves, which is the guard against a half added key.
//
// Ordering is by area, then by key, and is not significant: SettingDescriptors
// sorts before returning.
var settingDescriptors = []SettingDescriptor{
	// Library and metadata defaults.
	{
		Key: SettingDefaultMediaType, Type: SettingTypeEnum, Default: "ebook",
		Values:      []string{"ebook", "audiobook", "both"},
		Description: "Media type given to authors added without an explicit one.",
		State:       SettingStateActive,
	},
	{
		Key: SettingDefaultMediaTypeStrict, Type: SettingTypeBool, Default: "false",
		Description: "Narrow or skip catalogue books that do not match the default media type, instead of accumulating rows that can never be grabbed.",
		State:       SettingStateActive,
	},
	{
		Key: SettingAuthorDefaultMonitorMode, Type: SettingTypeEnum, Default: "all",
		Values:      []string{"all", "future", "latest", "none"},
		Description: "Which books are monitored on authors added without an explicit monitor mode. The per author \"series\" mode has no install wide meaning and is refused here.",
		State:       SettingStateActive,
	},
	{
		Key: SettingAuthorDefaultMonitorLatestCount, Type: SettingTypeInt, Default: "1", Min: "1",
		Description: "How many recent books \"latest\" monitors, for authors added without an explicit count.",
		State:       SettingStateActive,
	},
	{
		Key: SettingDefaultLibraryRootFolderID, Type: SettingTypeInt, Default: "", Min: "1",
		Description: "root_folder.id used as the library path for authors with no root folder of their own. Empty falls back to BINDERY_LIBRARY_DIR.",
		State:       SettingStateActive,
	},
	{
		Key: SettingMetadataPrimaryProvider, Type: SettingTypeEnum, Default: "openlibrary",
		Values:          MetadataPrimaryProviders,
		Description:     "Provider that decides what an author catalogue looks like. The others stay wired as enrichers. Selecting hardcover requires a stored Hardcover API token.",
		RestartRequired: true,
		State:           SettingStateActive,
	},
	{
		Key: "search.preferredLanguage", Type: SettingTypeString, Default: "",
		Description: "Preferred release language, used to rank search results and to build the recommendation taste profile. Empty means no preference.",
		State:       SettingStateActive,
	},

	// Scheduler cadences and job tuning.
	{
		Key: SettingSearchInterval, Type: SettingTypeDuration, Default: "12h", Min: "1h", Max: "168h",
		Description:     "How often the scheduler searches for wanted books.",
		RestartRequired: true,
		State:           SettingStateActive,
	},
	{
		Key: SettingHardcoverSyncInterval, Type: SettingTypeDuration, Default: "24h", Min: "1h", Max: "168h",
		Description:     "How often Hardcover import lists are synced.",
		RestartRequired: true,
		State:           SettingStateActive,
	},
	{
		Key: "stall.timeout_minutes", Type: SettingTypeInt, Default: "120", Min: "1",
		Description: "How long a download may sit in downloading before it is failed, blocklisted and searched for again.",
		State:       SettingStateActive,
	},
	{
		Key: "log.retention_days", Type: SettingTypeInt, Default: "14", Min: "1",
		Description: "How many days of log entries the daily trim job keeps.",
		State:       SettingStateActive,
	},
	{
		Key: SettingProwlarrSearchTimeoutSeconds, Type: SettingTypeInt, Default: "60", Min: "1",
		Description: "HTTP timeout for Prowlarr searches and syncs.",
		State:       SettingStateActive,
	},
	{
		Key: "autoGrab.enabled", Type: SettingTypeBool, Default: "true",
		Description: "Grab the best matching release automatically when a search finds one. Set to false to leave every decision to the operator.",
		State:       SettingStateActive,
	},
	{
		Key: "recommendations.enabled", Type: SettingTypeBool, Default: "false",
		Description: "Build recommendations from the library on a schedule.",
		State:       SettingStateActive,
	},
	{
		Key: "telemetry.enabled", Type: SettingTypeBool, Default: "true",
		Description: "Send the anonymous install ping. BINDERY_TELEMETRY_DISABLED=true opts out before any setting exists.",
		State:       SettingStateActive,
	},

	// Import placement and naming.
	{
		Key: SettingImportMode, Type: SettingTypeEnum, Default: "auto",
		Values:      []string{"auto", "move", "copy", "hardlink", "external"},
		Description: "How a finished download reaches the library. auto hardlinks when source and destination share a filesystem and copies otherwise, so seeding survives.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportAudiobookFlattenMultiDisc, Type: SettingTypeBool, Default: "false",
		Description: "Flatten a multi disc audiobook download into one Part sequence on import. Only ever runs in copy or hardlink mode.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportDropFolder, Type: SettingTypeString, Default: "",
		Description: "Directory an external mode import hands finished downloads to. Must exist inside the container. Empty disables the handoff.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportDropLayout, Type: SettingTypeEnum, Default: "flat",
		Values:      []string{"flat", "templated"},
		Description: "Whether the drop folder gets a flat named file or the full author and title folder structure.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportDropLinkMode, Type: SettingTypeEnum, Default: "copy",
		Values:      []string{"copy", "hardlink"},
		Description: "Whether the drop folder gets a copy or a hardlink. The source is never moved, so the download keeps seeding.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportDropPairGating, Type: SettingTypeBool, Default: "false",
		Description: "For books wanted in both formats, hold the first format that arrives until its sibling is ready so a paired reader tool ingests them together.",
		State:       SettingStateActive,
	},
	{
		Key: SettingImportDropPairGatingTimeoutHours, Type: SettingTypeInt, Default: "72", Min: "1",
		Description: "How long a held format waits for its sibling before it is dropped alone.",
		State:       SettingStateActive,
	},
	{
		Key: "naming.bookTemplate", Type: SettingTypeString, Default: "",
		Description: "Path template for imported ebooks. Empty uses the built in default.",
		State:       SettingStateActive,
	},
	{
		Key: "naming_template", Type: SettingTypeString, Default: "",
		Description: "Original backend only spelling of naming.bookTemplate, still read as a fallback for hand seeded values.",
		State:       SettingStateActive,
	},
	{
		Key: "naming_template_audiobook", Type: SettingTypeString, Default: "",
		Description: "Path template for imported audiobook folders. Empty uses the built in default.",
		State:       SettingStateActive,
	},
	{
		Key: SettingNamingAudiobookFileTemplate, Type: SettingTypeString, Default: "",
		Description: "Per track audiobook filename template. Must carry a {Part} token or every track collapses onto one filename. Empty preserves the download layout.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCWAIngestPath, Type: SettingTypeString, Default: "",
		Description: "Calibre Web Automated ingest directory that every successful import is mirrored into. Empty disables the mirror.",
		State:       SettingStateActive,
	},

	// Calibre.
	{
		Key: SettingCalibreMode, Type: SettingTypeEnum, Default: "off",
		Values:      []string{"off", "calibredb", "plugin"},
		Description: "How imports reach Calibre: not at all, by shelling out to calibredb, or through the Bindery Calibre plugin.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibreEnabled, Type: SettingTypeBool, Default: "false",
		Description: "Pre mode boolean toggle, still read so an install that upgraded from v0.8.0 and never picked a mode keeps working. New installs set calibre.mode instead.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibreLibraryPath, Type: SettingTypeString, Default: "",
		Description: "Path to the Calibre library directory as seen from inside the Bindery container. Must be an existing directory.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibreBinaryPath, Type: SettingTypeString, Default: "",
		Description: "Path to the calibredb executable. Must be an existing executable file.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibrePluginURL, Type: SettingTypeString, Default: "",
		Description: "Base URL of the Bindery Calibre plugin. Must be http or https, and link local and cloud metadata addresses are refused.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibrePluginAPIKey, Type: SettingTypeString, Default: "",
		Description: "API key the Calibre plugin expects. Stored but never read back over the settings API.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibrePushPathRemap, Type: SettingTypeString, Default: "",
		Description: "Path rewrites applied before pushing to Calibre, in from:to[,from:to] form, for when Bindery and Calibre mount the library at different paths.",
		State:       SettingStateActive,
	},
	{
		Key: SettingCalibreSyncOnStartup, Type: SettingTypeBool, Default: "false",
		Description:     "Run a Calibre library sync when Bindery starts.",
		RestartRequired: true,
		State:           SettingStateActive,
	},
	{
		Key: SettingCalibreLibraryImportEnabled, Type: SettingTypeBool, Default: "false",
		Description: "Allow importing an existing Calibre library into Bindery.",
		State:       SettingStateActive,
	},
	{
		Key: "calibre.last_import_at", Type: SettingTypeString, Default: "",
		Description: "RFC3339 timestamp of the last Calibre library import, written by the importer and shown on the Calibre settings tab.",
		State:       SettingStateInternal,
	},
	{
		Key: "calibre.drop_folder_path", Type: SettingTypeString, Default: "",
		Description: "Watch directory for the Calibre drop folder mode that PR #207 removed. Migration 011 still seeds it empty on every install and nothing has read it since.",
		State:       SettingStateInert,
	},

	// Audiobookshelf.
	{
		Key: SettingABSBaseURL, Type: SettingTypeString, Default: "",
		Description: "Base URL of the Audiobookshelf server.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSAPIKey, Type: SettingTypeString, Default: "",
		Description: "Audiobookshelf API token. Written by the Audiobookshelf settings screen, never returned.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSEnabled, Type: SettingTypeBool, Default: "false",
		Description: "Whether the Audiobookshelf integration is active.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSLabel, Type: SettingTypeString, Default: "",
		Description: "Display name for this Audiobookshelf server in the UI.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSLibraryID, Type: SettingTypeString, Default: "",
		Description: "First selected Audiobookshelf library, kept alongside abs.library_ids for callers that predate multi library selection.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSLibraryIDs, Type: SettingTypeString, Default: "[]",
		Description: "JSON array of the Audiobookshelf library IDs to import from.",
		State:       SettingStateActive,
	},
	{
		Key: SettingABSPathRemap, Type: SettingTypeString, Default: "",
		Description: "Path rewrites applied to Audiobookshelf file paths, in from:to[,from:to] form.",
		State:       SettingStateActive,
	},
	{
		Key: abs.SettingABSImportCheckpoint, Type: SettingTypeString, Default: "",
		Description: "JSON resume point for an interrupted Audiobookshelf import, written by the enumerator every few hundred items.",
		State:       SettingStateInternal,
	},
	{
		Key: abs.SettingABSLastImportAt, Type: SettingTypeString, Default: "",
		Description: "RFC3339 timestamp written after each Audiobookshelf import. Nothing reads it: unlike calibre.last_import_at it never grew a display.",
		State:       SettingStateInert,
	},

	// Hardcover.
	{
		Key: SettingHardcoverAPIToken, Type: SettingTypeString, Default: "",
		Description: "Hardcover API token. Every Hardcover query is authenticated, search included. Stored but never read back over the settings API.",
		State:       SettingStateActive,
	},
	{
		Key: SettingHardcoverEnhancedSeriesEnabled, Type: SettingTypeBool, Default: "false",
		Description: "Use the enhanced Hardcover series API. Also needs a stored token and the enhanced Hardcover build flag.",
		State:       SettingStateActive,
	},

	// Grimmory.
	{
		Key: SettingGrimmoryEnabled, Type: SettingTypeBool, Default: "false",
		Description: "Whether finished imports are pushed to Grimmory.",
		State:       SettingStateActive,
	},
	{
		Key: SettingGrimmoryBaseURL, Type: SettingTypeString, Default: "",
		Description: "Base URL of the Grimmory server.",
		State:       SettingStateActive,
	},
	{
		Key: SettingGrimmoryAPIKey, Type: SettingTypeString, Default: "",
		Description: "Grimmory API key. Written by the Grimmory settings screen, never returned.",
		State:       SettingStateActive,
	},
	{
		Key: SettingGrimmoryUsername, Type: SettingTypeString, Default: "",
		Description: "Grimmory username, for servers that authenticate with a username and password instead of an API key.",
		State:       SettingStateActive,
	},
	{
		Key: SettingGrimmoryPassword, Type: SettingTypeString, Default: "",
		Description: "Grimmory password. Written by the Grimmory settings screen, never returned.",
		State:       SettingStateActive,
	},

	// Google Books.
	{
		Key: SettingGoogleBooksAPIKey, Type: SettingTypeString, Default: "",
		Description:     "Google Books API key, used to enrich metadata. The enricher is wired at startup, so a newly added key does nothing until Bindery restarts.",
		RestartRequired: true,
		State:           SettingStateActive,
	},
	{
		Key: LegacySettingGoogleBooksAPIKey, Type: SettingTypeString, Default: "",
		Description:     "Original spelling of googlebooks.apiKey, still read as a fallback for installs that stored the key under it.",
		RestartRequired: true,
		State:           SettingStateActive,
	},

	// Auth. Every key here is classified secret, so the generic settings API
	// refuses to write it and never returns it. They are listed anyway so the
	// registry is a complete account of the settings table rather than a
	// partial one.
	{
		Key: SettingAuthMode, Type: SettingTypeEnum, Default: "enabled",
		Values:      []string{"enabled", "local-only", "proxy", "disabled"},
		Description: "How requests are authenticated. Set through the security settings screen, not the generic settings API.",
		State:       SettingStateActive,
	},
	{
		Key: SettingAuthAPIKey, Type: SettingTypeString, Default: "",
		Description: "API key for machine to machine callers. Generated and rotated through the security settings screen.",
		State:       SettingStateActive,
	},
	{
		Key: SettingAuthSessionSecret, Type: SettingTypeString, Default: "",
		Description: "HMAC secret that signs session cookies. Generated on first boot.",
		State:       SettingStateActive,
	},
	{
		Key: SettingAuthSessionSecretPrevious, Type: SettingTypeString, Default: "",
		Description: "Previous session secret, consulted only to verify cookies signed before the last rotation.",
		State:       SettingStateInternal,
	},
	{
		Key: SettingOIDCProviders, Type: SettingTypeString, Default: "",
		Description: "JSON array of configured OIDC providers, client secrets included. Set through the OIDC endpoints.",
		State:       SettingStateActive,
	},
	{
		Key: SettingOIDCFirstAdminPromoted, Type: SettingTypeBool, Default: "false",
		Description: "One shot guard recording that an OIDC user has already been auto promoted to admin, so deleting every admin cannot trigger promotion again.",
		State:       SettingStateInternal,
	},

	// Job state Bindery writes for itself.
	{
		Key: SettingLibraryLastScan, Type: SettingTypeString, Default: "",
		Description: "JSON summary of the most recent library scan, including every scanned path and every unmatched file.",
		State:       SettingStateInternal,
	},
	{
		Key: SettingAuthorBulkRefresh, Type: SettingTypeString, Default: "",
		Description: "JSON progress record for the refresh all authors job, served verbatim by its status endpoint.",
		State:       SettingStateInternal,
	},
	{
		Key: "telemetry.install_id", Type: SettingTypeString, Default: "",
		Description: "Random identifier generated on the first ping so repeat pings from one install are not counted as new ones.",
		State:       SettingStateInternal,
	},
}

// settingDescriptorsByKey indexes settingDescriptors for lookup. Built once at
// package init so validateSettingValue stays cheap on the request path.
var settingDescriptorsByKey = func() map[string]SettingDescriptor {
	m := make(map[string]SettingDescriptor, len(settingDescriptors))
	for _, d := range settingDescriptors {
		m[d.Key] = d
	}
	return m
}()

// LookupSettingDescriptor returns the descriptor for key, with the secrecy and
// writability flags filled in.
func LookupSettingDescriptor(key string) (SettingDescriptor, bool) {
	d, ok := settingDescriptorsByKey[key]
	if !ok {
		return SettingDescriptor{}, false
	}
	return withSettingGates(d), true
}

// IsKnownSettingKey reports whether key has a descriptor. This is the check
// that turns an unrecognised key into a 400 instead of a row that is stored
// forever and read by nothing.
func IsKnownSettingKey(key string) bool {
	_, ok := settingDescriptorsByKey[key]
	return ok
}

// SettingDescriptors returns every descriptor, sorted by key, with the secrecy
// and writability flags filled in.
func SettingDescriptors() []SettingDescriptor {
	out := make([]SettingDescriptor, 0, len(settingDescriptors))
	for _, d := range settingDescriptors {
		out = append(out, withSettingGates(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// withSettingGates fills Secret, AdminOnly and Writable from the same
// classifiers the handler enforces, so a descriptor cannot advertise a gate
// that is not real.
func withSettingGates(d SettingDescriptor) SettingDescriptor {
	d.Secret = isSecretSetting(d.Key)
	d.AdminOnly = isAdminOnlySetting(d.Key)
	d.Writable = !d.Secret || isWritableSecretSetting(d.Key)
	return d
}
