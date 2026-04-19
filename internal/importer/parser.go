package importer

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ParsedFile contains metadata extracted from a filename.
type ParsedFile struct {
	Title    string
	Author   string
	ISBN     string
	ASIN     string
	Year     string
	Format   string // epub, mobi, pdf, etc.
	FilePath string
}

var (
	// ISBN-13 pattern (with or without hyphens)
	isbnRe = regexp.MustCompile(`(?:978|979)[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d[-\s]?\d`)
	// ISBN-10 pattern
	isbn10Re = regexp.MustCompile(`\b\d{9}[\dXx]\b`)
	// ASIN pattern: 10-char alphanumeric starting with B (Amazon identifier)
	asinRe = regexp.MustCompile(`\bB[0-9A-Z]{9}\b`)
	// Year pattern
	yearRe = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	// Common separator patterns: "Title - Author" (spaces required around dash), "Title by Author"
	titleAuthorRe = regexp.MustCompile(`(?i)^(.+?)\s+[-–]\s+(.+?)$`)
	byAuthorRe    = regexp.MustCompile(`(?i)^(.+?)\s+by\s+(.+?)$`)
	// Dot/underscore word-separator pattern (common in release filenames)
	dotSepRe = regexp.MustCompile(`[._]+`)
	// Clean up patterns
	cleanRe = regexp.MustCompile(`[\[\(].*?[\]\)]`) // remove [brackets] and (parens)
	multiSp = regexp.MustCompile(`\s{2,}`)
)

// bookExtensions lists common ebook file extensions.
var bookExtensions = map[string]bool{
	".epub": true, ".mobi": true, ".azw3": true, ".azw": true,
	".pdf": true, ".djvu": true, ".cbr": true, ".cbz": true,
	".fb2": true, ".lit": true, ".txt": true, ".rtf": true,
	".mp3": true, ".m4a": true, ".m4b": true, ".flac": true, ".ogg": true,
}

// ParseFilename extracts book metadata from a filename or directory name.
func ParseFilename(path string) ParsedFile {
	p := ParsedFile{FilePath: path}

	ext := strings.ToLower(filepath.Ext(path))
	if bookExtensions[ext] {
		p.Format = strings.TrimPrefix(ext, ".")
	}

	// Work with the base name without extension
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))

	// Extract ASIN if present and strip it so it doesn't pollute the title
	if asin := asinRe.FindString(name); asin != "" {
		p.ASIN = asin
		name = strings.TrimSpace(asinRe.ReplaceAllString(name, " "))
	}

	// Extract ISBN if present
	if isbn := isbnRe.FindString(name); isbn != "" {
		p.ISBN = strings.ReplaceAll(strings.ReplaceAll(isbn, "-", ""), " ", "")
	} else if isbn := isbn10Re.FindString(name); isbn != "" {
		p.ISBN = isbn
	}

	// Extract year
	if y := yearRe.FindString(name); y != "" {
		p.Year = y
	}

	// Replace dots and underscores with spaces (common release filename separators)
	name = dotSepRe.ReplaceAllString(name, " ")

	// Clean brackets/parens content
	cleaned := cleanRe.ReplaceAllString(name, "")
	cleaned = multiSp.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)

	// Try "Title - Author" pattern
	if m := titleAuthorRe.FindStringSubmatch(cleaned); len(m) == 3 {
		p.Title = strings.TrimSpace(m[1])
		p.Author = strings.TrimSpace(m[2])
		return p
	}

	// Try "Title by Author" pattern
	if m := byAuthorRe.FindStringSubmatch(cleaned); len(m) == 3 {
		p.Title = strings.TrimSpace(m[1])
		p.Author = strings.TrimSpace(m[2])
		return p
	}

	// Fallback: entire cleaned name is the title
	p.Title = cleaned
	return p
}

// IsBookFile returns true if the path has a recognized book extension.
func IsBookFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return bookExtensions[ext]
}
