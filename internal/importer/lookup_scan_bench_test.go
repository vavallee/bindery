package importer

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// This file only uses API that predates catalogueIndex, so it can be copied
// onto an older main to measure the same benchmark there.

// genCommonWords are the head of the generated title vocabulary. Sampled with
// a Zipf distribution, they give the postings lists the long common tail
// real titles have ("night", "king", "book" and series numbers).
var genCommonWords = []string{
	"night", "shadow", "king", "war", "book", "one", "dark", "blood", "house",
	"fire", "queen", "death", "world", "last", "lost", "girl", "secret", "city",
	"stars", "road", "time", "black", "white", "red", "dragon", "magic", "heart",
	"light", "sea", "storm", "iron", "song", "winter", "summer", "game", "crown",
	"empire", "ghost", "witch", "murder", "garden", "river", "island", "two",
	"three", "first", "little", "long", "wolf", "sword", "silver", "golden",
	"truth", "lady", "man", "woman", "children", "end", "dead", "kingdom",
	"master", "edge", "fall", "rise", "dawn", "bones", "ash", "glass", "stone",
	"thief", "promise", "letters", "daughter", "son", "brother", "sister",
}

var genSyllables = []string{
	"ka", "lo", "mir", "tha", "ren", "vos", "eli", "dra", "quin", "sar", "tor",
	"bel", "nym", "ash", "cor", "fen", "gal", "hed", "ix", "jor", "mal", "nor",
	"or", "pel", "ryn", "sil", "ur", "vel", "wen", "zan",
}

var genFirstNames = []string{
	"Andy", "Gillian", "John", "Debbie", "James", "Ursula", "Terry", "Octavia",
	"Brandon", "Robin", "N. K.", "J. R. R.", "Ann", "Mary", "Stephen", "Agatha",
	"Jo", "Åsa", "Łukasz", "Haruki", "Liu", "Isabel", "Gabriel", "Chimamanda",
	"Neil", "Patrick", "George R. R.", "Becky", "Martha", "Ted",
}

var genLastNames = []string{
	"Weir", "Flynn", "Grisham", "Macomber", "Patterson", "Le Guin", "Pratchett",
	"Butler", "Sanderson", "Hobb", "Jemisin", "Tolkien", "Leckie", "Shelley",
	"King", "Christie", "Nesbø", "Larsson", "Orbitowski", "Murakami", "Cixin",
	"Allende", "García Márquez", "Adichie", "Gaiman", "Rothfuss", "Martin",
	"Chambers", "Wells", "Chiang", "Müller", "Brontë",
}

