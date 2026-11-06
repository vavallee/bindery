package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func (f *upgradeFixture) writeOPF(t *testing.T, name, metadata string) {
	t.Helper()
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
` + metadata + `
  </metadata>
</package>`
	src := writeTestEpub(t, "content.opf", opf)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeGate_E2E_OPFOddities(t *testing.T) {
	for _, tc := range []struct {
		name, bookTitle, meta string
		wantRemoved           bool
	}{
		{"baseline", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:creator>James Patterson</dc:creator>`, true},
		{"entity apostrophe", "Ender's Game", `<dc:title>Ender&#39;s Game</dc:title><dc:creator>James Patterson</dc:creator>`, true},
		{"entity amp title", "Sense & Sensibility", `<dc:title>Sense &amp; Sensibility</dc:title><dc:creator>James Patterson</dc:creator>`, true},
		{"cdata", "Mary, Mary", `<dc:title><![CDATA[Mary, Mary]]></dc:title><dc:creator>James Patterson</dc:creator>`, true},
		{"two titles good first", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:title>Dune Messiah</dc:title><dc:creator>James Patterson</dc:creator>`, true},
		{"two titles bad first", "Mary, Mary", `<dc:title>Dune Messiah</dc:title><dc:title>Mary, Mary</dc:title><dc:creator>James Patterson</dc:creator>`, false},
		{"role aut", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:creator opf:role="aut">James Patterson</dc:creator>`, true},
		{"editor first then aut", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:creator opf:role="edt">Some Editor</dc:creator><dc:creator opf:role="aut">James Patterson</dc:creator>`, true},
		{"coauthor first no roles", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:creator>Maxine Paetro</dc:creator><dc:creator>James Patterson</dc:creator>`, false},
		{"translator only", "Mary, Mary", `<dc:title>Mary, Mary</dc:title><dc:creator opf:role="trl">James Patterson</dc:creator>`, true},
		{"bracket range", "Dune", `<dc:title>Dune (Books 1-6)</dc:title><dc:creator>James Patterson</dc:creator>`, false},
		{"bracket box", "Dune", `<dc:title>Dune [Complete Collection]</dc:title><dc:creator>James Patterson</dc:creator>`, false},
		{"bracket messiah", "Dune", `<dc:title>Dune (Messiah)</dc:title><dc:creator>James Patterson</dc:creator>`, false},
		{"series name only", "Alex Cross, Book 12", `<dc:title>Alex Cross</dc:title><dc:creator>James Patterson</dc:creator>`, false},
		{"no title no creator", "Mary, Mary", ``, false},
		{"title only", "Mary, Mary", `<dc:title>Mary, Mary</dc:title>`, false},
		{"creator only", "Mary, Mary", `<dc:creator>James Patterson</dc:creator>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpgradeFixture(t, true, true)
			f.book.Title = tc.bookTitle
			f.book.OriginalTitle = ""
			if err := f.books.Update(context.Background(), f.book); err != nil {
				t.Fatal(err)
			}
			f.writeOPF(t, "book.epub", tc.meta)
			f.importDownload(t)
			removed := !f.mobiOnDisk(t)
			if removed != tc.wantRemoved {
				t.Errorf("RESULT-MISMATCH removed=%v want %v", removed, tc.wantRemoved)
			}
		})
	}
}
