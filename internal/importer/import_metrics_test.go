package importer

import (
	"fmt"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/metrics"
	"github.com/vavallee/bindery/internal/metrics/metricstest"
	"github.com/vavallee/bindery/internal/models"
)

func imports(t *testing.T, mediaType, result, reason string) float64 {
	return metricstest.Value(t, "bindery_imports_total",
		map[string]string{"media_type": mediaType, "result": result, "reason": reason})
}

// TestImport_BlockedRecordsMetric runs the #1591 video guard through
// tryImportInternal and checks the block lands in bindery_imports_total as
// blocked, rejected_format.
func TestImport_BlockedRecordsMetric(t *testing.T) {
	s, _, dl, downloadDir, ctx := videoGuardFixture(t)
	dl.MediaType = models.MediaTypeAudiobook
	before := imports(t, "audiobook", "blocked", "rejected_format")

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	if got := imports(t, "audiobook", "blocked", "rejected_format") - before; got != 1 {
		t.Errorf("imports{audiobook,blocked,rejected_format} moved by %v, want 1", got)
	}
}

// TestImport_NoFilesRecordsMetric runs an import over an empty download
// folder: a retryable failure with reason no_files.
func TestImport_NoFilesRecordsMetric(t *testing.T) {
	s, _, dl, _, ctx := videoGuardFixture(t)
	dl.MediaType = models.MediaTypeEbook
	before := imports(t, "ebook", "failed", "no_files")

	s.tryImportInternal(ctx, dl, t.TempDir(), "", "", "", nil, nil)

	if got := imports(t, "ebook", "failed", "no_files") - before; got != 1 {
		t.Errorf("imports{ebook,failed,no_files} moved by %v, want 1", got)
	}
}

// TestImport_SuccessRecordsMetric imports the fixture with an explicit
// audiobook format hint, which places it, and checks the success counter.
func TestImport_SuccessRecordsMetric(t *testing.T) {
	s, downloads, dl, downloadDir, ctx := videoGuardFixture(t)
	before := imports(t, "audiobook", "imported", "none")

	s.tryImportInternal(ctx, dl, downloadDir, "", "", models.MediaTypeAudiobook, nil, nil)

	reloaded, err := downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.StateImported {
		t.Fatalf("fixture did not import: %q %s", reloaded.Status, reloaded.ErrorMessage)
	}
	if got := imports(t, "audiobook", "imported", "none") - before; got != 1 {
		t.Errorf("imports{audiobook,imported,none} moved by %v, want 1", got)
	}
}

