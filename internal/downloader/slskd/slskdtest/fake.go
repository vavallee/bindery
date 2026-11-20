// Package slskdtest is a fake slskd for tests. It answers the endpoints Bindery
// uses (application, options, batch downloads, the download list and transfer
// removal) from memory, records what it was asked, and lets a test move
// transfers through their states.
package slskdtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Transfer states as slskd writes them.
const (
	Queued     = "Queued, Remotely"
	Starting   = "Initializing"
	InProgress = "InProgress"
	Succeeded  = "Completed, Succeeded"
	Errored    = "Completed, Errored"
	Cancelled  = "Completed, Cancelled"
	TimedOut   = "Completed, TimedOut"
	Rejected   = "Completed, Rejected"
)

// Transfer is one file of a fake batch.
type Transfer struct {
	ID       string
	Filename string
	Size     int64
	State    string
	Bytes    int64
	Place    *int
}

// Batch is a fake batch.
type Batch struct {
	ID          string
	Username    string
	Destination string
	Transfers   []*Transfer
}

// Fake is the fake daemon.
type Fake struct {
	mu sync.Mutex
	// Downloads is slskd's downloads folder as it reports it.
	Downloads string
	// Offline makes a batch request answer 404, as slskd does for an offline peer.
	Offline bool
	// Down makes every call answer 503.
	Down bool
	// Refuse lists filenames slskd refuses to queue (a 207 or 200 answer).
	Refuse map[string]bool
	// Batches by id.
	Batches map[string]*Batch
	// Requests logs "METHOD path" for every call.
	Requests []string
	// Keys logs the X-API-Key of every call.
	Keys []string
	next int
}

// New starts a fake and returns it with its server.
func New() (*Fake, *httptest.Server) {
	f := &Fake{Downloads: "/app/downloads", Batches: map[string]*Batch{}, Refuse: map[string]bool{}}
	return f, httptest.NewServer(f)
}

// Batch returns a copy-safe pointer to a batch, nil when slskd does not hold it.
func (f *Fake) Batch(id string) *Batch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Batches[strings.ToLower(id)]
}

// OnlyBatch returns the one batch the fake holds, nil when there is not exactly one.
func (f *Fake) OnlyBatch() *Batch {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Batches) != 1 {
		return nil
	}
	for _, b := range f.Batches {
		return b
	}
	return nil
}

// SetAll moves every transfer of the batch to state with bytes transferred.
func (f *Fake) SetAll(id, state string, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.Batches[strings.ToLower(id)]; b != nil {
		for _, t := range b.Transfers {
			t.State, t.Bytes = state, bytes
			if state == Succeeded {
				t.Bytes = t.Size
			}
		}
	}
}

// SetFile moves one transfer.
func (f *Fake) SetFile(id string, i int, state string, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.Batches[strings.ToLower(id)]; b != nil && i < len(b.Transfers) {
		b.Transfers[i].State, b.Transfers[i].Bytes = state, bytes
	}
}

// SetPlace sets the queue position of every transfer.
func (f *Fake) SetPlace(id string, place int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.Batches[strings.ToLower(id)]; b != nil {
		for _, t := range b.Transfers {
			p := place
			t.Place = &p
		}
	}
}

// Count returns how many logged requests start with prefix ("DELETE ", "POST ").
func (f *Fake) Count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.Requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func transferJSON(b *Batch, t *Transfer) map[string]any {
	m := map[string]any{
		"id": t.ID, "batchId": b.ID, "username": b.Username, "filename": t.Filename,
		"size": t.Size, "state": t.State, "bytesTransferred": t.Bytes, "averageSpeed": 0.0,
	}
	if strings.HasPrefix(t.State, "InProgress") && t.Bytes > 0 {
		m["averageSpeed"] = 1000.0
	}
	if t.Place != nil {
		m["placeInQueue"] = *t.Place
	}
	return m
}

func batchJSON(b *Batch) map[string]any {
	ts := make([]map[string]any, 0, len(b.Transfers))
	for _, t := range b.Transfers {
		ts = append(ts, transferJSON(b, t))
	}
	return map[string]any{"id": b.ID, "username": b.Username, "transfers": ts}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP implements http.Handler.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	f.Keys = append(f.Keys, r.Header.Get("X-API-Key"))
	if f.Down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/v0/application":
		writeJSON(w, 200, map[string]any{"version": map[string]any{"current": "0.26.0"}, "server": map[string]any{"isConnected": true, "isLoggedIn": true}})
	case p == "/api/v0/options":
		writeJSON(w, 200, map[string]any{"directories": map[string]any{"downloads": f.Downloads}})
	case p == "/api/v0/transfers/downloads/batches" && r.Method == http.MethodPost:
		f.enqueue(w, r)
	case strings.HasPrefix(p, "/api/v0/transfers/downloads/batches/") && r.Method == http.MethodGet:
		b := f.Batches[strings.ToLower(strings.TrimPrefix(p, "/api/v0/transfers/downloads/batches/"))]
		if b == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, batchJSON(b))
	case p == "/api/v0/transfers/downloads" && r.Method == http.MethodGet:
		byUser := map[string][]map[string]any{}
		var order []string
		for _, b := range f.Batches {
			for _, t := range b.Transfers {
				if _, ok := byUser[b.Username]; !ok {
					order = append(order, b.Username)
				}
				byUser[b.Username] = append(byUser[b.Username], transferJSON(b, t))
			}
		}
		users := []map[string]any{}
		for _, u := range order {
			users = append(users, map[string]any{"username": u, "directories": []map[string]any{{"directory": "d", "files": byUser[u]}}})
		}
		writeJSON(w, 200, users)
	case strings.HasPrefix(p, "/api/v0/transfers/downloads/") && r.Method == http.MethodDelete:
		parts := strings.Split(strings.TrimPrefix(p, "/api/v0/transfers/downloads/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		for _, b := range f.Batches {
			for i, t := range b.Transfers {
				if t.ID == parts[1] && b.Username == parts[0] {
					b.Transfers = append(b.Transfers[:i], b.Transfers[i+1:]...)
					w.WriteHeader(204)
					return
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) enqueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Files    []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"files"`
		Options struct {
			Destination string `json:"destination"`
		} `json:"options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if f.Offline {
		http.Error(w, "offline", 404)
		return
	}
	id := strings.ToLower(req.ID)
	if f.Batches[id] != nil {
		http.Error(w, "dup", http.StatusConflict)
		return
	}
	b := &Batch{ID: req.ID, Username: req.Username, Destination: req.Options.Destination}
	var failures []map[string]string
	for _, file := range req.Files {
		if f.Refuse[file.Filename] {
			failures = append(failures, map[string]string{"filename": file.Filename, "message": "refused"})
			continue
		}
		f.next++
		place := f.next
		b.Transfers = append(b.Transfers, &Transfer{ID: "t" + strconv.Itoa(f.next), Filename: file.Filename, Size: file.Size, State: Queued, Place: &place})
	}
	f.Batches[id] = b
	status := 201
	switch {
	case len(failures) == len(req.Files):
		status = 200
	case len(failures) > 0:
		status = 207
	}
	writeJSON(w, status, map[string]any{"batch": batchJSON(b), "failures": failures})
}
