package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedVersion is bumped whenever the generated data changes shape, so a cached
// seed directory from an older harness is rebuilt instead of reused.
const seedVersion = 6

// SeedSpec describes one synthetic library.
type SeedSpec struct {
	// Books is the number of book rows. Every other table is sized from it.
	Books int `json:"books"`
	// Seed fixes the random stream. The same spec always writes the same rows.
	Seed uint64 `json:"seed"`
	// Untracked leaves every on-disk book as a wanted row with no file, which
	// is the state a first library scan starts from. The file tree is the same
	// either way.
	Untracked bool `json:"untracked"`
	// LibraryDir is where the file tree lives. Tracked rows store absolute
	// paths under it.
	LibraryDir string `json:"libraryDir"`
}

// SeedManifest is written next to the database. The driver reads it to pick
// ids and search terms without opening the database itself.
type SeedManifest struct {
	Version   int      `json:"version"`
	Spec      SeedSpec `json:"spec"`
	DBPath    string   `json:"dbPath"`
	Authors   int      `json:"authors"`
	Books     int      `json:"books"`
	Editions  int      `json:"editions"`
	Series    int      `json:"series"`
	BookFiles int      `json:"bookFiles"`
	History   int      `json:"history"`
	Downloads int      `json:"downloads"`
	Queue     int      `json:"queue"`
	Blocklist int      `json:"blocklist"`
	TreeFiles int      `json:"treeFiles"`
	// SearchTerms are words that occur in titles and author names.
	SearchTerms []string `json:"searchTerms"`
	SeedSeconds float64  `json:"seedSeconds"`
}

const manifestName = "manifest.json"

var (
	firstNames = strings.Fields("Ada Alan Amara Anton Beatrix Boris Carmen Cyrus Dalia Dmitri Edith Elias Farah Felix Greta Hugo Imani Ivan Jasper Juno Kai Lena Lior Mara Milo Nadia Noor Odile Omar Petra Quinn Rafael Rosa Soren Tamsin Tobias Uma Viktor Wren Xavier Yara Zane Astrid Bram Celeste Dario Esme Fionn Gideon Hana")
	lastNames  = strings.Fields("Abbott Achebe Alvarez Bergstrom Blackwood Castellan Chen Dalgaard Okafor Dubois Eriksen Fairweather Fontaine Galloway Haldane Hargreave Ibarra Ishikawa Jovanovic Kalinowski Kestrel Lindqvist Lockhart Marchetti Mbeki Nakamura Novak Oyelaran Pemberton Quintero Rasmussen Rosenthal Sandoval Solberg Thackeray Tanaka Underhill Valdez Vasquez Whitlock Winterbourne Yamamoto Zielinski Ashdown Brightwater Coldharbour Dunmore Everhart Foxglove Greywater Holloway Ironwood Juniper Kingsley Larkspur Montclair Northcott Orrin Pendergast Quillfeather Ravenscroft Stormont Thornbury Umberfield Vance Wexford Yarrow Zephyrine Aldermoor Briarwood Crowhurst Dovecote Elmhurst Fenwick Gorsefield Hawkridge Inglenook Jessamy")
	adjectives = strings.Fields("Silent Burning Hollow Golden Forgotten Crimson Distant Broken Hidden Last First Frozen Wandering Iron Glass Paper Midnight Endless Shattered Sleeping Secret Savage Gentle Lost Ancient Drowned Scarlet Emerald Ashen Bitter Radiant Fallen Restless Quiet Winter Summer Autumn Painted Hungry Patient")
	nouns      = strings.Fields("Kingdom River Garden Tower Empire Orchard Harbor Library Lantern Compass Throne Archive Mirror Crown Bridge Forest Island Market Cathedral Engine Voyage Letter Oath Shadow Season Covenant Machine Witness Daughter Stranger Cartographer Alchemist Gardener Navigator Heretic Apprentice Smuggler Chronicle Requiem Almanac Inheritance Reckoning Labyrinth Observatory Menagerie Masquerade Pilgrimage Tempest Sanctuary Meridian Paradox Horizon Citadel Wilderness Lighthouse Carnival Exile Harvest Mountain Desert")
	places     = strings.Fields("Avalon Brighthollow Carcosa Dunmere Eastmarch Farrow Glasswater Highgarden Ironvale Jadeport Kestrelmoor Lowtide Mirefall Northreach Oakhaven Pinecrest Quarrytown Ravenna Saltmarsh Thornfield Umbria Valemount Westerly Yellowpine Zennor Ashgrove Blackmere Coppergate Deepwell Elderglen Frostholm Greenhollow Hartsford Icewind Kingsbridge Larkhaven Moonfall Nettlebank Oldcastle Stonehaven")
	genreNames = []string{"fantasy", "science fiction", "mystery", "romance", "historical", "thriller", "horror", "biography", "poetry", "young adult"}
	languages  = []string{"eng", "eng", "eng", "eng", "eng", "eng", "eng", "eng", "deu", "fra", "spa", "jpn"}
	loremWords = strings.Fields("a quiet account of what the valley remembered after the floods and of the people who stayed behind to rebuild what the water had taken from them told across three generations")
)

