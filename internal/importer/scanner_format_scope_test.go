package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// formatScopeFixture builds an in-memory Scanner with a real author and a
// media_type=both book that is Wanted in BOTH slots, plus separate ebook and
// audiobook library roots.
func formatScopeFixture(t *testing.T, libraryDir, audiobookDir string) (
	s *Scanner,
	book *models.Book,
	dlRepo *db.DownloadRepo,
	bookRepo *db.BookRepo,
	ctx context.Context,
) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	bookRepo = db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)
	dlRepo = db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	s = NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, audiobookDir, "", "", "")

	author := &models.Author{ForeignID: "OL-1885A", Name: "Jordan B. Peterson", SortName: "Peterson, Jordan B."}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book = &models.Book{
		ForeignID: "OL-1885W",
		AuthorID:  author.ID,
		Title:     "We Who Wrestle with God",
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeBoth,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return s, book, dlRepo, bookRepo, ctx
}

// TestTryImportInternal_AudiobookImportDoesNotCloseEbookGrab replays the #1885
// timeline from nathang21's logs, one auto-grab sweep on a media_type=both book:
//
//	17:42:05  ebook grabbed
//	17:42:19  audiobook grabbed
//	17:42:25  ebook import retry #1 fails — the torrent is still downloading,
//	          so its download path holds no book files yet
//	17:42:26  audiobook imports successfully
//	17:42:40  ebook import retry #2 — the already-in-library short-circuit fires
//	          because the AUDIOBOOK is now on disk, and the ebook download is
//	          marked StateImported and abandoned
//
// The ebook file never existed. Each title holds independent ebook/audiobook
// slots with separate pipelines, so an import of one format must never satisfy
// a pending download of the other: the ebook retry must take the ordinary
// no-book-files failure path, leaving the ebook slot empty, the download in the
// retryable StateImportFailed, and the book eligible for re-search (the
// scheduler treats importFailed as a dead grab — see
// TestWantedSearchQueue_SkipsInFlightGrabs).
//
// Both sub-cases matter: the release title may or may not carry a format token,
// which is the only per-download format signal a media_type=both grab has.
func TestTryImportInternal_AudiobookImportDoesNotCloseEbookGrab(t *testing.T) {
	for _, tc := range []struct {
		name    string
		quality string // Download.Quality — the format token parsed at grab time
	}{
		{"release title carries a format token", "epub"},
		{"release title carries no format token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			libraryDir := t.TempDir()
			audiobookDir := t.TempDir()
			s, book, dlRepo, bookRepo, ctx := formatScopeFixture(t, libraryDir, audiobookDir)

			// 17:42:19 — the audiobook grab lands a real m4b in its download dir.
			audioDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			audioDL := &models.Download{
				GUID:    "guid-1885-audiobook",
				Title:   "We Who Wrestle with God [M4B]",
				BookID:  &book.ID,
				Status:  models.StateCompleted,
				Quality: "m4b",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}

			// 17:42:05 — the ebook grab. Its torrent is still downloading at
			// retry time, so the download path exists but holds no book files.
			ebookDownloadDir := t.TempDir()
			ebookDL := &models.Download{
				GUID:             "guid-1885-ebook",
				Title:            "We Who Wrestle with God",
				BookID:           &book.ID,
				Status:           models.StateImportFailed, // retry #1 already failed
				ImportRetryCount: 1,
				Quality:          tc.quality,
			}
			if err := dlRepo.Create(ctx, ebookDL); err != nil {
				t.Fatal(err)
			}

			// 17:42:26 — the audiobook imports.
			s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)
			gotAudio, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if gotAudio.Status != models.StateImported {
				t.Fatalf("precondition: audiobook download status = %q, want %q", gotAudio.Status, models.StateImported)
			}
			if !s.alreadyImportedFormat(ctx, book, models.MediaTypeAudiobook) {
				t.Fatal("precondition: the audiobook must be tracked on disk for this race to be reproduced")
			}

			// 17:42:40 — ebook import retry #2.
			s.tryImportInternal(ctx, ebookDL, ebookDownloadDir, "", "", "", nil, nil)

			gotEbook, err := dlRepo.GetByGUID(ctx, ebookDL.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if gotEbook.Status == models.StateImported {
				t.Errorf("#1885 regression: ebook download marked %q because the AUDIOBOOK is in the library — "+
					"the epub never existed and the grab was silently abandoned", models.StateImported)
			}
			if gotEbook.Status != models.StateImportFailed {
				t.Errorf("ebook download status = %q, want %q (retryable no-book-files failure)",
					gotEbook.Status, models.StateImportFailed)
			}
			if !strings.Contains(gotEbook.ErrorMessage, "no book files found") {
				t.Errorf("ebook download error = %q, want the no-book-files message so the queue explains itself",
					gotEbook.ErrorMessage)
			}

			// The ebook slot must still be empty and wanted, so the next sweep
			// can re-search it.
			files, err := bookRepo.ListFiles(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if f.Format == models.MediaTypeEbook {
					t.Errorf("an ebook file was recorded at %q — nothing was ever imported", f.Path)
				}
			}
			refreshed, err := bookRepo.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !refreshed.NeedsEbook() {
				t.Errorf("book no longer needs an ebook (status=%q ebookFilePath=%q) — the ebook slot must stay wanted",
					refreshed.Status, refreshed.EbookFilePath)
			}
		})
	}
}

