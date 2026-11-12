package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// #3831: an author with no quality profile has no format filter at all, so a
// pdf only download imported under what the user thought was an epub only
// setup. These tests pin the three parts of the fix: the default quality
// profile for new authors, the explicit null that keeps "None" choosable, and
// the opt in apply for authors that already have none.

type qpDefaultFixture struct {
	db       *sql.DB
	h        *AuthorHandler
	authors  *db.AuthorRepo
	settings *db.SettingsRepo
	profiles *db.QualityProfileRepo
	jobs     *jobs.Group
}

func newQPDefaultFixture(t *testing.T) qpDefaultFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	authorRepo := db.NewAuthorRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	group := jobs.NewGroup(context.Background())
	t.Cleanup(func() { group.Shutdown(5 * time.Second) })
	h := NewAuthorHandler(authorRepo, nil, db.NewBookRepo(database), nil,
		metadata.NewAggregator(wellsCreateProvider()), settingsRepo, db.NewMetadataProfileRepo(database), nil).WithJobs(group)
	return qpDefaultFixture{db: database, h: h, authors: authorRepo, settings: settingsRepo, profiles: db.NewQualityProfileRepo(database), jobs: group}
}

func (f qpDefaultFixture) profile(t *testing.T, name string) int64 {
	t.Helper()
	p := &models.QualityProfile{Name: name, Items: []models.QualityItem{{Quality: "epub", Allowed: true}, {Quality: "pdf", Allowed: false}}}
	if err := f.profiles.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func (f qpDefaultFixture) setDefault(t *testing.T, id int64) {
	t.Helper()
	if err := f.settings.Set(context.Background(), SettingAuthorDefaultQualityProfileID, strconv.FormatInt(id, 10)); err != nil {
		t.Fatal(err)
	}
}

// create posts an Add Author body and returns the stored author.
func (f qpDefaultFixture) create(t *testing.T, body map[string]any) *models.Author {
	t.Helper()
	body["foreignAuthorId"] = wellsCreateParams.ForeignID
	body["authorName"] = wellsCreateParams.Name
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(raw)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if left := f.jobs.Shutdown(5 * time.Second); len(left) != 0 {
		t.Fatalf("jobs still running: %v", left)
	}
	stored, err := f.authors.GetByID(context.Background(), got.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored author: %v %v", stored, err)
	}
	return stored
}

func qpID(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

func TestCreateAuthor_NoQualityProfileGetsTheDefault(t *testing.T) {
	f := newQPDefaultFixture(t)
	epub := f.profile(t, "Epub only")
	f.setDefault(t, epub)

	got := f.create(t, map[string]any{"monitored": true})
	if got.QualityProfileID == nil || *got.QualityProfileID != epub {
		t.Fatalf("quality profile = %s, want the default %d", qpID(got.QualityProfileID), epub)
	}
}

func TestCreateAuthor_NoQualityProfileStaysNoneWithoutADefault(t *testing.T) {
	f := newQPDefaultFixture(t)
	f.profile(t, "Epub only")

	got := f.create(t, map[string]any{"monitored": true})
	if got.QualityProfileID != nil {
		t.Fatalf("quality profile = %s, want none when no default is set", qpID(got.QualityProfileID))
	}
}

func TestCreateAuthor_ExplicitNullKeepsNoneOverTheDefault(t *testing.T) {
	f := newQPDefaultFixture(t)
	f.setDefault(t, f.profile(t, "Epub only"))

	got := f.create(t, map[string]any{"monitored": true, "qualityProfileId": nil})
	if got.QualityProfileID != nil {
		t.Fatalf("quality profile = %s, want none: the user picked None", qpID(got.QualityProfileID))
	}
}

func TestCreateAuthor_ExplicitProfileWinsOverTheDefault(t *testing.T) {
	f := newQPDefaultFixture(t)
	f.setDefault(t, f.profile(t, "Epub only"))
	other := f.profile(t, "Anything")

	got := f.create(t, map[string]any{"monitored": true, "qualityProfileId": other})
	if got.QualityProfileID == nil || *got.QualityProfileID != other {
		t.Fatalf("quality profile = %s, want the chosen %d", qpID(got.QualityProfileID), other)
	}
}

func TestCreateAuthor_DeletedDefaultProfileIsIgnored(t *testing.T) {
	f := newQPDefaultFixture(t)
	gone := f.profile(t, "Deleted later")
	f.setDefault(t, gone)
	if err := f.profiles.Delete(context.Background(), gone); err != nil {
		t.Fatal(err)
	}

	got := f.create(t, map[string]any{"monitored": true})
	if got.QualityProfileID != nil {
		t.Fatalf("quality profile = %s, want none: the default points at a deleted profile", qpID(got.QualityProfileID))
	}
}

func TestCreateAuthorCore_RequestApprovalWithoutAChoiceGetsTheDefault(t *testing.T) {
	f := newQPDefaultFixture(t)
	epub := f.profile(t, "Epub only")
	f.setDefault(t, epub)
	p := wellsCreateParams
	p.SkipCatalogueSync = true

	res, err := f.h.createAuthorCore(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Author.QualityProfileID == nil || *res.Author.QualityProfileID != epub {
		t.Fatalf("quality profile = %s, want the default %d", qpID(res.Author.QualityProfileID), epub)
	}
}

func TestUpdateAuthor_ExplicitNullClearsTheQualityProfile(t *testing.T) {
	f := newQPDefaultFixture(t)
	epub := f.profile(t, "Epub only")
	a := &models.Author{ForeignID: "OL1A", Name: "Jim Butcher", QualityProfileID: &epub, Monitored: true}
	if err := f.authors.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	put := func(body string) *models.Author {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/author/"+strconv.FormatInt(a.ID, 10), bytes.NewReader([]byte(body)))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(a.ID, 10))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		f.h.Update(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("update %s: status %d: %s", body, rec.Code, rec.Body.String())
		}
		stored, _ := f.authors.GetByID(context.Background(), a.ID)
		return stored
	}

	if got := put(`{"monitored": true}`); got.QualityProfileID == nil || *got.QualityProfileID != epub {
		t.Fatalf("leaving qualityProfileId out changed it to %s, want %d kept", qpID(got.QualityProfileID), epub)
	}
	if got := put(`{"qualityProfileId": null}`); got.QualityProfileID != nil {
		t.Fatalf("explicit null left quality profile %s, want it cleared", qpID(got.QualityProfileID))
	}
	if got := put(`{"qualityProfileId": ` + strconv.FormatInt(epub, 10) + `}`); got.QualityProfileID == nil || *got.QualityProfileID != epub {
		t.Fatalf("setting it again gave %s, want %d", qpID(got.QualityProfileID), epub)
	}
}

// unsetFixture seeds one author per case the count and apply must tell
// apart, with tenancy on or off.
type unsetFixture struct {
	qpDefaultFixture
	alice, bob                            int64
	aliceNone, aliceSet, bobNone, unowned int64
	epub                                  int64
}

func newUnsetFixture(t *testing.T) unsetFixture {
	t.Helper()
	return newUnsetFixtureTenancy(t, true)
}

func newUnsetFixtureTenancy(t *testing.T, tenancy bool) unsetFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	f := unsetFixture{qpDefaultFixture: newQPDefaultFixture(t)}
	ctx := context.Background()
	f.epub = f.profile(t, "Epub only")
	other := f.profile(t, "Other")
	users := db.NewUserRepo(f.db)
	for _, u := range []struct {
		name string
		id   *int64
	}{{"alice", &f.alice}, {"bob", &f.bob}} {
		created, err := users.Create(ctx, u.name, "h-"+u.name)
		if err != nil {
			t.Fatal(err)
		}
		*u.id = created.ID
	}
	mk := func(fid string, owner int64, qp *int64) int64 {
		a := &models.Author{ForeignID: fid, Name: fid, Monitored: true, QualityProfileID: qp}
		if err := f.authors.CreateForUser(ctx, a, owner); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	f.aliceNone = mk("OL-ALICE-NONE", f.alice, nil)
	f.aliceSet = mk("OL-ALICE-SET", f.alice, &other)
	f.bobNone = mk("OL-BOB-NONE", f.bob, nil)
	f.unowned = mk("OL-UNOWNED-NONE", 0, nil)
	return f
}

func (f unsetFixture) profileOf(t *testing.T, id int64) *int64 {
	t.Helper()
	a, err := f.authors.GetByID(context.Background(), id)
	if err != nil || a == nil {
		t.Fatalf("author %d: %v", id, err)
	}
	return a.QualityProfileID
}

func callUnset(t *testing.T, h *AuthorQualityProfileUnsetHandler, ctx context.Context, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/authors/without-quality-profile", bytes.NewReader([]byte(body))).WithContext(ctx)
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.Count(rec, req)
	} else {
		h.Apply(rec, req)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// An admin covers every author in the library, whoever owns it, with tenancy
// on or off. Authors carry an owner even with tenancy off, and request
// approval creates the author as the requester (bob here), so a scope of own
// plus unowned rows missed exactly the authors that needed the fix: with
// tenancy off it counted 2 of the 3 authors with no profile.
func TestAuthorsWithoutQualityProfile_AdminCoversEveryAuthor(t *testing.T) {
	for _, tenancy := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenancy off", true: "tenancy on"}[tenancy], func(t *testing.T) {
			f := newUnsetFixtureTenancy(t, tenancy)
			h := NewAuthorQualityProfileUnsetHandler(f.authors, f.profiles)
			aliceAdmin := auth.WithUserRole(auth.WithUserID(context.Background(), f.alice), "admin")

			code, out := callUnset(t, h, aliceAdmin, http.MethodGet, "")
			if code != http.StatusOK || out["count"] != float64(3) {
				t.Fatalf("count = %d %v, want 200 and 3 (alice's, bob's and the unowned author with no profile)", code, out)
			}

			code, out = callUnset(t, h, aliceAdmin, http.MethodPost, `{"qualityProfileId": `+strconv.FormatInt(f.epub, 10)+`}`)
			if code != http.StatusOK || out["updated"] != float64(3) {
				t.Fatalf("apply = %d %v, want 200 and 3 updated", code, out)
			}
			for _, id := range []int64{f.aliceNone, f.bobNone, f.unowned} {
				if got := f.profileOf(t, id); got == nil || *got != f.epub {
					t.Errorf("author %d profile = %s, want %d", id, qpID(got), f.epub)
				}
			}
			if got := f.profileOf(t, f.aliceSet); got == nil || *got == f.epub {
				t.Errorf("alice's configured author was changed to %s; only authors with no profile may change", qpID(got))
			}

			if _, out := callUnset(t, h, aliceAdmin, http.MethodGet, ""); out["count"] != float64(0) {
				t.Errorf("count after apply = %v, want 0", out["count"])
			}
		})
	}
}

