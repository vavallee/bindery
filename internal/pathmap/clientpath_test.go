package pathmap

import "testing"

func TestCleanClientPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/downloads/.Completed/", "/downloads/.Completed"},
		{"/downloads//x/./y/../z", "/downloads/x/z"},
		{"/", "/"},
		// A POSIX client path never picks up a backslash, whatever the host.
		{"/downloads/a b", "/downloads/a b"},
		{`C:\Downloads\`, `C:\Downloads`},
		{`C:\Downloads\x\..\y`, `C:\Downloads\y`},
		{`C:\`, `C:\`},
		{`C:\..\..`, `C:\`},
		{"C:/Downloads/", "C:/Downloads"},
		{`\\nas\books\`, `\\nas\books`},
		{`\\nas\books\..\..\..`, `\\`},
		{"//nas/books/x/", "//nas/books/x"},
	}
	for _, c := range cases {
		if got := CleanClientPath(c.in); got != c.want {
			t.Errorf("CleanClientPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClientPathJoin(t *testing.T) {
	cases := []struct {
		base string
		elem []string
		want string
	}{
		{"/downloads/.Completed", []string{"Series/Vol 17.epub"}, "/downloads/.Completed/Series/Vol 17.epub"},
		{"/downloads/.Completed/", []string{"Vol 17.epub"}, "/downloads/.Completed/Vol 17.epub"},
		{"/downloads", []string{"a", "b.epub"}, "/downloads/a/b.epub"},
		{" /downloads ", []string{"x"}, "/downloads/x"},
		// qBittorrent on Windows reports forward slash file names under a
		// backslash save path; the result keeps the save path's style.
		{`C:\Downloads`, []string{"Series/Vol 17.epub"}, `C:\Downloads\Series\Vol 17.epub`},
		{"C:/Downloads", []string{"Series/Vol 17.epub"}, "C:/Downloads/Series/Vol 17.epub"},
		{`\\nas\books`, []string{"Series/a.epub"}, `\\nas\books\Series\a.epub`},
	}
	for _, c := range cases {
		if got := ClientPathJoin(c.base, c.elem...); got != c.want {
			t.Errorf("ClientPathJoin(%q, %q) = %q, want %q", c.base, c.elem, got, c.want)
		}
	}
}

func TestClientPathDirAndBase(t *testing.T) {
	cases := []struct{ in, dir, base string }{
		{"/downloads/.Completed/books", "/downloads/.Completed", "books"},
		{"/downloads/", "/", "downloads"},
		{"/", "/", ""},
		{"books", ".", "books"},
		{`C:\Torrents\books`, `C:\Torrents`, "books"},
		{`C:\Torrents`, `C:\`, "Torrents"},
		{`C:\`, `C:\`, ""},
		{"C:/Torrents/books/", "C:/Torrents", "books"},
	}
	for _, c := range cases {
		if got := ClientPathDir(c.in); got != c.dir {
			t.Errorf("ClientPathDir(%q) = %q, want %q", c.in, got, c.dir)
		}
		if got := ClientPathBase(c.in); got != c.base {
			t.Errorf("ClientPathBase(%q) = %q, want %q", c.in, got, c.base)
		}
	}
}

// TestClientJoinThenRemapReachesWindowsHost is #2902 at the pathmap level:
// qBittorrent in Docker reports "/downloads/.Completed", Bindery runs natively
// on Windows with the remap "/downloads:H:\Utorrent Downloads". Joined in the
// client's namespace, the file path hits the POSIX source rule and comes out
// as the Windows path the file really has.
//
// The second half pins down why the join must not be filepath.Join on a
// Windows host: that produces "\downloads\.Completed\...", which a POSIX
// source rule (matched with "/" only, case-sensitively) leaves untouched.
func TestClientJoinThenRemapReachesWindowsHost(t *testing.T) {
	r := Parse(`/downloads:H:\Utorrent Downloads`)
	joined := ClientPathJoin("/downloads/.Completed", "Mushoku Tensei/Vol 17.epub")
	if joined != "/downloads/.Completed/Mushoku Tensei/Vol 17.epub" {
		t.Fatalf("client join = %q", joined)
	}
	want := `H:\Utorrent Downloads\.Completed\Mushoku Tensei\Vol 17.epub`
	if got := r.Apply(joined); got != want {
		t.Fatalf("Apply(%q) = %q, want %q", joined, got, want)
	}

	// What filepath.Join produces on a Windows Bindery. Nothing matches it.
	hostJoined := `\downloads\.Completed\Mushoku Tensei\Vol 17.epub`
	if got := r.Apply(hostJoined); got != hostJoined {
		t.Fatalf("a backslashed client path unexpectedly matched the POSIX rule: %q", got)
	}
}
