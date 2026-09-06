package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/abs"
)

// TestSettingDescriptors_WellFormed checks that every descriptor describes
// something a client could actually render: a unique key, one of the known
// types, one of the known states, a description, and a default that agrees with
// the declared type. A default that does not parse as its own type is the
// registry lying about the key it documents.
func TestSettingDescriptors_WellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range SettingDescriptors() {
		if d.Key == "" {
			t.Fatalf("descriptor with an empty key: %+v", d)
		}
		if seen[d.Key] {
			t.Errorf("%s: listed twice", d.Key)
		}
		seen[d.Key] = true

		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("%s: no description", d.Key)
		}

		switch d.Type {
		case SettingTypeString:
			if len(d.Values) > 0 {
				t.Errorf("%s: string key declares enum values %v", d.Key, d.Values)
			}
		case SettingTypeBool:
			if d.Default != "true" && d.Default != "false" {
				t.Errorf("%s: bool default %q is neither true nor false", d.Key, d.Default)
			}
		case SettingTypeInt:
			for name, v := range map[string]string{"default": d.Default, "min": d.Min, "max": d.Max} {
				if v == "" {
					continue
				}
				if _, err := strconv.Atoi(v); err != nil {
					t.Errorf("%s: int %s %q does not parse: %v", d.Key, name, v, err)
				}
			}
		case SettingTypeDuration:
			for name, v := range map[string]string{"default": d.Default, "min": d.Min, "max": d.Max} {
				if v == "" {
					continue
				}
				if _, err := time.ParseDuration(v); err != nil {
					t.Errorf("%s: duration %s %q does not parse: %v", d.Key, name, v, err)
				}
			}
		case SettingTypeEnum:
			if len(d.Values) == 0 {
				t.Errorf("%s: enum key lists no values", d.Key)
			}
			if d.Default != "" && !contains(d.Values, d.Default) {
				t.Errorf("%s: default %q is not one of the declared values %v", d.Key, d.Default, d.Values)
			}
		default:
			t.Errorf("%s: unknown type %q", d.Key, d.Type)
		}

		switch d.State {
		case SettingStateActive, SettingStateInternal, SettingStateInert:
		default:
			t.Errorf("%s: unknown state %q", d.Key, d.State)
		}

		if d.Type != SettingTypeInt && d.Type != SettingTypeDuration && (d.Min != "" || d.Max != "") {
			t.Errorf("%s: type %q carries min/max bounds, which only mean something for int and duration", d.Key, d.Type)
		}
	}
}

// TestSettingDescriptors_SortedByKey pins the ordering the endpoint promises so
// a client can render the list without sorting it again.
func TestSettingDescriptors_SortedByKey(t *testing.T) {
	list := SettingDescriptors()
	for i := 1; i < len(list); i++ {
		if list[i-1].Key >= list[i].Key {
			t.Fatalf("descriptors are not sorted by key: %q then %q", list[i-1].Key, list[i].Key)
		}
	}
}

// TestSettingDescriptors_RoundTripThroughJSON checks that the type, default and
// state of every descriptor survive the wire unchanged, since a client cannot
// render a typed control from a field that marshals away.
func TestSettingDescriptors_RoundTripThroughJSON(t *testing.T) {
	want := SettingDescriptors()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []SettingDescriptor
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("descriptor count changed over the wire: sent %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Key != want[i].Key ||
			got[i].Type != want[i].Type ||
			got[i].Default != want[i].Default ||
			got[i].State != want[i].State ||
			got[i].RestartRequired != want[i].RestartRequired ||
			got[i].Secret != want[i].Secret ||
			got[i].AdminOnly != want[i].AdminOnly ||
			got[i].Writable != want[i].Writable ||
			strings.Join(got[i].Values, ",") != strings.Join(want[i].Values, ",") ||
			got[i].Min != want[i].Min ||
			got[i].Max != want[i].Max {
			t.Errorf("%s: descriptor changed over the wire\n sent %+v\n  got %+v", want[i].Key, want[i], got[i])
		}
	}
}

