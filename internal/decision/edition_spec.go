package decision

import (
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// EditionAllowed rejects an audiobook release that states it is abridged when
// the quality profile's edition preference is require_unabridged (#1358). It
// is off by default: any other preference, or none, passes every release.
//
// Only a stated abridgement rejects. A release whose edition type is unknown
// passes, because a title that does not say "unabridged" is not evidence that
// the recording is cut. The marker is read by indexer.ParseNarration, which
// ignores the word inside the wanted book's own title.
type EditionAllowed struct {
	Profile *models.QualityProfile
	// MediaType is the media type searched; the rule applies to audiobooks.
	MediaType string
	// Title and Authors are the wanted book's title and author names.
	Title   string
	Authors []string
}

func (s EditionAllowed) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if s.Profile == nil || s.Profile.EditionPreference != models.EditionPrefRequireUnabridged {
		return true, ""
	}
	mt := s.MediaType
	if mt != models.MediaTypeEbook && mt != models.MediaTypeAudiobook {
		mt = r.MediaType
	}
	if mt != models.MediaTypeAudiobook {
		return true, ""
	}
	n := indexer.ParseNarration(r.Title, []string{s.Title}, s.Authors)
	if n.EditionType == models.EditionTypeAbridged {
		return false, "abridged edition, the quality profile requires unabridged"
	}
	return true, ""
}
