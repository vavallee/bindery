package api

import (
	"reflect"
	"testing"
)

// The content check settings (#3015, #3246) default to warn, offer exactly off,
// warn and block, and only an admin may read or write them.
func TestContentCheckSettingDescriptors(t *testing.T) {
	for _, key := range []string{SettingImportEbookContentCheck, SettingImportAudiobookContentCheck} {
		var found *SettingDescriptor
		for _, d := range SettingDescriptors() {
			if d.Key == key {
				d := d
				found = &d
			}
		}
		if found == nil {
			t.Fatalf("%s has no descriptor", key)
		}
		if found.Type != SettingTypeEnum {
			t.Errorf("%s type = %q, want enum", key, found.Type)
		}
		if found.Default != "warn" {
			t.Errorf("%s default = %q, want warn: block must be opt in", key, found.Default)
		}
		if want := []string{"off", "warn", "block"}; !reflect.DeepEqual(found.Values, want) {
			t.Errorf("%s values = %v, want %v", key, found.Values, want)
		}
		if !found.AdminOnly {
			t.Errorf("%s is readable by a non admin", key)
		}
		if !found.Writable || found.Secret {
			t.Errorf("%s writable = %v, secret = %v", key, found.Writable, found.Secret)
		}
		if found.State != SettingStateActive {
			t.Errorf("%s state = %q, want active", key, found.State)
		}
	}
}

// A non admin must not be able to read the settings, and nothing about them is
// on the list a non admin screen reads.
func TestContentCheckSettingsAreNotNonAdminReadable(t *testing.T) {
	for _, key := range []string{SettingImportEbookContentCheck, SettingImportAudiobookContentCheck} {
		if !isAdminOnlySetting(key) {
			t.Errorf("%s is not admin only", key)
		}
	}
}
