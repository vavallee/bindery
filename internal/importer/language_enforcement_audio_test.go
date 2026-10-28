package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Fixtures for the audiobook language check (#3497) are built from bytes, each
// a few hundred bytes at most. The tags they carry:
//   id3Lang     ID3v2.3 TLAN frame (UTF-8 text), plus a TIT2 track title
//   id3TXXXLang ID3v2.3 TXXX user text frame with Description "LANGUAGE"
//   m4bLang     MP4 iTunes freeform atom ----:com.apple.iTunes:LANGUAGE
//   flacLang    FLAC Vorbis comment LANGUAGE=<value>
//   no language tag: a TIT2 only (id3Lang with an empty value)

func id3Lang(value string) []byte {
	textFrame := func(id, text string) []byte {
		body := append([]byte{0x03}, []byte(text)...)
		return append(frameHeaderV23(id, len(body)), body...)
	}
	frames := textFrame("TIT2", "Chapter")
	if value != "" {
		frames = append(frames, textFrame("TLAN", value)...)
	}
	header := append([]byte("ID3"), 0x03, 0x00, 0x00)
	header = append(header, synchsafe(uint32(len(frames)))...)
	return append(header, frames...)
}

func id3TXXXLang(value string) []byte {
	body := append([]byte{0x03}, []byte("LANGUAGE")...)
	body = append(body, 0x00)
	body = append(body, []byte(value)...)
	frame := append(frameHeaderV23("TXXX", len(body)), body...)
	header := append([]byte("ID3"), 0x03, 0x00, 0x00)
	header = append(header, synchsafe(uint32(len(frame)))...)
	return append(header, frame...)
}

func m4bLang(value string) []byte {
	return mp4File(mp4Freeform("com.apple.iTunes", "LANGUAGE", value))
}

func flacLang(value string) []byte {
	return flacFile(flacBlock{typ: 4, body: vorbisComment("TITLE=Chapter", "LANGUAGE="+value)})
}

func TestPickAudioLanguages_RealContainers(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want []string
	}{
		{"mp3 TLAN", id3Lang("spa"), []string{"spa"}},
		{"mp3 TLAN list", id3Lang("eng/spa"), []string{"eng", "spa"}},
		{"mp3 TXXX LANGUAGE", id3TXXXLang("Spanish"), []string{"Spanish"}},
		{"m4b freeform LANGUAGE", m4bLang("es-MX"), []string{"es-MX"}},
		{"flac LANGUAGE comment", flacLang("deu"), []string{"deu"}},
		{"mp3 without a language", id3Lang(""), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "x"+map[bool]string{true: ".m4b", false: ".mp3"}[strings.HasPrefix(tc.name, "m4b")])
			if strings.HasPrefix(tc.name, "flac") {
				p = filepath.Join(filepath.Dir(p), "x.flac")
			}
			if err := os.WriteFile(p, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadAudioTags(p)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Join(tc.want, "/"); got.Language != want {
				t.Errorf("Language = %q, want %q", got.Language, want)
			}
		})
	}
}

// audioLangFixture is the #2998 fixture with the book switched to an audiobook.
func newAudioLangFixture(t *testing.T, o langEnforceOpts) *langEnforceFixture {
	t.Helper()
	f := newLangEnforceFixture(t, o)
	f.scanner.audiobookDir = t.TempDir()
	f.dl.Title = "Dungeon.Crawler.Carl.Audiobook.M4B"
	f.book.MediaType = models.MediaTypeAudiobook
	if err := f.books.Update(context.Background(), f.book); err != nil {
		t.Fatal(err)
	}
	return f
}

