package nzbget

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func entries(lines ...string) []LogEntry {
	out := make([]LogEntry, len(lines))
	for i, l := range lines {
		out[i] = LogEntry{ID: i + 1, Text: l}
	}
	return out
}

// TestClassifyFailureLog pins the markers against lines in the shape NZBGet
// writes them (UnpackController.cpp, ParChecker.cpp, PrePostProcessor.cpp,
// ScriptController.cpp).
func TestClassifyFailureLog(t *testing.T) {
	cases := []struct {
		name string
		log  []LogEntry
		want FailureEvidence
	}{
		{"empty", nil, EvidenceUnknown},
		{"unrelated lines", entries("Unpacking Book", "Unrar failed"), EvidenceUnknown},
		{"unrar exit 1 only", entries("Unrar error code: 1", "Unrar failed"), EvidenceUnknown},

		// Host faults.
		{"unrar missing", entries("Unrar: Could not start /usr/bin/unrar: No such file or directory"), EvidenceHost},
		{"unrar command unparsable", entries("Could not start unrar, failed to parse command line: \"unrar"), EvidenceHost},
		{"unrar out of memory", entries("Unrar error code: 8", "Unrar failed"), EvidenceHost},
		{"unrar create error", entries("Unrar error code: 9"), EvidenceHost},
		{"move out of _unpack", entries("Could not move file /dl/_unpack/a.epub to /dl/a.epub: Permission denied"), EvidenceHost},
		{"par2 io error", entries("Repair failed for Book: an error occurred when accessing files. Recovery files created by: x"), EvidenceHost},
		{"par2 memory", entries("Repair failed for Book: out of memory. Recovery files created by: x"), EvidenceHost},
		{"7z exit without crc", entries("Executing 7-Zip", "7-Zip error code: 2", "7-Zip failed"), EvidenceHost},
		{"host wins over content", entries("Unrar: a.epub - CRC failed", "Could not move file a to b: denied"), EvidenceHost},

		// The release.
		{"unrar crc", entries("Unrar: a.epub - CRC failed", "Cancelling unrar due to errors"), EvidenceContent},
		{"unrar checksum", entries("Unrar: a.epub - checksum failed"), EvidenceContent},
		{"unrar packed data", entries("Unrar: a.epub : packed data CRC failed in volume a.part2.rar"), EvidenceContent},
		{"unrar missing volume", entries("Unrar: Cannot find volume a.part3.rar"), EvidenceContent},
		{"unrar previous volume", entries("Unrar: WARNING: You need to start extraction from a previous volume to unpack a.epub"), EvidenceContent},
		{"unrar crc exit", entries("Unrar error code: 3"), EvidenceContent},
		{"7z crc", entries("7-Zip: ERROR: CRC Failed : a.epub", "7-Zip error code: 2"), EvidenceContent},
		{"7z data error", entries("7-Zip: ERROR: Data Error : a.epub", "7-Zip error code: 2"), EvidenceContent},
		{"par insufficient", entries("Repair failed for Book: data files are damaged and there is insufficient recovery data available to be able to repair them. Recovery files created by: x"), EvidenceContent},
		{"par health skip", entries("Skipping par-check for Book due to health 41.2% below critical 85.0%"), EvidenceContent},
		{"renamed archives", entries("Could not unpack Book due to renamed archive files"), EvidenceContent},
		{"no par files is not a host fault", entries("Could not start par-check for Book. Could not find any par-files"), EvidenceUnknown},
		{"7z missing volume", entries("7-Zip: ERROR: Missing volume : book.7z.002", "7-Zip error code: 2"), EvidenceContent},
		{"unrar 5 checksum error", entries("Unrar: book.epub             - checksum error", "Unrar error code: 3"), EvidenceContent},
		{"7z command unparsable", entries("Could not start 7-Zip, failed to parse command line: \"7z"), EvidenceHost},
		{"7z missing", entries("7-Zip: Could not start /usr/bin/7z: No such file or directory"), EvidenceHost},

		// A post processing script that will not start is not the unpacker:
		// NZBGet logs "Could not start" for every program, and scripts run on
		// failed jobs too (#3024 review).
		{"broken script alone", entries("Could not start /scripts/Notify.py: Permission denied"), EvidenceUnknown},
		{"script prefix alone", entries("Notify: Could not start /scripts/Notify.py: No such file or directory"), EvidenceUnknown},
		{"broken script plus crc", entries("Unrar: a.epub - CRC failed", "Notify: Could not start /scripts/Notify.py: No such file or directory"), EvidenceContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := ClassifyFailureLog(tc.log)
			if got != tc.want {
				t.Fatalf("ClassifyFailureLog = %v (%q), want %v", got, detail, tc.want)
			}
			if got != EvidenceUnknown && detail == "" {
				t.Fatal("no deciding line returned")
			}
		})
	}
}