func authorName(i int) string {
	first := firstNames[i%len(firstNames)]
	last := lastNames[(i/len(firstNames))%len(lastNames)]
	if round := i / (len(firstNames) * len(lastNames)); round > 0 {
		return fmt.Sprintf("%s %s %d", first, last, round+1)
	}
	return first + " " + last
}

// bookTitle maps i to a title that is unique for every i below the size of the
// vocabulary product. The multiplier is coprime with the product, so the map
// is a permutation and neighbouring books get unrelated titles.
func bookTitle(i int) string {
	total := len(adjectives) * len(nouns) * len(places)
	k := (i*7919 + 104729) % total
	adj := adjectives[k%len(adjectives)]
	noun := nouns[(k/len(adjectives))%len(nouns)]
	place := places[k/(len(adjectives)*len(nouns))]
	title := fmt.Sprintf("The %s %s of %s", adj, noun, place)
	if i >= total {
		title = fmt.Sprintf("%s, Volume %d", title, i/total+1)
	}
	return title
}

func lorem(rng *rand.Rand, words int) string {
	out := make([]string, words)
	for i := range out {
		out[i] = loremWords[rng.IntN(len(loremWords))]
	}
	return strings.Join(out, " ")
}

// bookPlan is what the seeder decides about one book before it writes
// anything. The tree generator and the database writer both read it, so the
// files on disk and the rows always agree.
type bookPlan struct {
	title, author string
	authorIdx     int
	audio         bool
	onDisk        bool
	wanted        bool
	upcoming      bool
	path          string
}

func planBooks(spec SeedSpec) (authors int, plans []bookPlan) {
	authors = max(spec.Books/20, 5)
	rng := rand.New(rand.NewPCG(spec.Seed, 0x9e3779b97f4a7c15)) //nolint:gosec // G404: deterministic test data, not a secret
	plans = make([]bookPlan, spec.Books)
	for i := range plans {
		// Squaring the uniform draw skews books toward low author indexes, so
		// a few authors hold hundreds of books and most hold a handful.
		u := rng.Float64()
		p := bookPlan{title: bookTitle(i), authorIdx: int(u * u * float64(authors))}
		p.author = authorName(p.authorIdx)
		switch r := rng.IntN(100); {
		case spec.Untracked:
			// Every book has a file, so a size of n is n files to scan or
			// import.
			p.onDisk = true
			p.audio = i%8 == 7
		case r < 75:
			p.onDisk = true
			p.audio = rng.IntN(8) == 0
		case r < 85:
			p.wanted = true
		case r < 90:
			p.wanted, p.upcoming = true, true
		}
		if p.onDisk {
			ext := ".epub"
			if p.audio {
				ext = ".m4b"
			}
			p.path = filepath.Join(spec.LibraryDir, p.author, p.title, p.title+" - "+p.author+ext)
		}
		plans[i] = p
	}
	return authors, plans
}

