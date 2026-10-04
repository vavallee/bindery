package importer

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/dhowden/tag"
)

// patchedFile is an io.ReadSeeker of size bytes that are zero apart from the
// patches written at their offsets. It presents a multi-gigabyte audio file
// without allocating or writing one, and counts the bytes read from it so a
// test can tell a seek from a read.
type patchedFile struct {
	size    int64
	patches []filePatch
	off     int64
	read    int64
}

type filePatch struct {
	at   int64
	data []byte
}

func (f *patchedFile) Read(p []byte) (int, error) {
	if f.off >= f.size {
		return 0, io.EOF
	}
	if rest := f.size - f.off; int64(len(p)) > rest {
		p = p[:rest]
	}
	clear(p)
	for _, pt := range f.patches {
		lo, hi := max(pt.at, f.off), min(pt.at+int64(len(pt.data)), f.off+int64(len(p)))
		if lo < hi {
			copy(p[lo-f.off:hi-f.off], pt.data[lo-pt.at:hi-pt.at])
		}
	}
	f.off += int64(len(p))
	f.read += int64(len(p))
	return len(p), nil
}

func (f *patchedFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
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

// bigFile is the size of the hostile files below: large enough that reading
// it whole blows audioAllocBudget several times over, small enough that the
// unfixed library finishes reading it in a test.
const bigFile = 256 << 20

// readMeasured runs readAudioTagsFrom over r and reports heap bytes allocated.
func readMeasured(r io.ReadSeeker) (AudioTags, uint64, error) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	tags, err := readAudioTagsFrom(r)
	runtime.ReadMemStats(&after)
	return tags, after.TotalAlloc - before.TotalAlloc, err
}

// expectRejectedCheaply asserts the read failed with errAudioTagTooLarge and
// allocated within audioAllocBudget.
func expectRejectedCheaply(t *testing.T, r io.ReadSeeker) {
	t.Helper()
	_, alloc, err := readMeasured(r)
	if !errors.Is(err, errAudioTagTooLarge) {
		t.Errorf("expected errAudioTagTooLarge, got %v", err)
	}
	if alloc > audioAllocBudget {
		t.Errorf("allocated %d MiB, budget %d MiB", alloc>>20, audioAllocBudget>>20)
	}
}

// --- ID3v2 builders ---------------------------------------------------------

// id3Tag builds an ID3v2 header of the given version and flags, declaring
// size bytes of tag, followed by body.
func id3Tag(version, flags byte, size uint32, body ...[]byte) []byte {
	b := []byte{'I', 'D', '3', version, 0, flags}
	b = append(b, synchsafe(size)...)
	for _, part := range body {
		b = append(b, part...)
	}
	return b
}

// id3Frame23 builds an ID3v2.3 frame whose header declares size bytes.
func id3Frame23(id string, size uint32, flags [2]byte, body []byte) []byte {
	b := []byte(id)
	b = binary.BigEndian.AppendUint32(b, size)
	b = append(b, flags[:]...)
	return append(b, body...)
}

// id3Frame24 builds an ID3v2.4 frame (synchsafe size).
func id3Frame24(id string, size uint32, flags [2]byte, body []byte) []byte {
	b := append([]byte(id), synchsafe(size)...)
	b = append(b, flags[:]...)
	return append(b, body...)
}

func id3Text(text string) []byte { return append([]byte{0x03}, text...) }

// id3APIC builds an APIC body: UTF-8, MIME, front cover, empty description.
func id3APIC(img []byte) []byte {
	b := []byte{0x03}
	b = append(b, "image/png\x00"...)
	b = append(b, 3, 0)
	return append(b, img...)
}

// --- MP4 builders -------------------------------------------------------------

