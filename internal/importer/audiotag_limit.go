package importer

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// errAudioTagTooLarge reports embedded tag data that declares more bytes than
// it can hold, or more than any real tag needs.
//
// github.com/dhowden/tag reads a FLAC picture block, and the
// METADATA_BLOCK_PICTURE comment that FLAC, Ogg Vorbis and Opus files carry,
// by allocating the declared data length up front. That length is a 32-bit
// field the file controls, so a file of a few dozen bytes made every tag read
// allocate up to 4 GiB, and on the 32-bit ARM builds a length of 2 GiB or more
// went negative and panicked. The library has no option to skip pictures,
// which Bindery never uses, so checkAudioTagClaims walks the same structures
// first and refuses the file when a declared length overruns its container or
// maxAudioTagStructureBytes. The tag read then fails like any other unreadable
// file and the scan falls back to the filename. ID3v2 and MP4 tags have a
// related problem, covered in audiotag_limit_seek.go.
var errAudioTagTooLarge = errors.New("audio tags: embedded metadata declares more data than it holds")

// maxAudioTagStructureBytes bounds any single structure the library may be
// asked to hold: a picture, an ID3v2 frame, an MP4 atom. A FLAC metadata block
// length is a 24-bit field, so no real FLAC block, and therefore no FLAC
// picture, can be larger.
//
// Ogg has no such field. There a picture travels base64 encoded in a
// METADATA_BLOCK_PICTURE comment, a third larger than the picture itself, so
// the Ogg limit is set on the decoded picture instead: an Ogg picture may be
// as large as a FLAC picture block, no larger. maxVorbisCommentBytes and
// maxOggPacketBytes are the hard caps on the encoded text that carries it.
const maxAudioTagStructureBytes = 1<<24 - 1

// maxVorbisCommentBytes is the longest single Vorbis comment the check reads:
// the picture key plus a maxAudioTagStructureBytes picture in base64.
const maxVorbisCommentBytes = int64(len("METADATA_BLOCK_PICTURE=")) + (maxAudioTagStructureBytes+2)/3*4

// maxOggPacketBytes bounds the Ogg packet data held while packets complete:
// one maximal picture comment with room left for the rest of the comments.
const maxOggPacketBytes = 24 << 20

// checkAudioTagClaims returns an error if r's embedded tags declare lengths
// the library would allocate but the file cannot back, or that are larger
// than any real tag needs. It dispatches on the leading bytes exactly as
// tag.ReadFrom does (FLAC, Ogg, MP4, ID3v2, DSF) and leaves r positioned where
// it found it. Files the library reads only as ID3v1 pass through: that tag is
// a fixed 128 bytes.
//
// It fails closed: any fault in the walk is returned, so the library never
// runs on a file this check could not follow to its end. The walk reads the
// same bytes in the same order as the library, so a file the library can read
// is one this check can follow, apart from the extra limits it applies on
// purpose.
func checkAudioTagClaims(r io.ReadSeeker) error {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	defer func() { _, _ = r.Seek(start, io.SeekStart) }()

	// tag.ReadFrom reads these 11 bytes first and fails without them.
	var magic [11]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	cr := func() *claimReader { return &claimReader{r: bufio.NewReader(r), remaining: end - start} }
	w := &seekWalker{r: r, pos: start, end: end}
	switch {
	case string(magic[:4]) == "fLaC":
		return checkFLACClaims(cr())
	case string(magic[:4]) == "OggS":
		return checkOggClaims(cr())
	case string(magic[4:8]) == "ftyp":
		return checkMP4Claims(w)
	case string(magic[:3]) == "ID3":
		return checkID3v2Claims(w)
	case string(magic[:4]) == "DSD ":
		return checkDSFClaims(w)
	}
	return nil
}

// claimReader reads a stream while tracking how many bytes are left in it, so
// a declared length can be compared against what actually exists.
type claimReader struct {
	r         *bufio.Reader
	remaining int64
}

var errClaimTruncated = errors.New("audio tags: structure runs past end of data")

// bytes returns the next n bytes. It refuses before allocating when n is more
// than is left or more than maxAudioTagStructureBytes, so the check itself
// cannot be made to allocate much.
func (c *claimReader) bytes(n int64) ([]byte, error) {
	return c.bytesUpTo(n, maxAudioTagStructureBytes)
}