// TestSettingDescriptors_GatesMatchTheHandler checks that the secrecy and
// writability a descriptor advertises are the ones the handler enforces. A
// descriptor that claims a key is writable when Set refuses it would send a
// client into a save loop it cannot win.
func TestSettingDescriptors_GatesMatchTheHandler(t *testing.T) {
	for _, d := range SettingDescriptors() {
		if want := isSecretSetting(d.Key); d.Secret != want {
			t.Errorf("%s: Secret=%v, isSecretSetting=%v", d.Key, d.Secret, want)
		}
		if want := isAdminOnlySetting(d.Key); d.AdminOnly != want {
			t.Errorf("%s: AdminOnly=%v, isAdminOnlySetting=%v", d.Key, d.AdminOnly, want)
		}
		wantWritable := !isSecretSetting(d.Key) || isWritableSecretSetting(d.Key)
		if d.Writable != wantWritable {
			t.Errorf("%s: Writable=%v, but Set would %v it", d.Key, d.Writable, map[bool]string{true: "accept", false: "refuse"}[wantWritable])
		}
	}
}

// TestSettingDescriptors_InertKeys pins the keys that are accepted and stored
// and read by nothing.
//
// The state exists because of PR #2421: the quality profile Cutoff and
// UpgradeAllowed controls were pulled from the UI once it turned out no code
// path read them, while the model, the SELECT and the wire format kept the
// fields for compatibility. Those two are quality profile columns rather than
// settings keys, so they cannot appear in this registry, but the settings table
// has the same shape of leftover and this is the list of it. A generic settings
// screen must not offer these as controls, or it puts back exactly what #2421
// took away.
func TestSettingDescriptors_InertKeys(t *testing.T) {
	// calibre.drop_folder_path: seeded empty by migration 011 on every install
	// and orphaned when PR #207 deleted Calibre drop folder mode. The seed was
	// never removed, so a stock install still has the row.
	//
	// abs.last_import_at: written by the Audiobookshelf importer after every
	// run and read by nothing. Its Calibre twin, calibre.last_import_at, is
	// rendered on the Calibre settings tab; this one never grew a display.
	wantInert := map[string]bool{
		"calibre.drop_folder_path": true,
		abs.SettingABSLastImportAt: true,
	}

	gotInert := map[string]bool{}
	for _, d := range SettingDescriptors() {
		if d.State == SettingStateInert {
			gotInert[d.Key] = true
		}
	}

	for key := range wantInert {
		d, ok := LookupSettingDescriptor(key)
		if !ok {
			t.Fatalf("%s is not registered at all", key)
		}
		if d.State != SettingStateInert {
			t.Errorf("%s: state %q, want %q", key, d.State, SettingStateInert)
		}
	}
	for key := range gotInert {
		if !wantInert[key] {
			t.Errorf("%s is marked inert but is not in this test's list; either the key really is dead and belongs here, or the state is wrong", key)
		}
	}
}

// TestSettingDescriptors_InertKeysAreStillAccepted checks that marking a key
// inert describes it without breaking it. A client that has always written the
// key keeps working; it just has to stop offering it as a control.
func TestSettingDescriptors_InertKeysAreStillAccepted(t *testing.T) {
	for _, d := range SettingDescriptors() {
		if d.State != SettingStateInert {
			continue
		}
		if err := validateSettingValue(d.Key, ""); err != nil {
			t.Errorf("%s: inert key refused on write: %v", d.Key, err)
		}
	}
}

// TestValidateSettingValue_RejectsUnknownKey is the point of the registry. An
// unrecognised key used to fall off the end of the switch and be stored, so a
// typo saved, reported success and then did nothing forever.
func TestValidateSettingValue_RejectsUnknownKey(t *testing.T) {
	for _, key := range []string{
		"serch.interval",             // the typo from #2311
		"search.Interval",            // right key, wrong case
		"import.drop_folder_typo",    // right prefix, wrong key
		"totally.made.up",            // nothing like a real key
		"ui.theme",                   // plausible, and never read by anything
		"calibre.drop_folder_path.x", // an inert key with a suffix
		"",                           // no key at all
	} {
		err := validateSettingValue(key, "whatever")
		if err == nil {
			t.Errorf("validateSettingValue(%q) accepted an unknown key", key)
			continue
		}
		if !strings.Contains(err.Error(), "unknown setting key") {
			t.Errorf("validateSettingValue(%q) rejected for the wrong reason: %v", key, err)
		}
	}
}

// TestSettings_SetUnknownKeyReturns400 checks the refusal reaches the caller as
// a 400 with the key named, rather than a 200 and a row nothing reads.
func TestSettings_SetUnknownKeyReturns400(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/serch.interval", strings.NewReader(`{"value":"24h"}`)), "serch.interval")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "serch.interval") {
		t.Errorf("error does not name the offending key: %s", rec.Body.String())
	}
	if got, _ := repo.Get(ctx, "serch.interval"); got != nil {
		t.Errorf("unknown key was stored anyway: %+v", got)
	}
}

