package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeSame(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSameFileContent(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB, several chunks
	writeSame(t, a, "equal.rar", big)
	writeSame(t, b, "equal.rar", big)
	diffTail := append(append([]byte{}, big...), 'x')
	diffTail[len(diffTail)-1] = 'y'
	writeSame(t, a, "tail.rar", append(append([]byte{}, big...), 'x'))
	writeSame(t, b, "tail.rar", diffTail)
	writeSame(t, a, "size.rar", big)
	writeSame(t, b, "size.rar", big[:len(big)-1])
	flipped := append([]byte{}, big...)
	flipped[len(flipped)/2] ^= 0xff
	writeSame(t, a, "mid.rar", big)
	writeSame(t, b, "mid.rar", flipped)
	writeSame(t, a, "empty.rar", nil)
	writeSame(t, b, "empty.rar", nil)
	writeSame(t, a, "onlya.rar", big)

	for name, want := range map[string]bool{
		"equal.rar": true, "tail.rar": false, "size.rar": false, "mid.rar": false,
		"empty.rar": true, "onlya.rar": false, "missing.rar": false,
	} {
		if got := sameFileContent(a, b, name); got != want {
			t.Errorf("sameFileContent(%s) = %v, want %v", name, got, want)
		}
	}
}

// The comparison must not hold a whole volume in memory: comparing two 32 MiB
// files allocates a few hundred KiB, not tens of MiB.
func TestSameFileContent_DoesNotBufferTheFile(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	big := bytes.Repeat([]byte{7}, 32<<20)
	writeSame(t, a, "v.rar", big)
	writeSame(t, b, "v.rar", big)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if !sameFileContent(a, b, "v.rar") {
		t.Fatal("identical files reported different")
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<20 {
		t.Errorf("sameFileContent allocated %d bytes comparing two 32 MiB files", got)
	}
}
