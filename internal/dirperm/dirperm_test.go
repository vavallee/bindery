package dirperm

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		mode uint32
	}{
		{"empty falls back to default", "", 0o750},
		{"whitespace falls back to default", "  ", 0o750},
		{"whitelisted with leading zero", "0755", 0o755},
		{"whitelisted without leading zero", "755", 0o755},
		{"historical default", "0750", 0o750},
		{"group-writable", "0770", 0o770},
		{"group-writable without leading zero", "770", 0o770},
		{"group-writable world-readable", "0775", 0o775},
		{"owner-only stays valid", "0700", 0o700},
		{"world-writable falls back", "0777", 0o750},
		{"world-writable group-closed falls back", "0707", 0o750},
		{"setgid spelling falls back", "2770", 0o750},
		{"unlisted octal falls back", "0774", 0o750},
		{"invalid text falls back", "abc", 0o750},
		{"decimal digits fall back", "099", 0o750},
		{"go-style 0o prefix falls back", "0o755", 0o750},
		{"negative falls back", "-1", 0o750},
		{"not a directory mode falls back", "0644", 0o750},
		{"zero falls back", "0", 0o750},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parse(tc.raw); uint32(got) != tc.mode {
				t.Fatalf("parse(%q) = %#o, want %#o", tc.raw, got, tc.mode)
			}
		})
	}
}