// TestSettings_DeleteUnknownKeyStillWorks pins the other half of the decision.
// Writes are refused, reads and deletes are not, so a row an older or newer
// build left behind is still visible and can still be cleaned up.
func TestSettings_DeleteUnknownKeyStillWorks(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, "left.behind", "1"); err != nil {
		t.Fatal(err)
	}
	req := withKey(httptest.NewRequest(http.MethodDelete, "/api/v1/setting/left.behind", nil), "left.behind")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := repo.Get(ctx, "left.behind"); got != nil {
		t.Errorf("row not deleted: %+v", got)
	}
}

// TestValidateSettingValue_KnownKeysUnchanged is the behaviour preserving half
// of the refactor: every key that was validated before is validated the same
// way now. The cases mirror the rules in validateSettingValue, including the
// "empty means unset" escape every one of them has.
func TestValidateSettingValue_KnownKeysUnchanged(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{"hardcover token accepts text", SettingHardcoverAPIToken, "abc123", false},
		{"hardcover token rejects control chars", SettingHardcoverAPIToken, "abc\x01def", true},
		{"enhanced series accepts true", SettingHardcoverEnhancedSeriesEnabled, "TRUE", false},
		{"enhanced series accepts empty", SettingHardcoverEnhancedSeriesEnabled, "", false},
		{"enhanced series rejects other", SettingHardcoverEnhancedSeriesEnabled, "yes", true},

		{"drop layout accepts flat", SettingImportDropLayout, "flat", false},
		{"drop layout accepts templated", SettingImportDropLayout, "templated", false},
		{"drop layout accepts empty", SettingImportDropLayout, "", false},
		{"drop layout rejects other", SettingImportDropLayout, "nested", true},

		{"drop link mode accepts copy", SettingImportDropLinkMode, "copy", false},
		{"drop link mode accepts hardlink", SettingImportDropLinkMode, "hardlink", false},
		{"drop link mode rejects symlink", SettingImportDropLinkMode, "symlink", true},

		{"pair gating accepts true", SettingImportDropPairGating, "true", false},
		{"pair gating accepts false", SettingImportDropPairGating, "false", false},
		{"pair gating accepts empty", SettingImportDropPairGating, "", false},
		{"pair gating rejects other", SettingImportDropPairGating, "1", true},

		{"pair gating timeout accepts positive", SettingImportDropPairGatingTimeoutHours, "24", false},
		{"pair gating timeout accepts empty", SettingImportDropPairGatingTimeoutHours, "", false},
		{"pair gating timeout rejects zero", SettingImportDropPairGatingTimeoutHours, "0", true},
		{"pair gating timeout rejects text", SettingImportDropPairGatingTimeoutHours, "soon", true},

		{"import mode accepts external", SettingImportMode, "external", false},
		{"import mode accepts empty", SettingImportMode, "", false},
		{"import mode rejects symlink", SettingImportMode, "symlink", true},

		{"audiobook file template accepts a Part token", SettingNamingAudiobookFileTemplate, "{Title} - {Part}", false},
		{"audiobook file template accepts empty", SettingNamingAudiobookFileTemplate, "", false},
		{"audiobook file template rejects a template without Part", SettingNamingAudiobookFileTemplate, "{Title}", true},

		{"calibre mode accepts calibredb", SettingCalibreMode, "calibredb", false},
		{"calibre mode accepts empty", SettingCalibreMode, "", false},
		{"calibre mode rejects the removed drop_folder", SettingCalibreMode, "drop_folder", true},

		{"default media type accepts audiobook", SettingDefaultMediaType, "audiobook", false},
		{"default media type accepts empty", SettingDefaultMediaType, "", false},
		{"default media type rejects comic", SettingDefaultMediaType, "comic", true},

		{"media type strict accepts true", SettingDefaultMediaTypeStrict, "true", false},
		{"media type strict rejects other", SettingDefaultMediaTypeStrict, "maybe", true},

		{"monitor mode accepts future", SettingAuthorDefaultMonitorMode, "future", false},
		{"monitor mode accepts empty", SettingAuthorDefaultMonitorMode, "", false},
		{"monitor mode rejects series", SettingAuthorDefaultMonitorMode, "series", true},
		{"monitor mode rejects nonsense", SettingAuthorDefaultMonitorMode, "yesterday", true},

		{"monitor latest count accepts positive", SettingAuthorDefaultMonitorLatestCount, "5", false},
		{"monitor latest count accepts empty", SettingAuthorDefaultMonitorLatestCount, "", false},
		{"monitor latest count rejects zero", SettingAuthorDefaultMonitorLatestCount, "0", true},

		{"default root folder accepts an id", SettingDefaultLibraryRootFolderID, "3", false},
		{"default root folder accepts empty", SettingDefaultLibraryRootFolderID, "", false},
		{"default root folder rejects zero", SettingDefaultLibraryRootFolderID, "0", true},
		{"default root folder rejects text", SettingDefaultLibraryRootFolderID, "library", true},

		{"primary provider accepts dnb", SettingMetadataPrimaryProvider, "dnb", false},
		{"primary provider accepts empty", SettingMetadataPrimaryProvider, "", false},
		{"primary provider rejects goodreads", SettingMetadataPrimaryProvider, "goodreads", true},

		{"push path remap accepts a pair", SettingCalibrePushPathRemap, "/books:/library", false},
		{"push path remap accepts empty", SettingCalibrePushPathRemap, "", false},
		{"push path remap rejects a malformed pair", SettingCalibrePushPathRemap, "/books", true},

		// A literal RFC1918 address, not a hostname: the validator resolves
		// what it is given and a test must not depend on DNS.
		{"plugin url accepts a LAN address", SettingCalibrePluginURL, "https://192.168.1.50:8080", false},
		{"plugin url accepts empty", SettingCalibrePluginURL, "", false},
		{"plugin url rejects ftp", SettingCalibrePluginURL, "ftp://calibre.example.com", true},
		{"plugin url rejects a missing host", SettingCalibrePluginURL, "https://", true},
		{"plugin url rejects cloud metadata", SettingCalibrePluginURL, "http://169.254.169.254/latest/meta-data", true},

		{"abs enabled accepts false", SettingABSEnabled, "false", false},
		{"abs enabled accepts empty", SettingABSEnabled, "", false},
		{"abs enabled rejects other", SettingABSEnabled, "on", true},

		{"search interval accepts 24h", SettingSearchInterval, "24h", false},
		{"search interval accepts empty", SettingSearchInterval, "", false},
		{"search interval rejects gibberish", SettingSearchInterval, "soon", true},
		{"search interval rejects under an hour", SettingSearchInterval, "30m", true},
		{"search interval rejects over a week", SettingSearchInterval, "200h", true},

		{"hardcover sync interval accepts 6h", SettingHardcoverSyncInterval, "6h", false},
		{"hardcover sync interval accepts empty", SettingHardcoverSyncInterval, "", false},
		{"hardcover sync interval rejects under an hour", SettingHardcoverSyncInterval, "5m", true},
		{"hardcover sync interval rejects over a week", SettingHardcoverSyncInterval, "1000h", true},

		// Keys with a descriptor but no per key rule: they were accepted
		// before and still are, now on the strength of being registered
		// rather than of falling off the end of the switch.
		{"preferred language accepts anything", "search.preferredLanguage", "de", false},
		{"auto grab accepts true", "autoGrab.enabled", "true", false},
		{"stall timeout accepts a number", "stall.timeout_minutes", "45", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSettingValue(tc.key, tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("validateSettingValue(%q, %q) = nil, want an error", tc.key, tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateSettingValue(%q, %q) = %v, want nil", tc.key, tc.value, err)
			}
			if err != nil && strings.Contains(err.Error(), "unknown setting key") {
				t.Fatalf("%q is a real key but was refused as unknown: %v", tc.key, err)
			}
		})
	}
}

