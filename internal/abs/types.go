package abs

import (
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
)

type UserPermissions struct {
	AccessAllLibraries bool `json:"accessAllLibraries"`
}

type User struct {
	ID                  string          `json:"id"`
	Username            string          `json:"username"`
	Type                string          `json:"type"`
	LibrariesAccessible []string        `json:"librariesAccessible"`
	Permissions         UserPermissions `json:"permissions"`
}

type ServerSettings struct {
	Version string `json:"version"`
}

type AuthorizeResponse struct {
	User                 User           `json:"user"`
	UserDefaultLibraryID string         `json:"userDefaultLibraryId"`
	ServerSettings       ServerSettings `json:"serverSettings"`
	Source               string         `json:"Source"`
}

type LibraryFolder struct {
	ID       string `json:"id"`
	FullPath string `json:"fullPath"`
}

type Library struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	MediaType string          `json:"mediaType"`
	Icon      string          `json:"icon"`
	Provider  string          `json:"provider"`
	Folders   []LibraryFolder `json:"folders"`
}

type librariesResponse struct {
	Libraries []Library `json:"libraries"`
}

type Author struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Series struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SeriesSequence struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence"`
}

type AudioFileMetadata struct {
	Filename string `json:"filename"`
	Ext      string `json:"ext"`
}

type AudioFile struct {
	INO      string             `json:"ino"`
	Index    int                `json:"index"`
	Path     string             `json:"path"`
	Duration *float64           `json:"duration,omitempty"`
	Size     *int64             `json:"size,omitempty"`
	Metadata *AudioFileMetadata `json:"metadata,omitempty"`
}

type EbookFile struct {
	Path string `json:"path"`
	INO  string `json:"ino"`
}

type LibraryFileMetadata struct {
	Filename string `json:"filename"`
	Ext      string `json:"ext"`
	Path     string `json:"path"`
	RelPath  string `json:"relPath"`
}

// LibraryFile is one entry of a library item's libraryFiles array (returned
// by the expanded detail fetch). ABS lists every file it found for the item
// here, including ebooks it did NOT promote to media.ebookFile — a library
// with "Audiobooks only" enabled never promotes one, and marks the ebook
// isSupplementary instead.
type LibraryFile struct {
	INO             string              `json:"ino"`
	FileType        string              `json:"fileType"`
	IsSupplementary *bool               `json:"isSupplementary"`
	Metadata        LibraryFileMetadata `json:"metadata"`
}

type BookMetadata struct {
	Title         string           `json:"title"`
	Subtitle      string           `json:"subtitle"`
	Authors       []Author         `json:"authors"`
	Narrators     []string         `json:"narrators"`
	Series        []SeriesSequence `json:"series"`
	Genres        []string         `json:"genres"`
	PublishedYear string           `json:"publishedYear"`
	PublishedDate string           `json:"publishedDate"`
	Publisher     string           `json:"publisher"`
	Description   string           `json:"description"`
	Language      string           `json:"language"`
	ISBN          string           `json:"isbn"`
	ASIN          string           `json:"asin"`
	Explicit      bool             `json:"explicit"`
}

type BookMedia struct {
	LibraryItemID string       `json:"libraryItemId"`
	Metadata      BookMetadata `json:"metadata"`
	Duration      *float64     `json:"duration,omitempty"`
	Size          *int64       `json:"size,omitempty"`
	AudioFiles    []AudioFile  `json:"audioFiles"`
	EbookFile     *EbookFile   `json:"ebookFile,omitempty"`
}

type LibraryItem struct {
	ID           string        `json:"id"`
	LibraryID    string        `json:"libraryId"`
	FolderID     string        `json:"folderId"`
	Path         string        `json:"path"`
	RelPath      string        `json:"relPath"`
	IsFile       bool          `json:"isFile"`
	MediaType    string        `json:"mediaType"`
	Media        BookMedia     `json:"media"`
	LibraryFiles []LibraryFile `json:"libraryFiles,omitempty"`
}

type LibraryItemsPage struct {
	Results        []LibraryItem `json:"results"`
	Total          int           `json:"total"`
	Limit          int           `json:"limit"`
	Page           int           `json:"page"`
	MediaType      string        `json:"mediaType"`
	Minified       bool          `json:"minified"`
	CollapseSeries bool          `json:"collapseseries"`
}

type NormalizedAuthor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type NormalizedSeries struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence"`
}

type NormalizedAudioFile struct {
	INO             string  `json:"ino"`
	Index           int     `json:"index"`
	Path            string  `json:"path"`
	Filename        string  `json:"filename"`
	Extension       string  `json:"extension"`
	DurationSeconds float64 `json:"durationSeconds"`
	SizeBytes       int64   `json:"sizeBytes"`
}

