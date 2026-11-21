package importer

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Audiobook tags clearly for another book: warn imports for any other source
// (TestAudiobookContentCheck_WarnImportsAndAttachesTheFinding), and Soulseek
// refuses the same download in the default mode.
func TestSoulseekBlockMode_RefusesAudiobookTagsForAnotherBook(t *testing.T) {
	c := flaggedAudioCases[0]
	f := newAudioFixture(t, c.wanted, "Author Name")
	f.dl.Protocol = "soulseek"
	f.dl.MediaType = models.MediaTypeAudiobook
	f.dl.GUID = "slskd:fedcba9876543210fedcba9876543210"
	for _, a := range c.albums {
		f.writeAlbum(t, a.album, a.n)
	}
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want blocked", dl.Status, dl.ErrorMessage)
	}
	if !f.blocklisted(t) || len(f.placedAudiobookFiles(t)) != 0 {
		t.Errorf("blocklisted %v placed %v", f.blocklisted(t), f.placedAudiobookFiles(t))
	}
}
