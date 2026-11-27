package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// BenchmarkSearchBookFanOut measures one interactive book search against 1, 5
// and 20 indexers that answer at once with 30 releases each: the fan-out, the
// feed parsing and everything SearchBookWithDebug does to the merged results
// (dedup, relevance, ranking). The indexers add no delay, so what is left is
// Bindery's own cost per search, which is the part #3455 asks about.
//
// Every iteration searches a different title, because a repeated query is
// answered from the per indexer query cache and would measure nothing.
func BenchmarkSearchBookFanOut(b *testing.B) {
	shapes := []string{
		"%s (2021) [EPUB]", "%s RETAIL EPUB-GRP", "%s [AZW3] (v5.0)", "%s (epub, mobi)", "%s 2021 Retail eBook-BitBook",
		"%s Unabridged M4B 64kbps", "%s - Complete Collection 1-7 (epub)", "%s [ENG] [PDF]", "%s REPOST", "%s (German Edition) epub",
	}
	handler := func(indexer int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			name := q.Get("author") + " - " + q.Get("title")
			var sb strings.Builder
			sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`)
			for j := range 30 {
				fmt.Fprintf(&sb, `<item><title>%s x%d</title><guid isPermaLink="false">g-%d-%d-%s</guid><enclosure url="https://fake/dl/%d/%d" length="%d" type="application/x-nzb"/></item>`,
					fmt.Sprintf(shapes[j%len(shapes)], name), j, indexer, j, q.Get("title"), indexer, j, (j+1)<<20)
			}
			sb.WriteString(`</channel></rss>`)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(sb.String()))
		})
	}
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("indexers-%d", n), func(b *testing.B) {
			idxs := make([]models.Indexer, n)
			for i := range idxs {
				srv := httptest.NewServer(handler(i))
				b.Cleanup(srv.Close)
				idxs[i] = models.Indexer{ID: int64(i + 1), Name: fmt.Sprintf("bench-%d", i), URL: srv.URL, Enabled: true, Categories: []int{7020}}
			}
			s := newTestSearcher()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				results, _ := s.SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
					Title: fmt.Sprintf("The Silent Garden of Avalon %d", i), Author: "Lena Lindqvist", MediaType: models.MediaTypeEbook,
				})
				if len(results) == 0 {
					b.Fatal("search returned nothing; the benchmark would measure an empty pipeline")
				}
			}
		})
	}
}
