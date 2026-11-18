package api

import (
	"testing"

	"github.com/vavallee/bindery/internal/importer"
)

func TestSummarizeCountsSidecarActions(t *testing.T) {
	s := summarize([]importer.ReorganizeMove{
		{Status: importer.ReorgStatusNoop, Sidecar: importer.ReorgSidecarWrite},
		{Status: importer.ReorgStatusNoop, Sidecar: importer.ReorgSidecarWritten},
		{Status: importer.ReorgStatusMoved, Sidecar: importer.ReorgSidecarWritten},
		{Status: importer.ReorgStatusNoop, Sidecar: importer.ReorgSidecarUnchanged},
		{Status: importer.ReorgStatusNoop, Sidecar: importer.ReorgSidecarForeign},
		{Status: importer.ReorgStatusNoop, Sidecar: importer.ReorgSidecarFailed},
		{Status: importer.ReorgStatusNoop},
	})
	if s.SidecarToWrite != 1 || s.SidecarWritten != 2 || s.SidecarUnchanged != 1 || s.SidecarForeign != 1 || s.SidecarFailed != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Noop != 6 || s.Moved != 1 {
		t.Fatalf("status counts = %+v", s)
	}
}
