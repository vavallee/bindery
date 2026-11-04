// Command screenshots seeds a database with the demo library behind the README
// screenshots. capture.mjs runs it against a temporary database, boots the
// binary on that database and photographs the result: `make screenshots`.
//
// Names, years, ratings and work ids come from Open Library, whose catalogue
// data is CC0. Covers and avatars are not theirs: capture.mjs draws placeholders,
// so the image URLs below only carry the text to draw and are never fetched.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type demoBook struct {
	title   string
	olid    string
	year    int
	rating  float64
	ratings int
	wanted  bool
	audio   bool
}

type demoAuthor struct {
	name, sortName, olid string
	rating               float64
	ratings              int
	books                []demoBook
}

var library = []demoAuthor{
	{"Becky Chambers", "Chambers, Becky", "OL7387940A", 4.17, 223, []demoBook{
		{title: "The Long Way to a Small, Angry Planet", olid: "OL17716925W", year: 2014, rating: 4.09, ratings: 85},
		{title: "A Closed and Common Orbit", olid: "OL17897265W", year: 2016, rating: 4.25, ratings: 48},
		{title: "Record of a Spaceborn Few", olid: "OL17933495W", year: 2018, rating: 4.23, ratings: 26, wanted: true},
	}},
	{"N. K. Jemisin", "Jemisin, N. K.", "OL6575473A", 4.02, 144, []demoBook{
		{title: "The Fifth Season", olid: "OL17363125W", year: 2015, rating: 4.23, ratings: 22},
		{title: "The Obelisk Gate", olid: "OL17842279W", year: 2016, rating: 4.33, ratings: 15},
		{title: "The Stone Sky", olid: "OL17881901W", year: 2017, rating: 4.36, ratings: 14, wanted: true},
		{title: "The City We Became", olid: "OL20656799W", year: 2020, rating: 3.00, ratings: 4},
	}},
	{"Ursula K. Le Guin", "Le Guin, Ursula K.", "OL31353A", 4.03, 625, []demoBook{
		{title: "A Wizard of Earthsea", olid: "OL59798W", year: 1968, rating: 3.94, ratings: 121},
		{title: "The Left Hand of Darkness", olid: "OL59800W", year: 1969, rating: 4.27, ratings: 56},
		{title: "The Dispossessed", olid: "OL59863W", year: 1974, rating: 4.49, ratings: 37},
	}},
	{"Terry Pratchett", "Pratchett, Terry", "OL25712A", 4.15, 2244, []demoBook{
		{title: "The Colour of Magic", olid: "OL453657W", year: 1983, rating: 3.95, ratings: 160},
		{title: "Mort", olid: "OL453658W", year: 1987, rating: 4.21, ratings: 132},
		{title: "Guards! Guards!", olid: "OL453735W", year: 1989, rating: 4.34, ratings: 121},
		{title: "Going Postal", olid: "OL453733W", year: 2004, rating: 4.58, ratings: 64, wanted: true},
	}},
	{"Brandon Sanderson", "Sanderson, Brandon", "OL1394865A", 4.32, 1823, []demoBook{
		{title: "The Way of Kings", olid: "OL15358691W", year: 2010, rating: 4.51, ratings: 166},
		{title: "Words of Radiance", olid: "OL16813053W", year: 2014, rating: 4.69, ratings: 96},
		{title: "Oathbringer", olid: "OL17834026W", year: 2017, rating: 4.51, ratings: 90},
		{title: "Rhythm of War", olid: "OL20842226W", year: 2020, rating: 4.43, ratings: 44, audio: true},
		{title: "Wind and Truth", olid: "OL37577930W", year: 2024, rating: 4.40, ratings: 5, wanted: true},
	}},
	{"Andy Weir", "Weir, Andy", "OL7234434A", 4.27, 633, []demoBook{
		{title: "The Martian", olid: "OL17091839W", year: 2011, rating: 4.40, ratings: 337},
		{title: "Artemis", olid: "OL17837968W", year: 2017, rating: 3.68, ratings: 130},
		{title: "Project Hail Mary", olid: "OL21745884W", year: 2021, rating: 4.53, ratings: 190, audio: true},
	}},
}

