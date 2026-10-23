package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// BenchmarkCheckAudiobookContent300 is the cost of the audiobook check on a
// 300 file folder of real ID3 tagged files, with the tags read from disk through
// ReadAudioTags. Run with:
//
//	go test -run '^$' -bench CheckAudiobookContent -benchmem ./internal/importer/
func BenchmarkCheckAudiobookContent300(b *testing.B) {
	dir := b.TempDir()
	files := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		album := "The Firm"
		if i%5 == 0 {
			album = "The Runaway Jury"
		}
		p := filepath.Join(dir, fmt.Sprintf("%03d.mp3", i))
		if err := os.WriteFile(p, buildID3v23Album(fmt.Sprintf("Chapter %d", i), album), 0o644); err != nil {
			b.Fatal(err)
		}
		files = append(files, p)
	}
	s := &Scanner{}
	book := &models.Book{Title: "The Firm"}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res := s.checkAudiobookContent(ctx, book, nil, files); !res.flagged {
			b.Fatal("expected the mixed folder to be flagged")
		}
	}
}

// BenchmarkEvaluateAudiobookContent300 is the decision alone, with no file reads:
// the part of the cost that is Bindery's own CPU.
func BenchmarkEvaluateAudiobookContent300(b *testing.B) {
	tags := make([]AudioTags, 0, 300)
	for i := 0; i < 300; i++ {
		album := "The Firm"
		if i%5 == 0 {
			album = "The Runaway Jury"
		}
		tags = append(tags, AudioTags{Album: album, Title: fmt.Sprintf("Chapter %d", i)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res := evaluateAudiobookContent("The Firm", tags); !res.flagged {
			b.Fatal("expected the mixed folder to be flagged")
		}
	}
}
