package downloader

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCheckCompletedPathVisibility_NilClientIsUnknown(t *testing.T) {
	got := CheckCompletedPathVisibility(t.Context(), nil, "/d", "/a", "")
	if got.Status != PathUnknown || got.Message != "" || got.Path != "" {
		t.Fatalf("got %+v, want a bare unknown", got)
	}
}

func TestCreatedOnGrabVisibility(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "ebooks-category")
	warn := PathVisibility{Status: PathNotVisible, Message: "original warning", Path: missing, RemotePath: "/remote/ebooks-category"}

	t.Run("a readable parent means the client can create the folder", func(t *testing.T) {
		got := createdOnGrabVisibility(warn)
		if got.Status != PathVisible || got.Path != missing || got.RemotePath != "/remote/ebooks-category" {
			t.Fatalf("got %+v", got)
		}
		if !strings.Contains(got.Message, parent) || !strings.Contains(got.Message, missing) {
			t.Fatalf("message %q should name the parent and the folder to be created", got.Message)
		}
	})
	t.Run("a missing parent keeps the warning untouched", func(t *testing.T) {
		w := warn
		w.Path = filepath.Join(parent, "nope", "ebooks-category")
		if got := createdOnGrabVisibility(w); got != w {
			t.Fatalf("got %+v, want the original warning", got)
		}
	})
	t.Run("a parent that is a file keeps the warning", func(t *testing.T) {
		file := filepath.Join(parent, "plainfile")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := warn
		w.Path = filepath.Join(file, "child")
		if got := createdOnGrabVisibility(w); got != w {
			t.Fatalf("got %+v, want the original warning", got)
		}
	})
	t.Run("no local path keeps the warning", func(t *testing.T) {
		w := warn
		w.Path = ""
		if got := createdOnGrabVisibility(w); got != w {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestStatRemappedPath_FileBlankAndRemapPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &models.DownloadClient{Type: "qbittorrent"}

	if got := statRemappedPath(client, file, "", ""); got.Status != PathVisible || !strings.Contains(got.Message, "completed-downloads path at") {
		t.Fatalf("a file where a folder is expected is still reachable: %+v", got)
	}
	if got := statRemappedPath(client, dir, "", ""); got.Status != PathVisible || !strings.Contains(got.Message, "completed-downloads folder at") {
		t.Fatalf("directory: %+v", got)
	}
	for _, blank := range []string{"", "   ", "."} {
		if got := statRemappedPath(client, blank, "", ""); got.Status != PathUnknown {
			t.Errorf("blank path %q = %+v, want unknown", blank, got)
		}
	}

	// The client's own remap wins over the global one.
	remapped := &models.DownloadClient{Type: "qbittorrent", PathRemap: "/remote:" + dir}
	if got := statRemappedPath(remapped, "/remote", "", "/remote:/does/not/exist"); got.Status != PathVisible || got.Path != dir {
		t.Fatalf("client remap should beat the global remap: %+v", got)
	}
	// With no client remap the global one applies.
	if got := statRemappedPath(client, "/remote", "", "/remote:"+dir); got.Status != PathVisible || got.Path != dir {
		t.Fatalf("global remap fallback: %+v", got)
	}
}

func TestStatRemappedPath_WarningTextVariants(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")

	t.Run("same path both sides without a remap", func(t *testing.T) {
		got := statRemappedPath(&models.DownloadClient{}, missing, "", "")
		if got.Status != PathNotVisible || !strings.Contains(got.Message, "mount the same storage at the same path") || strings.Contains(got.Message, "maps to") {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a configured remap that lands on nothing names both paths", func(t *testing.T) {
		client := &models.DownloadClient{PathRemap: "/remote:" + missing}
		got := statRemappedPath(client, "/remote", "", "")
		if got.Status != PathNotVisible || !strings.Contains(got.Message, `maps to`) || !strings.Contains(got.Message, missing) {
			t.Fatalf("got %+v", got)
		}
		if strings.Contains(got.Message, "to translate the client's path") {
			t.Fatalf("a client that already has a remap must not be told to add one: %q", got.Message)
		}
		if got.RemotePath != "/remote" {
			t.Fatalf("RemotePath = %q", got.RemotePath)
		}
	})
	t.Run("windows drive path says a remap is required", func(t *testing.T) {
		got := statRemappedPath(&models.DownloadClient{}, `Z:\Downloads`, "", "")
		if got.Status != PathNotVisible || !strings.Contains(got.Message, "Windows drive path") || !strings.Contains(got.Message, strconv.Quote(`Z:\Downloads:/downloads`)) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("expected directories are appended when known", func(t *testing.T) {
		got := statRemappedPath(&models.DownloadClient{}, missing, `the ebook download directory "/books"`, "")
		if !strings.HasSuffix(got.Message, `Bindery is configured to read from the ebook download directory "/books".`) {
			t.Fatalf("message = %q", got.Message)
		}
	})
}
