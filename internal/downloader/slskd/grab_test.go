package slskd

import (
	"strconv"
	"strings"
	"testing"
)

func TestGrabRoundTrip(t *testing.T) {
	g := Grab{User: "peer1", Files: twoFiles()}
	addr, err := EncodeGrab(g)
	if err != nil {
		t.Fatal(err)
	}
	if !IsGrabURL(addr) || strings.Contains(addr, "Dune") {
		t.Errorf("address %q should be opaque", addr)
	}
	back, err := DecodeGrab(addr)
	if err != nil {
		t.Fatal(err)
	}
	if back.User != "peer1" || len(back.Files) != 2 || back.Files[1].Size != 300 || back.Ext() != "mp3" {
		t.Errorf("round trip = %+v", back)
	}
	if DownloadFolder(7) != "bindery/7" {
		t.Errorf("folder = %q", DownloadFolder(7))
	}
}

func TestGrabRejectsHostileFilenames(t *testing.T) {
	bad := []string{
		`..\..\etc\passwd`,
		`@@u\Books\..\..\secret.epub`,
		`../../etc/passwd`,
		`/etc/passwd`,
		`C:\Windows\system.epub`,
		"a\x00b.epub",
		"line\nbreak.epub",
		"",
		strings.Repeat("a", 2000) + ".epub",
	}
	for _, name := range bad {
		if _, err := EncodeGrab(Grab{User: "u", Files: []BatchFile{{Filename: name, Size: 10}}}); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	// A dot inside a name is fine.
	if _, err := EncodeGrab(Grab{User: "u", Files: []BatchFile{{Filename: `@@u\a..b\c...epub`, Size: 10}}}); err != nil {
		t.Errorf("a name with dots was refused: %v", err)
	}
}

func TestGrabCaps(t *testing.T) {
	many := make([]BatchFile, MaxGrabFiles+1)
	for i := range many {
		many[i] = BatchFile{Filename: "f" + strconv.Itoa(i), Size: 1}
	}
	if _, err := EncodeGrab(Grab{User: "u", Files: many}); err == nil {
		t.Error("over the file cap was accepted")
	}
	if _, err := EncodeGrab(Grab{User: "u", Files: many[:MaxGrabFiles]}); err != nil {
		t.Errorf("exactly the file cap was refused: %v", err)
	}
	big := []BatchFile{{Filename: "a", Size: MaxGrabBytes}, {Filename: "b", Size: 1}}
	if _, err := EncodeGrab(Grab{User: "u", Files: big}); err == nil {
		t.Error("over the byte cap was accepted")
	}
	huge := []BatchFile{{Filename: "a", Size: 1 << 62}, {Filename: "b", Size: 1 << 62}, {Filename: "c", Size: 1 << 62}}
	if _, err := EncodeGrab(Grab{User: "u", Files: huge}); err == nil {
		t.Error("an overflowing total was accepted")
	}
	if _, err := EncodeGrab(Grab{User: "u", Files: []BatchFile{{Filename: "a", Size: 0}}}); err == nil {
		t.Error("a zero size file was accepted")
	}
	if _, err := EncodeGrab(Grab{User: "u", Files: []BatchFile{{Filename: "a", Size: 1}, {Filename: "a", Size: 1}}}); err == nil {
		t.Error("a duplicated file was accepted")
	}
	if _, err := EncodeGrab(Grab{User: "", Files: many[:1]}); err == nil {
		t.Error("an empty user was accepted")
	}
}

func TestDecodeGrabRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "http://x/y.nzb", GrabScheme + "!!!", GrabScheme + "e30", GrabScheme} {
		if _, err := DecodeGrab(s); err == nil {
			t.Errorf("%q decoded", s)
		}
	}
}
