package importer

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// snapshotCorpus is the files of the snapshot tests in this package, in one
// library and one audiobook root, plus shapes the index has to get right:
// titles with no significant word, single word and numeric titles, non
// ASCII, inverted articles, and files directly under the root.
var snapshotCorpus = struct{ lib, ab []string }{
	lib: []string{
		"Jane Doe/First Book - Jane Doe.epub",
		"Jane Doe/Late Book - Jane Doe.epub",
		"Jane Doe/Title - Jane Doe.epub",
		"Jane Doe/Spoken - Jane Doe.m4b",
		"Jane Doe/Plain Book - Jane Doe.txt",
		"Jane Doe/Long Series Name 01/Long Series Name - Jane Doe.epub",
		"David Wong/Dungeon Crawler Carl.epub",
		"Dungeon Crawler Carl - Matt Dinniman.epub",
		"William Gibson/Burning Chrome (notes).txt",
		"William Gibson/Burning Chrome - William Gibson.epub",
		"Frank Herbert/a/Dune - Frank Herbert.epub",
		"Frank Herbert/b/Dune Messiah - Frank Herbert.epub",
		"Frank Herbert/Dune_ Deluxe Edition.epub",
		"J. K. Rowling/Harry Potter en de Steen der Wijzen (1997)/Harry Potter en de Steen der Wijzen - J. K. Rowling.epub",
		"J. K. Rowling/Harry Potter en het vervloekte kind (2016)/Harry Potter en het vervloekte kind - J. K. Rowling.epub",
		"Andy Weir/Project Hail Mary.epub",
		"Andy Weir/Project Hail Mary A Novel.epub",
		"George Orwell/1984 - George Orwell.epub",
		"Stephen King/It - Stephen King.epub",
		"Stephen King/The - Stephen King.epub",
		"V. E. Schwab/Darker Shade of Magic, A - V. E. Schwab.epub",
		"Franz Kafka/Der Prozeß - Franz Kafka.epub",
		"Unknown/Q.epub",
		"Unknown/A.epub",
		"Combined/Title - Jane Doe.mp3",
	},
	ab: []string{
		"Jane Doe/Title - Jane Doe.m4b",
		"Adrian Tchaikovsky/Dogs of War (2025)/Dogs of War.cue.txt",
		"Adrian Tchaikovsky/Dogs of War (2025)/Track 01.mp3",
		"Adrian Tchaikovsky/Dogs of War (2025)/Track 02.mp3",
		"TheFirstDefier/Defiance of the Fall 01/Defiance of the Fall_B094JZMCJX_LC_128_44100_Stereo.m4b",
		"TheFirstDefier/Defiance of the Fall 02/Defiance of the Fall_B099SH77S4_LC_64_22050_Stereo.m4b",
		"TheFirstDefier/Defiance of the Fall 10/Defiance of the Fall_B0C78G69ZW_LC_128_44100_Stereo.m4b",
		"TheFirstDefier/Defiance of the Fall 7/Defiance of the Fall 01.mp3",
		"TheFirstDefier/Defiance of the Fall 7/Defiance of the Fall 02.mp3",
		"TheFirstDefier/Defiance of the Fall 01.m4b",
		"Brandon Sanderson/Rhythm of War/Rhythm of War Part 1.mp3",
		"Brandon Sanderson/Rhythm of War/Part 2/Rhythm of War.mp3",
		"11. Track 11.mp3",
	},
}

var snapshotQueries = struct{ titles, authors []string }{
	titles: []string{
		"First Book", "Late Book", "Title", "Spoken", "Plain Book", "Long Series Name 1",
		"Long Series Name 3", "Dungeon Crawler Carl", "Some Other Title", "Burning Chrome",
		"Dune", "Dune Messiah", "Harry Potter", "Harry Potter en het vervloekte kind",
		"Project Hail Mary", "Project Hail Mary: A Novel", "1984", "It", "The", "A", "Q",
		"A Darker Shade of Magic", "Der Prozess", "Dogs of War", "Bear Head: Dogs of War, Book 2",
		"Defiance of the Fall", "Defiance of the Fall 2", "Defiance of the Fall, Book 10",
		"Defiance of the Fall 7", "Defiance of the Fall 17", "Rhythm of War (The Stormlight Archive, Book 4)",
		"Track 11", "11",
	},
	authors: []string{
		"", "Jane Doe", "Matt Dinniman", "David Wong", "William Gibson", "Frank Herbert",
		"J. K. Rowling", "Andy Weir", "George Orwell", "Stephen King", "V. E. Schwab",
		"Franz Kafka", "Adrian Tchaikovsky", "TheFirstDefier", "Brandon Sanderson",
	},
}

