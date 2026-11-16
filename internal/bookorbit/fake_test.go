package bookorbit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeServer answers the four routes the importer calls. Every response is
// built here from the test's own data, in the shape a BookOrbit 3.8.0 server
// was seen to answer with. Nothing is copied from BookOrbit.
type fakeServer struct {
	t *testing.T
	*httptest.Server

	apiKey    string
	libraries []map[string]any
	// books maps a library id to its book records, each a full detail
	// record as the test wants it served.
	books map[int64][]map[string]any

	mu       sync.Mutex
	requests []string // "METHOD path"
	lastKey  string
	lastAuth string
	queries  []map[string]any
}

const fakeKey = "bok_test_0000000000000000000000000000000000000000000"

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, apiKey: fakeKey, books: map[int64][]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) addLibrary(id int64, name, folder string) {
	f.libraries = append(f.libraries, map[string]any{
		"id":      id,
		"name":    name,
		"icon":    "book",
		"folders": []map[string]any{{"id": id, "path": folder}},
	})
}

func (f *fakeServer) addBook(libraryID int64, book map[string]any) {
	book["libraryId"] = libraryID
	f.books[libraryID] = append(f.books[libraryID], book)
}

func (f *fakeServer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.lastKey = r.Header.Get("X-Api-Key")
	f.lastAuth = r.Header.Get("Authorization")
	f.mu.Unlock()

	if r.Header.Get("X-Api-Key") != f.apiKey {
		writeFake(w, http.StatusUnauthorized, map[string]any{"statusCode": 401, "message": "Invalid API key"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case r.Method == http.MethodGet && path == "/auth/me":
		writeFake(w, http.StatusOK, map[string]any{"id": 1, "username": "reader", "authenticationMethod": "api_key", "permissions": []string{}})
	case r.Method == http.MethodGet && path == "/libraries":
		writeFake(w, http.StatusOK, f.libraries)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/libraries/") && strings.HasSuffix(path, "/books"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/libraries/"), "/books"), 10, 64)
		var query map[string]any
		_ = json.NewDecoder(r.Body).Decode(&query)
		f.mu.Lock()
		f.queries = append(f.queries, query)
		f.mu.Unlock()
		page, size := 0, 50
		if p, ok := query["pagination"].(map[string]any); ok {
			page, size = int(p["page"].(float64)), int(p["size"].(float64))
		}
		all := f.books[id]
		items := []map[string]any{}
		for idx := page * size; idx < len(all) && idx < (page+1)*size; idx++ {
			// A card: the listing has ids and titles but no paths.
			items = append(items, map[string]any{"id": all[idx]["id"], "title": all[idx]["title"], "libraryId": id})
		}
		writeFake(w, http.StatusOK, map[string]any{"items": items, "total": len(all), "page": page, "size": size})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/books/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/books/"), 10, 64)
		for _, lib := range f.books {
			for _, book := range lib {
				if book["id"] == id {
					writeFake(w, http.StatusOK, book)
					return
				}
			}
		}
		writeFake(w, http.StatusNotFound, map[string]any{"statusCode": 404, "message": "Book " + strconv.FormatInt(id, 10) + " not found"})
	default:
		f.t.Errorf("importer made a request it should never make: %s %s", r.Method, r.URL.Path)
		writeFake(w, http.StatusMethodNotAllowed, map[string]any{"statusCode": 405, "message": "no"})
	}
}

func writeFake(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// fakeFile is one entry of a book's files array.
func fakeFile(id int64, path, role string) map[string]any {
	format := path[strings.LastIndex(path, ".")+1:]
	return map[string]any{
		"id":           id,
		"format":       format,
		"role":         role,
		"sizeBytes":    1234,
		"absolutePath": path,
		"filename":     path[strings.LastIndex(path, "/")+1:],
		"fileHash":     "0000",
	}
}

// fakeBook is a detail record with the fields a real one carries, including
// several the importer never reads.
func fakeBook(id int64, title string, authors []string, files ...map[string]any) map[string]any {
	people := make([]map[string]any, 0, len(authors))
	for idx, name := range authors {
		people = append(people, map[string]any{"id": id*100 + int64(idx), "name": name, "sortName": name})
	}
	return map[string]any{
		"id":                id,
		"libraryName":       "Shelf",
		"status":            "present",
		"title":             title,
		"subtitle":          nil,
		"description":       nil,
		"isbn10":            nil,
		"isbn13":            nil,
		"publisher":         nil,
		"publishedDate":     nil,
		"publishedYear":     nil,
		"language":          "en",
		"seriesId":          nil,
		"seriesName":        nil,
		"seriesIndex":       nil,
		"seriesMemberships": []any{},
		"providerIds":       map[string]any{"audible": nil, "amazon": nil},
		"authors":           people,
		"genres":            []string{},
		"tags":              []string{},
		"files":             files,
		"audioMetadata":     nil,
		"readStatus":        nil,
		"rating":            nil,
	}
}

func withSeries(book map[string]any, seriesID int64, name string, index any) map[string]any {
	book["seriesId"] = seriesID
	book["seriesName"] = name
	book["seriesIndex"] = index
	book["seriesMemberships"] = []map[string]any{{"seriesId": seriesID, "seriesName": name, "seriesIndex": index, "displayOrder": 0}}
	return book
}
