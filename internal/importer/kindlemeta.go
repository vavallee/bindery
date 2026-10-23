package importer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/encoding/charmap"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// Kindle files (MOBI, AZW, AZW3) keep their metadata in the EXTH block of the
// first record of a Palm database (#3016). The layout read here:
//
//	PalmDB header   78 bytes: name[32] ... type+creator "BOOKMOBI" at 60,
//	                record count (uint16) at 76, then 8 bytes per record
//	                whose first 4 are the record's file offset.
//	record 0        PalmDOC header (16 bytes), then the MOBI header:
//	                "MOBI" at +16, its length at +20, text encoding at +28
//	                (1252 or 65001), full name offset/length at +84/+88
//	                (relative to record 0), EXTH flag 0x40 at +128.
//	EXTH            right after the MOBI header: "EXTH", block length,
//	                record count, then type/length/value records whose
//	                length includes their 8 byte header.
//
// AZW3 (KF8) files carry the same record 0 and EXTH block, so one reader
// covers all three. Nothing here is compressed.

const (
	exthAuthor   = 100
	exthISBN     = 104
	exthTitle    = 503
	exthLanguage = 524
	// exthCoverOffset is the cover image's place among the image records,
	// counted from the first image index in the MOBI header.
	exthCoverOffset = 201

	// maxKindleRecord0 caps the bytes read for record 0: the headers and EXTH
	// block are a few kilobytes, and a cap keeps a corrupt offset from making
	// the reader allocate the whole file.
	maxKindleRecord0 = 1 << 20
)

// IsKindleFile reports whether path is a Kindle ebook we can read embedded
// metadata from.
func IsKindleFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mobi", ".azw", ".azw3":
		return true
	}
	return false
}

// HasEmbeddedMetadata reports whether ReadEbookMetadata can read path: an EPUB
// or a Kindle file.
func HasEmbeddedMetadata(path string) bool {
	return IsEpubFile(path) || IsKindleFile(path)
}

// ReadEbookMetadata reads the embedded title, author, ISBN and language of an
// EPUB or a Kindle file. Like ReadEpubMetadata it is best effort: any error
// sends the caller back to filename parsing.
func ReadEbookMetadata(path string) (EpubMetadata, error) {
	if IsKindleFile(path) {
		return ReadKindleMetadata(path)
	}
	return ReadEpubMetadata(path)
}

// ReadKindleMetadata reads a MOBI, AZW or AZW3 file's EXTH metadata into the
// same struct ReadEpubMetadata fills, so every caller works unchanged: the
// title from EXTH 503 (else the MOBI full name, else the database name), the
// first author (EXTH 100), an ISBN (EXTH 104) and the languages (EXTH 524).
func ReadKindleMetadata(path string) (EpubMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("kindle: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	db, err := readKindleDB(f)
	if err != nil {
		return EpubMetadata{}, err
	}
	meta, err := parseMobiRecord0(db.rec0)
	if err != nil {
		return EpubMetadata{}, err
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(string(bytes.TrimRight(db.header[0:32], "\x00")))
	}
	return meta, nil
}

// kindleDB is an open Kindle file's Palm database header and record 0.
type kindleDB struct {
	f       *os.File
	size    int64
	header  []byte
	records uint64
	rec0    []byte
}

// readKindleDB checks f is a Kindle book and reads its header and record 0,
// capped at maxKindleRecord0.
func readKindleDB(f *os.File) (*kindleDB, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("kindle: stat: %w", err)
	}
	db := &kindleDB{f: f, size: info.Size(), header: make([]byte, 78)}
	if _, err := f.ReadAt(db.header, 0); err != nil {
		return nil, errors.New("kindle: file too short for a Palm database header")
	}
	if !bytes.Equal(db.header[60:68], []byte("BOOKMOBI")) {
		return nil, errors.New("kindle: not a MOBI file")
	}
	db.records = uint64(binary.BigEndian.Uint16(db.header[76:78]))
	if db.records == 0 {
		return nil, errors.New("kindle: no records")
	}
	start, end, ok := db.recordRange(0)
	if !ok {
		return nil, errors.New("kindle: record 0 outside the file")
	}
	end = min(end, start+maxKindleRecord0)
	db.rec0 = make([]byte, end-start)
	if n, err := f.ReadAt(db.rec0, start); err != nil && int64(n) != end-start {
		return nil, fmt.Errorf("kindle: read record 0: %w", err)
	}
	return db, nil
}

// recordRange returns the file offsets record i spans: from its entry in the
// record list to the next record's start, or the end of the file for the last
// one. ok is false for a record that doesn't exist or lies outside the file.
func (db *kindleDB) recordRange(i uint64) (start, end int64, ok bool) {
	if i >= db.records {
		return 0, 0, false
	}
	entries := make([]byte, 16)
	n, _ := db.f.ReadAt(entries, int64(78+8*i)) //nolint:gosec // i < db.records, a uint16 count, checked above
	if n < 4 {
		return 0, 0, false
	}
	start = int64(binary.BigEndian.Uint32(entries[0:4]))
	end = db.size
	if n >= 12 && i+1 < db.records {
		if next := int64(binary.BigEndian.Uint32(entries[8:12])); next > start && next < end {
			end = next
		}
	}
	if start <= 0 || start >= db.size {
		return 0, 0, false
	}
	return start, end, true
}