// The queue: which book, its state, its size, and how long ago it was grabbed.
var queue = []struct {
	title    string
	state    models.DownloadState
	protocol string
	size     int64
	age      time.Duration
}{
	{"Wind and Truth", models.StateDownloading, "usenet", 1_180_000_000, 29 * time.Minute},
	{"The Stone Sky", models.StateDownloading, "torrent", 540_000_000, 46 * time.Minute},
	{"Going Postal", models.StateGrabbed, "usenet", 612_000_000, 62 * time.Minute},
	{"The Dispossessed", models.StateGrabbed, "usenet", 739_000_000, 75 * time.Minute},
	{"Record of a Spaceborn Few", models.StateGrabbed, "torrent", 498_000_000, 88 * time.Minute},
}

func placeholder(kind, title, byline string) string {
	return "https://placeholder.invalid/" + kind + "?" + url.Values{"title": {title}, "by": {byline}}.Encode()
}

// seed writes the library. A wanted book is released on a fixed day of now's
// month, like the earlier demo library, so the calendar page always has entries;
// those dates are the one part of the catalogue that is made up.
func seed(ctx context.Context, database *sql.DB, now time.Time) error {
	authors, books, downloads := db.NewAuthorRepo(database), db.NewBookRepo(database), db.NewDownloadRepo(database)
	ids := map[string]int64{}
	calendarDays := []int{4, 12, 19, 25}
	wanted := 0
	for _, da := range library {
		a := models.Author{
			ForeignID: da.olid, Name: da.name, SortName: da.sortName, ImageURL: placeholder("avatar", da.name, ""),
			Description: "Selected works in the demo library.", AverageRating: da.rating, RatingsCount: da.ratings,
			Monitored: true, MonitorMode: "all", MonitorNewItems: "none", MetadataProvider: "openlibrary",
		}
		if err := authors.Create(ctx, &a); err != nil {
			return fmt.Errorf("author %s: %w", da.name, err)
		}
		for _, book := range da.books {
			release := time.Date(book.year, time.June, 1, 12, 0, 0, 0, time.UTC)
			status, path := models.BookStatusImported, "/books/"+da.name+"/"+book.title+".epub"
			if book.wanted {
				release = time.Date(now.Year(), now.Month(), calendarDays[wanted%len(calendarDays)], 12, 0, 0, 0, time.UTC)
				status, path = models.BookStatusWanted, ""
				wanted++
			}
			mediaType := models.MediaTypeEbook
			if book.audio {
				mediaType = models.MediaTypeAudiobook
			}
			b := models.Book{
				ForeignID: book.olid, AuthorID: a.ID, Title: book.title, SortTitle: book.title, ReleaseDate: &release,
				ImageURL: placeholder("cover", book.title, da.name), AverageRating: book.rating, RatingsCount: book.ratings,
				Monitored: true, Status: status, FilePath: path, Language: "eng", MediaType: mediaType, MetadataProvider: "openlibrary",
			}
			if err := books.Create(ctx, &b); err != nil {
				return fmt.Errorf("book %s: %w", book.title, err)
			}
			ids[book.title] = b.ID
		}
	}
	for _, q := range queue {
		bookID, ok := ids[q.title]
		if !ok {
			return fmt.Errorf("queue: no book %q", q.title)
		}
		d := models.Download{
			GUID: "demo-" + q.title, BookID: &bookID, Title: releaseName(q.title), Size: q.size,
			Status: q.state, Protocol: q.protocol, Quality: "epub",
		}
		if err := downloads.Create(ctx, &d); err != nil {
			return fmt.Errorf("download %s: %w", q.title, err)
		}
		if err := downloads.SetGrabbedAt(ctx, d.ID, now.Add(-q.age)); err != nil {
			return fmt.Errorf("download %s: %w", q.title, err)
		}
	}
	return nil
}

func releaseName(title string) string {
	name := []rune(title)
	for i, r := range name {
		if r == ' ' {
			name[i] = '.'
		}
	}
	return string(name) + ".RETAIL.EPUB-DEMO"
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: screenshots <database path>")
	}
	database, err := db.Open(args[0])
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	return seed(context.Background(), database, time.Now())
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
