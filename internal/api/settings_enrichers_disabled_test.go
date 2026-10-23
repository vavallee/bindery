package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// TestSettings_EnrichersDisabledRoundTrip covers #1890: a list is stored
// normalised (trimmed, lowercased, deduplicated), reads back, runs the change
// hook once, and clearing it stores the empty default.
func TestSettings_EnrichersDisabledRoundTrip(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	rec := &hookRecorder{handles: map[string]bool{SettingMetadataEnrichersDisabled: true}}
	h.WithChangeHook(rec.hook)

	// The value is sent through JSON, so build it by hand for the spaces.
	w := httptest.NewRecorder()
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingMetadataEnrichersDisabled,
		bytes.NewBufferString(`{"value":" DNB , dnb,,Hardcover "}`)), SettingMetadataEnrichersDisabled)
	h.Set(w, adminReq(req))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	s, _ := repo.Get(ctx, SettingMetadataEnrichersDisabled)
	if s == nil || s.Value != "dnb,hardcover" {
		t.Fatalf("stored value = %+v, want dnb,hardcover", s)
	}
	if !reflect.DeepEqual(rec.calls, []string{SettingMetadataEnrichersDisabled}) {
		t.Errorf("hook calls = %v, want one", rec.calls)
	}

	if code, body := putSettingWithHook(t, h, SettingMetadataEnrichersDisabled, ""); code != http.StatusOK {
		t.Fatalf("clearing = %d: %v", code, body)
	}
	if s, _ := repo.Get(ctx, SettingMetadataEnrichersDisabled); s == nil || s.Value != "" {
		t.Errorf("cleared value = %+v, want empty", s)
	}
}

// TestSettings_EnrichersDisabledRejectsUnknownNames chooses refusing over
// dropping, like an unknown primary provider: a typo must not look saved.
func TestSettings_EnrichersDisabledRejectsUnknownNames(t *testing.T) {
	for _, value := range []string{"goodreads", "dnb,goodreads", "nb", "bnf"} {
		h, repo, ctx := settingsFixture(t)
		rec := &hookRecorder{handles: map[string]bool{SettingMetadataEnrichersDisabled: true}}
		h.WithChangeHook(rec.hook)
		if code, body := putSettingWithHook(t, h, SettingMetadataEnrichersDisabled, value); code != http.StatusBadRequest {
			t.Errorf("PUT %q = %d, want 400 (%v)", value, code, body)
		}
		if s, _ := repo.Get(ctx, SettingMetadataEnrichersDisabled); s != nil {
			t.Errorf("PUT %q stored %+v", value, s)
		}
		if len(rec.calls) != 0 {
			t.Errorf("hook ran for rejected %q: %v", value, rec.calls)
		}
	}
}

// TestSettings_EnrichersDisabledAcceptsEveryEnricher keeps the accepted names
// tied to MetadataEnrichers.
func TestSettings_EnrichersDisabledAcceptsEveryEnricher(t *testing.T) {
	for _, name := range MetadataSourceToggles() {
		if err := validateSettingValue(SettingMetadataEnrichersDisabled, name); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

// TestSettings_EnrichersDisabledIsAdminOnly checks a non admin can neither
// write nor read the list.
func TestSettings_EnrichersDisabledIsAdminOnly(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	gated := auth.RequireAdmin(http.HandlerFunc(h.Set))
	req := userReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingMetadataEnrichersDisabled,
		bytes.NewBufferString(`{"value":"dnb"}`)), SettingMetadataEnrichersDisabled))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT as non-admin = %d, want 403", rec.Code)
	}
	if s, _ := repo.Get(ctx, SettingMetadataEnrichersDisabled); s != nil {
		t.Errorf("non-admin PUT stored %+v", s)
	}
	if !isAdminOnlySetting(SettingMetadataEnrichersDisabled) {
		t.Error("the setting is readable by a non admin")
	}
}

func TestSettings_EnrichersDisabledIsLiveInRegistry(t *testing.T) {
	for _, d := range SettingDescriptors() {
		if d.Key != SettingMetadataEnrichersDisabled {
			continue
		}
		if d.RestartRequired || d.Default != "" || d.State != SettingStateActive {
			t.Errorf("descriptor = %+v", d)
		}
		return
	}
	t.Fatal("no descriptor")
}
