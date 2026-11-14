package importer

import (
	"strings"

	"github.com/vavallee/bindery/internal/metrics"
	"github.com/vavallee/bindery/internal/models"
)

// Import metrics (#3851). The importer records each failure as a user facing
// message on the download row and in history, with no separate code, so the
// bindery_imports_total reason label is derived from that message here. The
// rules match the fixed wording of the failImport call sites, never the
// paths or titles a message embeds, and anything unrecognised is other. The
// result label comes from the state the failure leaves the download in.

// importReasonRules is checked in order; the first rule with a matching
// phrase wins. Specific phrases come before broad ones ("failed").
var importReasonRules = []struct {
	reason  string
	phrases []string
}{
	{metrics.ImportReasonRetryLimit, []string{"retry limit reached"}},
	{metrics.ImportReasonSourceGone, []string{"no longer available in the client"}},
	{metrics.ImportReasonRejectedLanguage, []string{"allows only"}},
	{metrics.ImportReasonRejectedContent, []string{"the file says it is", "the files contain"}},
	{metrics.ImportReasonRejectedFormat, []string{
		"quality profile", "file format", "video content", "cannot split a pack",
	}},
	{metrics.ImportReasonNoFiles, []string{
		"holds only an archive", "no book files", "too small to be a book",
	}},
	{metrics.ImportReasonPathMissing, []string{
		"path not found", "nothing at ", "no content path", "none of its files are on this host",
		"no longer present", "source unavailable",
	}},
	{metrics.ImportReasonUnmatched, []string{"could not match"}},
	{metrics.ImportReasonConflict, []string{
		"already has an audiobook", "another book", "same path", "same name",
	}},
	{metrics.ImportReasonPlacement, []string{
		"failed to import", "partial import", "destination invalid", "not recorded",
		"could not be recorded", "drop folder", "no library directory", "refused", "failed",
		"check audiobook source", "not imported",
	}},
}

// importFailureReason maps an import failure message onto the bounded reason
// label set.
func importFailureReason(message string) string {
	m := strings.ToLower(message)
	for _, rule := range importReasonRules {
		for _, p := range rule.phrases {
			if strings.Contains(m, p) {
				return rule.reason
			}
		}
	}
	return metrics.LabelOther
}

// observeImportFailure records a failed or blocked import.
func observeImportFailure(dl *models.Download, status models.DownloadState, message string) {
	result := metrics.ImportResultFailed
	if status == models.StateImportBlocked {
		result = metrics.ImportResultBlocked
	}
	metrics.ObserveImport(dl.MediaType, result, importFailureReason(message))
}

// observeImportSuccess records a download imported into the library.
func observeImportSuccess(dl *models.Download, mediaType string) {
	if mediaType == "" {
		mediaType = dl.MediaType
	}
	metrics.ObserveImport(mediaType, metrics.ImportResultImported, metrics.ImportReasonNone)
}
