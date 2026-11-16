package bookorbit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

const (
	// SourceID names BookOrbit in the import run and provenance tables it
	// shares with the Audiobookshelf importer.
	SourceID = "bookorbit"
	Label    = "BookOrbit"

	// libraryIDPrefix is put in front of a BookOrbit library id wherever the
	// shared import engine sees it. BookOrbit numbers its libraries from 1,
	// and the engine builds placeholder ids from the library id alone, so a
	// bare "17" could meet another source's library of the same name.
	libraryIDPrefix = "bookorbit-"

	// maxBooksPerLibrary stops a listing that never ends. It is far above
	// any real library and exists only so a server that keeps answering with
	// full pages cannot hold a run forever.
	maxBooksPerLibrary = 500000
)

// ErrAlreadyRunning is returned when a BookOrbit import is under way.
var ErrAlreadyRunning = errors.New("a BookOrbit import is already running")

// Config is one import run.
type Config struct {
	BaseURL    string
	APIKey     string
	PathRemap  string
	LibraryIDs []int64
	DryRun     bool
}

func (c Config) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("BookOrbit base URL is not set")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("BookOrbit API key is not set")
	}
	if len(c.LibraryIDs) == 0 {
		return errors.New("no BookOrbit library is selected")
	}
	return pathmap.Validate(strings.TrimSpace(c.PathRemap))
}

// API is the part of the BookOrbit client an import uses.
type API interface {
	ListLibraries(ctx context.Context) ([]Library, error)
	ListBookIDs(ctx context.Context, libraryID int64, page int) (*BookPage, error)
	GetBook(ctx context.Context, id int64) (*Book, error)
}

// Importer imports BookOrbit libraries through the Audiobookshelf import
// engine. This package only reads BookOrbit and describes each book the way
// the engine expects. Matching authors, books and series, registering files
// and recording runs are the engine's, so both importers dedupe the same way.
//
// A run has two parts. It first reads every selected library in full and
// checks every record, and only then hands the books to the engine. A server
// that answers in a shape the importer does not understand therefore fails the
// run before anything is written.
type Importer struct {
	engine    *abs.Importer
	newClient func(baseURL, apiKey string) (API, error)
	userAgent string

	startMu sync.Mutex // serialises Start and Run

	stateMu sync.Mutex
	state   *runState
}

type runState struct {
	client     API
	libraryIDs []int64

	once  sync.Once
	err   error
	items map[string][]abs.NormalizedLibraryItem
	stats map[string]abs.EnumerationStats

	mu      sync.Mutex
	reading string // what the read phase is doing, empty once it is over
}