// TestTryImportInternal_EbookImportDoesNotCloseAudiobookGrab is #1885 with the
// formats swapped, which the format resolver made reachable on its own.
//
// indexer.ParseRelease records the FIRST token in its formatTokens order, and
// that order lists every ebook container ahead of every audio one. An audiobook
// shipped with the publisher's PDF booklet — "(Unabridged) [M4B + PDF]", an
// entirely ordinary release shape — is therefore grabbed with Quality "pdf". If
// the resolver takes that at face value on a media_type=both book whose ebook is
// already on disk, the audiobook grab consults the EBOOK slot, finds it filled,
// and closes itself out as imported while its torrent is still downloading.
func TestTryImportInternal_EbookImportDoesNotCloseAudiobookGrab(t *testing.T) {
	for _, title := range []string{
		"We Who Wrestle with God (Unabridged) [M4B + PDF]",
		"We Who Wrestle with God - Unabridged M4B/PDF booklet-SEEDPOOL",
	} {
		t.Run(title, func(t *testing.T) {
			libraryDir := t.TempDir()
			audiobookDir := t.TempDir()
			s, book, dlRepo, bookRepo, ctx := formatScopeFixture(t, libraryDir, audiobookDir)

			// The ebook is already imported and on disk.
			libEpub := filepath.Join(libraryDir, "we-who-wrestle-with-god.epub")
			if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
				t.Fatal(err)
			}

			// The audiobook grab: still downloading, so its download path holds
			// no book files at retry time. Quality is what ParseRelease keeps.
			audioDL := &models.Download{
				GUID:             "guid-1885-swapped",
				Title:            title,
				BookID:           &book.ID,
				Status:           models.StateImportFailed,
				ImportRetryCount: 1,
				Quality:          "pdf",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}

			s.tryImportInternal(ctx, audioDL, t.TempDir(), "", "", "", nil, nil)

			got, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == models.StateImported {
				t.Errorf("#1885 (formats swapped): audiobook download marked %q because the EBOOK is in the "+
					"library — the release's first parsed token was the PDF booklet, not its M4B", models.StateImported)
			}
			if got.Status != models.StateImportFailed {
				t.Errorf("audiobook download status = %q, want %q (retryable no-book-files failure)",
					got.Status, models.StateImportFailed)
			}

			files, err := bookRepo.ListFiles(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if f.Format == models.MediaTypeAudiobook {
					t.Errorf("an audiobook file was recorded at %q — nothing was ever imported", f.Path)
				}
			}
			refreshed, err := bookRepo.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !refreshed.NeedsAudiobook() {
				t.Errorf("book no longer needs an audiobook (status=%q audiobookFilePath=%q) — the audiobook "+
					"slot must stay wanted", refreshed.Status, refreshed.AudiobookFilePath)
			}
		})
	}
}

