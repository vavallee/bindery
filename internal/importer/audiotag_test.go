package importer

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/dhowden/tag"
)

func TestIsAudioTagFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/lib/a.mp3", true},
		{"/lib/a.MP3", true},
		{"/lib/a.m4b", true},
		{"/lib/a.m4a", true},
		{"/lib/a.flac", true},
		{"/lib/a.ogg", true},
		{"/lib/a.opus", true},
		{"/lib/a.OPUS", true},
		{"/lib/a.epub", false},
		{"/lib/a.pdf", false},
		{"/lib/a", false},
	}
	for _, c := range cases {
		if got := IsAudioTagFile(c.path); got != c.want {
			t.Errorf("IsAudioTagFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsASIN(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"B01KNYJ0UG", true},
		{"B0CFJK1A2Z", true},
		{"A01KNYJ0UG", false}, // no leading B
		{"B01KNYJ0U", false},  // too short
		{"B01KNYJ0UGG", false},
		{"B01kNYJ0UG", false}, // lowercase
		{"B01-NYJ0UG", false}, // non-alphanumeric
		{"", false},
	}
	for _, c := range cases {
		if got := isASIN(c.in); got != c.want {
			t.Errorf("isASIN(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPickAudioASIN_MP4Freeform(t *testing.T) {
	// MP4 freeform atoms from com.apple.iTunes surface under the sub-atom
	// name directly.
	raw := map[string]any{"ASIN": "B01KNYJ0UG", "title": "Doesn't Matter"}
	if got := pickAudioASIN(raw); got != "B01KNYJ0UG" {
		t.Errorf("expected ASIN extracted from freeform atom, got %q", got)
	}
}

func TestPickAudioASIN_TXXX(t *testing.T) {
	// ID3v2 user-defined text frame — dhowden parses these into *tag.Comm
	// values keyed by frame name (with _N suffix when repeated).
	raw := map[string]any{
		"TPE1":   "Some Artist",
		"TXXX":   &tag.Comm{Description: "acoustid_id", Text: "irrelevant"},
		"TXXX_0": &tag.Comm{Description: "ASIN", Text: "B01KNYJ0UG"},
	}
	if got := pickAudioASIN(raw); got != "B01KNYJ0UG" {
		t.Errorf("expected ASIN from TXXX frame, got %q", got)
	}
}

func TestPickAudioASIN_RejectsNonASIN(t *testing.T) {
	// A TXXX frame that carries something called "ASIN" but whose text is
	// malformed (e.g. OpenLibrary work ID by mistake) must not be returned.
	raw := map[string]any{
		"TXXX": &tag.Comm{Description: "ASIN", Text: "OL1234567W"},
	}
	if got := pickAudioASIN(raw); got != "" {
		t.Errorf("expected empty ASIN for malformed value, got %q", got)
	}
}

func TestPickAudioASIN_NoMatch(t *testing.T) {
	if got := pickAudioASIN(nil); got != "" {
		t.Errorf("expected empty ASIN for nil map, got %q", got)
	}
	if got := pickAudioASIN(map[string]any{"TPE1": "Artist"}); got != "" {
		t.Errorf("expected empty ASIN when no ASIN tags present, got %q", got)
	}
}

// fakeMetadata is the subset of tag.Metadata used by pickAudioAuthor.
// The remaining methods are stubbed so the struct satisfies the interface.
type fakeMetadata struct {
	artist      string
	albumArtist string
	composer    string
}

func (f fakeMetadata) Format() tag.Format     { return tag.ID3v2_4 }
func (f fakeMetadata) FileType() tag.FileType { return tag.MP3 }
func (f fakeMetadata) Title() string          { return "" }
func (f fakeMetadata) Album() string          { return "" }
func (f fakeMetadata) Artist() string         { return f.artist }
func (f fakeMetadata) AlbumArtist() string    { return f.albumArtist }
func (f fakeMetadata) Composer() string       { return f.composer }
func (f fakeMetadata) Year() int              { return 0 }
func (f fakeMetadata) Genre() string          { return "" }
func (f fakeMetadata) Track() (int, int)      { return 0, 0 }
func (f fakeMetadata) Disc() (int, int)       { return 0, 0 }
func (f fakeMetadata) Picture() *tag.Picture  { return nil }
func (f fakeMetadata) Lyrics() string         { return "" }
func (f fakeMetadata) Comment() string        { return "" }
func (f fakeMetadata) Raw() map[string]any    { return nil }

func TestPickAudioAuthor_PrefersArtist(t *testing.T) {
	got := pickAudioAuthor(fakeMetadata{artist: "Brandon Sanderson", albumArtist: "Narrator"})
	if got != "Brandon Sanderson" {
		t.Errorf("expected Artist to win, got %q", got)
	}
}

func TestPickAudioAuthor_FallsBackToAlbumArtist(t *testing.T) {
	got := pickAudioAuthor(fakeMetadata{albumArtist: "Stephen Fry", composer: "Wodehouse"})
	if got != "Stephen Fry" {
		t.Errorf("expected AlbumArtist fallback, got %q", got)
	}
}

func TestPickAudioAuthor_FallsBackToComposer(t *testing.T) {
	got := pickAudioAuthor(fakeMetadata{composer: "  P.G. Wodehouse  "})
	if got != "P.G. Wodehouse" {
		t.Errorf("expected composer fallback (trimmed), got %q", got)
	}
}

func TestPickAudioAuthor_Empty(t *testing.T) {
	if got := pickAudioAuthor(fakeMetadata{}); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestPickAudioAuthor_SkipsNarratorCredit(t *testing.T) {
	// Artist holds a narrator credit; the real author is in AlbumArtist.
	got := pickAudioAuthor(fakeMetadata{artist: "Read by Nigel Planer", albumArtist: "Terry Pratchett"})
	if got != "Terry Pratchett" {
		t.Errorf("expected narrator credit to be skipped for AlbumArtist, got %q", got)
	}
}

func TestPickAudioAuthor_NarratorOnlyReturnsEmpty(t *testing.T) {
	// Only a narrator credit is present: return empty so the caller keeps the
	// folder-resolved author instead of a narrator name.
	if got := pickAudioAuthor(fakeMetadata{artist: "Narrated by Stephen Fry"}); got != "" {
		t.Errorf("expected empty when only a narrator credit is present, got %q", got)
	}
}

func TestIsNarratorCredit(t *testing.T) {
	credits := []string{"Read by Nigel Planer", "read by X", "Narrated by Stephen Fry", "Performed by A. B.", "Told by Someone"}
	for _, c := range credits {
		if !isNarratorCredit(c) {
			t.Errorf("expected %q to be a narrator credit", c)
		}
	}
	authors := []string{"Terry Pratchett", "Reade Brothers", "Narrator", "Bradbury, Ray", "Reading Rainbow"}
	for _, a := range authors {
		if isNarratorCredit(a) {
			t.Errorf("expected %q to NOT be a narrator credit", a)
		}
	}
}

// buildID3v23 returns an ID3v2.3 header block containing TIT2 (title), TPE1
// (artist), and a TXXX ASIN frame. The returned bytes are a valid tag
// followed by no audio — tag.ReadFrom only parses the tag, so this is
// sufficient for round-tripping through the library.
func buildID3v23(title, artist, asin string) []byte {
	textFrame := func(id, text string) []byte {
		body := append([]byte{0x03}, []byte(text)...) // $03 = UTF-8
		return append(frameHeaderV23(id, len(body)), body...)
	}
	txxx := func(desc, val string) []byte {
		body := []byte{0x03}
		body = append(body, []byte(desc)...)
		body = append(body, 0x00)
		body = append(body, []byte(val)...)
		return append(frameHeaderV23("TXXX", len(body)), body...)
	}

	var frames []byte
	if title != "" {
		frames = append(frames, textFrame("TIT2", title)...)
	}
	if artist != "" {
		frames = append(frames, textFrame("TPE1", artist)...)
	}
	if asin != "" {
		frames = append(frames, txxx("ASIN", asin)...)
	}

	header := []byte("ID3")
	header = append(header, 0x03, 0x00, 0x00) // v2.3, flags 0
	header = append(header, synchsafe(uint32(len(frames)))...)
	return append(header, frames...)
}

func frameHeaderV23(id string, bodySize int) []byte {
	h := []byte(id)
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(bodySize))
	h = append(h, sz[:]...)
	h = append(h, 0x00, 0x00) // flags
	return h
}

func synchsafe(n uint32) []byte {
	return []byte{
		byte((n >> 21) & 0x7F),
		byte((n >> 14) & 0x7F),
		byte((n >> 7) & 0x7F),
		byte(n & 0x7F),
	}
}

func TestReadAudioTagsFrom_ID3v23(t *testing.T) {
	data := buildID3v23("The Way of Kings", "Brandon Sanderson", "B003P2WO5E")
	tags, err := readAudioTagsFrom(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("readAudioTagsFrom: %v", err)
	}
	if tags.Title != "The Way of Kings" {
		t.Errorf("Title = %q, want %q", tags.Title, "The Way of Kings")
	}
	if tags.Author != "Brandon Sanderson" {
		t.Errorf("Author = %q, want %q", tags.Author, "Brandon Sanderson")
	}
	if tags.ASIN != "B003P2WO5E" {
		t.Errorf("ASIN = %q, want %q", tags.ASIN, "B003P2WO5E")
	}
}

func TestReadAudioTagsFrom_MalformedIsError(t *testing.T) {
	// Random bytes with no tag signature — tag.ReadFrom should fail and
	// ReadAudioTags must propagate the error so callers fall back to
	// filename parsing.
	if _, err := readAudioTagsFrom(bytes.NewReader([]byte("not-an-audio-file"))); err == nil {
		t.Error("expected error for malformed audio data, got nil")
	}
}
