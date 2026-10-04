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

// errAudioPictureTooLarge reports an embedded picture that declares more image
// data than the file holds.
//
// github.com/dhowden/tag reads a FLAC picture block, and the
// METADATA_BLOCK_PICTURE comment that FLAC, Ogg Vorbis and Opus files carry,
// by allocating the declared data length up front. That length is a 32-bit
// field the file controls, so a file of a few dozen bytes made every tag read
// allocate up to 4 GiB (and panic outright on the 32-bit ARM builds). The
// library has no option to skip pictures, which Bindery never uses, so
// checkAudioPictureClaims walks the same structures first and refuses the
// file when a declared length could not possibly be satisfied. The tag read
// then fails like any other unreadable file and the scan falls back to the
// filename.
var errAudioPictureTooLarge = errors.New("audio tags: embedded picture declares more data than the file holds")

// checkAudioPictureClaims returns errAudioPictureTooLarge if r is a FLAC or
// Ogg file whose embedded picture declares more data than is present. It
// dispatches on the leading bytes exactly as tag.ReadFrom does and leaves r
// positioned where it found it. Formats without this allocation (ID3, MP4) are
// passed through untouched.
func checkAudioPictureClaims(r io.ReadSeeker) error {
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
	cr := &claimReader{r: bufio.NewReader(r), remaining: end - start}
	defer func() { _, _ = r.Seek(start, io.SeekStart) }()

	magic, err := cr.peek(4)
	if err != nil {
		// Too short to be either format; the tag reader reports it.
		return nil
	}
	switch string(magic) {
	case "fLaC":
		err = checkFLACPictureClaims(cr)
	case "OggS":
		err = checkOggPictureClaims(cr)
	default:
		return nil
	}
	if errors.Is(err, errAudioPictureTooLarge) {
		return err
	}
	// Any other failure is a malformed file the tag reader rejects on its own
	// before it reaches a picture, so it is not this check's to report.
	return nil
}

// claimReader reads a stream while tracking how many bytes are left in it, so
// a declared length can be compared against what actually exists.
type claimReader struct {
	r         *bufio.Reader
	remaining int64
}

var errClaimTruncated = errors.New("audio tags: structure runs past end of data")

func (c *claimReader) peek(n int) ([]byte, error) {
	if int64(n) > c.remaining {
		return nil, errClaimTruncated
	}
	return c.r.Peek(n)
}

// bytes returns the next n bytes. It refuses before allocating when n is more
// than is left, so the check itself cannot be made to allocate.
func (c *claimReader) bytes(n int64) ([]byte, error) {
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
	if _, err := c.r.Discard(int(n)); err != nil {
		return err
	}
	c.remaining -= n
	return nil
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

// checkFLACPictureClaims follows tag.ReadFLACTags: after "fLaC", metadata
// blocks of a one-byte type (high bit marks the last) and a 24-bit length.
// The library parses comment and picture blocks from the stream without
// honouring that length and seeks over every other block by it, so this walk
// does the same to stay aligned with what the library will read.
func checkFLACPictureClaims(c *claimReader) error {
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
			err = checkVorbisCommentPictureClaims(c)
		case 6: // PICTURE
			err = checkPictureClaim(c)
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
	if dataLen > c.remaining {
		return fmt.Errorf("%w (%d bytes declared, %d available)", errAudioPictureTooLarge, dataLen, c.remaining)
	}
	return c.skip(dataLen)
}

// checkVorbisCommentPictureClaims follows the library's readVorbisComment:
// vendor string, then a count of length-prefixed KEY=value comments. Every
// METADATA_BLOCK_PICTURE value that decodes is checked against its own decoded
// length, which is all the data that picture can hold. The library only ever
// parses the last one, so checking all of them is stricter, never looser.
func checkVorbisCommentPictureClaims(c *claimReader) error {
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
		// Read the whole comment (bounded by what is left, as the library's
		// own read is) and match the key exactly as the library does. A byte
		// prefix compare would miss keys that only lowercase to the picture
		// key, such as one spelled with the Kelvin sign.
		comment, err := c.bytes(n)
		if err != nil {
			return err
		}
		key, value, ok := strings.Cut(string(comment), "=")
		if !ok || strings.ToLower(key) != "metadata_block_picture" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			// The library fails the whole read on an undecodable picture.
			continue
		}
		pic := &claimReader{r: bufio.NewReader(bytes.NewReader(data)), remaining: int64(len(data))}
		if err := checkPictureClaim(pic); errors.Is(err, errAudioPictureTooLarge) {
			return err
		}
		// Any other fault in an embedded picture is ignored by the library,
		// which discards readPictureBlock's error for comment pictures.
	}
	return nil
}

// checkOggPictureClaims follows tag.ReadOGGTags: it demultiplexes Ogg pages
// into packets per stream serial and inspects the first Vorbis or Opus comment
// packet, which is the only one the library reads. Page checksums are not
// verified; the library stops at a bad one, so skipping that check only means
// this walk may look further than the library would.
func checkOggPictureClaims(c *claimReader) error {
	partial := map[uint32]*bytes.Buffer{}
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

		buf := &bytes.Buffer{}
		if continued {
			b, ok := partial[serial]
			if !ok {
				return errClaimTruncated
			}
			buf = b
		}
		var p int
		for _, s := range lacing {
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
					return checkVorbisCommentPictureClaims(&claimReader{
						r:         bufio.NewReader(bytes.NewReader(body)),
						remaining: int64(len(body)),
					})
				}
			}
		}
		partial[serial] = buf
	}
}
