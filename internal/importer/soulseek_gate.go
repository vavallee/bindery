package importer

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/vavallee/bindery/internal/models"
)

// Soulseek files come from strangers, with no indexer vouching for them, so the
// post-download checks that only warn for other protocols refuse here. Three
// things make that up:
//
//   - the content checks (ebook and audiobook) run in block mode whatever the
//     warn default says (strictContentCheck); a setting of off is still off,
//   - the folder must hold the format the download was grabbed for,
//   - every EPUB and Kindle file must parse.
//
// PDF has no content check: it has no embedded metadata Bindery reads, so a PDF
// is only held to the format rule above.

type strictContentKey struct{}

// withStrictContentCheck makes contentCheckMode read warn as block.
func withStrictContentCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, strictContentKey{}, true)
}

func strictContentCheck(ctx context.Context) bool {
	v, _ := ctx.Value(strictContentKey{}).(bool)
	return v
}

// isSoulseekDownload reports whether the row was grabbed from Soulseek.
func isSoulseekDownload(dl *models.Download) bool {
	return dl != nil && dl.Protocol == "soulseek"
}

// soulseekGate is the structural half of the block mode check. It returns a
// reason when the files must be refused, "" when they pass.
func soulseekGate(dl *models.Download, files []string, detectedFormat string) string {
	if dl.MediaType != "" && detectedFormat != "" && detectedFormat != dl.MediaType {
		return fmt.Sprintf("this download was grabbed as an %s but the Soulseek folder holds %s files. "+
			"Not imported, and the release was blocklisted so the next search picks another. "+
			"To keep these files anyway, use Match to book in the Queue or manual import",
			dl.MediaType, detectedFormat)
	}
	for _, f := range files {
		if !HasEmbeddedMetadata(f) {
			continue
		}
		if _, err := ReadEbookMetadata(f); err != nil {
			return fmt.Sprintf("%s could not be opened as an e-book, so the file is damaged or not what it claims to be. "+
				"Not imported, and the release was blocklisted so the next search picks another. "+
				"To keep it anyway, use Match to book in the Queue or manual import", filepath.Base(f))
		}
	}
	return ""
}
