package downloader

import "testing"

// On a Windows Bindery filepath is Windows-aware, which is where #2902 bit:
// the remap hint came back as "\downloads\.Completed:..." and the under-check
// was case-sensitive although Windows paths are not.
func TestWindowsRemapHintKeepsDockerClientPOSIX(t *testing.T) {
	got := remapHint("/downloads/.Completed/books", `H:\Utorrent Downloads`)
	if want := `/downloads/.Completed:H:\Utorrent Downloads`; got != want {
		t.Fatalf("remapHint = %q, want %q", got, want)
	}
}

func TestWindowsPathIsAtOrUnderIgnoresCase(t *testing.T) {
	if !pathIsAtOrUnder(`H:\Utorrent Downloads\.Completed`, `h:\utorrent downloads`) {
		t.Fatal(`H:\Utorrent Downloads\.Completed should be under h:\utorrent downloads on Windows`)
	}
}
