package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type profileErrProvider struct {
	stubMetaProvider
	err error
}

func (p *profileErrProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return nil, p.err
}

// An explicit refresh stamps author freshness (#2595), including when the
// provider has nothing new: the profile was still checked, so the staggered
// background job must not fetch it again within the day.
func TestRefreshAuthorProfile_StampsFreshnessEvenWhenUnchanged(t *testing.T) {
	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL39307A", Name: "H. G. Wells", Description: "Same bio."}}
	f := newCoreFixture(t, provider)
	author := seedWells(t, f.authors)
	author.Description = "Same bio."
	if err := f.authors.Update(context.Background(), author); err != nil {
		t.Fatal(err)
	}
	if author.LastMetadataRefreshAt != nil {
		t.Fatalf("precondition: author already stamped %v", author.LastMetadataRefreshAt)
	}

	f.h.refreshAuthorProfile(context.Background(), author, false)

	got, err := f.authors.GetByID(context.Background(), author.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LastMetadataRefreshAt == nil || time.Since(*got.LastMetadataRefreshAt) > time.Minute {
		t.Errorf("explicit refresh did not stamp freshness: %v", got.LastMetadataRefreshAt)
	}
}

// A failed explicit refresh leaves the author stale so the staggered background job still
// picks it up.
func TestRefreshAuthorProfile_FailureDoesNotStamp(t *testing.T) {
	provider := &profileErrProvider{err: errors.Join(errors.New("boom"), metadata.ErrUnavailable)}
	f := newCoreFixture(t, provider)
	author := seedWells(t, f.authors)

	f.h.refreshAuthorProfile(context.Background(), author, false)

	got, err := f.authors.GetByID(context.Background(), author.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LastMetadataRefreshAt != nil {
		t.Errorf("failed refresh stamped freshness: %v", got.LastMetadataRefreshAt)
	}
}