// bytesUpTo is bytes with the caller's limit in place of
// maxAudioTagStructureBytes.
func (c *claimReader) bytesUpTo(n, limit int64) ([]byte, error) {
	if n > limit {
		return nil, fmt.Errorf("%w (%d bytes declared)", errAudioTagTooLarge, n)
	}
	if n < 0 || n > c.remaining {
		return nil, errClaimTruncated
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c.r, b); err != nil {
		return nil, err
	}
	c.remaining -= n
	return b, nil
}

func (c *claimReader) skip(n int64) error {
	if n < 0 || n > c.remaining {
		return errClaimTruncated
	}
	// io.CopyN keeps the count in int64; bufio.Reader.Discard takes an int,
	// which a 32-bit build would turn negative for lengths of 2 GiB and up.
	if _, err := io.CopyN(io.Discard, c.r, n); err != nil {
		return err
	}
	c.remaining -= n
	return nil
}

// within runs fn with the reader limited to the next n bytes (or what is
// left, if less), then restores the outer limit minus what fn consumed. The
// position afterwards is wherever fn stopped, not n bytes on, because that is
// where the library resumes.
func (c *claimReader) within(n int64, fn func() error) error {
	outer := c.remaining
	if n < c.remaining {
		c.remaining = n
	}
	inner := c.remaining
	err := fn()
	c.remaining = outer - (inner - c.remaining)
	return err
}

func (c *claimReader) uint32BE() (int64, error) {
	b, err := c.bytes(4)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint32(b)), nil
}

func (c *claimReader) uint32LE() (int64, error) {
	b, err := c.bytes(4)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint32(b)), nil
}

// checkFLACClaims follows tag.ReadFLACTags: after "fLaC", metadata blocks of
// a one-byte type (high bit marks the last) and a 24-bit length. The library
// parses comment and picture blocks from the stream without honouring that
// length and seeks over every other block by it. This walk holds each comment
// and picture to its declared block length, which the library does not, but
// resumes where the library will, after the last byte the block's contents
// used, so the two never disagree about where the next block starts.
func checkFLACClaims(c *claimReader) error {
	if err := c.skip(4); err != nil {
		return err
	}
	for {
		hdr, err := c.bytes(4)
		if err != nil {
			return err
		}
		last := hdr[0]&0x80 != 0
		blockLen := int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		switch hdr[0] &^ 0x80 {
		case 4: // VORBIS_COMMENT
			err = c.within(blockLen, func() error { return checkVorbisCommentClaims(c) })
		case 6: // PICTURE
			err = c.within(blockLen, func() error { return checkPictureClaim(c) })
		default:
			err = c.skip(blockLen)
		}
		if err != nil || last {
			return err
		}
	}
}

// checkPictureClaim follows the library's readPictureBlock: picture type,
// MIME type, description, four 32-bit image fields, then the data length and
// the data. It consumes the picture so a following block stays aligned.
func checkPictureClaim(c *claimReader) error {
	if err := c.skip(4); err != nil { // picture type
		return err
	}
	for range 2 { // MIME type, then description
		n, err := c.uint32BE()
		if err != nil {
			return err
		}
		if err := c.skip(n); err != nil {
			return err
		}
	}
	if err := c.skip(16); err != nil { // width, height, depth, colours
		return err
	}
	dataLen, err := c.uint32BE()
	if err != nil {
		return err
	}
	if dataLen > c.remaining || dataLen > maxAudioTagStructureBytes {
		return fmt.Errorf("%w (picture declares %d bytes, %d available)", errAudioTagTooLarge, dataLen, c.remaining)
	}
	return c.skip(dataLen)
}