// track writes track n with the given file contents.
func (f *langEnforceFixture) track(t *testing.T, n int, ext string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("track%02d.%s", n, ext)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *langEnforceFixture) assertBlockedForLanguage(t *testing.T, want string) {
	t.Helper()
	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	if !strings.Contains(dl.ErrorMessage, want) {
		t.Errorf("message %q does not name %s", dl.ErrorMessage, want)
	}
	if !f.blocked(t) {
		t.Error("release was not blocklisted")
	}
	if dl.ImportPath == "" {
		t.Error("import path not recorded for Match to book")
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want wanted", book.Status)
	}
	if n := len(f.bookFiles(t)); n != 0 {
		t.Errorf("%d files recorded for a rejected release", n)
	}
	if entries, _ := os.ReadDir(f.scanner.audiobookDir); len(entries) != 0 {
		t.Errorf("rejected audiobook was placed: %d entries", len(entries))
	}
}

func TestAudiobookLanguage_AllowedTagImports(t *testing.T) {
	for _, tag := range []string{"eng", "en", "English", "en-US", "en_GB"} {
		t.Run(tag, func(t *testing.T) {
			f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.track(t, 1, "m4b", m4bLang(tag))
			f.importDownload(t)
			f.assertImported(t)
		})
	}
}

func TestAudiobookLanguage_DisallowedTagBlocks(t *testing.T) {
	cases := []struct {
		name, ext string
		data      []byte
	}{
		{"ISO 639-2 spa in m4b", "m4b", m4bLang("spa")},
		{"ISO 639-1 es in m4b", "m4b", m4bLang("es")},
		{"name Spanish in m4b", "m4b", m4bLang("Spanish")},
		{"region es-MX in m4b", "m4b", m4bLang("es-MX")},
		{"mp3 TLAN spa", "mp3", id3Lang("spa")},
		{"mp3 TXXX LANGUAGE Spanish", "mp3", id3TXXXLang("Spanish")},
		{"flac LANGUAGE es", "flac", flacLang("es")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.track(t, 1, tc.ext, tc.data)
			f.importDownload(t)
			f.assertBlockedForLanguage(t, "Spanish")
		})
	}
}

func TestAudiobookLanguage_UntaggedAndUnknownImport(t *testing.T) {
	for name, data := range map[string][]byte{
		"no language tag": id3Lang(""),
		"und":             id3Lang("und"),
		"mul":             id3Lang("mul"),
		"garbage":         id3Lang("not a language at all"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.track(t, 1, "mp3", data)
			f.importDownload(t)
			f.assertImported(t)
		})
	}
	t.Run("unreadable file", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", []byte("not an audio file"))
		f.importDownload(t)
		f.assertImported(t)
	})
}

func TestAudiobookLanguage_MixedTracksJudgedByMajority(t *testing.T) {
	t.Run("one stray Spanish track among English does not block", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("eng"))
		f.track(t, 2, "mp3", id3Lang("spa"))
		f.track(t, 3, "mp3", id3Lang("eng"))
		f.importDownload(t)
		f.assertImported(t)
	})
	t.Run("one stray English track among Spanish blocks", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("spa"))
		f.track(t, 2, "mp3", id3Lang("eng"))
		f.track(t, 3, "mp3", id3Lang("spa"))
		f.importDownload(t)
		f.assertBlockedForLanguage(t, "Spanish")
	})
	t.Run("a tie does not block", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("spa"))
		f.track(t, 2, "mp3", id3Lang("eng"))
		f.importDownload(t)
		f.assertImported(t)
	})
	t.Run("untagged tracks do not outvote a tagged one", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("spa"))
		for i := 2; i <= 6; i++ {
			f.track(t, i, "mp3", id3Lang(""))
		}
		f.importDownload(t)
		// The only voter is Spanish: that is a majority of the votes cast.
		f.assertBlockedForLanguage(t, "Spanish")
	})
	t.Run("a bilingual track tagged eng/spa is allowed", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("spa/eng"))
		f.importDownload(t)
		f.assertImported(t)
	})
}

