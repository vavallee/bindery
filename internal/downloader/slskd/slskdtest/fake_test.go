package slskdtest

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func call(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const batchBody = `{"id":"AA-1","username":"peer1","files":[{"filename":"a.epub","size":10},{"filename":"b.epub","size":20}],"options":{"destination":"bindery/1"}}`

func TestFakeEnqueueStatesAndRemoval(t *testing.T) {
	f, srv := New()
	defer srv.Close()
	var seen []string
	f.OnRequest = func(m, p string) { seen = append(seen, m+" "+p) }

	if code, _ := call(t, "GET", srv.URL+"/api/v0/application", ""); code != 200 {
		t.Fatalf("application %d", code)
	}
	if code, body := call(t, "GET", srv.URL+"/api/v0/options", ""); code != 200 || !strings.Contains(body, "/app/downloads") {
		t.Fatalf("options %d %s", code, body)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", batchBody); code != 201 {
		t.Fatalf("enqueue %d", code)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", batchBody); code != http.StatusConflict {
		t.Fatalf("duplicate %d", code)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", "{nope"); code != 400 {
		t.Fatalf("malformed %d", code)
	}
	b := f.OnlyBatch()
	if b == nil || f.Batch("aa-1") != b || b.Destination != "bindery/1" {
		t.Fatalf("batch = %+v", b)
	}

	f.SetPlace("AA-1", 4)
	f.SetFile("AA-1", 0, InProgress, 5)
	f.SetFile("AA-1", 9, InProgress, 5) // out of range is ignored
	f.SetAll("missing", Errored, 0)     // an unknown batch is ignored
	if code, body := call(t, "GET", srv.URL+"/api/v0/transfers/downloads/batches/aa-1", ""); code != 200 || !strings.Contains(body, `"placeInQueue":4`) || !strings.Contains(body, `"averageSpeed":1000`) {
		t.Fatalf("batch read %d %s", code, body)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v0/transfers/downloads/batches/none", ""); code != 404 {
		t.Fatalf("missing batch %d", code)
	}
	if code, body := call(t, "GET", srv.URL+"/api/v0/transfers/downloads", ""); code != 200 || !strings.Contains(body, "peer1") {
		t.Fatalf("list %d %s", code, body)
	}

	f.SetAll("AA-1", Succeeded, 0)
	if b.Transfers[1].Bytes != 20 {
		t.Errorf("a succeeded transfer must have all its bytes, got %d", b.Transfers[1].Bytes)
	}

	id := b.Transfers[0].ID
	if code, _ := call(t, "DELETE", srv.URL+"/api/v0/transfers/downloads/peer1/"+id+"?remove=true", ""); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v0/transfers/downloads/peer1/"+id, ""); code != 404 {
		t.Fatalf("second delete %d", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v0/transfers/downloads/peer1", ""); code != 404 {
		t.Fatalf("short delete path %d", code)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v0/nothing", ""); code != 404 {
		t.Fatalf("unknown path %d", code)
	}
	if f.Count("DELETE ") != 3 || len(f.Keys) != len(f.Requests) || len(seen) != len(f.Requests) {
		t.Errorf("logs: deletes %d keys %d requests %d hook %d", f.Count("DELETE "), len(f.Keys), len(f.Requests), len(seen))
	}
	if f.Batch("none") != nil {
		t.Error("an unknown batch was found")
	}
}

func TestFakeRefusalOfflineDownAndOnlyBatch(t *testing.T) {
	f, srv := New()
	defer srv.Close()
	if f.OnlyBatch() != nil {
		t.Error("no batch yet")
	}
	f.Refuse["b.epub"] = true
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", batchBody); code != 207 {
		t.Fatalf("partial refusal %d", code)
	}
	f.Refuse["a.epub"] = true
	other := strings.Replace(batchBody, "AA-1", "AA-2", 1)
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", other); code != 200 {
		t.Fatalf("full refusal %d", code)
	}
	if f.OnlyBatch() != nil {
		t.Error("two batches: no single one")
	}
	f.Offline = true
	if code, _ := call(t, "POST", srv.URL+"/api/v0/transfers/downloads/batches", strings.Replace(batchBody, "AA-1", "AA-3", 1)); code != 404 {
		t.Fatalf("offline %d", code)
	}
	f.Down = true
	if code, _ := call(t, "GET", srv.URL+"/api/v0/application", ""); code != 503 {
		t.Fatalf("down %d", code)
	}
}