type NormalizedLibraryItem struct {
	ItemID                  string                `json:"itemId"`
	LibraryID               string                `json:"libraryId"`
	MediaType               string                `json:"mediaType"`
	Path                    string                `json:"path"`
	RelPath                 string                `json:"relPath"`
	IsFile                  bool                  `json:"isFile"`
	Title                   string                `json:"title"`
	Subtitle                string                `json:"subtitle"`
	Description             string                `json:"description"`
	Publisher               string                `json:"publisher"`
	Language                string                `json:"language"`
	ISBN                    string                `json:"isbn"`
	ASIN                    string                `json:"asin"`
	PublishedYear           string                `json:"publishedYear"`
	PublishedDate           string                `json:"publishedDate"`
	Explicit                bool                  `json:"explicit"`
	DurationSeconds         float64               `json:"durationSeconds"`
	SizeBytes               int64                 `json:"sizeBytes"`
	Authors                 []NormalizedAuthor    `json:"authors"`
	Narrators               []string              `json:"narrators"`
	Series                  []NormalizedSeries    `json:"series"`
	Genres                  []string              `json:"genres"`
	AudioFiles              []NormalizedAudioFile `json:"audioFiles"`
	EbookPath               string                `json:"ebookPath,omitempty"`
	EbookINO                string                `json:"ebookIno,omitempty"`
	DetailFetched           bool                  `json:"detailFetched"`
	ResolvedAuthorForeignID string                `json:"resolvedAuthorForeignId,omitempty"`
	ResolvedAuthorName      string                `json:"resolvedAuthorName,omitempty"`
	ResolvedBookForeignID   string                `json:"resolvedBookForeignId,omitempty"`
	ResolvedBookTitle       string                `json:"resolvedBookTitle,omitempty"`
	EditedTitle             string                `json:"editedTitle,omitempty"`
}

// SupplementaryEbookFile returns the libraryFiles entry to use as the item's
// ebook when ABS didn't promote one to media.ebookFile — the case for every
// item in a library with "Audiobooks only" enabled, where ebooks are only
// listed as supplementary library files (#1565, Discussion #1556). Prefers
// .epub over other formats, mirroring ABS's own primary-ebook selection
// (BookScanner picks the first epub, else the first ebook file). Returns nil
// when the item has no ebook-typed library files.
func (i LibraryItem) SupplementaryEbookFile() *LibraryFile {
	var first *LibraryFile
	for idx := range i.LibraryFiles {
		lf := &i.LibraryFiles[idx]
		if lf.FileType != "ebook" || lf.Metadata.Path == "" {
			continue
		}
		if strings.EqualFold(strings.TrimPrefix(lf.Metadata.Ext, "."), "epub") {
			return lf
		}
		if first == nil {
			first = lf
		}
	}
	return first
}

func (i LibraryItem) DetailFetchReasons() []string {
	reasons := make([]string, 0, 5)
	if !i.IsFile {
		reasons = append(reasons, "folder-backed item")
	}
	if len(i.Media.AudioFiles) == 0 && i.Media.EbookFile == nil {
		reasons = append(reasons, "missing audio files")
	}
	if i.Media.Duration == nil || *i.Media.Duration <= 0 {
		reasons = append(reasons, "missing duration")
	}
	if i.Media.Size == nil || *i.Media.Size <= 0 {
		reasons = append(reasons, "missing size")
	}
	if len(i.Media.Metadata.Authors) == 0 {
		reasons = append(reasons, "missing authors")
	}
	for _, series := range i.Media.Metadata.Series {
		if strings.TrimSpace(series.ID) == "" || strings.TrimSpace(series.Name) == "" {
			reasons = append(reasons, "incomplete series metadata")
			break
		}
	}
	return reasons
}