func TestAudiobookLanguage_MultiLanguageProfile(t *testing.T) {
	f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng,spa", bookLang: "eng"})
	f.track(t, 1, "m4b", m4bLang("Spanish"))
	f.importDownload(t)
	f.assertImported(t)

	g := newAudioLangFixture(t, langEnforceOpts{allowed: "eng,spa", bookLang: "eng"})
	g.track(t, 1, "m4b", m4bLang("de"))
	g.importDownload(t)
	g.assertBlockedForLanguage(t, "German")
	if !strings.Contains(mustReload(t, g).ErrorMessage, "Spanish") {
		t.Error("message does not list both allowed languages")
	}
}

func mustReload(t *testing.T, f *langEnforceFixture) *models.Download {
	dl, _ := f.reload(t)
	return dl
}

func TestAudiobookLanguage_AnyLanguageProfileDoesNotEnforce(t *testing.T) {
	f := newAudioLangFixture(t, langEnforceOpts{allowed: "", bookLang: "eng"})
	f.track(t, 1, "m4b", m4bLang("spa"))
	f.importDownload(t)
	f.assertImported(t)
}

func TestAudiobookLanguage_ManualImportNotRefused(t *testing.T) {
	f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.track(t, 1, "m4b", m4bLang("spa"))
	f.scanner.tryImportInternal(withManualImport(context.Background()), f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)
	f.assertImported(t)
}

func TestAudiobookLanguage_LockedBookLanguageIsAllowed(t *testing.T) {
	f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "spa", locked: true})
	f.track(t, 1, "m4b", m4bLang("es"))
	f.importDownload(t)
	f.assertImported(t)
}

// TestAudiobookLanguage_EbookCheckUnchanged: the ebook path still blocks a
// Swedish EPUB, so sharing the allowed set did not change it.
func TestAudiobookLanguage_EbookCheckUnchanged(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.writeEpub(t, "sv")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want blocked", dl.Status)
	}
}

func TestAudiobookLanguage_JunkTagsAbstain(t *testing.T) {
	for _, tag := range []string{"XXX", "unk", "zzz", "nul", "abc", "N/A", "None", "Unknown"} {
		t.Run(tag, func(t *testing.T) {
			f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.track(t, 1, "mp3", id3Lang(tag))
			f.importDownload(t)
			f.assertImported(t)
		})
	}
	t.Run("eng/xxx imports", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("eng/xxx"))
		f.importDownload(t)
		f.assertImported(t)
	})
	t.Run("a real disallowed code still blocks", func(t *testing.T) {
		f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
		f.track(t, 1, "mp3", id3Lang("spa"))
		f.importDownload(t)
		f.assertBlockedForLanguage(t, "Spanish")
	})
}

func TestAudiobookLanguage_ReadsAreBoundedAndSampled(t *testing.T) {
	// 120 tracks: the first 70 Spanish, the last 50 English. Reading all would
	// give 70 v 50 Spanish; the even sample of 50 still has Spanish ahead, and
	// must include the first and last track.
	var files []string
	dir := t.TempDir()
	const n = 120
	for i := 0; i < n; i++ {
		lang := "eng"
		if i < 70 {
			lang = "spa"
		}
		p := filepath.Join(dir, fmt.Sprintf("t%03d.mp3", i))
		if err := os.WriteFile(p, id3Lang(lang), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	var read []string
	orig := languageTagReader
	languageTagReader = func(p string) (AudioTags, error) {
		read = append(read, p)
		return orig(p)
	}
	t.Cleanup(func() { languageTagReader = orig })
	f := newAudioLangFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	author, err := f.scanner.authors.GetByID(context.Background(), f.book.AuthorID)
	if err != nil || author == nil {
		t.Fatalf("load author: %v", err)
	}
	lc := f.scanner.checkAudiobookLanguage(context.Background(), f.book, author, files)
	if !lc.reject {
		t.Fatal("a majority Spanish sample of a 120 track folder was not rejected")
	}
	if len(read) != 50 {
		t.Fatalf("read %d tracks, want exactly 50", len(read))
	}
	if read[0] != files[0] || read[len(read)-1] != files[n-1] {
		t.Errorf("sample did not span the folder: first %q last %q", read[0], read[len(read)-1])
	}
}
