package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// savingDuringLookup runs onGet the first time the provider is asked for an
// author, the moment a user's save would land while a Refresh or relink waits
// on the provider.
type savingDuringLookup struct {
	*searchableAuthorProvider
	onGet func()
}

func (p *savingDuringLookup) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if p.onGet != nil {
		p.onGet()
		p.onGet = nil
	}
	return p.searchableAuthorProvider.GetAuthor(ctx, foreignID)
}

// A Refresh, a relink and a Calibre relink each build the whole author row
// from a snapshot read before the provider call. A bio the user saves during
// that call must survive, with its user credit, instead of being written over
// by the older snapshot (#3287).
func TestStaleAuthorSnapshotDoesNotOverwriteUserSave(t *testing.T) {
	const name = "Kari Nordmann"
	userBio := "Written by hand."
	newFixture := func(t *testing.T) (*relinkUpstreamFixture, *savingDuringLookup) {
		p := &savingDuringLookup{searchableAuthorProvider: &searchableAuthorProvider{
			stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: "OL1A"}}},
			authors:              map[string]*models.Author{"OL1A": {Name: name, ForeignID: "OL1A", Description: "From the provider."}},
		}}
		return newRelinkUpstreamFixture(t, p), p
	}
	// userSaves writes the bio the way the author page does, on a fresh read.
	userSaves := func(t *testing.T, f *relinkUpstreamFixture, id int64) {
		t.Helper()
		fresh, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		fresh.Description = userBio
		fresh.DescriptionSource = &models.ProfileSource{Provider: models.ProfileSourceUser}
		if ok, err := f.authors.UpdateIfUnchanged(f.ctx, fresh, fresh.UpdatedAtRaw); err != nil || !ok {
			t.Fatalf("user save: %v, %v", ok, err)
		}
	}
	keptUserBio := func(t *testing.T, f *relinkUpstreamFixture, id int64) {
		t.Helper()
		got, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Description != userBio || got.DescriptionSource == nil || got.DescriptionSource.Provider != models.ProfileSourceUser {
			t.Errorf("bio %q from %+v; the user's bio should survive with its credit", got.Description, got.DescriptionSource)
		}
	}

	t.Run("refresh", func(t *testing.T) {
		f, _ := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "OL1A", MetadataProvider: "openlibrary"})
		stale := *author
		userSaves(t, f, author.ID)
		f.handler.refreshAuthorProfile(f.ctx, &stale, false)
		keptUserBio(t, f, author.ID)
	})
	t.Run("relink upstream", func(t *testing.T) {
		f, p := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "abs:kari"})
		p.onGet = func() { userSaves(t, f, author.ID) }
		if rec := f.relink(t, author.ID); rec.Code != http.StatusConflict {
			t.Errorf("status %d: %s; want 409", rec.Code, rec.Body.String())
		}
		keptUserBio(t, f, author.ID)
	})
	t.Run("calibre relink", func(t *testing.T) {
		f, _ := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "calibre:author:1", MetadataProvider: "calibre"})
		stale := *author
		userSaves(t, f, author.ID)
		if err := f.handler.relinkCalibreAuthor(f.ctx, &stale); !errors.Is(err, errAuthorRowChanged) {
			t.Errorf("err %v, want errAuthorRowChanged", err)
		}
		keptUserBio(t, f, author.ID)
	})
}

// Adding an author whose name matches one already in the library relinks
// that author in place (createAuthorCore). A save landing between the read of
// that author and the relink's write is kept, and the add answers 409 with
// the reload message, not a 500 or a silent overwrite (#3287). The trigger
// plays the concurrent save: it fires on the identifier row the relink updates
// just before its guarded author write.
func TestCreateRelinkAnswersConflictWhenTheAuthorChanged(t *testing.T) {
	const name = "Kari Nordmann"
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authors := db.NewAuthorRepo(database)
	provider := &searchableAuthorProvider{
		stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: "OL1A"}}},
		authors:              map[string]*models.Author{"OL1A": {Name: name, ForeignID: "OL1A", Description: "From the provider."}},
	}
	h := NewAuthorHandler(authors, db.NewAuthorAliasRepo(database), db.NewBookRepo(database), nil, metadata.NewAggregator(provider), nil, db.NewMetadataProfileRepo(database), nil)
	existing := &models.Author{Name: name, SortName: "Nordmann, Kari", ForeignID: "abs:kari"}
	if err := authors.Create(t.Context(), existing); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER user_saves_during_relink AFTER UPDATE ON author_identifiers
		WHEN NEW.foreign_id = 'abs:kari'
		BEGIN UPDATE authors SET description = 'Written by hand.', updated_at = '2099-01-01T00:00:00Z' WHERE id = NEW.author_id; END`); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"foreignAuthorId": "OL1A", "authorName": name, "monitored": true})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errAuthorChanged) {
		t.Fatalf("status %d: %s; want 409 with the reload message", rec.Code, rec.Body.String())
	}
	got, err := authors.GetByID(t.Context(), existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "Written by hand." || got.ForeignID != "abs:kari" {
		t.Errorf("author now %q with bio %q; the save made during the add should stand", got.ForeignID, got.Description)
	}
}
