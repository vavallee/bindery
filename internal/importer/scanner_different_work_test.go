package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// albumID3 is an ID3v2.3 block with the three tags an audiobook rip of a Dutch
// Harry Potter volume carries in #3388: the album names the book, the artist
// is the narrator, the composer is the author, and there is no title tag.
func albumID3(album, artist, composer string) []byte {
	text := func(id, s string) []byte {
		body := append([]byte{0x03}, []byte(s)...)
		return append(frameHeaderV23(id, len(body)), body...)
	}
	var frames []byte
	frames = append(frames, text("TALB", album)...)
	frames = append(frames, text("TPE1", artist)...)
	frames = append(frames, text("TCOM", composer)...)
	header := append([]byte("ID3"), 0x03, 0x00, 0x00)
	header = append(header, synchsafe(uint32(len(frames)))...)
	return append(header, frames...)
}

func writeAlbumTrack(t *testing.T, path, album string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, albumID3(album, "Jan Meng", "J.K. Rowling"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// potterEnv is the catalogue of #3388: book 4606 tracks the parent audiobook
// folder "Harry Potter (series) 1-4 (1999)", Orde van de Feniks (3328) and
// the Cursed Child (2634) accept audio, and the Dutch Deathly Hallows (3673) is
// excluded, so it is not a candidate at all.
type potterEnv struct {
	*volumeScanEnv
	series, phoenix, cursed, hallows *models.Book
	parent                           string
}

func newPotterEnv(t *testing.T) *potterEnv {
	t.Helper()
	e := &potterEnv{volumeScanEnv: newVolumeScanEnv(t, "J. K. Rowling")}
	e.parent = filepath.Join(e.abDir, "J. K. Rowling", "Harry Potter (series) 1-4 (1999)")
	writeAlbumTrack(t, filepath.Join(e.parent, "HP01 001 - Harry Potter En De Steen Der Wijzen.mp3"),
		"Harry Potter 01 - De Steen der Wijzen")
	e.series = &models.Book{ForeignID: "hc:4606", AuthorID: e.authorID, Title: "Harry Potter (series) 1-4",
		Status: models.BookStatusImported, MediaType: models.MediaTypeAudiobook}
	if err := e.books.Create(e.ctx, e.series); err != nil {
		t.Fatal(err)
	}
	if err := e.books.AddBookFile(e.ctx, e.series.ID, models.MediaTypeAudiobook, e.parent); err != nil {
		t.Fatal(err)
	}
	e.phoenix = e.wanted(t, "Harry Potter en de Orde van de Feniks", models.MediaTypeAudiobook)
	e.cursed = e.wanted(t, "Harry Potter and the Cursed Child", models.MediaTypeAudiobook)
	e.hallows = e.wanted(t, "Harry Potter en de Relieken van de Dood", models.MediaTypeEbook)
	if err := e.books.SetExcluded(e.ctx, e.hallows.ID, true); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *potterEnv) hallowsChapter(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(e.parent, "7. Harry Potter en de Relieken van de Dood")
	writeAlbumTrack(t, filepath.Join(dir, "HP07 001 - Harry Potter En De Relieken Van De Dood.mp3"),
		"Harry Potter 07 - De Relieken van de Dood")
	return dir
}

func audiobookPaths(t *testing.T, e *potterEnv, id int64) []string {
	t.Helper()
	files, err := e.books.ListFiles(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestScanLibrary_DoesNotReconcileOntoADifferentWork is #3388 as reported: the
// Deathly Hallows chapter under a tracked series folder shares "Harry Potter en
// de" and "van de" with the wanted Order of the Phoenix, so Jaro-Winkler put
// the two at 0.929. The folder's number is not in the Dutch catalogue title, so
// the volume veto had nothing to compare, and with the Deathly Hallows entry
// excluded nothing competed. The file must stay unmatched.
func TestScanLibrary_DoesNotReconcileOntoADifferentWork(t *testing.T) {
	e := newPotterEnv(t)
	e.hallowsChapter(t)

	e.s.ScanLibrary(e.ctx)

	if got := audiobookPaths(t, e, e.phoenix.ID); len(got) != 0 {
		t.Errorf("Orde van de Feniks was given %v; that is the Relieken van de Dood folder", got)
	}
	if got := audiobookPaths(t, e, e.cursed.ID); len(got) != 0 {
		t.Errorf("the Cursed Child was given %v", got)
	}
	if got := audiobookPaths(t, e, e.series.ID); len(got) != 1 || got[0] != e.parent {
		t.Errorf("the series book's own folder changed: %v", got)
	}
}

// TestScanLibrary_OwnFolderStillReconcilesBesideADifferentWork: the reporter's
// positive controls. A correctly tagged folder of each intended book reconciles
// to it, with the unrelated chapter present in the same scan.
func TestScanLibrary_OwnFolderStillReconcilesBesideADifferentWork(t *testing.T) {
	e := newPotterEnv(t)
	e.hallowsChapter(t)
	phoenixDir := filepath.Join(e.abDir, "J. K. Rowling", "Harry Potter en de Orde van de Feniks")
	writeAlbumTrack(t, filepath.Join(phoenixDir, "HP05 001 - Harry Potter En De Orde Van De Feniks.mp3"),
		"Harry Potter 05 - De Orde van de Feniks")
	cursedDir := filepath.Join(e.abDir, "J. K. Rowling", "Harry Potter and the Cursed Child")
	writeAlbumTrack(t, filepath.Join(cursedDir, "001 - Harry Potter And The Cursed Child.mp3"),
		"Harry Potter and the Cursed Child")

	e.s.ScanLibrary(e.ctx)

	if got := audiobookPaths(t, e, e.phoenix.ID); len(got) != 1 || got[0] != phoenixDir {
		t.Errorf("Orde van de Feniks files = %v, want its own folder %q", got, phoenixDir)
	}
	if got := audiobookPaths(t, e, e.cursed.ID); len(got) != 1 || got[0] != cursedDir {
		t.Errorf("Cursed Child files = %v, want its own folder %q", got, cursedDir)
	}
}