// atom builds an MP4 atom from its name and children.
func atom(name string, body ...[]byte) []byte {
	var n int
	for _, b := range body {
		n += len(b)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(8+n))
	out = append(out, name...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

// atomHeader is an atom header declaring size, with no body.
func atomHeader(name string, size uint32) []byte {
	return append(binary.BigEndian.AppendUint32(nil, size), name...)
}

// dataAtom is an ilst item's "data" child: class, locale, payload.
func dataAtom(class byte, payload []byte) []byte {
	return atom("data", []byte{0, 0, 0, class, 0, 0, 0, 0}, payload)
}

var mp4Ftyp = atom("ftyp", []byte("M4B \x00\x00\x00\x00M4B isom"))

// mp4File builds ftyp + moov/udta/meta/ilst holding items.
func mp4File(items ...[]byte) []byte {
	ilst := atom("ilst", items...)
	meta := atom("meta", []byte{0, 0, 0, 0}, atom("hdlr", make([]byte, 25)), ilst)
	moov := atom("moov", atom("mvhd", make([]byte, 100)), atom("udta", meta))
	return append(append([]byte{}, mp4Ftyp...), moov...)
}

// mp4Freeform builds an iTunes "----" atom.
func mp4Freeform(mean, name, value string) []byte {
	return atom("----",
		atom("mean", []byte{0, 0, 0, 0}, []byte(mean)),
		atom("name", []byte{0, 0, 0, 0}, []byte(name)),
		atom("data", []byte{0, 0, 0, 1}, []byte(value)),
	)
}

// --- hostile ID3v2 ------------------------------------------------------------

func TestReadAudioTags_ID3v2HostileFrameClaims(t *testing.T) {
	title := id3Frame23("TIT2", uint32(len(id3Text("Guards! Guards!"))), [2]byte{}, id3Text("Guards! Guards!"))
	cases := []struct {
		name   string
		prefix []byte
		size   int64
	}{
		{"v2.3 APIC declaring 4 GiB", id3Tag(3, 0, 4096, title, id3Frame23("APIC", 0xFFFFFFF0, [2]byte{}, id3APIC([]byte("tiny")))), bigFile},
		{"v2.3 non picture frame declaring 200 MiB", id3Tag(3, 0, 4096, title, id3Frame23("PRIV", 200<<20, [2]byte{}, nil)), bigFile},
		{"v2.3 frame size under 4 with compression", id3Tag(3, 0, 4096, title, id3Frame23("PRIV", 2, [2]byte{0, 0x80}, []byte{0, 0, 0, 0})), bigFile},
		// Only the data length indicator is read; the frame size is honest.
		{"v2.4 data length indicator declaring 256 MiB", id3Tag(4, 0, 4096, id3Frame24("GEOB", 10, [2]byte{0, 0x01}, synchsafe(0x0FFFFFFF))), bigFile},
		{"v2.3 extended header declaring 4 GiB", id3Tag(3, 0x40, 4096, binary.BigEndian.AppendUint32(nil, 0xFFFFFFF0)), bigFile},
		{"v2.4 unsynchronised APIC", id3Tag(4, 0x80, 4096, id3Frame24("APIC", 0x0FFFFFFF, [2]byte{}, id3APIC([]byte("tiny")))), 96 << 20},
		{"dsf pointing at a hostile tag", append(append([]byte("DSD \x1c\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), binary.LittleEndian.AppendUint64(nil, 64)...), make([]byte, 36)...), bigFile},
	}
	dsfTag := id3Tag(3, 0, 4096, title, id3Frame23("APIC", 0xFFFFFFF0, [2]byte{}, id3APIC([]byte("tiny"))))
	cases[len(cases)-1].prefix = append(cases[len(cases)-1].prefix, dsfTag...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectRejectedCheaply(t, &patchedFile{size: tc.size, patches: []filePatch{{0, tc.prefix}}})
		})
	}
}

// TestReadAudioTags_ID3v2OffsetWrapsLikeTheLibrary covers a frame whose
// declared size takes the library's uint offset past 4 GiB. On 64-bit the
// offset lands past the tag and the library stops at the unknown frame name.
// On 32-bit it wraps to a small value, the library keeps going and reads the
// declared 4 GiB. The walk keeps the same uint offset, so it must refuse the
// file on 32-bit and pass it on 64-bit.
func TestReadAudioTags_ID3v2OffsetWrapsLikeTheLibrary(t *testing.T) {
	prefix := id3Tag(3, 0, 100, id3Frame23("\x01\x02\x03\x04", 0xFFFFFFF0, [2]byte{}, nil))
	_, alloc, err := readMeasured(&patchedFile{size: bigFile, patches: []filePatch{{0, prefix}}})
	if bits.UintSize == 32 {
		if !errors.Is(err, errAudioTagTooLarge) {
			t.Errorf("32-bit: expected errAudioTagTooLarge, got %v", err)
		}
	} else if err != nil {
		t.Errorf("64-bit: the library stops at this frame, so the check must too; got %v", err)
	}
	if alloc > audioAllocBudget {
		t.Errorf("allocated %d MiB, budget %d MiB", alloc>>20, audioAllocBudget>>20)
	}
}

// TestReadAudioTags_ID3v2TotalIsCapped pins that frames which each fit the
// per frame cap cannot add up past maxAudioTagTotalBytes, and that a tag just
// under it, such as an audiobook with a cover and chapter art, passes.
func TestReadAudioTags_ID3v2TotalIsCapped(t *testing.T) {
	const frame = 12 << 20
	tagOf := func(n int) *patchedFile {
		f := &patchedFile{size: 10 + int64(n)*(frame+10) + 64}
		f.patches = append(f.patches, filePatch{0, id3Tag(3, 0, uint32(n*(frame+10)))})
		for i := range n {
			f.patches = append(f.patches, filePatch{10 + int64(i)*(frame+10), id3Frame23("PRIV", frame, [2]byte{}, nil)})
		}
		return f
	}
	if err := checkAudioTagClaims(tagOf(maxAudioTagTotalBytes / frame)); err != nil {
		t.Errorf("%d frames of 12 MiB: %v", maxAudioTagTotalBytes/frame, err)
	}
	if err := checkAudioTagClaims(tagOf(maxAudioTagTotalBytes/frame + 1)); !errors.Is(err, errAudioTagOverLimit) {
		t.Errorf("expected errAudioTagOverLimit, got %v", err)
	}
}

// TestReadAudioTags_ID3v2FrameCountIsCapped pins the frame count bound, and
// that padding after the last frame does not count as a frame. The library
// stores every frame in a map and renames repeats by probing NAME_0, NAME_1,
// ... from zero each time, so n repeats cost n squared work.
func TestReadAudioTags_ID3v2FrameCountIsCapped(t *testing.T) {
	one := id3Frame23("PRIV", 1, [2]byte{}, []byte{0})
	tagOf := func(n int) []byte {
		frames := bytes.Repeat(one, n)
		return id3Tag(3, 0, uint32(len(frames)+64), frames, make([]byte, 64))
	}
	if err := checkAudioTagClaims(bytes.NewReader(tagOf(maxID3v2Frames))); err != nil {
		t.Errorf("exactly %d frames and padding: %v", maxID3v2Frames, err)
	}
	if _, err := readAudioTagsFrom(bytes.NewReader(tagOf(maxID3v2Frames + 1))); !errors.Is(err, errAudioTagOverLimit) {
		t.Errorf("expected errAudioTagOverLimit, got %v", err)
	}
}

// TestReadAudioTags_ID3v2StopsWhereLibraryStops covers frames that run past
// the end of the tag. The library reads one whose ID is in its frame tables
// and stops at one whose ID is not, so the walk must not refuse the second
// kind whatever size it declares.
func TestReadAudioTags_ID3v2StopsWhereLibraryStops(t *testing.T) {
	title := id3Frame23("TIT2", uint32(len(id3Text("Guards! Guards!"))), [2]byte{}, id3Text("Guards! Guards!"))
	cases := []struct {
		name string
		file []byte
	}{
		// The library starts its offset at 10 against a size that excludes
		// the header, so the tag must declare 10 bytes beyond the title for
		// the library to reach the ABCD frame at all.
		{"unknown ID shaped like a frame", id3Tag(3, 0, uint32(len(title)+14), title, id3Frame23("ABCD", 0x01FFFFFF, [2]byte{}, make([]byte, 16)))},
		{"digits as the ID (fuzzer case)", []byte("ID3\x04" + "0000000000000001")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, wantErr := tag.ReadFrom(bytes.NewReader(tc.file))
			if wantErr != nil {
				t.Fatalf("fixture: the library refuses it: %v", wantErr)
			}
			if err := checkAudioTagClaims(bytes.NewReader(tc.file)); err != nil {
				t.Errorf("library reads it, check refuses it: %v", err)
			}
			got, err := readAudioTagsFrom(bytes.NewReader(tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if want := bareLibraryTags(t, bytes.NewReader(tc.file)); got != want {
				t.Errorf("got %+v, bare library %+v", got, want)
			}
		})
	}
}

// TestID3v2FrameIDsMatchLibrary pins id3v2FrameIDs against the library: a
// frame that runs past the tag and past the data is read (and fails at the end
// of the data) when its ID is in the library's table, and skipped when not.
func TestID3v2FrameIDsMatchLibrary(t *testing.T) {
	overrun := func(version byte, id string) []byte {
		var frame []byte
		switch version {
		case 2:
			frame = append([]byte(id), 0, 0x03, 0xE8)
		case 3:
			frame = id3Frame23(id, 1000, [2]byte{}, nil)
		case 4:
			frame = id3Frame24(id, 1000, [2]byte{}, nil)
		}
		return id3Tag(version, 0, 20, frame, make([]byte, 10))
	}
	for version, ids := range id3v2FrameIDs {
		for id := range ids {
			if _, err := tag.ReadFrom(bytes.NewReader(overrun(version, id))); err == nil {
				t.Errorf("v2.%d %q: listed, but the library does not read it", version, id)
			}
		}
		for _, id := range []string{"ABC", "CHAP", "CTOC", "ZZZZ", "0000"} {
			if len(id) != map[byte]int{2: 3, 3: 4, 4: 4}[version] || ids[id] {
				continue
			}
			if _, err := tag.ReadFrom(bytes.NewReader(overrun(version, id))); err != nil {
				t.Errorf("v2.%d %q: not listed, but the library reads it: %v", version, id, err)
			}
		}
	}
}

// --- hostile MP4 --------------------------------------------------------------

func TestReadAudioTags_MP4HostileAtomClaims(t *testing.T) {
	path := func(tail ...[]byte) []byte {
		// The library ignores container sizes, so hostile headers declare
		// whatever they like.
		b := append([]byte{}, mp4Ftyp...)
		b = append(b, atomHeader("moov", 0xFFFFFFFF)...)
		b = append(b, atomHeader("udta", 0xFFFFFFFF)...)
		b = append(b, atomHeader("meta", 0xFFFFFFFF)...)
		b = append(b, 0, 0, 0, 0)
		b = append(b, atomHeader("ilst", 0xFFFFFFFF)...)
		for _, x := range tail {
			b = append(b, x...)
		}
		return b
	}
	cases := []struct {
		name   string
		prefix []byte
	}{
		{"covr declaring 4 GiB", path(atomHeader("covr", 0xFFFFFFF0), atomHeader("data", 0xFFFFFFE8))},
		{"covr size below its header", path(atomHeader("covr", 4))},
		{"title atom at top level declaring 200 MiB", append(append([]byte{}, mp4Ftyp...), atomHeader("\xa9nam", 200<<20)...)},
		{"freeform data atom of size zero", path(atomHeader("----", 100), atomHeader("data", 0))},
		{"freeform mean atom declaring 4 GiB", path(atomHeader("----", 0xFFFFFFFF), atomHeader("mean", 0xFFFFFFF0))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectRejectedCheaply(t, &patchedFile{size: bigFile, patches: []filePatch{{0, tc.prefix}}})
		})
	}
}

// TestReadAudioTags_MP4NestingIsCapped pins the container depth bound. The
// library recurses once per moov, udta, meta or ilst header it meets and never
// unwinds until the end of the file, so a file of nothing but such headers
// grows the stack by a frame every eight bytes.
func TestReadAudioTags_MP4NestingIsCapped(t *testing.T) {
	file := append(append([]byte{}, mp4Ftyp...), bytes.Repeat(atomHeader("moov", 8), maxMP4ContainerDepth+1)...)
	if _, err := readAudioTagsFrom(bytes.NewReader(file)); !errors.Is(err, errAudioTagOverLimit) {
		t.Errorf("expected errAudioTagOverLimit, got %v", err)
	}
}

func TestReadAudioTags_MP4TotalIsCapped(t *testing.T) {
	const item = 12 << 20
	tagOf := func(n int) *patchedFile {
		head := mp4File()
		f := &patchedFile{size: int64(len(head)) + int64(n)*(item+8) + 64}
		f.patches = append(f.patches, filePatch{0, head})
		for i := range n {
			f.patches = append(f.patches, filePatch{int64(len(head)) + int64(i)*(item+8), atomHeader("\xa9cmt", item+8)})
		}
		return f
	}
	if err := checkAudioTagClaims(tagOf(maxAudioTagTotalBytes / item)); err != nil {
		t.Errorf("%d atoms of 12 MiB: %v", maxAudioTagTotalBytes/item, err)
	}
	if err := checkAudioTagClaims(tagOf(maxAudioTagTotalBytes/item + 1)); !errors.Is(err, errAudioTagOverLimit) {
		t.Errorf("expected errAudioTagOverLimit, got %v", err)
	}
}

// --- honest files read exactly as the bare library reads them -----------------

// bareLibraryTags is what readAudioTagsFrom returned before any pre-check.
func bareLibraryTags(t *testing.T, r io.ReadSeeker) AudioTags {
	t.Helper()
	m, err := tag.ReadFrom(r)
	if err != nil {
		t.Fatalf("bare library: %v", err)
	}
	return AudioTags{
		Title:       strings.TrimSpace(m.Title()),
		Author:      pickAudioAuthor(m),
		ASIN:        pickAudioASIN(m.Raw()),
		Album:       strings.TrimSpace(m.Album()),
		Artist:      strings.TrimSpace(m.Artist()),
		AlbumArtist: strings.TrimSpace(m.AlbumArtist()),
		Composer:    strings.TrimSpace(m.Composer()),
	}
}

// unsync applies ID3v2 unsynchronisation: a zero after every 0xFF.
func unsync(b []byte) []byte {
	var out []byte
	for _, c := range b {
		out = append(out, c)
		if c == 0xFF {
			out = append(out, 0)
		}
	}
	return out
}

func TestReadAudioTags_HonestID3AndMP4MatchLibrary(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xFF, 0x00, 0xE0}, 4000)...)
	frames23 := func() []byte {
		var b []byte
		for _, f := range [][2]string{{"TIT2", "Guards! Guards!"}, {"TPE1", "Terry Pratchett"}} {
			b = append(b, id3Frame23(f[0], uint32(len(id3Text(f[1]))), [2]byte{}, id3Text(f[1]))...)
		}
		txxx := append(id3Text("ASIN"), append([]byte{0}, "B003P2WO5E"...)...)
		b = append(b, id3Frame23("TXXX", uint32(len(txxx)), [2]byte{}, txxx)...)
		return append(b, id3Frame23("APIC", uint32(len(id3APIC(png))), [2]byte{}, id3APIC(png))...)
	}()
	frames24 := func() []byte {
		var b []byte
		for _, f := range [][2]string{{"TIT2", "Guards! Guards!"}, {"TPE1", "Terry Pratchett"}} {
			b = append(b, id3Frame24(f[0], uint32(len(id3Text(f[1]))), [2]byte{}, id3Text(f[1]))...)
		}
		return append(b, id3Frame24("APIC", uint32(len(id3APIC(png))), [2]byte{}, id3APIC(png))...)
	}()
	frames22 := func() []byte {
		var b []byte
		for _, f := range [][2]string{{"TT2", "Guards! Guards!"}, {"TP1", "Terry Pratchett"}} {
			body := append([]byte{0}, f[1]...)
			b = append(b, f[0][0], f[0][1], f[0][2], 0, 0, byte(len(body)))
			b = append(b, body...)
		}
		pic := append([]byte{0, 'P', 'N', 'G', 3, 0}, png...)
		n := len(pic)
		b = append(b, 'P', 'I', 'C', byte(n>>16), byte(n>>8), byte(n))
		return append(b, pic...)
	}()
	padded := func(frames []byte, pad int) []byte { return append(append([]byte{}, frames...), make([]byte, pad)...) }
	// Junk after the last frame: a header whose name is not a frame ID and
	// whose size runs past the tag. The library stops there.
	junk := append(append([]byte{}, frames23...), "\x01\x02\x03\x04\xff\xff\xff\xf0\x00\x00"...)

	m4b := mp4File(
		atom("\xa9nam", dataAtom(1, []byte("Guards! Guards!"))),
		atom("\xa9ART", dataAtom(1, []byte("Terry Pratchett"))),
		atom("covr", dataAtom(14, png)),
		atom("trkn", dataAtom(0, []byte{0, 0, 0, 1, 0, 9, 0, 0})),
		mp4Freeform("com.apple.iTunes", "ASIN", "B003P2WO5E"),
		mp4Freeform("org.example", "ignored", "x"),
		atom("free", make([]byte, 32)),
	)
	// A 4 GiB mdat before moov, as most encoders lay out an audiobook. The
	// check must seek past it, as the library does, not read it.
	const mdatSize = 0xFFFF0000
	moovAt := int64(len(mp4Ftyp)) + mdatSize
	bigM4B := func() *patchedFile {
		moov := m4b[len(mp4Ftyp):]
		return &patchedFile{size: moovAt + int64(len(moov)), patches: []filePatch{
			{0, mp4Ftyp}, {int64(len(mp4Ftyp)), atomHeader("mdat", mdatSize)}, {moovAt, moov},
		}}
	}
	dsf := append([]byte("DSD \x1c\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), binary.LittleEndian.AppendUint64(nil, 64)...)
	dsf = append(dsf, make([]byte, 64-len(dsf))...)
	dsf = append(dsf, id3Tag(3, 0, uint32(len(frames23)), frames23)...)

	cases := []struct {
		name string
		open func() io.ReadSeeker
	}{
		{"id3v2.3 with cover", func() io.ReadSeeker {
			return bytes.NewReader(id3Tag(3, 0, uint32(len(frames23)+512), padded(frames23, 512)))
		}},
		{"id3v2.3 junk after frames", func() io.ReadSeeker { return bytes.NewReader(id3Tag(3, 0, uint32(len(frames23)+4), junk)) }},
		{"id3v2.4 with cover", func() io.ReadSeeker { return bytes.NewReader(id3Tag(4, 0, uint32(len(frames24)), frames24)) }},
		{"id3v2.4 unsynchronised", func() io.ReadSeeker {
			// The library counts declared frame sizes against the tag size,
			// so the tag size is the frame bytes before unsynchronisation.
			return bytes.NewReader(id3Tag(4, 0x80, uint32(len(frames24)), unsync(frames24), make([]byte, 64)))
		}},
		{"id3v2.2 with cover", func() io.ReadSeeker { return bytes.NewReader(id3Tag(2, 0, uint32(len(frames22)), frames22)) }},
		{"dsf", func() io.ReadSeeker { return bytes.NewReader(dsf) }},
		{"m4b", func() io.ReadSeeker { return bytes.NewReader(m4b) }},
		{"m4b with 4 GiB mdat first", func() io.ReadSeeker { return bigM4B() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := bareLibraryTags(t, tc.open())
			if want.Title != "Guards! Guards!" || want.Author != "Terry Pratchett" {
				t.Fatalf("fixture does not read as intended through the library: %+v", want)
			}
			got, err := readAudioTagsFrom(tc.open())
			if err != nil {
				t.Fatalf("readAudioTagsFrom: %v", err)
			}
			if got != want {
				t.Errorf("got %+v, bare library %+v", got, want)
			}
		})
	}

	t.Run("raw metadata identical", func(t *testing.T) {
		for _, open := range []func() io.ReadSeeker{
			func() io.ReadSeeker { return bytes.NewReader(id3Tag(3, 0, uint32(len(frames23)), frames23)) },
			func() io.ReadSeeker { return bytes.NewReader(m4b) },
		} {
			want, err := tag.ReadFrom(open())
			if err != nil {
				t.Fatal(err)
			}
			r := open()
			if err := checkAudioTagClaims(r); err != nil {
				t.Fatalf("check: %v", err)
			}
			got, err := tag.ReadFrom(r)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Raw(), want.Raw()) {
				t.Errorf("raw tags differ after the check:\n got %v\nwant %v", keys(got.Raw()), keys(want.Raw()))
			}
		}
	})

	t.Run("mdat is seeked, not read", func(t *testing.T) {
		f := bigM4B()
		if err := checkAudioTagClaims(f); err != nil {
			t.Fatalf("check: %v", err)
		}
		if f.read > 1<<20 {
			t.Errorf("check read %d MiB of a 4 GiB file", f.read>>20)
		}
		if f.off != 0 {
			t.Errorf("check left the reader at %d, want 0", f.off)
		}
	})
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMP4LibraryAtomsAreRead pins that every name in mp4LibraryAtoms is one
// the library reads into memory, so the list cannot hold a name it skips.
func TestMP4LibraryAtomsAreRead(t *testing.T) {
	for name := range mp4LibraryAtoms {
		m, err := tag.ReadFrom(bytes.NewReader(mp4File(atom(name, dataAtom(1, []byte("0123456789"))))))
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if _, ok := m.Raw()[name]; !ok {
			t.Errorf("%q is not read by the library", name)
		}
	}
}

// TestReadAudioTags_LibraryPanicIsAnError covers files the library parses
// but then panics on: it stores an MP4 atom by its data class, and its
// accessors assert the type they expect, so a text atom typed as a number or
// a picture makes Artist() or Title() panic. The read must fail like any other
// unreadable file instead of killing the scan.
func TestReadAudioTags_LibraryPanicIsAnError(t *testing.T) {
	cases := []struct {
		name string
		file []byte
	}{
		{"artist typed as a number", mp4File(atom("\xa9nam", dataAtom(1, []byte("Guards! Guards!"))), atom("\xa9ART", dataAtom(21, []byte{7})))},
		{"title typed as a picture", mp4File(atom("\xa9nam", dataAtom(13, []byte("not a jpeg"))))},
		{"album typed as a number", mp4File(atom("\xa9nam", dataAtom(1, []byte("Guards! Guards!"))), atom("\xa9alb", dataAtom(21, []byte{7})))},
		{"composer typed as a number", mp4File(atom("\xa9nam", dataAtom(1, []byte("Guards! Guards!"))), atom("\xa9wrt", dataAtom(21, []byte{7})))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAudioTagsFrom(bytes.NewReader(tc.file))
			if !errors.Is(err, errAudioTagReaderPanicked) {
				t.Errorf("expected errAudioTagReaderPanicked, got %v", err)
			}
		})
	}
}

// --- Ogg covers are capped by decoded size --------------------------------------

// oggPages splits packet across as many Ogg pages as it needs.
func oggPages(prefix, packet []byte) []byte {
	var out bytes.Buffer
	out.Write(prefix)
	seq := uint32(0)
	for first := true; ; first = false {
		var lacing []byte
		n := 0
		for len(lacing) < 255 {
			rest := len(packet) - n
			if rest < 255 {
				lacing = append(lacing, byte(rest))
				n += rest
				break
			}
			lacing = append(lacing, 255)
			n += 255
		}
		var b bytes.Buffer
		b.WriteString("OggS")
		b.WriteByte(0)
		flags := byte(0x01) // continued
		if first {
			flags = 0x02 // beginning of stream
		}
		b.WriteByte(flags)
		_ = binary.Write(&b, binary.LittleEndian, uint64(0))
		_ = binary.Write(&b, binary.LittleEndian, uint32(1))
		_ = binary.Write(&b, binary.LittleEndian, seq)
		_ = binary.Write(&b, binary.LittleEndian, uint32(0))
		b.WriteByte(byte(len(lacing)))
		b.Write(lacing)
		b.Write(packet[:n])
		page := b.Bytes()
		binary.LittleEndian.PutUint32(page[22:26], oggPageCRC(page))
		out.Write(page)
		packet = packet[n:]
		seq++
		if lacing[len(lacing)-1] != 255 {
			break
		}
	}
	return out.Bytes()
}

var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func oggPageCRC(p []byte) uint32 {
	var crc uint32
	for _, v := range p {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^v]
	}
	return crc
}

func TestReadAudioTags_OggCoverCapIsOnDecodedSize(t *testing.T) {
	cover := func(raw int) []byte {
		pic := "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(flacPicture(uint32(raw), make([]byte, raw)))
		return oggPages(nil, append([]byte("\x03vorbis"), vorbisComment("TITLE=Guards! Guards!", "ARTIST=Terry Pratchett", pic)...))
	}
	t.Run("12.5 MiB cover reads", func(t *testing.T) {
		tags, err := readAudioTagsFrom(bytes.NewReader(cover(12<<20 + 512<<10)))
		if err != nil {
			t.Fatalf("readAudioTagsFrom: %v", err)
		}
		if tags.Title != "Guards! Guards!" || tags.Author != "Terry Pratchett" {
			t.Errorf("got %+v", tags)
		}
	})
	t.Run("17 MiB cover is refused", func(t *testing.T) {
		if _, err := readAudioTagsFrom(bytes.NewReader(cover(17 << 20))); !errors.Is(err, errAudioTagTooLarge) {
			t.Errorf("expected errAudioTagTooLarge, got %v", err)
		}
	})
	t.Run("same raw limit as a FLAC picture block", func(t *testing.T) {
		// The largest picture a FLAC block can hold, carried in Ogg.
		raw := maxAudioTagStructureBytes - len(flacPicture(0, nil))
		if _, err := readAudioTagsFrom(bytes.NewReader(cover(raw))); err != nil {
			t.Errorf("largest FLAC sized picture refused in Ogg: %v", err)
		}
		if _, err := readAudioTagsFrom(bytes.NewReader(cover(raw + 1))); !errors.Is(err, errAudioTagTooLarge) {
			t.Errorf("one byte over: expected errAudioTagTooLarge, got %v", err)
		}
	})
}

// --- fuzz ---------------------------------------------------------------------

// FuzzCheckAudioTagClaims feeds arbitrary bytes to the pre-check. It must not
// panic, must leave the reader where it found it, and must stay within a
// small allocation of its own whatever the input declares.
func FuzzCheckAudioTagClaims(f *testing.F) {
	title := id3Frame23("TIT2", uint32(len(id3Text("T"))), [2]byte{}, id3Text("T"))
	for _, seed := range [][]byte{
		nil,
		id3Tag(3, 0, uint32(len(title)), title),
		id3Tag(4, 0xC0, 64, synchsafe(10), make([]byte, 6), id3Frame24("APIC", 20, [2]byte{0, 0x0F}, make([]byte, 24))),
		id3Tag(2, 0, 12, []byte("TT2\x00\x00\x02\x00T")),
		mp4File(atom("\xa9nam", dataAtom(1, []byte("T"))), mp4Freeform("com.apple.iTunes", "ASIN", "B003P2WO5E")),
		append([]byte("DSD \x1c\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), binary.LittleEndian.AppendUint64(nil, 28)...),
		flacFile(flacBlock{4, vorbisComment("TITLE=T")}),
		oggFile(append([]byte("OpusTags"), vorbisComment("TITLE=T")...)),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		r := bytes.NewReader(data)
		_ = checkAudioTagClaims(r)
		if pos, _ := r.Seek(0, io.SeekCurrent); pos != 0 {
			t.Fatalf("check left the reader at %d", pos)
		}
	})
}

// differentialPad is zero padding appended to every differential fuzz input,
// so a claim over maxAudioTagStructureBytes finds the bytes to read and the
// library's cost shows as bytes read instead of an early EOF.
const differentialPad = maxAudioTagStructureBytes + 1<<20

// FuzzAudioTagClaimsMatchLibrary runs the ID3v2, MP4 and DSF walks and the
// bare library on the same input and checks they agree:
//
//   - the walk passes: the library reads no more than the input plus
//     maxAudioTagTotalBytes;
//   - the walk refuses a structure as too large: the library reads more than
//     maxAudioTagStructureBytes, or fails on the file anyway;
//   - the walk fails on the structure: the library fails too.
//
// Policy limits (errAudioTagOverLimit) and sizes that only wrap on 32-bit
// builds (errClaimWraps) are refusals by design and are not compared.
func FuzzAudioTagClaimsMatchLibrary(f *testing.F) {
	title := id3Frame23("TIT2", uint32(len(id3Text("T"))), [2]byte{}, id3Text("T"))
	dsf := append([]byte("DSD \x1c\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), binary.LittleEndian.AppendUint64(nil, 28)...)
	for _, seed := range [][]byte{
		id3Tag(3, 0, uint32(len(title)), title),
		id3Tag(3, 0, uint32(len(title)+14), title, id3Frame23("ABCD", 0x01FFFFFF, [2]byte{}, nil)),
		[]byte("ID3\x04" + "0000000000000001"),
		id3Tag(3, 0, 4096, title, id3Frame23("APIC", 0xFFFFFFF0, [2]byte{}, id3APIC([]byte("tiny")))),
		id3Tag(4, 0, 4096, id3Frame24("GEOB", 10, [2]byte{0, 0x01}, synchsafe(0x0FFFFFFF))),
		id3Tag(4, 0x80, 4096, id3Frame24("APIC", 0x01FFFFFF, [2]byte{}, id3APIC([]byte{0xFF, 0, 1}))),
		id3Tag(3, 0x40, 64, binary.BigEndian.AppendUint32(nil, 6), make([]byte, 6), title),
		id3Tag(2, 0, 12, []byte("TT2\x00\x00\x02\x00T")),
		append(dsf, id3Tag(3, 0, uint32(len(title)), title)...),
		mp4File(atom("\xa9nam", dataAtom(1, []byte("T"))), mp4Freeform("com.apple.iTunes", "ASIN", "B003P2WO5E"), atom("covr", dataAtom(14, []byte("png")))),
		append(append([]byte{}, mp4Ftyp...), atomHeader("covr", 0x01FFFFFF)...),
		append(append([]byte{}, mp4Ftyp...), atom("----", atomHeader("data", 0))...),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 11 || len(data) > 4096 {
			return
		}
		if string(data[:4]) == "fLaC" || string(data[:4]) == "OggS" {
			return // the FLAC and Ogg walks are stricter than the library on purpose
		}
		if string(data[4:8]) != "ftyp" && string(data[:3]) != "ID3" && string(data[:4]) != "DSD " {
			return
		}
		open := func() *patchedFile {
			return &patchedFile{size: int64(len(data)) + differentialPad, patches: []filePatch{{0, data}}}
		}
		walkErr := checkAudioTagClaims(open())
		lib := open()
		libErr := func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("panic: %v", rec)
				}
			}()
			_, err = tag.ReadFrom(lib)
			return err
		}()
		switch {
		case walkErr == nil:
			if lib.read > int64(len(data))+maxAudioTagTotalBytes {
				t.Fatalf("walk passed, library read %d bytes", lib.read)
			}
		case errors.Is(walkErr, errAudioTagOverLimit), errors.Is(walkErr, errClaimWraps):
		case errors.Is(walkErr, errAudioTagTooLarge):
			if lib.read <= maxAudioTagStructureBytes && libErr == nil {
				t.Fatalf("walk refused (%v), library read the file in %d bytes", walkErr, lib.read)
			}
		default:
			if libErr == nil {
				t.Fatalf("walk failed (%v), library read the file", walkErr)
			}
		}
	})
}