// checkVorbisCommentClaims follows the library's readVorbisComment:
// vendor string, then a count of length-prefixed KEY=value comments. Every
// METADATA_BLOCK_PICTURE value is held to maxAudioTagStructureBytes decoded,
// the picture size a FLAC picture block allows, and every one that decodes is
// checked against its own decoded length, which is all the data that picture
// can hold. The library only ever parses the last one, so checking all of
// them is stricter, never looser.
func checkVorbisCommentClaims(c *claimReader) error {
	vendorLen, err := c.uint32LE()
	if err != nil {
		return err
	}
	if err := c.skip(vendorLen); err != nil {
		return err
	}
	count, err := c.uint32LE()
	if err != nil {
		return err
	}
	for range count {
		n, err := c.uint32LE()
		if err != nil {
			return err
		}
		// Read the whole comment (bounded by what is left in the block and
		// by maxVorbisCommentBytes) and match the key exactly as the
		// library does. A byte prefix compare would miss keys that only
		// lowercase to the picture key, such as one spelled with the Kelvin
		// sign.
		comment, err := c.bytesUpTo(n, maxVorbisCommentBytes)
		if err != nil {
			return err
		}
		key, value, ok := strings.Cut(string(comment), "=")
		if !ok || strings.ToLower(key) != "metadata_block_picture" {
			continue
		}
		if decoded := base64DecodedLen(value); decoded > maxAudioTagStructureBytes {
			return fmt.Errorf("%w (picture comment of %d bytes decoded)", errAudioTagTooLarge, decoded)
		}
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			// The library fails the whole read on an undecodable picture.
			continue
		}
		pic := &claimReader{r: bufio.NewReader(bytes.NewReader(data)), remaining: int64(len(data))}
		if err := checkPictureClaim(pic); errors.Is(err, errAudioTagTooLarge) {
			return err
		}
		// Any other fault in an embedded picture is ignored by the library,
		// which discards readPictureBlock's error for comment pictures, and
		// costs little: every other field it reads from the decoded bytes is
		// bounded by them or by the library's own 10 MiB up front limit.
	}
	return nil
}

// checkOggClaims follows tag.ReadOGGTags: it demultiplexes Ogg pages into
// packets per stream serial and inspects the first Vorbis or Opus comment
// packet, which is the only one the library reads. Page checksums are not
// verified; the library stops at a bad one, so skipping that check only means
// this walk may look further than the library would. Packet data held while
// waiting for packets to complete is capped in total at maxOggPacketBytes,
// since the library holds the same data.
func checkOggClaims(c *claimReader) error {
	partial := map[uint32]*bytes.Buffer{}
	var held int64 // bytes in partial
	for {
		hdr, err := c.bytes(27)
		if err != nil {
			return err
		}
		if string(hdr[:4]) != "OggS" {
			return errClaimTruncated
		}
		continued := hdr[5]&0x1 != 0
		serial := binary.LittleEndian.Uint32(hdr[14:18])
		lacing, err := c.bytes(int64(hdr[26]))
		if err != nil {
			return err
		}
		var size int64
		for _, s := range lacing {
			size += int64(s)
		}
		data, err := c.bytes(size)
		if err != nil {
			return err
		}

		buf := partial[serial]
		delete(partial, serial)
		if buf != nil {
			held -= int64(buf.Len())
		}
		if !continued {
			buf = &bytes.Buffer{}
		} else if buf == nil {
			return errClaimTruncated
		}
		var p int
		for _, s := range lacing {
			if held+int64(buf.Len())+int64(s) > maxOggPacketBytes {
				return fmt.Errorf("%w (ogg packets over %d bytes)", errAudioTagTooLarge, maxOggPacketBytes)
			}
			buf.Write(data[p : p+int(s)])
			p += int(s)
			if s == 255 {
				continue
			}
			packet := buf.Bytes()
			buf = &bytes.Buffer{}
			for _, prefix := range []string{"\x03vorbis", "OpusTags"} {
				if bytes.HasPrefix(packet, []byte(prefix)) {
					body := packet[len(prefix):]
					return checkVorbisCommentClaims(&claimReader{
						r:         bufio.NewReader(bytes.NewReader(body)),
						remaining: int64(len(body)),
					})
				}
			}
		}
		partial[serial] = buf
		held += int64(buf.Len())
	}
}

// base64DecodedLen is the length standard padded base64 text decodes to: three
// bytes per four characters, less one per trailing '=' (at most two).
func base64DecodedLen(s string) int {
	n := len(s) / 4 * 3
	for i := 0; i < 2 && i < len(s) && s[len(s)-1-i] == '='; i++ {
		n--
	}
	return n
}
