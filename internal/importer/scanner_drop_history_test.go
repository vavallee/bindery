package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// dropHistory returns the data of the single Folder Imported history event.
func dropHistory(t *testing.T, s *Scanner, ctx context.Context) map[string]any {
	t.Helper()
	events, err := s.history.ListByType(ctx, models.HistoryEventDownloadFolderImport)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d Folder Imported events, want 1", len(events))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("decode %q: %v", events[0].Data, err)
	}
	return data
}

// TestDrop_HistoryNamesTheDroppedEbookFile: the Folder Imported event for an
// ebook hand-off used to record only the drop folder, so History never said
// which file was dropped. It records the file now, and the download records
// the source it handed off.
func TestDrop_HistoryNamesTheDroppedEbookFile(t *testing.T) {
	for _, layout := range []string{"flat", "templated"} {
		t.Run(layout, func(t *testing.T) {
			s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
			setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir, "import.drop_layout": layout})
			downloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(downloadDir, "whatever.epub"), bookSized("epub bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			dl := newDropDownload(t, downloads, book, ctx)

			s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

			dropped := findOneFile(t, dropDir)
			data := dropHistory(t, s, ctx)
			if data["path"] != dropped {
				t.Errorf("history path = %v, want the dropped file %q", data["path"], dropped)
			}
			got, err := downloads.GetByGUID(ctx, dl.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != models.StateImportExternal || got.ImportPath != downloadDir {
				t.Errorf("download = %q with import_path %q, want %q with the source %q",
					got.Status, got.ImportPath, models.StateImportExternal, downloadDir)
			}
		})
	}
}

// TestDrop_HistoryNamesTheDroppedAudiobookFolder: an audiobook drops as a
// folder of its own, and that folder is what History records.
func TestDrop_HistoryNamesTheDroppedAudiobookFolder(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeAudiobook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})
	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "audiobook.m4b"), []byte("m4b bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	want := filepath.Join(dropDir, "Title T - Author A")
	if data := dropHistory(t, s, ctx); data["path"] != want {
		t.Errorf("history path = %v, want the dropped folder %q", data["path"], want)
	}
	if got, _ := downloads.GetByGUID(ctx, dl.GUID); got == nil || got.ImportPath != downloadDir {
		t.Errorf("download import_path = %+v, want the source %q", got, downloadDir)
	}
}

// TestExternalNoDrop_RecordsHandedOffSource: external mode without a drop
// folder hands the download off in place, and records that source too.
func TestExternalNoDrop_RecordsHandedOffSource(t *testing.T) {
	s, settings, downloads, _, _, _, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{})
	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "whatever.epub"), bookSized("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(context.Background(), dl, downloadDir, "", "", "", nil, nil)

	got, err := downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportExternal || got.ImportPath != downloadDir {
		t.Errorf("download = %q with import_path %q, want %q with the source %q",
			got.Status, got.ImportPath, models.StateImportExternal, downloadDir)
	}
}
