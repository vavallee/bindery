package importer

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// ID3v2 and MP4 tags, and the ID3v2 tag a DSF file points at, are read by
// github.com/dhowden/tag through its readBytes helper. That helper allocates
// up front only up to 10 MiB, but past that it copies into a growing buffer
// until it has the declared count or the file ends, so a frame or atom that
// declares gigabytes costs as many bytes as the file really holds, every
// scan. A multi-gigabyte audiobook download is a file that holds a lot. The
// walks below follow the library's reads and refuse any single read over
// maxAudioTagStructureBytes (one cover, one frame, one atom), and any file
// whose reads add up to more than maxAudioTagTotalBytes, before the library
// runs. They seek where the library
// seeks (over mdat, for one), so they cost a few header reads, not a pass over
// the audio.

// maxAudioTagTotalBytes bounds the sum of every read the library will make
// for one ID3v2 or MP4 tag. A maximal cover plus several hundred chapters with
// their own chapter art (300 chapters of 100 KB images is about 35 MiB) fits.
const maxAudioTagTotalBytes = 64 << 20

// maxID3v2Frames bounds the frames in one ID3v2 tag, padding not counted. The
// library stores each frame in a map and names a repeat by probing NAME_0,
// NAME_1, ... from zero every time, so n repeats of a frame cost n squared map
// lookups. Measured on a Ryzen 7 5700X3D, 4096 repeated CHAP frames take the
// library 0.7 s (0.85 s on a 386 build) and 8192 take 3 s, so this is the
// largest power of two that keeps one hostile file near a second. It is still
// twice the chapter count of the longest real audiobooks.
const maxID3v2Frames = 4096

// maxMP4ContainerDepth bounds how deep the MP4 walk goes. The library recurses
// on every moov, udta, meta or ilst header and never unwinds before the end of
// the file, so a file of nothing but those headers grows its stack by one
// call per eight bytes until the runtime aborts the process. Real files nest
// four deep (moov, udta, meta, ilst).
const maxMP4ContainerDepth = 32

// maxMP4Atoms bounds the atoms the MP4 walk visits. The library visits every
// atom at the levels it walks; a fragmented audiobook has two per fragment, a
// few tens of thousands at most.
const maxMP4Atoms = 1 << 20

var errClaimMalformed = errors.New("audio tags: malformed tag structure")

// errAudioTagOverLimit reports a tag that may be honest but is bigger than
// Bindery reads: too many frames, too many bytes in all, containers nested too
// deep. It is kept apart from errAudioTagTooLarge so the scan's warning says
// which it was.
var errAudioTagOverLimit = errors.New("audio tags: embedded metadata is over Bindery's limits")

// errClaimWraps marks a size the library would compute by subtracting past
// zero. A 64-bit build then reads nothing; a 32-bit build reads about 4 GiB.
var errClaimWraps = errors.New("size wraps below zero")

