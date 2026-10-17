package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorProfileSourcesRoundTrip covers #3287: the credit for an enriched
// photo and bio is stored with the author, read back by every author query,
// and cleared when the field goes back to a primary value.
func TestAuthorProfileSourcesRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	photo := &models.ProfileSource{Provider: "wikimedia-commons", URL: "https://commons.example/wiki/File:K.jpg",
		Credit: "Ola Nordmann", License: "CC BY-SA 4.0", LicenseURL: "https://creativecommons.org/licenses/by-sa/4.0/"}
	bio := &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/wiki/Kari_Nordmann", License: "CC BY-SA 4.0"}
	a := &models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann", ImageURL: "https://upload.example/k.jpg", ImageSource: photo,
		Description: "Kari Nordmann is a writer.", DescriptionSource: bio}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v, %v", got, err)
	}
	if got.ImageSource == nil || *got.ImageSource != *photo || got.DescriptionSource == nil || *got.DescriptionSource != *bio {
		t.Fatalf("credits did not round-trip: image %+v, description %+v", got.ImageSource, got.DescriptionSource)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 || list[0].ImageSource == nil {
		t.Fatalf("List lost the credit: %+v, %v", list, err)
	}

	got.ImageURL, got.ImageSource = "https://primary.example/k.jpg", nil
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := repo.GetByID(ctx, a.ID)
	if again.ImageSource != nil || again.DescriptionSource == nil {
		t.Errorf("after replacing the photo with a primary one: image credit %+v (want none), description credit %+v (want kept)", again.ImageSource, again.DescriptionSource)
	}

	// A malformed stored credit must not stop the author from loading.
	if _, err := database.ExecContext(ctx, `UPDATE authors SET description_source = '{not json' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	broken, err := repo.GetByID(ctx, a.ID)
	if err != nil || broken == nil || broken.DescriptionSource != nil {
		t.Errorf("malformed credit: author %v, credit %+v, err %v; want the author with no credit", broken != nil, broken.DescriptionSource, err)
	}

	// Valid JSON that names no source is no credit either.
	if _, err := database.ExecContext(ctx, `UPDATE authors SET description_source = '{"url":"https://example.invalid"}' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if nameless, err := repo.GetByID(ctx, a.ID); err != nil || nameless.DescriptionSource != nil {
		t.Errorf("credit with no provider: %+v, err %v; want no credit", nameless.DescriptionSource, err)
	}
}
