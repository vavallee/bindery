package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Upgrading a synthetic DNB author to a canonical identity fills its bio and
// photo from the provider, but never over a value the user set by hand
// (#3287), and a provider value that does replace an enriched one drops the
// enricher's credit with it.
func TestUpgradeSyntheticDNBRespectsTheUserMark(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	user := &models.ProfileSource{Provider: models.ProfileSourceUser}
	wiki := &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k"}

	for name, tc := range map[string]struct {
		desc, img          string
		descSrc, imgSrc    *models.ProfileSource
		wantDesc, wantImg  string
		wantUser, wantNone bool
	}{
		"the user's bio and photo stay": {"Mine.", "bindery-cover:mine", user, user, "Mine.", "bindery-cover:mine", true, false},
		"an enriched pair is replaced":  {"From Wikipedia.", "https://commons.example/k.jpg", wiki, wiki, "From the provider.", "https://provider.example/k.jpg", false, true},
		"a provider pair is replaced":   {"Old.", "https://old.example/k.jpg", nil, nil, "From the provider.", "https://provider.example/k.jpg", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			synthetic := "dnb:author:" + name
			a := &models.Author{ForeignID: synthetic, Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "dnb",
				Description: tc.desc, DescriptionSource: tc.descSrc, ImageURL: tc.img, ImageSource: tc.imgSrc}
			if err := repo.Create(ctx, a); err != nil {
				t.Fatal(err)
			}
			target := &models.Author{ForeignID: "dnb:gnd:" + name, Name: "Kari Nordmann", MetadataProvider: "dnb",
				Description: "From the provider.", ImageURL: "https://provider.example/k.jpg"}
			if err := repo.UpgradeSyntheticDNB(ctx, synthetic, target); err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetByID(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Description != tc.wantDesc || got.ImageURL != tc.wantImg {
				t.Errorf("got %q / %q, want %q / %q", got.Description, got.ImageURL, tc.wantDesc, tc.wantImg)
			}
			if tc.wantUser && (got.DescriptionSource == nil || got.DescriptionSource.Provider != models.ProfileSourceUser || got.ImageSource == nil || got.ImageSource.Provider != models.ProfileSourceUser) {
				t.Errorf("the user's mark was lost: %+v / %+v", got.DescriptionSource, got.ImageSource)
			}
			if tc.wantNone && (got.DescriptionSource != nil || got.ImageSource != nil) {
				t.Errorf("a replaced value kept a credit: %+v / %+v", got.DescriptionSource, got.ImageSource)
			}
		})
	}
}