func TestLibrary(t *testing.T) {
	tests := []struct {
		name  string
		value string
		mode  uint32
	}{
		{"unset falls back to default", "", 0o750},
		{"whitelisted mode honoured", "0755", 0o755},
		{"whitelisted mode without leading zero", "750", 0o750},
		{"group-writable honoured", "0770", 0o770},
		{"group-writable world-readable honoured", "0775", 0o775},
		{"world-writable falls back", "0777", 0o750},
		{"unrecognised value falls back", "0o755", 0o750},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BINDERY_DIR_MODE", tc.value)
			if got := Library(); uint32(got) != tc.mode {
				t.Fatalf("Library() with BINDERY_DIR_MODE=%q = %#o, want %#o", tc.value, got, tc.mode)
			}
		})
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty is valid (use default)", "", true},
		{"whitelisted value", "0755", true},
		{"whitelisted without leading zero", "755", true},
		{"whitespace only", "  ", true},
		{"unrecognised value", "0o755", false},
		{"group-writable", "0770", true},
		{"group-writable world-readable", "0775", true},
		{"world-writable", "0777", false},
		{"unlisted octal", "0774", false},
		{"junk", "abc", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.raw); got != tc.want {
				t.Fatalf("Valid(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestMkdirAllIgnoresUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	for _, tc := range []struct {
		value string
		want  os.FileMode
	}{
		{"", 0o750},
		{"0700", 0o700},
		{"0755", 0o755},
		{"0770", 0o770},
		{"0775", 0o775},
		{"0777", 0o750},
	} {
		t.Run("MkdirAll "+tc.value, func(t *testing.T) {
			t.Setenv("BINDERY_DIR_MODE", tc.value)
			base := t.TempDir()
			deep := filepath.Join(base, "Author", "Book (2020)")
			if err := MkdirAll(deep); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{filepath.Join(base, "Author"), deep} {
				fi, err := os.Stat(d)
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode().Perm() != tc.want {
					t.Fatalf("%s = %#o, want %#o", d, fi.Mode().Perm(), tc.want)
				}
			}
		})
		t.Run("Mkdir "+tc.value, func(t *testing.T) {
			t.Setenv("BINDERY_DIR_MODE", tc.value)
			base := t.TempDir()
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := Mkdir(root, "Author"); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(filepath.Join(base, "Author"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != tc.want {
				t.Fatalf("mode = %#o, want %#o", fi.Mode().Perm(), tc.want)
			}
			if err := Mkdir(root, "Author"); !os.IsExist(err) {
				t.Fatalf("second Mkdir err = %v, want exists", err)
			}
		})
	}
}

func TestMkdirAllLeavesExistingDirsAlone(t *testing.T) {
	t.Setenv("BINDERY_DIR_MODE", "0770")
	base := t.TempDir()
	existing := filepath.Join(base, "lib")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(filepath.Join(existing, "Author")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(existing)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("existing dir changed to %#o", fi.Mode().Perm())
	}
}

func TestMkdirAllErrors(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(filepath.Join(file, "sub")); err == nil {
		t.Fatal("MkdirAll under a regular file should fail")
	}
	if err := MkdirAll(file); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("MkdirAll on an existing regular file = %v, want ENOTDIR", err)
	}
	t.Setenv("BINDERY_DIR_MODE", "0770")
	if err := MkdirAll(file); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("group-writable MkdirAll on a regular file = %v, want ENOTDIR", err)
	}
	if err := MkdirAll(filepath.Join(file, "sub")); err == nil {
		t.Fatal("group-writable MkdirAll under a regular file should fail")
	}
	want := os.MkdirAll("", 0o750)
	for _, mode := range []string{"", "0770"} {
		t.Setenv("BINDERY_DIR_MODE", mode)
		got := MkdirAll("")
		if got == nil || want == nil || got.Error() != want.Error() {
			t.Fatalf("MkdirAll(\"\") with mode %q = %v, want %v", mode, got, want)
		}
	}
	if err := MkdirAll(base); err != nil {
		t.Fatalf("existing dir should be fine: %v", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := Mkdir(root, "missing/child"); err == nil {
		t.Fatal("Mkdir with a missing parent should fail")
	}
}

func TestFixModeChmodFailureIsNotFatal(t *testing.T) {
	fixMode("x", 0o770,
		func(os.FileMode) error { return os.ErrPermission },
		func() (os.FileMode, error) { return os.ModeDir | 0o750, nil })
	fixMode("x", 0o770,
		func(os.FileMode) error { t.Fatal("chmod called"); return nil },
		func() (os.FileMode, error) { return 0, os.ErrNotExist })
}

func TestMkdirAllSetgidParentKept(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	t.Setenv("BINDERY_DIR_MODE", "0770")

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o770|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	// Skip where the filesystem does not hand setgid down to new directories.
	probe := filepath.Join(parent, "probe")
	if err := os.Mkdir(probe, 0o770); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(probe); fi.Mode()&os.ModeSetgid == 0 {
		t.Skip("filesystem does not inherit setgid on mkdir")
	}

	deep := filepath.Join(parent, "n1", "n2")
	if err := MkdirAll(deep); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := Mkdir(root, "n3"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(parent, "n1"), deep, filepath.Join(parent, "n3")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSetgid == 0 || fi.Mode().Perm() != 0o770 {
			t.Fatalf("%s = %v, want setgid and 0770", d, fi.Mode())
		}
	}
}

// Modes that are not group-writable must behave exactly like the plain os
// calls, so an operator who hardened with the umask is not loosened.
func TestStrictUmaskStillApplies(t *testing.T) {
	for _, tc := range []struct {
		umask int
		value string
		want  os.FileMode
	}{
		{0o077, "", 0o700},
		{0o027, "0755", 0o750},
		{0o077, "0750", 0o700},
	} {
		t.Run(tc.value+"/"+tc.want.String(), func(t *testing.T) {
			old := syscall.Umask(tc.umask)
			t.Cleanup(func() { syscall.Umask(old) })
			t.Setenv("BINDERY_DIR_MODE", tc.value)
			base := t.TempDir()
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := MkdirAll(filepath.Join(base, "a", "b")); err != nil {
				t.Fatal(err)
			}
			if err := Mkdir(root, "c"); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{"a", "a/b", "c"} {
				fi, err := os.Stat(filepath.Join(base, d))
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode().Perm() != tc.want {
					t.Fatalf("%s = %#o, want %#o", d, fi.Mode().Perm(), tc.want)
				}
			}
		})
	}
}
