package newznab

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// queryVariants lists the distinct search strings one book can cost an indexer
// when every tier answers empty, which is the worst case of BookSearchTraced:
// per spelling (transliterated, original, decomposed) the four tiers, then the
// number word retry. It mirrors the strings bookSearchTiers builds and is a
// measuring aid for #3455, not a second implementation of the search; the
// tiers that depend on an indexer's answer are all counted.
func queryVariants(title, author string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(kind, a, b string) {
		k := kind + "\x00" + a + "\x00" + b
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	orig := primaryTitleForQuery(title)
	if !hasSearchableText(orig) {
		return nil
	}
	origAuthor := norm.NFC.String(author)
	spellings := [][2]string{
		{TransliterateQuery(orig), TransliterateQuery(origAuthor)},
		{orig, origAuthor},
		{norm.NFD.String(orig), norm.NFD.String(origAuthor)},
	}
	for _, sp := range spellings {
		t, a := sp[0], sp[1]
		if a != "" {
			add("book", t, a)
		}
		if s := authorSurname(a); s != "" && s != a {
			add("search", s+" "+t, "")
		}
		if a != "" {
			add("search", a+" "+t, "")
		}
		add("search", t, "")
	}
	if digit := FoldNumberWords(orig); digit != orig && hasSearchableText(digit) {
		add("search", TransliterateQuery(digit), "")
	}
	return out
}

var queryVariantBooks = []struct{ title, author string }{
	{"Dune", "Frank Herbert"},
	{"The Name of the Wind", "Patrick Rothfuss"},
	{"Harry Potter and the Philosopher's Stone: Book One", "J.K. Rowling"},
	{"Path of the Deathless Book One", "OstensibleMammal"},
	{"Le Petit Prince", "Antoine de Saint-Exupéry"},
	{"Die Verwandlung (German Edition)", "Franz Kafka"},
	{"The Subtle Art of Not Giving a F*ck", "Mark Manson"},
	{"Мастер и Маргарита", "Михаил Булгаков"},
}

// TestQueryVariantCounts logs how many distinct queries each sample book can
// cost. Only a sanity bound is asserted: the ceiling is a number to watch, not
// a contract.
func TestQueryVariantCounts(t *testing.T) {
	total := 0
	for _, b := range queryVariantBooks {
		n := len(queryVariants(b.title, b.author))
		total += n
		t.Logf("%2d variants  %s / %s", n, b.title, b.author)
		if n > 16 {
			t.Errorf("%q: %d variants, more than the 16 this sample is expected to stay under", b.title, n)
		}
	}
	t.Logf("%d variants over %d books", total, len(queryVariantBooks))
}

// BenchmarkQueryVariants times building the query variants for a book, the
// per search cost before any request is sent.
func BenchmarkQueryVariants(b *testing.B) {
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		for _, bk := range queryVariantBooks {
			_ = queryVariants(bk.title, bk.author)
		}
	}
}

// BenchmarkPrimaryTitleForQuery times the title normalisation every variant
// starts from.
func BenchmarkPrimaryTitleForQuery(b *testing.B) {
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		for _, bk := range queryVariantBooks {
			_ = primaryTitleForQuery(bk.title)
		}
	}
}