// webSettingKeys is every key web/src writes through PUT /api/v1/setting/{key}.
// Refusing unknown keys turns a missing descriptor into a settings screen that
// cannot save, so this list is the guard against shipping that.
//
// Regenerate with, from the repo root:
//
//	grep -rhon "setSetting(\s*'[^']*'" web/src --include=*.ts --include=*.tsx
//	grep -rhon "saveSetting(\s*'[^']*'" web/src --include=*.tsx
//	grep -rhon "saveSettingWithErrorThrowing(\s*'[^']*'" web/src --include=*.tsx
//
// plus DEFAULT_ROOT_FOLDER_KEY in web/src/pages/settings/RootFoldersTab.tsx.
var webSettingKeys = []string{
	"author.default_monitor_latest_count",
	"author.default_monitor_mode",
	"autoGrab.enabled",
	"calibre.binary_path",
	"calibre.library_import_enabled",
	"calibre.library_path",
	"calibre.mode",
	"calibre.plugin_api_key",
	"calibre.plugin_url",
	"calibre.push_path_remap",
	"calibre.sync_on_startup",
	"cwa.ingest_path",
	"default.media_type",
	"default.media_type_strict",
	"googlebooks.apiKey",
	"hardcover.api_token",
	"hardcover.enhanced_series_enabled",
	"hardcover.sync_interval",
	"import.audiobook.flatten_multi_disc",
	"import.drop_folder",
	"import.drop_layout",
	"import.drop_link_mode",
	"import.mode",
	"library.defaultRootFolderId",
	"log.retention_days",
	"metadata.primary_provider",
	"naming.audiobook_file_template",
	"naming.bookTemplate",
	"naming_template_audiobook",
	"recommendations.enabled",
	"search.interval",
	"search.preferredLanguage",
	"telemetry.enabled",
}