// With tenancy off there is no per account library, so even a non admin sees
// every author (ListScopeUserID is 0). The route is admin only; this pins the
// handler's own scope.
func TestAuthorsWithoutQualityProfile_TenancyOffNonAdminCoversEveryAuthor(t *testing.T) {
	f := newUnsetFixtureTenancy(t, false)
	h := NewAuthorQualityProfileUnsetHandler(f.authors, f.profiles)
	alice := auth.WithUserRole(auth.WithUserID(context.Background(), f.alice), "user")

	if _, out := callUnset(t, h, alice, http.MethodGet, ""); out["count"] != float64(3) {
		t.Fatalf("count = %v, want 3", out["count"])
	}
}

// The route is admin only, but the handler's own scope must still keep a non
// admin to their own rows: under tenancy an unowned author is not theirs to
// change, the same rule CheckWriteOwnership applies one author at a time.
func TestAuthorsWithoutQualityProfile_NonAdminScopeIsOwnRowsOnly(t *testing.T) {
	f := newUnsetFixture(t)
	h := NewAuthorQualityProfileUnsetHandler(f.authors, f.profiles)
	alice := auth.WithUserRole(auth.WithUserID(context.Background(), f.alice), "user")

	if _, out := callUnset(t, h, alice, http.MethodGet, ""); out["count"] != float64(1) {
		t.Fatalf("count = %v, want 1 (only alice's own unset author)", out["count"])
	}
	if _, out := callUnset(t, h, alice, http.MethodPost, `{"qualityProfileId": `+strconv.FormatInt(f.epub, 10)+`}`); out["updated"] != float64(1) {
		t.Fatalf("updated = %v, want 1", out["updated"])
	}
	if got := f.profileOf(t, f.unowned); got != nil {
		t.Errorf("unowned author changed to %s by a non admin under tenancy", qpID(got))
	}
}