func MergeLibraryItem(listItem, detailItem LibraryItem) LibraryItem {
	merged := detailItem
	if merged.ID == "" {
		merged.ID = listItem.ID
	}
	if merged.LibraryID == "" {
		merged.LibraryID = listItem.LibraryID
	}
	if merged.FolderID == "" {
		merged.FolderID = listItem.FolderID
	}
	if merged.Path == "" {
		merged.Path = listItem.Path
	}
	if merged.RelPath == "" {
		merged.RelPath = listItem.RelPath
	}
	if merged.MediaType == "" {
		merged.MediaType = listItem.MediaType
	}
	if merged.Media.LibraryItemID == "" {
		merged.Media.LibraryItemID = listItem.Media.LibraryItemID
	}
	if merged.Media.Metadata.Title == "" {
		merged.Media.Metadata.Title = listItem.Media.Metadata.Title
	}
	if merged.Media.Metadata.Subtitle == "" {
		merged.Media.Metadata.Subtitle = listItem.Media.Metadata.Subtitle
	}
	if len(merged.Media.Metadata.Authors) == 0 {
		merged.Media.Metadata.Authors = listItem.Media.Metadata.Authors
	}
	if len(merged.Media.Metadata.Narrators) == 0 {
		merged.Media.Metadata.Narrators = listItem.Media.Metadata.Narrators
	}
	if len(merged.Media.Metadata.Series) == 0 {
		merged.Media.Metadata.Series = listItem.Media.Metadata.Series
	}
	if len(merged.Media.Metadata.Genres) == 0 {
		merged.Media.Metadata.Genres = listItem.Media.Metadata.Genres
	}
	if merged.Media.Metadata.PublishedYear == "" {
		merged.Media.Metadata.PublishedYear = listItem.Media.Metadata.PublishedYear
	}
	if merged.Media.Metadata.PublishedDate == "" {
		merged.Media.Metadata.PublishedDate = listItem.Media.Metadata.PublishedDate
	}
	if merged.Media.Metadata.Publisher == "" {
		merged.Media.Metadata.Publisher = listItem.Media.Metadata.Publisher
	}
	if merged.Media.Metadata.Description == "" {
		merged.Media.Metadata.Description = listItem.Media.Metadata.Description
	}
	if merged.Media.Metadata.Language == "" {
		merged.Media.Metadata.Language = listItem.Media.Metadata.Language
	}
	if merged.Media.Metadata.ISBN == "" {
		merged.Media.Metadata.ISBN = listItem.Media.Metadata.ISBN
	}
	if merged.Media.Metadata.ASIN == "" {
		merged.Media.Metadata.ASIN = listItem.Media.Metadata.ASIN
	}
	if merged.Media.Duration == nil {
		merged.Media.Duration = listItem.Media.Duration
	}
	if merged.Media.Size == nil {
		merged.Media.Size = listItem.Media.Size
	}
	if len(merged.Media.AudioFiles) == 0 {
		merged.Media.AudioFiles = listItem.Media.AudioFiles
	}
	if merged.Media.EbookFile == nil {
		merged.Media.EbookFile = listItem.Media.EbookFile
	}
	if len(merged.LibraryFiles) == 0 {
		merged.LibraryFiles = listItem.LibraryFiles
	}
	if !merged.IsFile && listItem.IsFile {
		merged.IsFile = true
	}
	return merged
}

func NormalizeLibraryItem(item LibraryItem, detailFetched bool) NormalizedLibraryItem {
	out := NormalizedLibraryItem{
		ItemID:        item.ID,
		LibraryID:     item.LibraryID,
		MediaType:     item.MediaType,
		Path:          item.Path,
		RelPath:       item.RelPath,
		IsFile:        item.IsFile,
		Title:         item.Media.Metadata.Title,
		Subtitle:      item.Media.Metadata.Subtitle,
		Description:   textutil.CleanDescription(item.Media.Metadata.Description),
		Publisher:     item.Media.Metadata.Publisher,
		Language:      item.Media.Metadata.Language,
		ISBN:          item.Media.Metadata.ISBN,
		ASIN:          item.Media.Metadata.ASIN,
		PublishedYear: item.Media.Metadata.PublishedYear,
		PublishedDate: item.Media.Metadata.PublishedDate,
		Explicit:      item.Media.Metadata.Explicit,
		Genres:        append([]string(nil), item.Media.Metadata.Genres...),
		Narrators:     append([]string(nil), item.Media.Metadata.Narrators...),
		DetailFetched: detailFetched,
	}
	if item.Media.Duration != nil {
		out.DurationSeconds = *item.Media.Duration
	}
	if item.Media.Size != nil {
		out.SizeBytes = *item.Media.Size
	}
	if item.Media.EbookFile != nil {
		out.EbookPath = item.Media.EbookFile.Path
		out.EbookINO = item.Media.EbookFile.INO
	}
	// ABS only sets media.ebookFile when the library allows a primary ebook.
	// With "Audiobooks only" enabled the epub is still on disk and listed in
	// libraryFiles as a supplementary ebook — fall back to it so combined
	// audiobook+ebook items import both formats (#1565, Discussion #1556).
	if out.EbookPath == "" {
		if lf := item.SupplementaryEbookFile(); lf != nil {
			out.EbookPath = lf.Metadata.Path
			out.EbookINO = lf.INO
		}
	}
	out.Authors = make([]NormalizedAuthor, 0, len(item.Media.Metadata.Authors))
	for _, author := range item.Media.Metadata.Authors {
		out.Authors = append(out.Authors, NormalizedAuthor(author))
	}
	out.Series = make([]NormalizedSeries, 0, len(item.Media.Metadata.Series))
	for _, series := range item.Media.Metadata.Series {
		out.Series = append(out.Series, NormalizedSeries(series))
	}
	out.AudioFiles = make([]NormalizedAudioFile, 0, len(item.Media.AudioFiles))
	for _, file := range item.Media.AudioFiles {
		audio := NormalizedAudioFile{
			INO:   file.INO,
			Index: file.Index,
			Path:  file.Path,
		}
		if file.Metadata != nil {
			audio.Filename = file.Metadata.Filename
			audio.Extension = file.Metadata.Ext
		}
		if file.Duration != nil {
			audio.DurationSeconds = *file.Duration
		}
		if file.Size != nil {
			audio.SizeBytes = *file.Size
		}
		out.AudioFiles = append(out.AudioFiles, audio)
	}
	return out
}