// TestSettingDescriptors_CoverEveryKeyTheUIWrites is the regression that keeps
// the refusal from breaking the app that ships with it.
func TestSettingDescriptors_CoverEveryKeyTheUIWrites(t *testing.T) {
	for _, key := range webSettingKeys {
		if !IsKnownSettingKey(key) {
			t.Errorf("%s is written by the settings UI but has no descriptor, so saving it would now 400", key)
		}
	}
}

// settingKeyConstant matches the settings key constants declared in this
// package: an exported identifier starting with Setting (or the one Legacy
// prefixed alias) assigned a string literal. SettingType* and SettingState* are
// this file's own vocabulary rather than keys and are skipped by name.
var settingKeyConstant = regexp.MustCompile(`(?m)^\s*((?:Legacy)?Setting[A-Za-z0-9]*)\s+(?:[A-Za-z]+\s+)?=\s*"([^"]+)"`)

// TestSettingDescriptors_CoverEverySettingConstant walks this package's own
// source for settings key constants and checks each one is registered. It is
// the reason a new key cannot be added without a descriptor: the constant
// itself trips the test.
func TestSettingDescriptors_CoverEverySettingConstant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range settingKeyConstant.FindAllStringSubmatch(string(src), -1) {
			constName, key := m[1], m[2]
			if strings.HasPrefix(constName, "SettingType") || strings.HasPrefix(constName, "SettingState") {
				continue
			}
			found++
			if !IsKnownSettingKey(key) {
				t.Errorf("%s declares %s = %q with no descriptor in settings_registry.go", name, constName, key)
			}
		}
	}
	// A scan that matches nothing would pass silently and guard nothing.
	if found < 20 {
		t.Fatalf("only matched %d settings key constants; the scan regexp has probably stopped matching", found)
	}
}

// TestSettingsHandler_Descriptors checks the endpoint serves the registry as
// JSON with no stored values in it.
func TestSettingsHandler_Descriptors(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, SettingHardcoverAPIToken, "supersecret-hardcover"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreLibraryPath, "/srv/private/calibre"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Descriptors(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/descriptors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, leak := range []string{"supersecret-hardcover", "/srv/private/calibre"} {
		if strings.Contains(body, leak) {
			t.Errorf("descriptor response carries a stored value: %s", leak)
		}
	}

	var got []SettingDescriptor
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response is not a descriptor list: %v", err)
	}
	if len(got) != len(settingDescriptors) {
		t.Errorf("served %d descriptors, registry holds %d", len(got), len(settingDescriptors))
	}

	byKey := map[string]SettingDescriptor{}
	for _, d := range got {
		byKey[d.Key] = d
	}
	interval, ok := byKey[SettingSearchInterval]
	if !ok {
		t.Fatalf("%s missing from the served registry", SettingSearchInterval)
	}
	if interval.Type != SettingTypeDuration || interval.Default != "12h" || interval.Min != "1h" || interval.Max != "168h" {
		t.Errorf("%s served as %+v", SettingSearchInterval, interval)
	}
	if !interval.RestartRequired {
		t.Errorf("%s is read once at scheduler start, so restartRequired must survive the wire", SettingSearchInterval)
	}
	if token := byKey[SettingHardcoverAPIToken]; !token.Secret || !token.Writable {
		t.Errorf("%s served as secret=%v writable=%v, want secret and still writable", SettingHardcoverAPIToken, token.Secret, token.Writable)
	}
	if path := byKey[SettingCalibreLibraryPath]; path.Secret || !path.AdminOnly {
		t.Errorf("%s served as secret=%v adminOnly=%v, want admin only but not secret", SettingCalibreLibraryPath, path.Secret, path.AdminOnly)
	}
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