func TestAuthorsWithoutQualityProfile_ApplyRefusesAMissingProfile(t *testing.T) {
	f := newUnsetFixture(t)
	h := NewAuthorQualityProfileUnsetHandler(f.authors, f.profiles)
	aliceAdmin := auth.WithUserRole(auth.WithUserID(context.Background(), f.alice), "admin")

	for _, body := range []string{`{}`, `{"qualityProfileId": null}`, `{"qualityProfileId": 9999}`, `not json`} {
		if code, _ := callUnset(t, h, aliceAdmin, http.MethodPost, body); code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, code)
		}
	}
	if got := f.profileOf(t, f.aliceNone); got != nil {
		t.Errorf("a refused apply changed alice's author to %s", qpID(got))
	}
}

func TestSettingDefaultQualityProfile_RefusesAProfileThatDoesNotExist(t *testing.T) {
	f := newQPDefaultFixture(t)
	epub := f.profile(t, "Epub only")
	sh := NewSettingsHandler(f.settings)

	put := func(value string) int {
		body, _ := json.Marshal(map[string]string{"value": value})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingAuthorDefaultQualityProfileID, bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("key", SettingAuthorDefaultQualityProfileID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		sh.Set(rec, req)
		return rec.Code
	}
	for value, want := range map[string]int{
		strconv.FormatInt(epub, 10): http.StatusOK,
		"":                          http.StatusOK,
		"9999":                      http.StatusBadRequest,
		"0":                         http.StatusBadRequest,
		"epub":                      http.StatusBadRequest,
	} {
		if got := put(value); got != want {
			t.Errorf("PUT %q: status %d, want %d", value, got, want)
		}
	}
}
