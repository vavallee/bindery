package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
// the source file it handed off.
func TestDrop_HistoryNamesTheDroppedEbookFile(t *testing.T) {
	for _, layout := range []string{"flat", "templated"} {
		t.Run(layout, func(t *testing.T) {
			s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
			setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir, "import.drop_layout": layout})
			downloadDir := t.TempDir()
			src := filepath.Join(downloadDir, "whatever.epub")
			if err := os.WriteFile(src, bookSized("epub bytes"), 0o644); err != nil {
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
			if got.Status != models.StateImportExternal || got.ImportPath != src {
				t.Errorf("download = %q with import_path %q, want %q with the source %q",
					got.Status, got.ImportPath, models.StateImportExternal, src)
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
	src := filepath.Join(downloadDir, "audiobook.m4b")
	if err := os.WriteFile(src, []byte("m4b bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	want := filepath.Join(dropDir, "Title T - Author A")
	if data := dropHistory(t, s, ctx); data["path"] != want {
		t.Errorf("history path = %v, want the dropped folder %q", data["path"], want)
	}
	if got, _ := downloads.GetByGUID(ctx, dl.GUID); got == nil || got.ImportPath != src {
		t.Errorf("download import_path = %+v, want the source %q", got, src)
	}
}

// TestExternalNoDrop_RecordsHandedOffSource: external mode without a drop
// folder hands the download off in place, and records that source too.
func TestExternalNoDrop_RecordsHandedOffSource(t *testing.T) {
	s, settings, downloads, _, _, _, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{})
	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "whatever.epub")
	if err := os.WriteFile(src, bookSized("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(context.Background(), dl, downloadDir, "", "", "", nil, nil)

	got, err := downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportExternal || got.ImportPath != src {
		t.Errorf("download = %q with import_path %q, want %q with the source %q",
			got.Status, got.ImportPath, models.StateImportExternal, src)
	}
}

// TestDrop_TransmissionRecordsOnlyItsOwnFiles: Transmission passes the shared
// category folder as the download path and the torrent's own files
// separately. The hand-off records those files, never the shared folder, with
// or without a drop folder.
func TestDrop_TransmissionRecordsOnlyItsOwnFiles(t *testing.T) {
	for name, kv := range map[string]map[string]string{"drop folder": nil, "in place": {}} {
		t.Run(name, func(t *testing.T) {
			s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
			if kv == nil {
				kv = map[string]string{"import.drop_folder": dropDir}
			}
			setDropSettings(t, settings, ctx, kv)
			shared := t.TempDir()
			mine := filepath.Join(shared, "Title T - Author A.epub")
			mine2 := filepath.Join(shared, "Title T - Author A.azw3")
			for _, p := range []string{mine, mine2, filepath.Join(shared, "Someone Else.epub")} {
				if err := os.WriteFile(p, bookSized("bytes "+p), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			dl := newDropDownload(t, downloads, book, ctx)

			s.tryImportInternal(ctx, dl, shared, "", "", "", nil, []string{mine, mine2})

			got, err := downloads.GetByGUID(ctx, dl.GUID)
			if err != nil {
				t.Fatal(err)
			}
			srcs := got.HandedOffSources()
			slices.Sort(srcs)
			if got.Status != models.StateImportExternal || !slices.Equal(srcs, []string{mine2, mine}) {
				t.Errorf("download = %q with sources %v (import_path %q), want %q with only %q and %q",
					got.Status, srcs, got.ImportPath, models.StateImportExternal, mine, mine2)
			}
		})
	}
}