// NewImporter wraps engine, which must be an importer of its own (not the one
// serving Audiobookshelf): the two keep separate progress, and the engine is
// given BookOrbit's enumerator here.
func NewImporter(engine *abs.Importer) *Importer {
	i := &Importer{engine: engine}
	i.newClient = func(baseURL, apiKey string) (API, error) {
		client, err := NewClient(baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		return client.WithUserAgent(i.userAgent), nil
	}
	engine.WithEnumerator(i.enumerate).WithRunSource(SourceID)
	return i
}

func (i *Importer) WithUserAgent(userAgent string) *Importer {
	i.userAgent = strings.TrimSpace(userAgent)
	return i
}

// EngineLibraryID is the id the import engine and its tables know a BookOrbit
// library by.
func EngineLibraryID(id int64) string {
	return libraryIDPrefix + strconv.FormatInt(id, 10)
}

// DisplayLibraryID turns an engine library id back into BookOrbit's own.
func DisplayLibraryID(engineID string) string {
	return strings.TrimPrefix(engineID, libraryIDPrefix)
}

func (i *Importer) engineConfig(cfg Config) abs.ImportConfig {
	ids := make([]string, 0, len(cfg.LibraryIDs))
	for _, id := range cfg.LibraryIDs {
		ids = append(ids, EngineLibraryID(id))
	}
	return abs.ImportConfig{
		SourceID:   SourceID,
		BaseURL:    strings.TrimSpace(cfg.BaseURL),
		APIKey:     strings.TrimSpace(cfg.APIKey),
		LibraryIDs: ids,
		PathRemap:  strings.TrimSpace(cfg.PathRemap),
		Label:      Label,
		Enabled:    true,
		DryRun:     cfg.DryRun,
	}
}

func (i *Importer) prepare(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if i.engine.Running() {
		return ErrAlreadyRunning
	}
	client, err := i.newClient(cfg.BaseURL, cfg.APIKey)
	if err != nil {
		return err
	}
	// The engine is idle, so no enumerator call can still be reading the
	// previous run's state.
	i.stateMu.Lock()
	i.state = &runState{client: client, libraryIDs: append([]int64(nil), cfg.LibraryIDs...)}
	i.stateMu.Unlock()
	return nil
}

// Start launches an import in the background. Progress reports on it.
func (i *Importer) Start(ctx context.Context, cfg Config) error {
	i.startMu.Lock()
	defer i.startMu.Unlock()
	if err := i.prepare(cfg); err != nil {
		return err
	}
	return i.engine.Start(ctx, i.engineConfig(cfg))
}

// Run imports and waits for the result.
func (i *Importer) Run(ctx context.Context, cfg Config) (*abs.ImportStats, error) {
	i.startMu.Lock()
	defer i.startMu.Unlock()
	if err := i.prepare(cfg); err != nil {
		return nil, err
	}
	return i.engine.Run(ctx, i.engineConfig(cfg))
}

// Progress is the engine's progress, with the message replaced while the
// importer is still reading BookOrbit and the engine has nothing to say yet.
func (i *Importer) Progress() abs.ImportProgress {
	progress := i.engine.Progress()
	i.stateMu.Lock()
	state := i.state
	i.stateMu.Unlock()
	if state != nil && progress.Running {
		state.mu.Lock()
		if state.reading != "" {
			progress.Message = state.reading
		}
		state.mu.Unlock()
	}
	return progress
}

// RunsPage lists recorded BookOrbit runs, newest first.
func (i *Importer) RunsPage(ctx context.Context, limit, offset int) ([]models.ABSImportRun, error) {
	return i.engine.RunsPage(ctx, limit, offset)
}

func (s *runState) setReading(msg string) {
	s.mu.Lock()
	s.reading = msg
	s.mu.Unlock()
}

// enumerate is the engine's source of books for one library. The first call
// of a run reads every selected library, so a problem in the last one stops
// the run before the first is written.
func (i *Importer) enumerate(ctx context.Context, engineLibraryID string, fn func(context.Context, abs.NormalizedLibraryItem) error) (abs.EnumerationStats, error) {
	i.stateMu.Lock()
	state := i.state
	i.stateMu.Unlock()
	if state == nil {
		return abs.EnumerationStats{}, errors.New("bookorbit import was started without a prepared run")
	}
	state.once.Do(func() {
		state.err = state.readAll(ctx)
		state.setReading("")
	})
	if state.err != nil {
		return abs.EnumerationStats{}, state.err
	}
	stats := state.stats[engineLibraryID]
	for _, item := range state.items[engineLibraryID] {
		if err := fn(ctx, item); err != nil {
			return stats, err
		}
		stats.ItemsNormalized++
	}
	return stats, nil
}

func (s *runState) readAll(ctx context.Context) error {
	s.items = make(map[string][]abs.NormalizedLibraryItem, len(s.libraryIDs))
	s.stats = make(map[string]abs.EnumerationStats, len(s.libraryIDs))

	s.setReading("reading BookOrbit libraries")
	libraries, err := s.client.ListLibraries(ctx)
	if err != nil {
		return describeError(err)
	}
	names := make(map[int64]string, len(libraries))
	for _, lib := range libraries {
		names[lib.ID] = lib.Name
	}
	for _, id := range s.libraryIDs {
		if _, ok := names[id]; !ok {
			return fmt.Errorf("BookOrbit library %d is not visible to this API key; check the key's library limit, or remove the library from the selection", id)
		}
	}

	for _, libraryID := range s.libraryIDs {
		if err := s.readLibrary(ctx, libraryID, names[libraryID]); err != nil {
			return describeError(err)
		}
	}
	return nil
}

func (s *runState) readLibrary(ctx context.Context, libraryID int64, name string) error {
	engineID := EngineLibraryID(libraryID)
	var stats abs.EnumerationStats

	// List first, so the count is known before any book is read.
	seen := make(map[int64]struct{})
	var ids []int64
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.client.ListBookIDs(ctx, libraryID, page)
		if err != nil {
			return err
		}
		stats.PagesScanned++
		for _, id := range result.IDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if len(result.IDs) == 0 || (page+1)*PageSize >= result.Total {
			break
		}
		if len(ids) > maxBooksPerLibrary {
			return fmt.Errorf("BookOrbit library %q lists more than %d books, refusing to continue", name, maxBooksPerLibrary)
		}
	}
	stats.ItemsSeen = len(ids)

	items := make([]abs.NormalizedLibraryItem, 0, len(ids))
	for idx, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if idx%10 == 0 {
			s.setReading(fmt.Sprintf("reading BookOrbit library %q: book %d of %d", name, idx+1, len(ids)))
		}
		book, err := s.client.GetBook(ctx, id)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				// Deleted between the listing and now.
				continue
			}
			return err
		}
		stats.ItemsDetailFetched++
		item, ok := normalize(book, engineID)
		if !ok {
			slog.Info("bookorbit import: book has no ebook or audiobook file, skipping it", "bookID", book.ID, "title", book.Title)
			continue
		}
		items = append(items, item)
	}
	s.items[engineID] = items
	s.stats[engineID] = stats
	return nil
}

// describeError turns the failures an admin can act on into a sentence that
// says what to do.
func describeError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("BookOrbit rejected the API key (it may be revoked, expired, or API keys may be turned off on the server): %w", err)
	case http.StatusForbidden:
		return fmt.Errorf("BookOrbit refused the request; the API key may not have access to a selected library: %w", err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("BookOrbit is rate limiting this address; wait a minute and try again: %w", err)
	}
	return err
}

