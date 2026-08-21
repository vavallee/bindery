// Package formatsniff identifies a book file's real format from its contents.
//
// It exists because a quality profile's allowed-format list could only ever be
// applied to a release *name* (#1782). decision.QualityAllowed runs once, at
// grab time, and passes any release whose title carries no recognisable format
// token, which is common for Usenet posts titled "Author - Title (Year)". That
// fallback is deliberate: rejecting unparseable titles would turn the filter
// into a near-total grab blackout. But it means the case the filter admits it
// cannot judge is exactly the case that arrives unchecked.
//
// After the download there is a real file to look at, and extensions are not
// enough. The reproduction in #1782 landed as "Will Wight - Bloodline- Cradle,
// Book 9" with no extension at all, and was a MOBI, which the active profile
// disallowed. It imported anyway.
package formatsniff

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// headerLen is how much of the file is read for signature matching. The
// deepest signature checked is BOOKMOBI at offset 0x3C, so this is generous.
const headerLen = 512

// Ebook and audio format tokens, matching the vocabulary in
// indexer.formatTokens, which is what a quality profile's items are named
// after. Anything returned here must be a token QualityAllowed can compare.
const (
	FormatEPUB = "epub"
	FormatMOBI = "mobi"
	FormatAZW3 = "azw3"
	FormatPDF  = "pdf"
	FormatDJVU = "djvu"
	FormatCBZ  = "cbz"
	FormatCBR  = "cbr"
	FormatFB2  = "fb2"
	FormatTXT  = "txt"
	FormatRTF  = "rtf"
	FormatM4B  = "m4b"
	FormatM4A  = "m4a"
	FormatMP3  = "mp3"
	FormatFLAC = "flac"
	FormatOGG  = "ogg"
)

// Detect reports the format of the file at path.
//
// Content wins over the extension, because the extension is the thing that is
// missing or wrong in the cases this exists for. It falls back to the
// extension when the bytes are not conclusive, and returns "" when neither
// answers, which callers must treat as "cannot judge" rather than as a
// rejection.
func Detect(path string) string {
	if format := DetectContent(path); format != "" {
		return format
	}
	return FromExtension(path)
}

// DetectContent identifies a format from the file's leading bytes, or returns
// "" when no signature matches.
func DetectContent(path string) string {
	f, err := os.Open(path) // #nosec G304 -- path comes from the importer's own scan of the download directory
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck

	header := make([]byte, headerLen)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ""
	}
	return identify(header[:n], f)
}

// identify matches header against the known signatures. zipEntry is used only
// to tell an EPUB from another zip container and may be nil.
func identify(header []byte, zipEntry io.ReaderAt) string {
	switch {
	case bytes.HasPrefix(header, []byte("%PDF")):
		return FormatPDF

	// Palm database header. The type/creator pair sits at offset 0x3C: BOOKMOBI
	// for MOBI, TPZ for Topaz. This is the signature the #1782 repro carried.
	case len(header) >= 0x44 && bytes.Equal(header[0x3C:0x44], []byte("BOOKMOBI")):
		return FormatMOBI

	// AZW3 (KF8) is a Palm container too, but modern AZW3 files are TPZ3/zip
	// shaped; the reliable discriminator is the extension, handled by the
	// caller. Bare "TPZ" is Topaz, which Bindery has no token for.

	case bytes.HasPrefix(header, []byte("AT&TFORM")):
		return FormatDJVU

	case bytes.HasPrefix(header, []byte("fLaC")):
		return FormatFLAC

	case bytes.HasPrefix(header, []byte("OggS")):
		return FormatOGG

	case bytes.HasPrefix(header, []byte("ID3")):
		return FormatMP3

	// MPEG audio frame sync: 11 set bits. Covers an MP3 with no ID3 tag.
	case len(header) >= 2 && header[0] == 0xFF && header[1]&0xE0 == 0xE0:
		return FormatMP3

	// ISO base media container. The brand at offset 8 separates an audiobook
	// (M4B) from plain M4A; both are in the format vocabulary and a profile can
	// allow one without the other.
	case len(header) >= 12 && bytes.Equal(header[4:8], []byte("ftyp")):
		if bytes.Equal(header[8:12], []byte("M4B ")) {
			return FormatM4B
		}
		return FormatM4A

	case bytes.HasPrefix(header, []byte("PK\x03\x04")):
		return zipFormat(header, zipEntry)
	}
	return ""
}

// zipFormat distinguishes the zip-container formats. An EPUB is required by
// spec to store an uncompressed "mimetype" entry first, naming
// application/epub+zip, which shows up in the first few hundred bytes.
func zipFormat(header []byte, _ io.ReaderAt) string {
	if bytes.Contains(header, []byte("application/epub+zip")) {
		return FormatEPUB
	}
	if bytes.Contains(header, []byte("mimetype")) {
		// A mimetype entry that is not epub is some other OCF container; not a
		// format Bindery has a token for.
		return ""
	}
	// A bare zip is most often a CBZ in this context, but calling it one from
	// the container alone would be a guess. Let the extension decide.
	return ""
}

// FromExtension maps a file extension onto a format token, or returns "" when
// the extension is absent or not one Bindery names.
func FromExtension(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case FormatEPUB, FormatMOBI, FormatAZW3, FormatPDF, FormatDJVU,
		FormatCBZ, FormatCBR, FormatFB2, FormatTXT, FormatRTF,
		FormatM4B, FormatM4A, FormatMP3, FormatFLAC, FormatOGG:
		return ext
	case "azw":
		// AZW is Mobipocket in an Amazon wrapper. The profile vocabulary has
		// both, and the token is what the user ticked.
		return "azw"
	case "opus":
		// Opus is carried in an Ogg container and the profile vocabulary has no
		// separate token for it.
		return FormatOGG
	case "aac":
		// Raw AAC; closest profile token is m4a, the container it normally
		// arrives in.
		return FormatM4A
	}
	return ""
}