// id3v2FrameIDs holds, per ID3v2 minor version, the frame IDs the library
// treats as valid (id3v2frames.go: id3v22Frames, id3v23Frames, id3v24Frames,
// at the pinned version; BSD licensed, see THIRD_PARTY_LICENSES.md). A frame
// that runs past the end of the tag is still read when its ID is in these
// tables and skipped, ending the tag, when it is not, so the walk needs the
// same tables to stop where the library stops.
var id3v2FrameIDs = map[byte]map[string]bool{
	2: frameIDSet(
		"BUF", "CNT", "COM", "CRA", "CRM", "ETC", "EQU", "GEO", "IPL", "LNK",
		"MCI", "MLL", "PIC", "POP", "REV", "RVA", "SLT", "STC", "TAL", "TBP",
		"TCM", "TCO", "TCR", "TDA", "TDY", "TEN", "TFT", "TIM", "TKE", "TLA",
		"TLE", "TMT", "TOA", "TOF", "TOL", "TOR", "TOT", "TP1", "TP2", "TP3",
		"TP4", "TPA", "TPB", "TRC", "TRD", "TRK", "TSI", "TSS", "TT1", "TT2",
		"TT3", "TXT", "TXX", "TYE", "UFI", "ULT", "WAF", "WAR", "WAS", "WCM",
		"WCP", "WPB", "WXX",
	),
	3: frameIDSet(
		"AENC", "APIC", "COMM", "COMR", "ENCR", "EQUA", "ETCO", "GEOB", "GRID",
		"IPLS", "LINK", "MCDI", "MLLT", "OWNE", "PRIV", "PCNT", "POPM", "POSS",
		"RBUF", "RVAD", "RVRB", "SYLT", "SYTC", "TALB", "TBPM", "TCMP", "TCOM",
		"TCON", "TCOP", "TDAT", "TDLY", "TENC", "TEXT", "TFLT", "TIME", "TIT1",
		"TIT2", "TIT3", "TKEY", "TLAN", "TLEN", "TMED", "TOAL", "TOFN", "TOLY",
		"TOPE", "TORY", "TOWN", "TPE1", "TPE2", "TPE3", "TPE4", "TPOS", "TPUB",
		"TRCK", "TRDA", "TRSN", "TRSO", "TSIZ", "TSO2", "TSOC", "TSRC", "TSSE",
		"TYER", "TXXX", "UFID", "USER", "USLT", "WCOM", "WCOP", "WOAF", "WOAR",
		"WOAS", "WORS", "WPAY", "WPUB", "WXXX",
	),
	4: frameIDSet(
		"AENC", "APIC", "ASPI", "COMM", "COMR", "ENCR", "EQU2", "ETCO", "GEOB",
		"GRID", "LINK", "MCDI", "MLLT", "OWNE", "PRIV", "PCNT", "POPM", "POSS",
		"RBUF", "RVA2", "RVRB", "SEEK", "SIGN", "SYLT", "SYTC", "TALB", "TBPM",
		"TCMP", "TCOM", "TCON", "TCOP", "TDEN", "TDLY", "TDOR", "TDRC", "TDRL",
		"TDTG", "TENC", "TEXT", "TFLT", "TIPL", "TIT1", "TIT2", "TIT3", "TKEY",
		"TLAN", "TLEN", "TMCL", "TMED", "TMOO", "TOAL", "TOFN", "TOLY", "TOPE",
		"TOWN", "TPE1", "TPE2", "TPE3", "TPE4", "TPOS", "TPRO", "TPUB", "TRCK",
		"TRSN", "TRSO", "TSO2", "TSOA", "TSOC", "TSOP", "TSOT", "TSRC", "TSSE",
		"TSST", "TXXX", "UFID", "USER", "USLT", "WCOM", "WCOP", "WOAF", "WOAR",
		"WOAS", "WORS", "WPAY", "WPUB", "WXXX",
	),
}

