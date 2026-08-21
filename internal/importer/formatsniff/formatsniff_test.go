package formatsniff

import (
	"os"
	"path/filepath"
	"testing"
)

// write creates a file with the given leading bytes, padded so signature
// offsets are reachable.
func write(t *testing.T, name string, header []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	buf := make([]byte, 600)
	copy(buf, header)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// palmMobi builds a Palm database header with BOOKMOBI at offset 0x3C, which is
// the shape of the file in the #1782 reproduction.
func palmMobi() []byte {
	b := make([]byte, 0x44)
	copy(b[0:], []byte("Bloodline"))
	copy(b[0x3C:], []byte("BOOKMOBI"))
	return b
}

func epubHeader() []byte {
	b := []byte("PK\x03\x04")
	b = append(b, make([]byte, 26)...)
	b = append(b, []byte("mimetypeapplication/epub+zip")...)
	return b
}

func TestDetectContent(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		header []byte
		want   string
	}{
		{"pdf", "book.pdf", []byte("%PDF-1.7\n"), FormatPDF},
		{"mobi at 0x3C", "book.mobi", palmMobi(), FormatMOBI},
		{"epub mimetype entry", "book.epub", epubHeader(), FormatEPUB},
		{"djvu", "book.djvu", []byte("AT&TFORM"), FormatDJVU},
		{"flac", "a.flac", []byte("fLaC"), FormatFLAC},
		{"ogg", "a.ogg", []byte("OggS"), FormatOGG},
		{"mp3 with id3", "a.mp3", []byte("ID3\x03\x00"), FormatMP3},
		{"mp3 bare frame sync", "a.mp3", []byte{0xFF, 0xFB, 0x90, 0x00}, FormatMP3},
		{"m4b brand", "a.m4b", append([]byte{0, 0, 0, 0x20}, []byte("ftypM4B ")...), FormatM4B},
		{"m4a brand", "a.m4a", append([]byte{0, 0, 0, 0x20}, []byte("ftypM4A ")...), FormatM4A},
		{"unrecognised", "a.bin", []byte("not a book at all"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectContent(write(t, tc.file, tc.header)); got != tc.want {
				t.Errorf("DetectContent = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetect_ExtensionlessMobi is the #1782 reproduction: the file landed with
// no extension, so detectDownloadFormat fell through to "ebook" and the
// disallowed MOBI imported anyway.
func TestDetect_ExtensionlessMobi(t *testing.T) {
	path := write(t, "Will Wight - Bloodline- Cradle, Book 9", palmMobi())
	if got := Detect(path); got != FormatMOBI {
		t.Fatalf("Detect = %q, want %q; an extensionless MOBI is the case this exists for", got, FormatMOBI)
	}
	if got := FromExtension(path); got != "" {
		t.Errorf("FromExtension = %q, want empty for a file with no extension", got)
	}
}

// TestDetect_ContentBeatsAMisleadingExtension: the extension is the thing that
// is wrong in the cases that matter, so it must not win.
func TestDetect_ContentBeatsAMisleadingExtension(t *testing.T) {
	path := write(t, "actually-a-mobi.epub", palmMobi())
	if got := Detect(path); got != FormatMOBI {
		t.Errorf("Detect = %q, want %q; the extension overrode the contents", got, FormatMOBI)
	}
}

// TestDetect_FallsBackToExtension covers formats with no usable signature.
func TestDetect_FallsBackToExtension(t *testing.T) {
	cases := map[string]string{
		"book.cbz":  FormatCBZ,
		"book.cbr":  FormatCBR,
		"book.txt":  FormatTXT,
		"book.fb2":  FormatFB2,
		"book.azw3": FormatAZW3,
		"book.azw":  "azw",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			// Contents deliberately meaningless, so only the extension can answer.
			if got := Detect(write(t, name, []byte("\x00\x01\x02\x03"))); got != want {
				t.Errorf("Detect(%s) = %q, want %q", name, got, want)
			}
		})
	}
}

// TestFromExtension_AudioGaps: .opus and .aac are recognised audio extensions
// in the importer but have no token of their own in the profile vocabulary, so
// they map onto the container token a profile can actually express.
func TestFromExtension_AudioGaps(t *testing.T) {
	if got := FromExtension("chapter.opus"); got != FormatOGG {
		t.Errorf("FromExtension(.opus) = %q, want %q", got, FormatOGG)
	}
	if got := FromExtension("chapter.aac"); got != FormatM4A {
		t.Errorf("FromExtension(.aac) = %q, want %q", got, FormatM4A)
	}
}

// TestDetect_UnknownIsEmptyNotAGuess. Callers treat "" as "cannot judge" and
// let the import through, mirroring QualityAllowed's own r.Format == "" branch.
// A wrong guess here would reject a legitimate book.
func TestDetect_UnknownIsEmptyNotAGuess(t *testing.T) {
	if got := Detect(write(t, "mystery", []byte("\x00\x00\x00\x00"))); got != "" {
		t.Errorf("Detect = %q, want empty for an unidentifiable file", got)
	}
	if got := Detect(filepath.Join(t.TempDir(), "does-not-exist.epub")); got != FormatEPUB {
		t.Errorf("Detect on a missing file = %q, want the extension answer %q", got, FormatEPUB)
	}
}

// TestDetect_BareZipIsNotClaimedAsEpub: a zip with no epub mimetype entry is
// not an EPUB, and saying so would let a disallowed format through under an
// allowed name.
func TestDetect_BareZipIsNotClaimedAsEpub(t *testing.T) {
	bare := append([]byte("PK\x03\x04"), make([]byte, 40)...)
	if got := DetectContent(write(t, "archive.zip", bare)); got != "" {
		t.Errorf("DetectContent = %q, want empty for a zip that is not an epub", got)
	}
}
