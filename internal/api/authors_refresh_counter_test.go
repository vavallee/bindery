package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// The manual Refresh claims the author in runningSyncs before it answers, and
// every exit from the sync has to give that claim back, or the author answers
// 409 to every later click until the server restarts (#2601 review). These
// pin the early exits: a works error, a panic the jobs group recovers, the
// Calibre relink returning early, and a jobs group that refused the sync.

// refreshCounterWorksStub fails or panics inside the catalogue fetch, after the
// running sync counter has been taken.
type refreshCounterWorksStub struct {
	*stubMetaProvider
	err     error
	doPanic bool
}

func (p *refreshCounterWorksStub) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	if p.doPanic {
		panic("provider blew up mid sync")
	}
	return nil, p.err
}

func TestAuthorRefresh_RunningMarkReleasedOnEarlyExit(t *testing.T) {
	cases := []struct {
		name      string
		foreignID string
		stub      metadata.Provider
	}{
		{"works error", "OL2601A", &refreshCounterWorksStub{stubMetaProvider: &stubMetaProvider{}, err: errors.New("503")}},
		{"provider panic", "OL2601A", &refreshCounterWorksStub{stubMetaProvider: &stubMetaProvider{}, doPanic: true}},
		{"calibre relink fails", "calibre:author:7", &stubMetaProvider{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			profileRepo := db.NewMetadataProfileRepo(database)
			author := &models.Author{ForeignID: tc.foreignID, Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary", Monitored: true}
			if err := authorRepo.Create(context.Background(), author); err != nil {
				t.Fatal(err)
			}
			group := jobs.NewGroup(context.Background())
			defer group.Shutdown(5 * time.Second)
			h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(tc.stub), nil, profileRepo, nil).WithJobs(group)
			id := strconv.FormatInt(author.ID, 10)
			click := func() int {
				req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/refresh", nil), "id", id)
				rec := httptest.NewRecorder()
				h.Refresh(rec, req)
				return rec.Code
			}
			for round := 1; round <= 3; round++ {
				if code := click(); code != http.StatusAccepted {
					t.Fatalf("round %d Refresh = %d, want 202: the running sync mark leaked from an earlier failed sync", round, code)
				}
				deadline := time.Now().Add(5 * time.Second)
				for h.runningSyncs.running(author.ID) {
					if time.Now().After(deadline) {
						t.Fatalf("round %d: running mark never cleared after the sync failed", round)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			h.runningSyncs.mu.Lock()
			n := len(h.runningSyncs.byAuthor)
			h.runningSyncs.mu.Unlock()
			if n != 0 {
				t.Fatalf("runningSyncs map holds %d entries after every sync finished, want 0", n)
			}
		})
	}
}

// A sync the shutdown refused must release the claim Refresh took.
func TestAuthorRefresh_RunningMarkReleasedWhenJobsRefuse(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL2601A", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(context.Background(), author); err != nil {
		t.Fatal(err)
	}
	group := jobs.NewGroup(context.Background())
	group.Shutdown(time.Second)
	h := NewAuthorHandler(authorRepo, nil, db.NewBookRepo(database), nil, metadata.NewAggregator(&stubMetaProvider{}), nil, db.NewMetadataProfileRepo(database), nil).WithJobs(group)
	id := strconv.FormatInt(author.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/refresh", nil), "id", id)
	h.Refresh(httptest.NewRecorder(), req)
	if h.runningSyncs.running(author.ID) {
		t.Fatal("a Refresh whose sync the jobs group refused left the author marked running")
	}
}
