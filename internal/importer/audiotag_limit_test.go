package importer

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

// hostilePictureClaim is the data length a hostile embedded picture declares.
// The tag library allocates whatever is declared before reading, so a file of
// a few dozen bytes used to cost about 4 GiB per read.
const hostilePictureClaim = 0xFFFFFFF0

// audioAllocBudget is far above what reading the tiny fixtures below costs and
// far below a single allocation of hostilePictureClaim.
const audioAllocBudget = 64 << 20

// flacPicture builds a FLAC/Vorbis picture structure that declares dataLen
// bytes of image data and actually carries data.
func flacPicture(dataLen uint32, data []byte) []byte {
	var b bytes.Buffer
	be := func(v uint32) { _ = binary.Write(&b, binary.BigEndian, v) }
	be(3) // front cover
	be(uint32(len("image/png")))
	b.WriteString("image/png")
	be(0) // empty description
	be(1) // width
	be(1) // height
	be(8) // colour depth
	be(0) // colours used
	be(dataLen)
	b.Write(data)
	return b.Bytes()
}

// vorbisComment builds a Vorbis comment body (no framing bit, as FLAC stores
// it) from KEY=value strings.
func vorbisComment(comments ...string) []byte {
	var b bytes.Buffer
	le := func(v uint32) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(len("bindery-test")))
	b.WriteString("bindery-test")
	le(uint32(len(comments)))
	for _, c := range comments {
		le(uint32(len(c)))
		b.WriteString(c)
	}
	return b.Bytes()
}

type flacBlock struct {
	typ  byte
	body []byte
}

// flacFile builds "fLaC" followed by the given metadata blocks, marking the
// final one as last.
func flacFile(blocks ...flacBlock) []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	for i, blk := range blocks {
		h := blk.typ
		if i == len(blocks)-1 {
			h |= 0x80
		}
		n := len(blk.body)
		b.Write([]byte{h, byte(n >> 16), byte(n >> 8), byte(n)})
		b.Write(blk.body)
	}
	return b.Bytes()
}

// oggCRC is the Ogg page checksum (CRC-32, polynomial 0x04c11db7, no
// reflection). The tag library verifies it, so fixtures must carry a real one
// for the parser to reach the comment packet.
func oggCRC(p []byte) uint32 {
	var crc uint32
	for _, v := range p {
		crc ^= uint32(v) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// oggFile wraps one packet in a single Ogg page.
func oggFile(packet []byte) []byte {
	var lacing []byte
	n := len(packet)
	for n >= 255 {
		lacing = append(lacing, 255)
		n -= 255
	}
	lacing = append(lacing, byte(n))
	if len(lacing) > 255 {
		panic("test packet too large for one page")
	}
	var b bytes.Buffer
	b.WriteString("OggS")
	b.WriteByte(0)                                       // version
	b.WriteByte(0x02)                                    // beginning of stream
	_ = binary.Write(&b, binary.LittleEndian, uint64(0)) // granule
	_ = binary.Write(&b, binary.LittleEndian, uint32(1)) // serial
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // sequence
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // crc placeholder
	b.WriteByte(byte(len(lacing)))
	b.Write(lacing)
	b.Write(packet)
	page := b.Bytes()
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	return page
}

// readAudioTagsMeasured runs readAudioTagsFrom and reports heap bytes
// allocated while it ran.
func readAudioTagsMeasured(t *testing.T, file []byte) (AudioTags, uint64, error) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	tags, err := readAudioTagsFrom(bytes.NewReader(file))
	runtime.ReadMemStats(&after)
	return tags, after.TotalAlloc - before.TotalAlloc, err
}

func TestReadAudioTags_HostilePictureClaimsAreRejected(t *testing.T) {
	hostileB64 := "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(flacPicture(hostilePictureClaim, []byte("tiny")))
	cases := []struct {
		name string
		file []byte
	}{
		{"flac picture block", flacFile(
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!")},
			flacBlock{6, flacPicture(hostilePictureClaim, []byte("tiny"))},
		)},
		{"flac comment picture", flacFile(
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!", hostileB64)},
		)},
		// The library lowercases keys with strings.ToLower, which maps the
		// Kelvin sign to "k", so this spelling is still parsed as a picture.
		{"flac comment picture, kelvin sign key", flacFile(
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!", strings.Replace(hostileB64, "BLOCK", "BLOC\u212A", 1))},
		)},
		{"ogg vorbis comment picture", oggFile(append([]byte("\x03vorbis"), vorbisComment("TITLE=Guards! Guards!", hostileB64)...))},
		{"opus tags picture", oggFile(append([]byte("OpusTags"), vorbisComment("TITLE=Guards! Guards!", hostileB64)...))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, alloc, err := readAudioTagsMeasured(t, tc.file)
			if !errors.Is(err, errAudioTagTooLarge) {
				t.Errorf("expected errAudioTagTooLarge, got %v", err)
			}
			if alloc > audioAllocBudget {
				t.Errorf("allocated %d MiB reading a %d byte file, budget %d MiB", alloc>>20, len(tc.file), audioAllocBudget>>20)
			}
		})
	}
}