// TestTryImportInternal_FormatScopedShortCircuitStillFires is the counterweight
// to the test above: narrowing the already-in-library check to the download's
// own format must not disarm the #769 guard it exists for. A re-grabbed ebook
// whose files a prior import already moved into the library still short-circuits
// to StateImported instead of burning the retry budget.
func TestTryImportInternal_FormatScopedShortCircuitStillFires(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	s, book, dlRepo, bookRepo, ctx := formatScopeFixture(t, libraryDir, audiobookDir)

	libEpub := filepath.Join(libraryDir, "we-who-wrestle-with-god.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:    "guid-1885-regrab",
		Title:   "We Who Wrestle with God EPUB",
		BookID:  &book.ID,
		Status:  models.StateCompleted,
		Quality: "epub",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	// Empty download path: the files were moved to the library by the earlier
	// import (move mode) and the torrent was re-added via qBittorrent's 409
	// duplicate-add path.
	s.tryImportInternal(ctx, dl, t.TempDir(), "", "", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Errorf("#769 guard: re-grab of an already-imported EBOOK = %q, want %q", got.Status, models.StateImported)
	}
}

// TestCheckQbittorrentDownloads_1885_SiblingFormatDoesNotCloseGrab guards the
// same short-circuit on the qBittorrent poll path, where the download's format
// is recovered from the category the torrent sits under (the inverse of
// downloader.ResolveCategory).
//
// Scenario: a media_type=both book whose audiobook has just imported. The ebook
// torrent is complete but its content_path is momentarily unresolvable, which
// is the branch that closes a download out as already-imported. Because the
// torrent sits under the client's EBOOK category, the audiobook on disk must
// not satisfy it — the poll leaves the download alone to retry next cycle.
func TestCheckQbittorrentDownloads_1885_SiblingFormatDoesNotCloseGrab(t *testing.T) {
	saveRoot := t.TempDir()
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()

	// The audiobook imported moments ago; no ebook exists.
	libM4b := filepath.Join(audiobookDir, "book.m4b")
	if err := os.WriteFile(libM4b, []byte("m4b-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const torrentHash = "1885abcdef01885abcdef01885abcdef01885abc"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "We Who Wrestle with God",
				"state":        "stalledUP",
				"progress":     1.0,
				"save_path":    saveRoot,
				"category":     "books", // the client's EBOOK category
				"content_path": "",      // unresolvable this cycle
			}}
			_ = json.NewEncoder(w).Encode(torrents)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, audiobookDir, "", "", "")

	author := &models.Author{Name: "Jordan B. Peterson", ForeignID: "a-1885q", SortName: "Peterson, Jordan B."}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		AuthorID: author.ID, Title: "We Who Wrestle with God", ForeignID: "b-1885q",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeBoth,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, libM4b); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "qbit-1885", Type: "qbittorrent",
		Host: host, Port: port, Enabled: true,
		Category: "books", CategoryAudiobook: "audiobooks",
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-1885-qbit-ebook",
		Title:            "We Who Wrestle with God",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		BookID:           &book.ID,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status == models.StateImported {
		t.Errorf("#1885 regression: ebook torrent closed out as %q because the AUDIOBOOK is on disk", models.StateImported)
	}
	if got.Status != models.StateGrabbed {
		t.Errorf("download status = %q, want %q (untouched, retried next cycle)", got.Status, models.StateGrabbed)
	}
}

