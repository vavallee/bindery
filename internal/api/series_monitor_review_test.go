package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// A book whose hold was never written (a failure or timeout between creating
// it and holding it) is repaired by the next check, before the sweep can
// search it.
func TestSeriesCheckRepairsAMissingHold(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	f.check(t)
	oath := f.book(t, "hc:oathbringer")
	if err := f.books.ClearSearchHold(context.Background(), oath.ID); err != nil {
		t.Fatal(err)
	}
	f.check(t)
	holds, _ := f.books.SearchHolds(context.Background(), time.Now())
	if until, ok := holds[oath.ID]; !ok || !until.Equal(f.future) {
		t.Errorf("hold after repair = %v (%v), want %v", until, ok, f.future)
	}
}

// The repro for lost releases: ok on day 0, provider down on day 1 (stamped),
// the book is released on day 1, ok on day 2. Upcoming mode compares against
// the last good check, so the book is still added.
func TestSeriesCheckDoesNotLoseARelease(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := f.repo.StampCheck(ctx, f.series.ID, now.Add(-48*time.Hour), now, models.SeriesCheckOK, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.StampCheck(ctx, f.series.ID, now.Add(-24*time.Hour), now, models.SeriesCheckFailed, "down", 0); err != nil {
		t.Fatal(err)
	}
	setCatalogDate(t, f, "hc:words-of-radiance", ptrTime(now.Add(-36*time.Hour)))
	res := f.check(t)
	if res.Created != 2 || f.book(t, "hc:words-of-radiance") == nil {
		t.Errorf("check = %+v, want the book released while the provider was down added", res)
	}
}

// Monitoring switched off and on again starts from a first check: the old last
// good check says nothing about the gap.
func TestSeriesCheckResetsAfterReMonitoring(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := f.repo.StampCheck(ctx, f.series.ID, now.Add(-48*time.Hour), now.Add(time.Hour), models.SeriesCheckOK, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SetMonitoring(ctx, f.series.ID, false, models.SeriesMonitorUpcoming, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SetMonitoring(ctx, f.series.ID, true, models.SeriesMonitorUpcoming, 0); err != nil {
		t.Fatal(err)
	}
	mon, _ := f.repo.GetMonitoring(ctx, f.series.ID)
	if mon.LastOKAt != nil || mon.NextCheckAt != nil {
		t.Errorf("re-monitoring kept the old schedule: %+v", mon)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSeriesCheckSkipsAMultiAccountSeries(t *testing.T) {
	h, series, books, _, database := tenancyMonitorFixture(t, true)
	ctx := context.Background()
	// A second account's book in the same series.
	other, err := db.NewUserRepo(database).Create(ctx, "carol", "x")
	if err != nil {
		t.Fatal(err)
	}
	authors := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "carol-a", Name: "Carol A", SortName: "A"}
	if err := authors.CreateForUser(ctx, a, other.ID); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "carol-b", AuthorID: a.ID, Title: "Carol Book", SortTitle: "Carol Book", Status: models.BookStatusWanted, Monitored: true,
		MediaType: models.MediaTypeEbook, Genres: []string{}, OwnerUserID: other.ID}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := h.series.LinkBook(ctx, series.ID, b.ID, "9", false); err != nil {
		t.Fatal(err)
	}
	res, err := h.CheckSeries(ctx, series)
	if err != nil || res.Status != models.SeriesCheckAmbiguous || res.Created != 0 {
		t.Fatalf("res = %+v, err = %v, want ambiguous_owner and nothing added", res, err)
	}
	auth.SetEnforceTenancyForTests(t, false)
	// Without tenancy there is no owner question, so the same series is checked.
	if res, _ := h.CheckSeries(ctx, series); res.Status == models.SeriesCheckAmbiguous {
		t.Error("ambiguous_owner reported with tenancy off")
	}
}

func TestSeriesCheckProviderDisabled(t *testing.T) {
	catalog := stormlightCatalog()
	h, repo, _, _, _ := seriesFixtureWithProviderAndSettings(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil, false)
	ctx := context.Background()
	s := &models.Series{ForeignID: "s", Title: "S"}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: s.ID, HardcoverSeriesID: catalog.ForeignID, Confidence: 1, LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitoring(ctx, s.ID, true, models.SeriesMonitorAll, 0); err != nil {
		t.Fatal(err)
	}
	res, err := h.CheckSeries(ctx, s)
	if err != nil || res.Status != models.SeriesCheckDisabled || res.Created != 0 {
		t.Errorf("res = %+v, err = %v, want provider_disabled", res, err)
	}
}

func monitorRequest(id, body string) *http.Request {
	return withURLParam(httptest.NewRequest(http.MethodPatch, "/api/v1/series/"+id, bytes.NewBufferString(body)), "id", id)
}

func TestSeriesMonitorHandlerModes(t *testing.T) {
	h, repo, _, _ := seriesFixture(t)
	ctx := context.Background()
	s := &models.Series{ForeignID: "m", Title: "M"}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(s.ID, 10)

	rec := httptest.NewRecorder()
	h.Monitor(rec, monitorRequest(id, `{"monitored":true,"mode":"sideways"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad mode = %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Monitor(rec, monitorRequest(id, `{"monitored":true,"mode":"all"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("mode all = %d: %s", rec.Code, rec.Body.String())
	}
	// Omitting the mode keeps the stored one.
	rec = httptest.NewRecorder()
	h.Monitor(rec, monitorRequest(id, `{"monitored":true}`))
	if mon, _ := repo.GetMonitoring(ctx, s.ID); rec.Code != http.StatusOK || mon.Mode != models.SeriesMonitorAll {
		t.Errorf("mode after an omitted one = %+v, want all kept", mon)
	}
	rec = httptest.NewRecorder()
	h.Monitor(rec, monitorRequest("9999", `{"monitored":true}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing series = %d, want 404", rec.Code)
	}
}

func TestValidateSeriesMonitoringInterval(t *testing.T) {
	for _, ok := range []string{"", "off", "24h", "168h", "720h"} {
		if err := validateSettingValue(SettingSeriesMonitoringInterval, ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"1h", "23h", "721h", "abc"} {
		if err := validateSettingValue(SettingSeriesMonitoringInterval, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestListWantedUpcomingParameter(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	mk := func(id string) *models.Book {
		b := &models.Book{ForeignID: id, AuthorID: author.ID, Title: id, SortTitle: id, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	held, free := mk("held"), mk("free")
	if err := books.SetSearchHold(ctx, held.ID, time.Now().Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	titles := func(query string) []string {
		rec := httptest.NewRecorder()
		h.ListWanted(rec, httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing"+query, nil))
		var got []models.Book
		if rec.Body.Len() > 0 && rec.Body.Bytes()[0] == '{' {
			var page struct{ Items []models.Book }
			_ = json.Unmarshal(rec.Body.Bytes(), &page)
			got = page.Items
		} else {
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
		}
		var out []string
		for _, b := range got {
			out = append(out, b.Title)
			if (b.ID == held.ID) != (b.SearchHeldUntil != nil) {
				t.Errorf("%s: searchHeldUntil = %v", b.Title, b.SearchHeldUntil)
			}
		}
		return out
	}
	for _, q := range []string{"", "?limit=50"} {
		if got := titles(q + sep(q) + "upcoming=only"); len(got) != 1 || got[0] != held.Title {
			t.Errorf("only (%q) = %v", q, got)
		}
		if got := titles(q + sep(q) + "upcoming=hide"); len(got) != 1 || got[0] != free.Title {
			t.Errorf("hide (%q) = %v", q, got)
		}
		if got := titles(q); len(got) != 2 {
			t.Errorf("default (%q) = %v, want both", q, got)
		}
	}
}

func sep(q string) string {
	if q == "" {
		return "?"
	}
	return "&"
}