// kindleCover returns the cover image of a MOBI, AZW or AZW3 file, or nil.
// The MOBI header gives the record holding the book's first image (at 0x6C of
// record 0) and EXTH 201 the cover's place counted from it; that record is the
// raw JPEG, PNG or GIF. A file with no EXTH 201 gets no cover: the first image
// is often a map or a publisher logo, and a cover is only ever filled, never
// guessed. Opened like every cover source, never through a link (#2961).
func kindleCover(filePath string) []byte {
	f, err := openRegular(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	db, err := readKindleDB(f)
	if err != nil {
		return nil
	}
	idx, ok := mobiCoverRecord(db.rec0)
	if !ok {
		return nil
	}
	start, end, ok := db.recordRange(idx)
	if !ok || end-start > covers.MaxBytes {
		return nil
	}
	img := make([]byte, end-start)
	if n, err := f.ReadAt(img, start); err != nil && int64(n) != end-start {
		return nil
	}
	return img
}

// noKindleIndex is how a MOBI header and EXTH 201 say "none".
const noKindleIndex = 0xFFFFFFFF

// mobiCoverRecord returns the record number of the cover image, from the first
// image index in the MOBI header and the offset in EXTH 201. Both are uint32s
// added as uint64s, so the sum can't wrap on a 32-bit build.
func mobiCoverRecord(rec []byte) (uint64, bool) {
	if len(rec) < 112 || !bytes.Equal(rec[16:20], []byte("MOBI")) {
		return 0, false
	}
	first := uint64(binary.BigEndian.Uint32(rec[108:112]))
	offset := uint64(noKindleIndex)
	walkEXTH(rec, func(typ uint32, val []byte) {
		if typ == exthCoverOffset && len(val) == 4 && offset == noKindleIndex {
			offset = uint64(binary.BigEndian.Uint32(val))
		}
	})
	if first == noKindleIndex || offset == noKindleIndex {
		return 0, false
	}
	return first + offset, true
}

// parseMobiRecord0 reads the MOBI header and EXTH block of record 0.
//
// Every offset and length is a uint32 from the file, and is checked against
// len(rec) as a uint64 before it becomes an int: on a 32-bit build (armv6,
// armv7) a value of 0x80000000 or more converted straight to int is negative
// and would pass a "<= len" check, then panic on the slice.
func parseMobiRecord0(rec []byte) (EpubMetadata, error) {
	var meta EpubMetadata
	if len(rec) < 32 || !bytes.Equal(rec[16:20], []byte("MOBI")) {
		return meta, errors.New("kindle: record 0 has no MOBI header")
	}
	recLen := uint64(len(rec))
	u32 := func(at uint64) uint64 { return uint64(binary.BigEndian.Uint32(rec[at : at+4])) }
	text := decoderFor(binary.BigEndian.Uint32(rec[28:32]))

	if len(rec) >= 92 {
		off, n := u32(84), u32(88)
		if off > 0 && n > 0 && off+n <= recLen {
			meta.Title = text(rec[off : off+n])
		}
	}

	var isbn10 string
	walkEXTH(rec, func(typ uint32, raw []byte) {
		val := text(raw)
		if val == "" {
			return
		}
		switch typ {
		case exthTitle:
			meta.Title = val
		case exthAuthor:
			if meta.Author == "" {
				meta.Author = val
			}
		case exthISBN:
			i13, i10 := extractISBN(val)
			if i13 != "" && meta.ISBN == "" {
				meta.ISBN = i13
			}
			if i10 != "" && isbn10 == "" {
				isbn10 = i10
			}
		case exthLanguage:
			if code := models.NormalizeLanguageCode(val); code != "" && !slices.Contains(meta.Languages, code) {
				meta.Languages = append(meta.Languages, code)
			}
		}
	})
	if meta.ISBN == "" {
		meta.ISBN = isbn10
	}
	if len(meta.Languages) > 0 {
		meta.Language = meta.Languages[0]
	}
	return meta, nil
}

// walkEXTH calls fn with the type and raw value of each record in record 0's
// EXTH block, in order. It reads nothing when the header doesn't flag one, and
// stops at the first record that doesn't fit.
func walkEXTH(rec []byte, fn func(typ uint32, val []byte)) {
	recLen := uint64(len(rec))
	u32 := func(at uint64) uint64 { return uint64(binary.BigEndian.Uint32(rec[at : at+4])) }
	if len(rec) < 24 {
		return
	}
	exthAt := 16 + u32(20)
	hasEXTH := len(rec) >= 132 && u32(128)&0x40 != 0
	if !hasEXTH || exthAt+12 > recLen || !bytes.Equal(rec[exthAt:exthAt+4], []byte("EXTH")) {
		return
	}
	count := u32(exthAt + 8)
	pos := exthAt + 12
	for i := uint64(0); i < count && pos+8 <= recLen; i++ {
		typ := binary.BigEndian.Uint32(rec[pos : pos+4])
		length := u32(pos + 4)
		// Each record's length counts its own 8 byte header, so anything
		// shorter is corrupt, and would never move pos forward.
		if length < 8 || pos+length > recLen {
			return
		}
		fn(typ, rec[pos+8:pos+length])
		pos += length
	}
}

// decoderFor returns the text decoder for a MOBI text encoding: 65001 is
// UTF-8, and 1252 (and anything unrecognised, which older files use) is
// Windows-1252.
func decoderFor(encoding uint32) func([]byte) string {
	if encoding == 65001 {
		return func(b []byte) string { return strings.TrimSpace(strings.ToValidUTF8(string(b), "")) }
	}
	// Windows-1252 maps every byte, so decoding cannot fail.
	dec := charmap.Windows1252.NewDecoder()
	return func(b []byte) string {
		out, _ := dec.Bytes(b)
		return strings.TrimSpace(string(out))
	}
}
