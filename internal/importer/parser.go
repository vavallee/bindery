package importer

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ParsedFile contains metadata extracted from a filename.
type ParsedFile struct {
	Title        string
	Author       string
	Series       string
	SeriesNumber string
	ISBN         string
	ASIN         string
	Year         string
	Format       string // epub, mobi, pdf, etc.
	FilePath     string
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
	// "Author - [Series #N] - Title (tags)" — the de-facto Calibre/release naming
	// convention where the AUTHOR leads and the title trails the series tag.
	// The generic Title-Author splitter below gets this exactly backwards (it
	// would call the author the title), so detect it first (issue #1014).
	// Groups: 1=author, 2=series, 3=number, 4=title remainder.
	authorSeriesTitleRe = regexp.MustCompile(`(?i)^(.+?)\s+[-–]\s+\[([A-Za-z][^\]]*?),?\s*(?:book|vol(?:ume)?|part)?\.?\s*#?(\d+(?:\.\d+)?)\]\s+[-–]\s+(.+)$`)
	// Collapse runs of " - " separators left behind when an inline tag (e.g. a
	// stripped series bracket) sat between two dashes: "A -  - B" -> "A - B".
	dashCollapseRe = regexp.MustCompile(`(?:\s+[-–]\s+){2,}`)
	// Dot/underscore word-separator pattern (common in release filenames)
	dotSepRe = regexp.MustCompile(`[._]+`)
	// Clean up patterns
	cleanRe = regexp.MustCompile(`[\[\(].*?[\]\)]`) // remove [brackets] and (parens)
	multiSp = regexp.MustCompile(`\s{2,}`)

	// Series patterns extracted before bracket/paren stripping.
	// Series name must start with a letter to avoid matching ISBNs like [978-...] or years like (2012).
	// Matches: [Series Name #N], [Series Name, Book N], [Series Name Vol. N]
	seriesBracketRe = regexp.MustCompile(`(?i)\[([A-Za-z][^\]]*?),?\s*(?:book|vol(?:ume)?|part)?\.?\s*#?(\d+(?:\.\d+)?)\]`)
	// Matches: (Series Name #N), (Series Name, Book N)
	seriesParenRe = regexp.MustCompile(`(?i)\(([A-Za-z][^)]*?),?\s*(?:book|vol(?:ume)?|part)?\.?\s*#?(\d+(?:\.\d+)?)\)`)
	// Leading position number at start of base name: "01 - Title" or "1. Title"
	leadingNumRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*[-–.]\s+`)
	// "Series Book N - Title" or "Series Book N: Title" inline pattern (after dot/underscore expansion).
	// Captures: series name, book number, title (and optional author after another " - ").
	seriesBookInlineRe = regexp.MustCompile(`(?i)^(.+?)\s+(?:book|vol(?:ume)?|part)\.?\s*(\d+(?:\.\d+)?)\s*[-–:]\s*(.+)$`)
)

// bookExtensions lists common ebook file extensions.
var bookExtensions = map[string]bool{
	".epub": true, ".mobi": true, ".azw3": true, ".azw": true,
	".pdf": true, ".djvu": true, ".cbr": true, ".cbz": true,
	".fb2": true, ".lit": true, ".txt": true, ".rtf": true,
	".mp3": true, ".m4a": true, ".m4b": true, ".flac": true, ".ogg": true, ".opus": true,
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

	// "Author - [Series #N] - Title" convention (issue #1014): the author leads
	// and the title trails the series tag. Handle this before the generic
	// Title-Author split, which would otherwise invert them and glue the stray
	// post-bracket dash onto the author.
	if m := authorSeriesTitleRe.FindStringSubmatch(name); len(m) == 5 {
		p.Author = strings.TrimSpace(m[1])
		p.Series = strings.TrimSpace(m[2])
		p.SeriesNumber = strings.TrimSpace(m[3])
		titlePart := m[4]
		// Pull ISBN / year out of the title remainder, then strip tag groups.
		if isbn := isbnRe.FindString(titlePart); isbn != "" {
			p.ISBN = strings.ReplaceAll(strings.ReplaceAll(isbn, "-", ""), " ", "")
		} else if isbn := isbn10Re.FindString(titlePart); isbn != "" {
			p.ISBN = isbn
		}
		if y := yearRe.FindString(titlePart); y != "" {
			p.Year = y
		}
		titlePart = dotSepRe.ReplaceAllString(titlePart, " ")
		titlePart = cleanRe.ReplaceAllString(titlePart, "")
		titlePart = multiSp.ReplaceAllString(titlePart, " ")
		p.Title = strings.TrimSpace(titlePart)
		return p
	}

	// Extract series info from bracket/paren notation before cleanRe strips them.
	// Try the filename first, then the parent directory name (ABS-style layout).
	if m := seriesBracketRe.FindStringSubmatch(name); len(m) == 3 {
		p.Series = strings.TrimSpace(m[1])
		p.SeriesNumber = strings.TrimSpace(m[2])
		name = strings.TrimSpace(seriesBracketRe.ReplaceAllString(name, " "))
	} else if m := seriesParenRe.FindStringSubmatch(name); len(m) == 3 {
		p.Series = strings.TrimSpace(m[1])
		p.SeriesNumber = strings.TrimSpace(m[2])
		name = strings.TrimSpace(seriesParenRe.ReplaceAllString(name, " "))
	}
	if p.Series == "" {
		// Fall back to parent directory: covers ABS layout Author/Series Name, Book N/title.m4b
		dir := filepath.Base(filepath.Dir(path))
		if m := seriesBracketRe.FindStringSubmatch(dir); len(m) == 3 {
			p.Series = strings.TrimSpace(m[1])
			p.SeriesNumber = strings.TrimSpace(m[2])
		} else if m := seriesParenRe.FindStringSubmatch(dir); len(m) == 3 {
			p.Series = strings.TrimSpace(m[1])
			p.SeriesNumber = strings.TrimSpace(m[2])
		}
	}
	// If we have a series but no number yet, check if the base name leads with one:
	// e.g. ABS "01 - Dune.m4b" inside a "Dune Chronicles" folder.
	if p.Series != "" && p.SeriesNumber == "" {
		if m := leadingNumRe.FindStringSubmatch(name); len(m) == 2 {
			p.SeriesNumber = m[1]
			name = strings.TrimSpace(leadingNumRe.ReplaceAllString(name, ""))
		}
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
	// Collapse the empty "A -  - B" gap a stripped inline tag can leave behind.
	cleaned = dashCollapseRe.ReplaceAllString(cleaned, " - ")
	cleaned = strings.TrimSpace(cleaned)

	// Try "Series Book N - Title" or "Series Book N - Title - Author" inline pattern.
	// Must run before titleAuthorRe so "Book 2 -" isn't mis-split as title/author.
	if m := seriesBookInlineRe.FindStringSubmatch(cleaned); len(m) == 4 {
		if p.Series == "" {
			p.Series = strings.TrimSpace(m[1])
		}
		if p.SeriesNumber == "" {
			p.SeriesNumber = strings.TrimSpace(m[2])
		}
		rest := strings.TrimSpace(m[3])
		// The rest may itself be "Title - Author"
		if m2 := titleAuthorRe.FindStringSubmatch(rest); len(m2) == 3 {
			p.Title = strings.TrimSpace(m2[1])
			p.Author = strings.TrimSpace(m2[2])
		} else {
			p.Title = rest
		}
		return p
	}

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