// TestDownloadFormat covers the format resolver's precedence: an explicit
// caller hint, then a single-format book's own media type, then the release
// token parsed at grab time — and "" when nothing answers.
func TestDownloadFormat(t *testing.T) {
	both := &models.Book{MediaType: models.MediaTypeBoth}
	ebookOnly := &models.Book{MediaType: models.MediaTypeEbook}
	audioOnly := &models.Book{MediaType: models.MediaTypeAudiobook}

	cases := []struct {
		name    string
		dl      *models.Download
		book    *models.Book
		hint    string
		want    string
		because string
	}{
		{"hint wins over the book's media type", &models.Download{}, ebookOnly, models.MediaTypeAudiobook,
			models.MediaTypeAudiobook, "a manual import declared the format outright"},
		{"hint wins over the release token", &models.Download{Quality: "m4b"}, both, models.MediaTypeEbook,
			models.MediaTypeEbook, "the caller's declaration beats inference"},
		{"single-format book beats a stray release token", &models.Download{Quality: "pdf"}, audioOnly, "",
			models.MediaTypeAudiobook, "an audiobook-only book has no ebook slot to fill, whatever the title says"},
		{"single-format ebook book", &models.Download{}, ebookOnly, "", models.MediaTypeEbook, ""},
		{"dual-format falls back to the release token (audio)", &models.Download{Quality: "M4B "}, both, "",
			models.MediaTypeAudiobook, "tokens are matched case- and space-insensitively"},
		{"dual-format falls back to the release token (ebook)", &models.Download{Quality: "epub"}, both, "",
			models.MediaTypeEbook, ""},
		{"dual-format with no token is unknown", &models.Download{}, both, "", "",
			"#1885: unknown must never be read as 'either format'"},
		{"unrecognised token is unknown", &models.Download{Quality: "web-dl"}, both, "", "", ""},
		// The release-token tier reads the whole title, not just the token
		// ParseRelease kept. Its formatTokens order puts every ebook container
		// ahead of every audio one, so these titles all arrive with an EBOOK
		// Quality — and consulting the ebook slot for an audiobook grab is
		// #1885 with the formats swapped.
		{
			"audiobook with a PDF booklet is not an ebook",
			&models.Download{Title: "Bob the Drag Queen - Harriet Tubman Live in Concert (Unabridged) [M4B + PDF]", Quality: "pdf"},
			both, "", models.MediaTypeAudiobook,
			"the M4B in the title outranks the parser's first-token pick",
		},
		{
			"audiobook with a PDF booklet, slash-separated",
			&models.Download{Title: "Andy Weir - Project Hail Mary (Unabridged) M4B/PDF booklet-SEEDPOOL", Quality: "pdf"},
			both, "", models.MediaTypeAudiobook, "",
		},
		{
			"an ebook bundle stays an ebook",
			&models.Download{Title: "Andy Weir - Project Hail Mary (retail) [EPUB+MOBI+AZW3]", Quality: "epub"},
			both, "", models.MediaTypeEbook, "several ebook containers are still one slot",
		},
		{
			"a title naming both kinds with nothing to break the tie is unknown",
			&models.Download{Title: "Andy Weir - Project Hail Mary [EPUB + MP3]", Quality: "epub"},
			both, "", "",
			"neither slot may be closed out on a release that could be filling either",
		},
		{
			"the word audiobook breaks the tie",
			&models.Download{Title: "Andy Weir - Project Hail Mary (audiobook) [MP3 + EPUB]", Quality: "epub"},
			both, "", models.MediaTypeAudiobook, "",
		},
		{
			"an Audible ASIN breaks the tie",
			&models.Download{Title: "Andy Weir - Project Hail Mary B08G9PRS1K [M4B, PDF]", Quality: "pdf"},
			both, "", models.MediaTypeAudiobook, "",
		},
		{
			"the recorded quality still counts when the title says nothing",
			&models.Download{Title: "Andy Weir - Project Hail Mary-SEEDPOOL", Quality: "m4b"},
			both, "", models.MediaTypeAudiobook, "",
		},
		{"garbage hint is ignored", &models.Download{Quality: "epub"}, both, "both", models.MediaTypeEbook, ""},
		{"nil book and no signals", &models.Download{}, nil, "", "", ""},
		{"nil download", nil, both, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := downloadFormat(c.dl, c.book, c.hint); got != c.want {
				msg := ""
				if c.because != "" {
					msg = " — " + c.because
				}
				t.Errorf("downloadFormat() = %q, want %q%s", got, c.want, msg)
			}
		})
	}
}