// TestReadAudioTags_HonestPicturesStillRead pins that ordinary files with
// embedded cover art keep their tags.
func TestReadAudioTags_HonestPicturesStillRead(t *testing.T) {
	art := []byte("\x89PNG not really")
	honestB64 := "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(flacPicture(uint32(len(art)), art))
	cases := []struct {
		name string
		file []byte
	}{
		{"flac picture block", flacFile(
			flacBlock{1, make([]byte, 64)}, // padding, skipped by length
			flacBlock{6, flacPicture(uint32(len(art)), art)},
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!", "ARTIST=Terry Pratchett")},
		)},
		{"flac comment picture", flacFile(
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!", "ARTIST=Terry Pratchett", honestB64)},
		)},
		{"ogg vorbis comment picture", oggFile(append([]byte("\x03vorbis"), vorbisComment("TITLE=Guards! Guards!", "ARTIST=Terry Pratchett", honestB64)...))},
		{"opus tags", oggFile(append([]byte("OpusTags"), vorbisComment("TITLE=Guards! Guards!", "ARTIST=Terry Pratchett")...))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tags, err := readAudioTagsFrom(bytes.NewReader(tc.file))
			if err != nil {
				t.Fatalf("readAudioTagsFrom: %v", err)
			}
			if tags.Title != "Guards! Guards!" || tags.Author != "Terry Pratchett" {
				t.Errorf("got title %q author %q", tags.Title, tags.Author)
			}
		})
	}
}

// sparseFile is an io.ReadSeeker over prefix followed by zero bytes up to
// size. It lets a test present a multi-gigabyte file without allocating or
// writing one.
type sparseFile struct {
	prefix []byte
	size   int64
	off    int64
}

func (f *sparseFile) Read(p []byte) (int, error) {
	if f.off >= f.size {
		return 0, io.EOF
	}
	if rest := f.size - f.off; int64(len(p)) > rest {
		p = p[:rest]
	}
	n := 0
	if f.off < int64(len(f.prefix)) {
		n = copy(p, f.prefix[f.off:])
	}
	clear(p[n:])
	f.off += int64(len(p))
	return len(p), nil
}

func (f *sparseFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.off
	case io.SeekEnd:
		offset += f.size
	}
	if offset < 0 {
		return 0, errors.New("negative seek")
	}
	f.off = offset
	return offset, nil
}

// TestReadAudioTags_PictureLongerThanItsBlock covers a large file whose
// picture block declares far more data than the block holds. Checking only
// against the bytes left in the file let these through: the library then
// allocated the declared length on 64-bit, and on 32-bit a length of 2 GiB or
// more went negative and panicked.
func TestReadAudioTags_PictureLongerThanItsBlock(t *testing.T) {
	const fileSize = 3 << 30
	for _, claim := range []uint32{256 << 20, 0x90000000} {
		prefix := flacFile(
			flacBlock{4, vorbisComment("TITLE=Guards! Guards!")},
			flacBlock{6, flacPicture(claim, []byte("tiny"))},
		)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := readAudioTagsFrom(&sparseFile{prefix: prefix, size: fileSize})
		runtime.ReadMemStats(&after)
		if !errors.Is(err, errAudioTagTooLarge) {
			t.Errorf("claim %d MiB: expected errAudioTagTooLarge, got %v", claim>>20, err)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > audioAllocBudget {
			t.Errorf("claim %d MiB: allocated %d MiB, budget %d MiB", claim>>20, alloc>>20, audioAllocBudget>>20)
		}
	}
}

// TestReadAudioTags_FLACBlockLengthIsEnforced pins that a Vorbis comment
// block is read within its own declared length, and that the walk stays
// aligned with the library, which resumes after the last comment rather than
// at the end of the declared block.
func TestReadAudioTags_FLACBlockLengthIsEnforced(t *testing.T) {
	header := func(typ byte, n int) []byte { return []byte{typ, byte(n >> 16), byte(n >> 8), byte(n)} }
	comment := vorbisComment("TITLE=Guards! Guards!")

	t.Run("picture hidden in comment block slack", func(t *testing.T) {
		pic := flacPicture(hostilePictureClaim, []byte("tiny"))
		hidden := append(header(0x80|6, len(pic)), pic...)
		var b bytes.Buffer
		b.WriteString("fLaC")
		b.Write(header(4, len(comment)+len(hidden)))
		b.Write(comment)
		b.Write(hidden)
		_, alloc, err := readAudioTagsMeasured(t, b.Bytes())
		if !errors.Is(err, errAudioTagTooLarge) {
			t.Errorf("expected errAudioTagTooLarge, got %v", err)
		}
		if alloc > audioAllocBudget {
			t.Errorf("allocated %d MiB, budget %d MiB", alloc>>20, audioAllocBudget>>20)
		}
	})

	t.Run("comment runs past its block", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("fLaC")
		b.Write(header(0x80|4, 8))
		b.Write(comment)
		if _, err := readAudioTagsFrom(bytes.NewReader(b.Bytes())); err == nil {
			t.Error("expected an error for a comment that overruns its block length")
		}
	})
}

// TestReadAudioTags_OggPacketIsCapped pins that the Ogg walk refuses a packet
// continued across pages past maxOggPacketBytes instead of buffering
// it, as the library would, for as long as the file goes on.
func TestReadAudioTags_OggPacketIsCapped(t *testing.T) {
	var b bytes.Buffer
	lacing := bytes.Repeat([]byte{255}, 255)
	body := make([]byte, 255*255)
	for i := 0; int64(i*len(body)) <= maxOggPacketBytes; i++ {
		b.WriteString("OggS")
		b.WriteByte(0)
		flags := byte(0x01) // continued
		if i == 0 {
			flags = 0x02 // beginning of stream
		}
		b.WriteByte(flags)
		_ = binary.Write(&b, binary.LittleEndian, uint64(0))
		_ = binary.Write(&b, binary.LittleEndian, uint32(1))
		_ = binary.Write(&b, binary.LittleEndian, uint32(i))
		_ = binary.Write(&b, binary.LittleEndian, uint32(0))
		b.WriteByte(byte(len(lacing)))
		b.Write(lacing)
		b.Write(body)
	}
	if _, err := readAudioTagsFrom(bytes.NewReader(b.Bytes())); !errors.Is(err, errAudioTagTooLarge) {
		t.Errorf("expected errAudioTagTooLarge, got %v", err)
	}
}