func TestNeedsLogEvidenceAndStageSucceeded(t *testing.T) {
	for status, want := range map[string]bool{
		"FAILURE/UNPACK": true, "FAILURE/PAR": true, "FAILURE/SCAN": true,
		"FAILURE/HEALTH": false, "FAILURE/BAD": false,
	} {
		if got := NeedsLogEvidence(status); got != want {
			t.Errorf("NeedsLogEvidence(%q) = %v, want %v", status, got, want)
		}
	}
	epub := HistoryItem{Status: "SUCCESS/HEALTH", UnpackStatus: "NONE", ParStatus: "NONE"}
	rar := HistoryItem{Status: "SUCCESS/ALL", UnpackStatus: "SUCCESS", ParStatus: "SUCCESS"}
	cases := []struct {
		kind string
		item HistoryItem
		want bool
	}{
		{"FAILURE/UNPACK", epub, false},
		{"FAILURE/UNPACK", rar, true},
		{"FAILURE/PAR", epub, false},
		{"FAILURE/PAR", HistoryItem{Status: "SUCCESS/UNPACK", UnpackStatus: "SUCCESS", ParStatus: "NONE"}, false},
		{"FAILURE/PAR", rar, true},
		{"FAILURE/SCAN", epub, true},
		{"FAILURE/HEALTH", rar, false},
	}
	for _, tc := range cases {
		if got := StageSucceeded(tc.kind, tc.item); got != tc.want {
			t.Errorf("StageSucceeded(%q, %+v) = %v, want %v", tc.kind, tc.item, got, tc.want)
		}
	}
}

// rpcServer answers one JSON-RPC method with result, and anything else with
// NZBGet's error for an unknown method.
func rpcServer(t *testing.T, method string, result any, gotParams *[]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method != method {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": 1, "message": "Invalid procedure"}})
			return
		}
		if gotParams != nil {
			*gotParams = req.Params
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
}

func newClientForServer(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	host, port := serverHostPort(t, srv.URL)
	return New(host, port, "", "", "", false)
}

func TestLoadLog(t *testing.T) {
	var params []any
	srv := rpcServer(t, "loadlog", []map[string]any{{"ID": 7, "Kind": "ERROR", "Time": 1, "Text": "Unrar error code: 8"}}, &params)
	defer srv.Close()
	c := newClientForServer(t, srv)
	got, err := c.LoadLog(context.Background(), 42, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "Unrar error code: 8" || got[0].Kind != "ERROR" {
		t.Fatalf("LoadLog = %+v", got)
	}
	if !reflect.DeepEqual(params, []any{float64(42), float64(0), float64(500)}) {
		t.Fatalf("loadlog params = %v, want [42 0 500]", params)
	}
}

func TestSysInfoMissingUnpackers(t *testing.T) {
	srv := rpcServer(t, "sysinfo", map[string]any{"Tools": []map[string]any{
		{"Name": "Python", "Version": "", "Path": ""},
		{"Name": "7-Zip", "Version": "23.01", "Path": "/usr/bin/7z"},
		{"Name": "UnRAR", "Version": "", "Path": ""},
	}}, nil)
	defer srv.Close()
	info, err := newClientForServer(t, srv).SysInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MissingUnpackers(); !reflect.DeepEqual(got, []string{"UnRAR"}) {
		t.Fatalf("MissingUnpackers = %v, want [UnRAR] (Python is not an unpacker)", got)
	}

	old := rpcServer(t, "nothing", nil, nil)
	defer old.Close()
	if _, err := newClientForServer(t, old).SysInfo(context.Background()); err == nil {
		t.Fatal("SysInfo against an NZBGet without sysinfo did not return an error")
	}
}
