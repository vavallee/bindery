package importer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

const defaultNamingTemplate = "{Author}/{Title} ({Year})/{Title} - {Author}.{ext}"
const defaultAudiobookTemplate = "{Author}/{Title} ({Year})"

// Renamer moves and renames imported book files according to a naming template.
// Separate templates for ebook (per-file) and audiobook (per-folder) outputs.
type Renamer struct {
	template          string
	audiobookTemplate string
}

// NewRenamer creates a renamer with the given naming template.
// If template is empty, the default template is used.
func NewRenamer(template string) *Renamer {
	return NewRenamerWithAudiobook(template, "")
}

// NewRenamerWithAudiobook creates a renamer with separate ebook and audiobook
// templates. Empty strings fall back to the defaults.
func NewRenamerWithAudiobook(ebookTemplate, audiobookTemplate string) *Renamer {
	if ebookTemplate == "" {
		ebookTemplate = defaultNamingTemplate
	}
	if audiobookTemplate == "" {
		audiobookTemplate = defaultAudiobookTemplate
	}
	return &Renamer{template: ebookTemplate, audiobookTemplate: audiobookTemplate}
}

// DestPath computes the destination path for an ebook file.
func (r *Renamer) DestPath(rootFolder string, author *models.Author, book *models.Book, srcPath string) string {
	ext := strings.TrimPrefix(filepath.Ext(srcPath), ".")
	return filepath.Join(rootFolder, r.apply(r.template, author, book, ext))
}

// AudiobookDestDir computes the destination directory into which an audiobook
// download folder should be moved. The download's internal file structure is
// preserved inside (multi-part m4b/mp3 + cover + cue sheet stay together).
func (r *Renamer) AudiobookDestDir(rootFolder string, author *models.Author, book *models.Book) string {
	return filepath.Join(rootFolder, r.apply(r.audiobookTemplate, author, book, ""))
}

func (r *Renamer) apply(template string, author *models.Author, book *models.Book, ext string) string {
	year := ""
	if book.ReleaseDate != nil {
		year = fmt.Sprintf("%d", book.ReleaseDate.Year())
	}
	authorName := "Unknown Author"
	if author != nil {
		authorName = author.Name
	}
	result := template
	result = strings.ReplaceAll(result, "{Author}", sanitizePath(authorName))
	result = strings.ReplaceAll(result, "{SortAuthor}", sanitizePath(authorSortName(authorName)))
	result = strings.ReplaceAll(result, "{Title}", sanitizePath(book.Title))
	result = strings.ReplaceAll(result, "{Year}", year)
	result = strings.ReplaceAll(result, "{ext}", ext)
	return result
}

// MoveFile atomically copies a file to the destination and then removes the source.
// This handles cross-filesystem moves (e.g., NFS download dir → NFS library).
func MoveFile(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Try rename first (same filesystem, instant)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Cross-filesystem: copy then delete
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	// Verify copy
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat destination: %w", err)
	}
	if srcInfo.Size() != dstInfo.Size() {
		os.Remove(dst)
		return fmt.Errorf("size mismatch: src=%d dst=%d", srcInfo.Size(), dstInfo.Size())
	}

	// Remove source
	return os.Remove(src)
}

// MoveDir moves a directory (with all its contents) to the destination.
// Cross-filesystem safe: tries rename first, else recursive copy + delete.
// The destination directory must not already exist.
func MoveDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Fast path: same filesystem.
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Slow path: recursive copy, then verify, then remove.
	if err := copyDir(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("copy dir: %w", err)
	}
	return os.RemoveAll(src)
}

// copyDir recursively copies srcDir contents into dstDir, preserving the
// internal layout. dstDir will be created (including parents).
func copyDir(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func sanitizePath(s string) string {
	// Remove characters that are problematic in file paths
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "", "?", "",
		"\"", "", "<", "", ">", "", "|", "",
	)
	return strings.TrimSpace(replacer.Replace(s))
}

func authorSortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// DefaultNamingTemplate returns the default naming template for reference.
func DefaultNamingTemplate() string {
	return defaultNamingTemplate
}

// UniqueDir returns dst if nothing exists there; otherwise appends
// " (2)", " (3)", ... until a free path is found. MoveDir refuses an
// existing destination, so callers that import the same title twice
// (duplicate grab, reprocessed history, second edition) resolve the
// collision here before the move rather than failing silently.
func UniqueDir(dst string) string {
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		return dst
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", dst, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return dst
}

// NowYear returns the current year as a string, used as fallback.
func NowYear() string {
	return fmt.Sprintf("%d", time.Now().Year())
}