func frameIDSet(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// mp4LibraryAtoms is the library's own list (mp4.go, atoms) of atom names it
// reads into memory wherever it meets them, at the pinned version. Every
// other atom it does not descend into, it seeks over. A test pins that each
// name here is one the library reads.
var mp4LibraryAtoms = map[string]bool{
	"\xa9alb": true, "\xa9art": true, "\xa9ART": true, "aART": true,
	"\xa9day": true, "\xa9nam": true, "\xa9gen": true, "trkn": true,
	"\xa9wrt": true, "\xa9too": true, "cprt": true, "covr": true,
	"\xa9grp": true, "keyw": true, "\xa9lyr": true, "\xa9cmt": true,
	"tmpo": true, "cpil": true, "disk": true,
}

// mp4FreeformMeans is the library's list (mp4.go, means) of "----" atom
// namespaces it keeps. Whether a "----" atom is kept decides where the
// library reads next, so the walk has to know.
var mp4FreeformMeans = map[string]bool{
	"com.apple.iTunes":          true,
	"com.mixedinkey.mixedinkey": true,
	"com.serato.dj":             true,
}

// tagBudget counts the bytes the library will read into memory for one tag.
type tagBudget struct{ total int64 }

// take admits a read of n bytes, or refuses it when it is over the single
// structure cap or would take the tag over maxAudioTagTotalBytes. It returns
// n as an int64, which the cap makes safe on any build.
func (b *tagBudget) take(n uint) (int64, error) {
	if n > maxAudioTagStructureBytes {
		return 0, fmt.Errorf("%w (%d bytes declared)", errAudioTagTooLarge, n)
	}
	k := int64(n)
	b.total += k
	if b.total > maxAudioTagTotalBytes {
		return 0, fmt.Errorf("%w (tags over %d bytes in all)", errAudioTagOverLimit, maxAudioTagTotalBytes)
	}
	return k, nil
}

// tagStream is the byte stream a tag's frames or atoms are read from.
type tagStream interface {
	// read returns the next n (at most 64) bytes, with io.ReadFull's errors:
	// io.EOF when nothing was left, io.ErrUnexpectedEOF when some was.
	read(n int) ([]byte, error)
	// discard consumes n bytes the library reads, failing if they are not
	// there.
	discard(n int64) error
}

// readInto admits a read of n bytes against the budget and consumes them.
func (b *tagBudget) readInto(s tagStream, n uint) error {
	k, err := b.take(n)
	if err != nil {
		return err
	}
	return s.discard(k)
}

// seekWalker reads an io.ReadSeeker directly, without buffering, so its
// position is always the reader's and it can seek exactly where the library
// does.
type seekWalker struct {
	r   io.ReadSeeker
	pos int64 // absolute offset in r
	end int64
	buf [64]byte
}

func (w *seekWalker) read(n int) ([]byte, error) {
	got, err := io.ReadFull(w.r, w.buf[:n])
	w.pos += int64(got)
	return w.buf[:n], err
}

func (w *seekWalker) discard(n int64) error {
	if n > w.end-w.pos {
		return errClaimTruncated
	}
	return w.seek(n)
}

// seek moves forward n bytes. Like the library's own Seek calls on a file, it
// may go past the end; the next read then sees EOF.
func (w *seekWalker) seek(n int64) error {
	pos, err := w.r.Seek(n, io.SeekCurrent)
	w.pos = pos
	return err
}

func (w *seekWalker) seekTo(abs int64) error {
	pos, err := w.r.Seek(abs, io.SeekStart)
	w.pos = pos
	return err
}

// unsyncStream undoes ID3v2 unsynchronisation exactly as the library's
// unsynchroniser does: a zero byte straight after 0xFF is dropped.
type unsyncStream struct {
	r   *bufio.Reader
	ff  bool
	buf [64]byte
}

func (u *unsyncStream) next() (byte, error) {
	for {
		b, err := u.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if u.ff && b == 0 {
			u.ff = false
			continue
		}
		u.ff = b == 0xFF
		return b, nil
	}
}

func (u *unsyncStream) read(n int) ([]byte, error) {
	for i := range n {
		b, err := u.next()
		if err != nil {
			if i > 0 && errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		u.buf[i] = b
	}
	return u.buf[:n], nil
}

func (u *unsyncStream) discard(n int64) error {
	for ; n > 0; n-- {
		if _, err := u.next(); err != nil {
			return err
		}
	}
	return nil
}

// id3SynchsafeUint mirrors the library's get7BitChunkedInt, including that it
// does not mask the high bit of each byte. The library converts the int it
// returns to uint; for four bytes the value is under 2^30, so computing in
// uint gives the same number on any build.
func id3SynchsafeUint(b []byte) uint {
	var n uint
	for _, x := range b {
		n = n<<7 | uint(x)
	}
	return n
}

// checkID3v2Claims follows tag.ReadID3v2Tags: header, optional extended
// header, then frames until the declared tag size is used up. Offsets are
// kept in uint, as the library keeps them, so the walk wraps where the library
// does on 32-bit builds. Sizes the library would compute by subtracting past
// zero are refused outright; a 64-bit build reads nothing for them and a
// 32-bit build reads about 4 GiB.
func checkID3v2Claims(w *seekWalker) error {
	hdr, err := w.read(10)
	if err != nil {
		return err
	}
	if string(hdr[:3]) != "ID3" || hdr[3] < 2 || hdr[3] > 4 {
		return errClaimMalformed
	}
	version, flags := hdr[3], hdr[5]
	tagSize := id3SynchsafeUint(hdr[6:10])
	offset := uint(10)
	var budget tagBudget

	if flags&0x40 != 0 && version != 2 {
		b, err := w.read(4)
		if err != nil {
			return err
		}
		var ext uint
		if version == 3 {
			ext = uint(binary.BigEndian.Uint32(b))
		} else {
			n := id3SynchsafeUint(b)
			if n < 4 {
				return fmt.Errorf("%w (%w: ID3v2.4 extended header of %d bytes)", errAudioTagTooLarge, errClaimWraps, n)
			}
			ext = n - 4
		}
		if err := budget.readInto(w, ext); err != nil {
			return err
		}
		offset += ext
	}

	var s tagStream = w
	if flags&0x80 != 0 {
		s = &unsyncStream{r: bufio.NewReader(w.r)}
	}
	hdrLen := uint(10)
	if version == 2 {
		hdrLen = 6
	}
	for frames := 0; offset < tagSize; {
		h, err := s.read(int(hdrLen))
		if err != nil {
			return err
		}
		var name string
		var size uint
		var compressed, encrypted, lengthIndicator bool
		switch version {
		case 2:
			name = string(h[:3])
			size = uint(h[3])<<16 | uint(h[4])<<8 | uint(h[5])
		case 3:
			name = string(h[:4])
			size = uint(binary.BigEndian.Uint32(h[4:8]))
			compressed, encrypted = h[9]&0x80 != 0, h[9]&0x40 != 0
		case 4:
			name = string(h[:4])
			size = id3SynchsafeUint(h[4:8])
			compressed, encrypted, lengthIndicator = h[9]&0x08 != 0, h[9]&0x04 != 0, h[9]&0x01 != 0
		}
		if size == 0 {
			return nil // padding: the library stops here
		}
		if frames++; frames > maxID3v2Frames {
			return fmt.Errorf("%w (more than %d ID3v2 frames)", errAudioTagOverLimit, maxID3v2Frames)
		}
		offset += hdrLen + size
		if offset > tagSize && !id3v2FrameIDs[version][name] {
			return nil // corrupt padding: the library stops here too
		}
		if compressed {
			if version == 4 && !lengthIndicator {
				return errClaimMalformed
			}
			if version == 3 {
				if _, err := s.read(4); err != nil {
					return err
				}
				if size < 4 {
					return fmt.Errorf("%w (%w: compressed ID3v2 frame of %d bytes)", errAudioTagTooLarge, errClaimWraps, size)
				}
				size -= 4
			}
		}
		if lengthIndicator {
			b, err := s.read(4)
			if err != nil {
				return err
			}
			size = id3SynchsafeUint(b)
		}
		if encrypted {
			if _, err := s.read(1); err != nil {
				return err
			}
			if size == 0 {
				return fmt.Errorf("%w (%w: empty encrypted ID3v2 frame)", errAudioTagTooLarge, errClaimWraps)
			}
			size--
		}
		if err := budget.readInto(s, size); err != nil {
			return err
		}
	}
	return nil
}

// checkDSFClaims follows tag.ReadDSFTags: a fixed header holding the absolute
// offset of an ID3v2 tag, which is then read as any other.
func checkDSFClaims(w *seekWalker) error {
	if _, err := w.read(4); err != nil {
		return err
	}
	if err := w.seek(16); err != nil {
		return err
	}
	b, err := w.read(8)
	if err != nil {
		return err
	}
	ptr := binary.LittleEndian.Uint64(b)
	if ptr > math.MaxInt64 {
		return errClaimMalformed // the library's Seek fails on it
	}
	if err := w.seekTo(int64(ptr)); err != nil {
		return err
	}
	return checkID3v2Claims(w)
}

// checkMP4Claims follows tag.ReadAtoms. The library ignores container sizes:
// on moov, udta, meta (after four bytes) and ilst it simply carries on reading
// headers from inside, at any level, until the file ends. It reads an atom
// whose name is in mp4LibraryAtoms into memory, size minus eight bytes, which
// wraps to about 4 GiB when the size is under eight; it parses "----" atoms
// child by child; and it seeks over everything else, mdat included.
func checkMP4Claims(w *seekWalker) error {
	var budget tagBudget
	depth := 0
	for atoms := 0; ; atoms++ {
		if atoms == maxMP4Atoms {
			return fmt.Errorf("%w (more than %d MP4 atoms)", errAudioTagOverLimit, maxMP4Atoms)
		}
		size, name, err := mp4AtomHeader(w)
		if errors.Is(err, io.EOF) {
			return nil // the library's normal end
		}
		if err != nil {
			return err
		}
		switch name {
		case "meta":
			if _, err := w.read(4); err != nil {
				return err
			}
			fallthrough
		case "moov", "udta", "ilst":
			depth++
			if depth > maxMP4ContainerDepth {
				return fmt.Errorf("%w (MP4 containers nested over %d deep)", errAudioTagOverLimit, maxMP4ContainerDepth)
			}
			continue
		}
		if name == "----" {
			kept, err := checkMP4FreeformClaims(w, size, &budget)
			if err != nil {
				return err
			}
			if kept {
				continue
			}
		}
		if !mp4LibraryAtoms[name] {
			if err := w.seek(int64(size - 8)); err != nil {
				return err
			}
			continue
		}
		if err := budget.readInto(w, uint(size-8)); err != nil {
			return err
		}
	}
}

// mp4AtomHeader reads a size and a name as the library's readAtomHeader does,
// returning io.EOF only when the file ends cleanly before the size or before
// the name, which the library treats as the end of the tags.
func mp4AtomHeader(w *seekWalker) (uint32, string, error) {
	b, err := w.read(4)
	if err != nil {
		return 0, "", err
	}
	size := binary.BigEndian.Uint32(b)
	b, err = w.read(4)
	if err != nil {
		return 0, "", err
	}
	return size, string(b), nil
}

// checkMP4FreeformClaims follows the library's readCustomAtom over a "----"
// atom of the given size, reporting whether the library keeps it (a known
// mean, a non-empty name and some data). A kept atom ends the library's work
// on it; one it drops is followed by a seek of size minus eight more bytes.
func checkMP4FreeformClaims(w *seekWalker, size uint32, budget *tagBudget) (bool, error) {
	var meanOK, named, hasData bool
	for size > 8 {
		b, err := w.read(8)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF // the library fails here, it does not finish
			}
			return false, err
		}
		subSize := binary.BigEndian.Uint32(b[:4])
		subName := string(b[4:8])
		if size < subSize {
			return false, errClaimMalformed
		}
		size -= subSize
		n, err := budget.take(uint(subSize - 8)) // wraps under eight, as the library's does
		if err != nil {
			return false, err
		}
		if n < 4 {
			return false, errClaimMalformed
		}
		if subName == "mean" && n <= int64(len(w.buf)) {
			b, err := w.read(int(n))
			if err != nil {
				return false, err
			}
			meanOK = mp4FreeformMeans[string(b[4:])]
			continue
		}
		if err := w.discard(n); err != nil {
			return false, err
		}
		switch subName {
		case "mean":
			meanOK = false // longer than any mean the library keeps
		case "name":
			named = n > 4
		case "data":
			hasData = true
		}
	}
	if size != 8 {
		return false, errClaimMalformed
	}
	return meanOK && named && hasData, nil
}