// TestImportFailureReason_MapsEveryFailureFamily pins the reason for each
// family of message the failImport call sites write. Paths and titles inside
// a message must not change the answer.
func TestImportFailureReason_MapsEveryFailureFamily(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{fmt.Sprintf("import retry limit reached (%d attempts), fix the underlying problem", importRetryLimit), metrics.ImportReasonRetryLimit},
		{"download source no longer available in the client, re-download or import the files manually", metrics.ImportReasonSourceGone},
		{"file declares German, but the profile allows only English. Not imported", metrics.ImportReasonRejectedLanguage},
		{"audio files declare French, but the book allows only English.", metrics.ImportReasonRejectedLanguage},
		{`the file says it is "Other" by Someone, but the book is "Book" by Author.`, metrics.ImportReasonRejectedContent},
		{`the files contain "Other", but the book is "Book" (1 of 9 tracks match).`, metrics.ImportReasonRejectedContent},
		{`file format "mp3" is a audiobook file but this download is being imported as ebook. Not imported. Use manual import to override, or allow the format in the quality profile.`, metrics.ImportReasonRejectedFormat},
		{"download looks like video content (largest file is a video file), not imported", metrics.ImportReasonRejectedFormat},
		{`release is titled "Books 1-3" but is linked to the single book "X". Bindery cannot split a pack across book records, so nothing was imported.`, metrics.ImportReasonRejectedFormat},
		{`waiting for x.rar to be extracted: this download holds only an archive, and Bindery does not unpack archives.`, metrics.ImportReasonNoFiles},
		{`this download holds only an archive (x.rar), and nothing extracted it in the 5 checks Bindery waited.`, metrics.ImportReasonNoFiles},
		{`no book files found in "/downloads/archive/path not found"`, metrics.ImportReasonNoFiles},
		{`nothing was imported: the only ebook files in "/d" are under 1024 bytes, too small to be a book.`, metrics.ImportReasonNoFiles},
		{`download path not found: "/downloads/x", configure PathRemap on the download client`, metrics.ImportReasonPathMissing},
		{`nothing at "/downloads/x" on this host, the download may still be finishing`, metrics.ImportReasonPathMissing},
		{"the download client reports this download as complete, but no content path for it is on this host", metrics.ImportReasonPathMissing},
		{"the download client still reports this download as complete, but none of its files are on this host", metrics.ImportReasonPathMissing},
		{"pair gating timeout: held download files are no longer present, retry the import", metrics.ImportReasonPathMissing},
		{"audiobook source unavailable: stat /x: no such file", metrics.ImportReasonPathMissing},
		{"could not match any book to this download, check the release title", metrics.ImportReasonUnmatched},
		{`could not match this download to any book in your library. Parsed from filename: title="a"`, metrics.ImportReasonUnmatched},
		{"this book already has an audiobook at /lib/x, so nothing was imported", metrics.ImportReasonConflict},
		{`two files in this download would be placed at the same path "/a": "b" and "c"`, metrics.ImportReasonConflict},
		{`the audiobook file template gives "a" and "b" the same name "c", so one would overwrite the other.`, metrics.ImportReasonConflict},
		{"audiobook move refused: the folder holds a file another book tracks", metrics.ImportReasonConflict},
		{"all 2 file(s) failed to import: permission denied", metrics.ImportReasonPlacement},
		{"partial import: 1 of 3 file(s) failed: disk full", metrics.ImportReasonPlacement},
		{"audiobook destination invalid: escapes library", metrics.ImportReasonPlacement},
		{"drop folder handoff failed: permission denied", metrics.ImportReasonPlacement},
		{"no library directory configured, set one in Settings", metrics.ImportReasonPlacement},
		{"audiobook hardlink refused: cross device", metrics.ImportReasonPlacement},
		{"audiobook moved to /lib/x but could not be recorded (db locked)", metrics.ImportReasonPlacement},
		{"check audiobook source: permission denied", metrics.ImportReasonPlacement},
		{"something nobody has written yet", metrics.LabelOther},
	}
	for _, c := range cases {
		if got := importFailureReason(c.msg); got != c.want {
			t.Errorf("importFailureReason(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}

// TestImportMetrics_LabelsStayBounded checks every label value the counter
// holds after the tests above is in the fixed sets.
func TestImportMetrics_LabelsStayBounded(t *testing.T) {
	observeImportFailure(&models.Download{MediaType: "Free Text"}, models.StateImportFailed, "/some/path/Title by Author")
	reasons := []string{
		"none", "path_missing", "no_files", "unmatched", "rejected_format", "rejected_language",
		"rejected_content", "conflict", "placement", "source_gone", "retry_limit", "other",
	}
	for _, v := range metricstest.LabelValues(t, "bindery_imports_total", "reason") {
		if !slices.Contains(reasons, v) {
			t.Errorf("reason label %q is outside the bounded set", v)
		}
	}
	for _, v := range metricstest.LabelValues(t, "bindery_imports_total", "media_type") {
		if !slices.Contains([]string{"ebook", "audiobook", "other"}, v) {
			t.Errorf("media_type label %q is outside the bounded set", v)
		}
	}
	for _, v := range metricstest.LabelValues(t, "bindery_imports_total", "result") {
		if !slices.Contains([]string{"imported", "failed", "blocked"}, v) {
			t.Errorf("result label %q is outside the bounded set", v)
		}
	}
	if imports(t, "other", "failed", "other") < 1 {
		t.Error("free text media type and message did not collapse to other")
	}
}