// genVocabulary returns the title vocabulary: the common words first, then
// syllable compounds, so a Zipf draw lands on the common words most often.
func genVocabulary(rng *rand.Rand, size int) []string {
	out := append([]string(nil), genCommonWords...)
	seen := make(map[string]bool, size)
	for _, w := range out {
		seen[w] = true
	}
	for len(out) < size {
		n := 2 + rng.Intn(2)
		var b strings.Builder
		for range n {
			b.WriteString(genSyllables[rng.Intn(len(genSyllables))])
		}
		if w := b.String(); !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func genTitleCase(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

func genASIN(rng *rand.Rand) string {
	const alnum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := []byte("B0")
	for range 8 {
		b = append(b, alnum[rng.Intn(len(alnum))])
	}
	return string(b)
}

// genCatalogue is a generated books and authors catalogue. Book and author
// IDs are positions plus one, matching what a fresh database hands out when
// the rows are inserted in order.
type genCatalogue struct {
	authors []models.Author
	books   []models.Book
}

// generateCatalogue builds n books whose titles cover what the matcher has
// to deal with: plain phrases, leading and comma suffixed articles, numbered
// series volumes with and without subtitles, one word and numeric titles,
// colon subtitles, accented and CJK titles, exact duplicates (ties) and
// duplicate ASINs.
func generateCatalogue(n int, seed int64) genCatalogue {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // G404: deterministic test data
	vocab := genVocabulary(rng, 4000)
	zipf := rand.NewZipf(rng, 1.15, 4, uint64(len(vocab)-1))
	word := func() string { return vocab[zipf.Uint64()] }
	phrase := func(k int) string {
		ws := make([]string, k)
		for i := range ws {
			ws[i] = genTitleCase(word())
		}
		return strings.Join(ws, " ")
	}

	nAuthors := n/8 + 1
	cat := genCatalogue{authors: make([]models.Author, nAuthors)}
	for i := range cat.authors {
		name := genFirstNames[rng.Intn(len(genFirstNames))] + " " + genLastNames[rng.Intn(len(genLastNames))]
		if i%3 == 0 {
			name += " " + genTitleCase(vocab[len(genCommonWords)+rng.Intn(len(vocab)-len(genCommonWords))])
		}
		cat.authors[i] = models.Author{ID: int64(i + 1), Name: name, SortName: name, ForeignID: fmt.Sprintf("ga-%d", i)}
	}

	series := make([]string, n/20+1)
	for i := range series {
		series[i] = phrase(1 + rng.Intn(3))
	}
	var asins []string
	cat.books = make([]models.Book, n)
	for i := range cat.books {
		var title string
		switch r := rng.Intn(100); {
		case r < 35:
			title = phrase(2 + rng.Intn(3))
		case r < 42:
			title = "The " + phrase(1+rng.Intn(3))
		case r < 47:
			title = phrase(1+rng.Intn(3)) + ", The"
		case r < 50:
			title = "A " + phrase(1+rng.Intn(2))
		case r < 62:
			s := series[rng.Intn(len(series))]
			v := 1 + rng.Intn(20)
			switch rng.Intn(4) {
			case 0:
				title = fmt.Sprintf("%s %d", s, v)
			case 1:
				title = fmt.Sprintf("%s %d: A %s Adventure", s, v, genTitleCase(word()))
			case 2:
				title = fmt.Sprintf("%s, Book %d", s, v)
			default:
				title = fmt.Sprintf("%s Volume %d", s, v)
			}
		case r < 67:
			title = genTitleCase(word())
		case r < 69:
			title = fmt.Sprintf("%d", 1900+rng.Intn(150))
		case r < 78:
			title = phrase(1+rng.Intn(2)) + ": " + phrase(2+rng.Intn(3))
		case r < 81:
			title = []string{"Die Höhle", "Der Prozeß", "Ender's Game", "Phönix aus der Asche", "三体", "村上春樹の世界", "ハリー・ポッター", "Cien años de soledad", "Łódź Nocą"}[rng.Intn(9)] + " " + phrase(rng.Intn(2))
		case r < 91 && i > 0:
			title = cat.books[rng.Intn(i)].Title // exact duplicate: a tie
		default:
			title = phrase(3 + rng.Intn(4))
		}
		title = strings.TrimSpace(title)
		asin := ""
		switch r := rng.Intn(100); {
		case r < 25:
			asin = genASIN(rng)
			asins = append(asins, asin)
		case r < 27 && len(asins) > 0:
			asin = asins[rng.Intn(len(asins))] // duplicate ASIN: first one wins
		}
		cat.books[i] = models.Book{
			ID:        int64(i + 1),
			AuthorID:  int64(1 + rng.Intn(nAuthors)),
			Title:     title,
			SortTitle: title,
			ForeignID: fmt.Sprintf("gb-%d", i),
			ASIN:      asin,
			Status:    "wanted",
		}
	}
	return cat
}

// generateUnitPaths returns n bulk import unit paths under root aimed at cat:
// exact and near miss titles under their author folder, flat "Author - Title"
// and backwards "Title - Author" files, a wrong or missing author, ASIN only
// files, folder units, release folders and titles the catalogue does not
// have. Nothing is created on disk.
func generateUnitPaths(cat genCatalogue, root string, n int, seed int64) []string {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // G404: deterministic test data
	vocab := genVocabulary(rng, 4000)
	authorName := func(b models.Book) string { return cat.authors[b.AuthorID-1].Name }
	clean := func(s string) string { return strings.NewReplacer("/", " ", ":", " -").Replace(s) }
	out := make([]string, n)
	for i := range out {
		b := cat.books[rng.Intn(len(cat.books))]
		a := authorName(b)
		t := clean(b.Title)
		words := strings.Fields(t)
		switch rng.Intn(16) {
		case 0:
			out[i] = filepath.Join(root, a, t+".epub")
		case 1:
			out[i] = filepath.Join(root, a+" - "+t+".epub")
		case 2:
			out[i] = filepath.Join(root, a, t+" - "+a+".epub")
		case 3: // drop a word
			if len(words) > 1 {
				k := rng.Intn(len(words))
				words = append(words[:k:k], words[k+1:]...)
			}
			out[i] = filepath.Join(root, a, strings.Join(words, " ")+".epub")
		case 4: // add a word
			out[i] = filepath.Join(root, a, t+" "+genTitleCase(vocab[rng.Intn(len(vocab))])+".m4b")
		case 5: // swap two words
			if len(words) > 1 {
				words[0], words[len(words)-1] = words[len(words)-1], words[0]
			}
			out[i] = filepath.Join(root, a, strings.Join(words, " ")+".epub")
		case 6: // another volume
			out[i] = filepath.Join(root, a, t+" "+fmt.Sprint(1+rng.Intn(20))+".epub")
		case 7: // a typo
			if len(t) > 3 {
				k := 1 + rng.Intn(len(t)-2)
				t = t[:k] + "x" + t[k+1:]
			}
			out[i] = filepath.Join(root, a, t+".epub")
		case 8: // no author anywhere
			out[i] = filepath.Join(root, t+".epub")
		case 9: // the wrong author folder
			other := cat.authors[rng.Intn(len(cat.authors))].Name
			out[i] = filepath.Join(root, other, t+".epub")
		case 10: // ASIN only, or ASIN plus title
			asin := b.ASIN
			if asin == "" {
				asin = genASIN(rng)
			}
			if rng.Intn(2) == 0 {
				out[i] = filepath.Join(root, asin+".m4b")
			} else {
				out[i] = filepath.Join(root, a, t+" ["+asin+"].m4b")
			}
		case 11: // multi format folder unit
			out[i] = filepath.Join(root, a, t)
		case 12: // release folder holding the file
			out[i] = filepath.Join(root, t+" - "+a+" [epub]", "book.epub")
		case 13: // lower case, year and tags
			out[i] = filepath.Join(root, a, strings.ToLower(t)+" (2019) (Unabridged).mp3")
		default: // not in the catalogue
			k := 1 + rng.Intn(4)
			ws := make([]string, k)
			for j := range ws {
				ws[j] = genTitleCase(vocab[rng.Intn(len(vocab))])
			}
			out[i] = filepath.Join(root, a, strings.Join(ws, " ")+".epub")
		}
	}
	return out
}

// seedGenerated inserts cat into a fresh in-memory database and returns a
// Scanner over it.
func seedGenerated(tb testing.TB, cat genCatalogue) *Scanner {
	tb.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	for i := range cat.authors {
		a := cat.authors[i]
		if err := authors.Create(ctx, &a); err != nil {
			tb.Fatal(err)
		}
	}
	for i := range cat.books {
		b := cat.books[i]
		if err := books.Create(ctx, &b); err != nil {
			tb.Fatal(err)
		}
	}
	return NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), books, authors, db.NewHistoryRepo(database), tb.TempDir(), "", "", "", "")
}

// BenchmarkLookupBatchLayoutScale is one manual import scan request, 1000
// units (the request cap), against generated catalogues of 1k, 10k and 40k
// books.
func BenchmarkLookupBatchLayoutScale(b *testing.B) {
	for _, n := range []int{1000, 10000, 40000} {
		b.Run(fmt.Sprintf("books=%d", n), func(b *testing.B) {
			cat := generateCatalogue(n, 42)
			s := seedGenerated(b, cat)
			root := b.TempDir()
			paths := generateUnitPaths(cat, root, 1000, 7)
			ctx := context.Background()
			for b.Loop() {
				if _, err := s.LookupBatchLayout(ctx, root, paths); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