// audioExtensions are the audio formats Bindery tracks as an audiobook file.
var audioExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true, ".flac": true, ".ogg": true, ".opus": true,
}

// normalize describes a BookOrbit book the way the import engine expects. It
// reports false for a book with no file Bindery tracks, which is not imported:
// the point of the import is to register files.
func normalize(book *Book, engineLibraryID string) (abs.NormalizedLibraryItem, bool) {
	item := abs.NormalizedLibraryItem{
		ItemID:          strconv.FormatInt(book.ID, 10),
		LibraryID:       engineLibraryID,
		MediaType:       "book",
		Title:           strings.TrimSpace(book.Title),
		Subtitle:        book.Subtitle,
		Description:     book.Description,
		Publisher:       book.Publisher,
		Language:        book.Language,
		ISBN:            firstNonEmpty(book.ISBN13, book.ISBN10),
		ASIN:            book.ASIN,
		PublishedDate:   book.PublishedDate,
		DurationSeconds: book.DurationSeconds,
		Narrators:       book.Narrators,
		Genres:          book.Genres,
		DetailFetched:   true,
	}
	if book.PublishedYear > 0 {
		item.PublishedYear = strconv.Itoa(book.PublishedYear)
	}
	for _, a := range book.Authors {
		author := abs.NormalizedAuthor{Name: strings.TrimSpace(a.Name)}
		if a.ID > 0 {
			author.ID = strconv.FormatInt(a.ID, 10)
		}
		if author.Name != "" {
			item.Authors = append(item.Authors, author)
		}
	}
	for _, sref := range book.Series {
		series := abs.NormalizedSeries{Name: sref.Name, Sequence: sref.Index}
		if sref.ID > 0 {
			series.ID = strconv.FormatInt(sref.ID, 10)
		}
		item.Series = append(item.Series, series)
	}

	var ebooks, audio []File
	for _, f := range book.Files {
		path := strings.TrimSpace(f.Path)
		if path == "" || strings.EqualFold(f.Role, "cover") {
			continue
		}
		// The extension decides, as it does for every other file Bindery
		// tracks. BookOrbit's own format label is not consulted: it has
		// formats Bindery does not track, and those are left alone.
		switch ext := strings.ToLower(filepath.Ext(path)); {
		case audioExtensions[ext]:
			audio = append(audio, f)
		case models.IsBookFilePath(path):
			ebooks = append(ebooks, f)
		}
	}
	if len(ebooks) == 0 && len(audio) == 0 {
		return item, false
	}

	if len(ebooks) > 0 {
		// The file BookOrbit calls primary leads, then EPUB, then the rest
		// in a fixed order so two runs agree.
		sort.SliceStable(ebooks, func(a, b int) bool {
			ra, rb := ebookRank(ebooks[a]), ebookRank(ebooks[b])
			if ra != rb {
				return ra < rb
			}
			return ebooks[a].Path < ebooks[b].Path
		})
		item.EbookPath = ebooks[0].Path
		item.EbookINO = fileID(ebooks[0])
		for _, f := range ebooks[1:] {
			item.ExtraEbookPaths = append(item.ExtraEbookPaths, f.Path)
		}
	}

	if len(audio) > 0 {
		sort.SliceStable(audio, func(a, b int) bool { return audio[a].Path < audio[b].Path })
		for idx, f := range audio {
			item.AudioFiles = append(item.AudioFiles, abs.NormalizedAudioFile{
				INO:       fileID(f),
				Index:     idx + 1,
				Path:      f.Path,
				Filename:  pathmap.ClientPathBase(f.Path),
				Extension: strings.ToLower(filepath.Ext(f.Path)),
				SizeBytes: f.SizeBytes,
			})
			item.SizeBytes += f.SizeBytes
		}
		item.Path = audiobookPath(audio)
		item.IsFile = len(audio) == 1
	}
	return item, true
}

func ebookRank(f File) int {
	switch {
	case strings.EqualFold(f.Role, "primary"):
		return 0
	case strings.EqualFold(filepath.Ext(f.Path), ".epub"):
		return 1
	}
	return 2
}

func fileID(f File) string {
	if f.ID > 0 {
		return strconv.FormatInt(f.ID, 10)
	}
	return ""
}

// audiobookPath is what Bindery registers as the book's audiobook: the file
// when there is one, and the folder holding them when there are several,
// which is how Bindery tracks a multi file audiobook from any source.
func audiobookPath(audio []File) string {
	if len(audio) == 1 {
		return audio[0].Path
	}
	dir := pathmap.ClientPathDir(audio[0].Path)
	for _, f := range audio[1:] {
		for dir != "" && !underDir(f.Path, dir) {
			parent := pathmap.ClientPathDir(dir)
			if parent == dir {
				return ""
			}
			dir = parent
		}
	}
	return dir
}

func underDir(path, dir string) bool {
	dir = strings.TrimRight(dir, `/\`)
	if len(path) <= len(dir) || !strings.HasPrefix(path, dir) {
		return false
	}
	return path[len(dir)] == '/' || path[len(dir)] == '\\'
}