// TestLibrarySnapshot_IndexMatchesLinearPass holds the title index to the
// pass it replaced: for every query, the entries the predicate accepts out
// of the index's candidates are exactly, and in the same order, the entries
// it accepts out of every entry. FindExisting's answer is computed from that
// list, so equal lists mean equal answers; the answers are compared too.
func TestLibrarySnapshot_IndexMatchesLinearPass(t *testing.T) {
	libDir, abDir := t.TempDir(), t.TempDir()
	for _, f := range snapshotCorpus.lib {
		writeFile(t, filepath.Join(libDir, filepath.FromSlash(f)))
	}
	for _, f := range snapshotCorpus.ab {
		writeFile(t, filepath.Join(abDir, filepath.FromSlash(f)))
	}
	ctx := context.Background()
	snap := NewLibrarySnapshot(libDir, abDir)
	mediaTypes := []string{models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth, ""}
	compared, matched := 0, 0
	for _, mt := range mediaTypes {
		for _, title := range snapshotQueries.titles {
			for _, author := range snapshotQueries.authors {
				var indexed, linear []*libraryEntry
				for _, root := range snap.rootsForMediaType(mt) {
					idx, ok := snap.entriesFor(ctx, root)
					if !ok {
						t.Fatal("walk failed")
					}
					for _, i := range idx.candidates(title) {
						if e := &idx.entries[i]; entryMatchesQuery(e, title, author, mt) {
							indexed = append(indexed, e)
						}
					}
					for i := range idx.entries {
						if e := &idx.entries[i]; entryMatchesQuery(e, title, author, mt) {
							linear = append(linear, e)
						}
					}
				}
				compared++
				if len(linear) > 0 {
					matched++
				}
				if !slices.Equal(indexed, linear) {
					t.Errorf("%s %q by %q: index %v, linear pass %v", mt, title, author, paths(indexed), paths(linear))
					continue
				}
				got := snap.FindExisting(ctx, title, author, mt)
				want := ""
				if best := bestExistingFile(linear, title); best != nil && !titlesNameDifferentWorks(best.title, title) {
					want = best.path
				}
				if got != want {
					t.Errorf("%s %q by %q: FindExisting %q, linear pass answers %q", mt, title, author, got, want)
				}
			}
		}
	}
	// Guard the corpus itself: a query set that matches nothing proves
	// nothing about the index.
	if matched < 150 {
		t.Fatalf("only %d of %d queries matched any file; the corpus no longer exercises the index", matched, compared)
	}
	t.Logf("compared %d queries, %d with at least one matching file", compared, matched)
}

func paths(es []*libraryEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.path
	}
	return out
}

// BenchmarkLibrarySnapshotMemory reports the heap a walked 40,000 file
// snapshot holds (the rootIndex cost stated on its type), and the time of one
// lookup through the title index against the linear pass it replaced. Titles
// are two to four words from a 5,000 word vocabulary, so words recur across
// authors the way real titles do. Run with:
//
//	go test -run '^$' -bench LibrarySnapshotMemory ./internal/importer/
func BenchmarkLibrarySnapshotMemory(b *testing.B) {
	const authors, booksPerAuthor = 2000, 20
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: deterministic test data
	word := func() string { return fmt.Sprintf("w%04dx", rng.IntN(5000)) }
	libDir := b.TempDir()
	body := bookSized("x")
	type query struct{ title, author string }
	var queries []query
	for a := range authors {
		author := fmt.Sprintf("Author%04d Surname%04d", a, a)
		for k := range booksPerAuthor {
			words := make([]string, 2+rng.IntN(3))
			for i := range words {
				words[i] = word()
			}
			title := strings.Join(words, " ")
			book := filepath.Join(libDir, author, title, title+" - "+author+".epub")
			if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(book, body, 0o644); err != nil {
				b.Fatal(err)
			}
			if k == 0 {
				queries = append(queries, query{title, author})
			}
		}
	}
	ctx := context.Background()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	snap := NewLibrarySnapshot(libDir, "")
	idx, ok := snap.entriesFor(ctx, libDir)
	if !ok {
		b.Fatal("walk failed")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := float64(after.HeapAlloc) - float64(before.HeapAlloc)
	files := len(idx.entries)
	b.Logf("%d files, %d paths visited: %.1f MiB held, %.0f B/file", files, len(idx.visited), held/(1<<20), held/float64(files))

	b.Run("index", func(b *testing.B) {
		for i := range b.N {
			q := queries[i%len(queries)]
			if snap.FindExisting(ctx, q.title, q.author, models.MediaTypeEbook) == "" {
				b.Fatal("lookup missed")
			}
		}
	})
	b.Run("linear", func(b *testing.B) {
		for i := range b.N {
			q := queries[i%len(queries)]
			n := 0
			for j := range idx.entries {
				if entryMatchesQuery(&idx.entries[j], q.title, q.author, models.MediaTypeEbook) {
					n++
				}
			}
			if n == 0 {
				b.Fatal("lookup missed")
			}
		}
	})
	runtime.KeepAlive(snap)
}