// epubBytes builds a small archive Bindery's EPUB reader accepts: the mimetype
// entry first, a container pointing at the package file, a package file
// carrying title, creator and language, and one chapter. It is about 5 KB.
func epubBytes(title, author string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}
	files := []struct{ name, body string }{
		{"META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">urn:perf:` + xmlEscape(title) + `</dc:identifier><dc:title>` + xmlEscape(title) + `</dc:title><dc:creator>` + xmlEscape(author) + `</dc:creator><dc:language>en</dc:language></metadata><manifest><item id="c" href="c.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c"/></spine></package>`},
		{"OEBPS/c.xhtml", `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + xmlEscape(title) + `</title></head><body>` + strings.Repeat("<p>Performance fixture text, repeated so the file clears the size below which Bindery does not take a file for a book.</p>", 40) + `</body></html>`},
	}
	for _, f := range files {
		// Stored, not deflated: the repeated paragraph would compress to
		// nothing and the file has to stay above importer.MinPlausibleEbookBytes.
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// xmlEscape escapes text for an XML document. html.EscapeString produces only
// entities and numeric references that XML also defines.
func xmlEscape(s string) string { return html.EscapeString(s) }

// m4bBytes is an MPEG-4 file type box followed by an empty media data box. It
// is a file named and shaped like an audiobook container with no audio in it,
// which is all a scan needs.
func m4bBytes() []byte {
	return []byte("\x00\x00\x00\x1cftypM4B \x00\x00\x00\x00M4B mp42isom\x00\x00\x00\x08mdat")
}

// writeTree writes one dummy file per on-disk book and returns the count.
func writeTree(plans []bookPlan) (int, error) {
	n := 0
	for i := range plans {
		p := &plans[i]
		if !p.onDisk {
			continue
		}
		if err := writeBookFile(p.path, p.title, p.author, p.audio); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func writeBookFile(path, title, author string, audio bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := m4bBytes()
	if !audio {
		var err error
		if body, err = epubBytes(title, author); err != nil {
			return err
		}
	}
	return os.WriteFile(path, body, 0o644)
}

// loadManifest returns the manifest in dir when it was written for spec by
// this version of the seeder and its database is still there.
func loadManifest(dir string, spec SeedSpec) (*SeedManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, false
	}
	var m SeedManifest
	if json.Unmarshal(raw, &m) != nil || m.Version != seedVersion || m.Spec != spec {
		return nil, false
	}
	if _, err := os.Stat(m.DBPath); err != nil {
		return nil, false
	}
	return &m, true
}

// Seed writes the database for spec into dir, and the file tree into
// spec.LibraryDir unless it already holds one. A directory that already holds
// a matching seed is returned as is.
func Seed(ctx context.Context, dir string, spec SeedSpec, logf func(string, ...any)) (*SeedManifest, error) {
	if spec.Books <= 0 {
		return nil, errors.New("seed: books must be positive")
	}
	if m, ok := loadManifest(dir, spec); ok {
		logf("seed %s: reusing", dir)
		return m, nil
	}
	start := time.Now()
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	authorCount, plans := planBooks(spec)
	m := &SeedManifest{Version: seedVersion, Spec: spec, DBPath: filepath.Join(dir, "bindery.db"), Authors: authorCount, Books: spec.Books}

	// One tree serves the tracked and the untracked database of a size. The
	// marker records which plan wrote it.
	marker := filepath.Join(spec.LibraryDir, ".perf-tree")
	want := fmt.Sprintf("v%d books=%d seed=%d", seedVersion, spec.Books, spec.Seed)
	if got, err := os.ReadFile(marker); err != nil || string(got) != want {
		if err := os.RemoveAll(spec.LibraryDir); err != nil {
			return nil, err
		}
		n, err := writeTree(plans)
		if err != nil {
			return nil, fmt.Errorf("seed tree: %w", err)
		}
		if err := os.MkdirAll(spec.LibraryDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(marker, []byte(want), 0o644); err != nil {
			return nil, err
		}
		logf("seed %s: wrote %d files in %s", spec.LibraryDir, n, time.Since(start).Round(time.Millisecond))
	}
	for i := range plans {
		if plans[i].onDisk {
			m.TreeFiles++
		}
	}

	database, err := db.Open(m.DBPath)
	if err != nil {
		return nil, fmt.Errorf("seed open: %w", err)
	}
	if err := seedRows(ctx, database, spec, authorCount, plans, m); err != nil {
		_ = database.Close()
		return nil, err
	}
	// Fold the write ahead log into the main file so a plain file copy is a
	// complete database.
	if _, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("seed checkpoint: %w", err)
	}
	if err := database.Close(); err != nil {
		return nil, err
	}
	m.SearchTerms = []string{"silent", "garden", "avalon", "lindqvist", "amara", "crimson tower", "the last", "kingdom of", "zennor", "nakamura"}
	m.SeedSeconds = time.Since(start).Seconds()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o644); err != nil {
		return nil, err
	}
	logf("seed %s: %d books, %d history rows in %s", dir, m.Books, m.History, time.Since(start).Round(time.Millisecond))
	return m, nil
}

func seedRows(ctx context.Context, database *sql.DB, spec SeedSpec, authorCount int, plans []bookPlan, m *SeedManifest) error {
	rng := rand.New(rand.NewPCG(spec.Seed, 0xda942042e4dd58b5)) //nolint:gosec // G404: deterministic test data, not a secret
	authors, books, editions := db.NewAuthorRepo(database), db.NewBookRepo(database), db.NewEditionRepo(database)
	series, history, downloads, blocklist := db.NewSeriesRepo(database), db.NewHistoryRepo(database), db.NewDownloadRepo(database), db.NewBlocklistRepo(database)
	now := time.Now().UTC()

	authorIDs := make([]int64, authorCount)
	for i := range authorIDs {
		name := authorName(i)
		parts := strings.SplitN(name, " ", 2)
		a := models.Author{
			ForeignID: fmt.Sprintf("OL%07dA", i+1), Name: name, SortName: parts[1] + ", " + parts[0],
			Description: lorem(rng, 40), ImageURL: fmt.Sprintf("https://covers.invalid/a/%d.jpg", i+1),
			AverageRating: 3 + rng.Float64()*2, RatingsCount: rng.IntN(5000),
			Monitored: i%10 != 0, MonitorMode: "all", MonitorNewItems: "none", MetadataProvider: "openlibrary",
		}
		if err := authors.Create(ctx, &a); err != nil {
			return fmt.Errorf("seed author %d: %w", i, err)
		}
		authorIDs[i] = a.ID
	}

	type openSeries struct {
		id    int64
		count int
	}
	seriesByAuthor := map[int]*openSeries{}
	eventTypes := []string{models.HistoryEventDownloadFailed, models.HistoryEventBookRenamed, models.HistoryEventImportFailed, models.HistoryEventGrabbed}
	queueTarget := min(max(spec.Books/50, 5), 60)

	for i := range plans {
		p := &plans[i]
		release := time.Date(1950+rng.IntN(75), time.Month(1+rng.IntN(12)), 1+rng.IntN(28), 12, 0, 0, 0, time.UTC)
		status := models.BookStatusSkipped
		if p.wanted || p.onDisk {
			status = models.BookStatusWanted
		}
		if p.upcoming {
			release = now.AddDate(0, 0, rng.IntN(90)-30)
		}
		mediaType := models.MediaTypeEbook
		if p.audio {
			mediaType = models.MediaTypeAudiobook
		}
		b := models.Book{
			ForeignID: fmt.Sprintf("OL%08dW", i+1), AuthorID: authorIDs[p.authorIdx], Title: p.title, SortTitle: p.title,
			Description: lorem(rng, 60), ImageURL: fmt.Sprintf("https://covers.invalid/b/%d.jpg", i+1), ReleaseDate: &release,
			Genres:        []string{genreNames[rng.IntN(len(genreNames))], genreNames[rng.IntN(len(genreNames))]},
			AverageRating: 2.5 + rng.Float64()*2.5, RatingsCount: rng.IntN(20000),
			Monitored: status != models.BookStatusSkipped, Status: status, AnyEditionOK: true,
			Language: languages[rng.IntN(len(languages))], MediaType: mediaType, MetadataProvider: "openlibrary",
		}
		if err := books.Create(ctx, &b); err != nil {
			return fmt.Errorf("seed book %d: %w", i, err)
		}
		tracked := p.onDisk && !spec.Untracked
		if tracked {
			if err := books.AddBookFile(ctx, b.ID, mediaType, p.path); err != nil {
				return fmt.Errorf("seed book file %d: %w", i, err)
			}
			m.BookFiles++
		}

		for e := 0; e < 1+boolInt(i%3 == 0); e++ {
			isbn := fmt.Sprintf("978%010d", i*2+e)
			pages := 120 + rng.IntN(700)
			ed := models.Edition{
				ForeignID: fmt.Sprintf("OL%08dM", i*2+e+1), BookID: b.ID, Title: p.title, ISBN13: &isbn,
				Publisher: places[rng.IntN(len(places))] + " Press", PublishDate: &release, Format: editionFormats[e%len(editionFormats)],
				NumPages: &pages, Language: b.Language, IsEbook: e == 1, Monitored: true,
			}
			if err := editions.Upsert(ctx, &ed); err != nil {
				return fmt.Errorf("seed edition %d: %w", i, err)
			}
			m.Editions++
		}

		if i%3 == 0 {
			s := seriesByAuthor[p.authorIdx]
			if s == nil || s.count >= 5 {
				row := models.Series{ForeignID: fmt.Sprintf("perf-series-%d", m.Series+1), Title: fmt.Sprintf("%s Cycle %d", strings.TrimPrefix(p.title, "The "), m.Series+1), Description: lorem(rng, 20)}
				if err := series.Create(ctx, &row); err != nil {
					return fmt.Errorf("seed series: %w", err)
				}
				s = &openSeries{id: row.ID}
				seriesByAuthor[p.authorIdx] = s
				m.Series++
			}
			s.count++
			if err := series.LinkBook(ctx, s.id, b.ID, fmt.Sprint(s.count), true); err != nil {
				return fmt.Errorf("seed series link: %w", err)
			}
		}

		release2 := strings.ReplaceAll(p.author+" - "+p.title, " ", ".") + ".RETAIL.EPUB-PERF"
		events := i % 5
		if p.onDisk {
			for _, t := range []string{models.HistoryEventGrabbed, models.HistoryEventBookImported} {
				if err := history.Create(ctx, &models.HistoryEvent{BookID: &b.ID, EventType: t, SourceTitle: release2, Data: `{"indexer":"perf"}`}); err != nil {
					return fmt.Errorf("seed history: %w", err)
				}
				m.History++
			}
		}
		// Finished downloads are kept for 90 days by default, so the table
		// holds a recent slice of the library, not one row per book.
		if p.onDisk && !spec.Untracked && i%4 == 0 {
			d := models.Download{GUID: fmt.Sprintf("perf-imported-%d", i), BookID: &b.ID, Title: release2, Size: int64(1+rng.IntN(900)) << 20, Status: models.StateImported, Protocol: "torrent", Quality: "epub", MediaType: mediaType}
			if err := downloads.Create(ctx, &d); err != nil {
				return fmt.Errorf("seed download: %w", err)
			}
			m.Downloads++
		}
		for e := range events {
			if err := history.Create(ctx, &models.HistoryEvent{BookID: &b.ID, EventType: eventTypes[(i+e)%len(eventTypes)], SourceTitle: release2, Data: `{"indexer":"perf"}`}); err != nil {
				return fmt.Errorf("seed history: %w", err)
			}
			m.History++
		}
		if i%16 == 0 {
			d := models.Download{GUID: fmt.Sprintf("perf-failed-%d", i), BookID: &b.ID, Title: release2 + ".REPACK", Size: int64(1+rng.IntN(900)) << 20, Status: models.StateFailed, Protocol: "usenet", Quality: "epub", ErrorMessage: "aborted by the download client", MediaType: mediaType}
			if err := downloads.Create(ctx, &d); err != nil {
				return fmt.Errorf("seed download: %w", err)
			}
			m.Downloads++
		}
		if p.wanted && !p.upcoming && m.Queue < queueTarget {
			state := models.StateDownloading
			if m.Queue%3 == 0 {
				state = models.StateGrabbed
			}
			d := models.Download{GUID: fmt.Sprintf("perf-queue-%d", i), BookID: &b.ID, Title: release2, Size: int64(1+rng.IntN(900)) << 20, Status: state, Protocol: "usenet", Quality: "epub", MediaType: mediaType}
			if err := downloads.Create(ctx, &d); err != nil {
				return fmt.Errorf("seed queue: %w", err)
			}
			m.Downloads++
			m.Queue++
		}
		if i%20 == 0 {
			if err := blocklist.Create(ctx, &models.BlocklistEntry{BookID: &b.ID, GUID: fmt.Sprintf("perf-blocked-%d", i), Title: release2 + ".NUKED", Reason: "failed import", Scope: models.BlocklistScopeRelease}); err != nil {
				return fmt.Errorf("seed blocklist: %w", err)
			}
			m.Blocklist++
		}
	}
	return spreadTimestamps(ctx, database, now)
}

// spreadTimestamps moves history rows back over the past year and download
// rows back over the past 80 days, inside the default 90 day retention.
// The repositories stamp every row with the time of the insert, and a table
// whose rows all share one second does not exercise the date ordered indexes
// the History and Queue pages read.
func spreadTimestamps(ctx context.Context, database *sql.DB, now time.Time) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range []struct {
		table, column string
		window        time.Duration
	}{{"history", "created_at", 365 * 24 * time.Hour}, {"downloads", "added_at", 80 * 24 * time.Hour}} {
		var maxID int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM "+t.table).Scan(&maxID); err != nil { //nolint:gosec // G202: table name is one of two literals above
			return fmt.Errorf("seed spread %s: %w", t.table, err)
		}
		if err := spreadTable(ctx, tx, "UPDATE "+t.table+" SET "+t.column+" = ? WHERE id = ?", maxID, t.window, now); err != nil {
			return fmt.Errorf("seed spread %s: %w", t.table, err)
		}
	}
	return tx.Commit()
}

// spreadTable runs update, a two parameter statement taking a time and an id,
// for ids 1 to maxID, giving the highest id now and the lowest now minus
// window.
func spreadTable(ctx context.Context, tx *sql.Tx, update string, maxID int64, window time.Duration, now time.Time) error {
	stmt, err := tx.PrepareContext(ctx, update)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for id := int64(1); id <= maxID; id++ {
		age := time.Duration(float64(window) * float64(maxID-id) / float64(maxID))
		if _, err := stmt.ExecContext(ctx, now.Add(-age), id); err != nil {
			return err
		}
	}
	return nil
}

var editionFormats = []string{"Paperback", "Ebook"}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
