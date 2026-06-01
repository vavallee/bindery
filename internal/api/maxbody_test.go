package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// readAllHandler is a tiny sink that drains r.Body so the MaxBytesReader cap
// is actually consulted on the read path. Real handlers do the equivalent
// via json.Decode or io.ReadAll; we model the read side directly to keep the
// middleware tests independent of any specific decoder.
func readAllHandler(t *testing.T, gotErr *error, gotBytes *int) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		*gotErr = err
		*gotBytes = len(buf)
		if err != nil {
			// Mirror what real handlers do: turn the cap error into a 4xx
			// so the test can assert the status code in the integration test.
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMaxBody_AllowsUnderLimit(t *testing.T) {
	var readErr error
	var readN int
	h := PreserveRawBody(MaxRequestBody(readAllHandler(t, &readErr, &readN)))

	// 500 KiB, well under the 1 MiB default cap.
	body := bytes.Repeat([]byte("a"), 500<<10)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if readErr != nil {
		t.Fatalf("expected handler to read body cleanly, got error: %v", readErr)
	}
	if readN != len(body) {
		t.Errorf("read %d bytes, want %d", readN, len(body))
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestMaxBody_BlocksOverLimit(t *testing.T) {
	var readErr error
	var readN int
	h := PreserveRawBody(MaxRequestBody(readAllHandler(t, &readErr, &readN)))

	// 2 MiB, over the 1 MiB default cap; the read must error out.
	body := bytes.Repeat([]byte("b"), 2<<20)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if readErr == nil {
		t.Fatal("expected read error from MaxBytesReader, got nil")
	}
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Fatalf("expected *http.MaxBytesError, got %T: %v", readErr, readErr)
	}
	if mbe.Limit != DefaultMaxRequestBody {
		t.Errorf("MaxBytesError.Limit = %d, want %d", mbe.Limit, DefaultMaxRequestBody)
	}
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rr.Code)
	}
}

func TestMaxBody_PerRouteOverride(t *testing.T) {
	// A request that would be blocked by the default (2 MiB body, 1 MiB cap)
	// must pass when the route opts in to a higher cap via WithMaxBody. The
	// fixture chains the same middleware stack the real router uses:
	// PreserveRawBody at the top, MaxRequestBody on the route group, then
	// WithMaxBody on the specific route.
	var readErr error
	var readN int
	final := readAllHandler(t, &readErr, &readN)

	r := chi.NewRouter()
	r.Use(PreserveRawBody)
	r.Use(MaxRequestBody)
	r.With(WithMaxBody(10<<20)).Post("/big", final.ServeHTTP)

	body := bytes.Repeat([]byte("c"), 2<<20)
	req := httptest.NewRequest(http.MethodPost, "/big", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if readErr != nil {
		t.Fatalf("override should accept 2 MiB body, got error: %v", readErr)
	}
	if readN != len(body) {
		t.Errorf("read %d bytes, want %d", readN, len(body))
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestMaxBody_PerRouteOverrideStillEnforcesItsCap(t *testing.T) {
	// Sanity check that the override is itself a real cap, not "unlimited".
	// A 12 MiB body with a 10 MiB override must still fail.
	var readErr error
	var readN int
	final := readAllHandler(t, &readErr, &readN)

	r := chi.NewRouter()
	r.Use(PreserveRawBody)
	r.Use(MaxRequestBody)
	r.With(WithMaxBody(10<<20)).Post("/big", final.ServeHTTP)

	body := bytes.Repeat([]byte("d"), 12<<20)
	req := httptest.NewRequest(http.MethodPost, "/big", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if readErr == nil {
		t.Fatal("expected read error when body exceeds the override cap")
	}
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Fatalf("expected *http.MaxBytesError, got %T: %v", readErr, readErr)
	}
	if mbe.Limit != 10<<20 {
		t.Errorf("MaxBytesError.Limit = %d, want %d", mbe.Limit, 10<<20)
	}
}

func TestMaxBody_GetIsExempt(t *testing.T) {
	// GET has no body to read; the middleware must not allocate a wrapper
	// or otherwise interfere with the request. We can't easily assert "no
	// wrapper installed" so we just confirm a downstream Read returns 0
	// bytes with EOF (the default Body for a body-less request).
	var readErr error
	var readN int
	h := PreserveRawBody(MaxRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		readErr = err
		readN = len(buf)
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/y", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if readErr != nil {
		t.Errorf("GET body read returned error: %v", readErr)
	}
	if readN != 0 {
		t.Errorf("GET body read %d bytes, want 0", readN)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestMaxBody_DeleteIsExempt(t *testing.T) {
	// DELETE bodies are unusual in REST; Bindery never reads them. Confirm
	// the middleware does not wrap them so a hypothetical larger body would
	// pass through. Reading 2 MiB on DELETE must not produce a MaxBytesError.
	var readErr error
	var readN int
	h := PreserveRawBody(MaxRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		readErr = err
		readN = len(buf)
		w.WriteHeader(http.StatusOK)
	})))

	body := bytes.Repeat([]byte("e"), 2<<20)
	req := httptest.NewRequest(http.MethodDelete, "/z", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if readErr != nil {
		t.Fatalf("DELETE read returned error: %v", readErr)
	}
	if readN != len(body) {
		t.Errorf("DELETE read %d bytes, want %d (must pass through)", readN, len(body))
	}
}

func TestMaxBody_PutCapped(t *testing.T) {
	// PUT carries bodies (settings updates, OIDC providers). Confirm the cap
	// applies symmetrically with POST.
	var readErr error
	var readN int
	h := PreserveRawBody(MaxRequestBody(readAllHandler(t, &readErr, &readN)))

	body := bytes.Repeat([]byte("f"), 2<<20)
	req := httptest.NewRequest(http.MethodPut, "/w", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if readErr == nil {
		t.Fatal("expected PUT body to be capped")
	}
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Fatalf("expected *http.MaxBytesError, got %T", readErr)
	}
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rr.Code)
	}
}

func TestMaxBody_DefaultIsOneMiB(t *testing.T) {
	// Pin the documented default. A regression here would silently raise or
	// lower the cap for every JSON endpoint.
	if DefaultMaxRequestBody != 1<<20 {
		t.Errorf("DefaultMaxRequestBody = %d, want %d (1 MiB)", DefaultMaxRequestBody, 1<<20)
	}
}

// TestMaxBody_OversizedJSONReturns4xx is an end-to-end check on a real chi
// route that decodes JSON via json.NewDecoder(r.Body).Decode, the exact
// pattern audited in internal/api/bulk.go:57. The handler is a stand-in
// because spinning up the full BulkHandler would drag in db fixtures; the
// behavior the middleware guarantees is identical: an oversized body causes
// json.Decode to surface a *http.MaxBytesError before any allocations
// proportional to the body size happen.
func TestMaxBody_OversizedJSONReturns4xx(t *testing.T) {
	r := chi.NewRouter()
	r.Use(PreserveRawBody)
	r.Use(MaxRequestBody)
	r.Post("/api/v1/bulk", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// 2 MiB of JSON noise wrapped as a string field. The decoder will start
	// reading and trip the cap inside json.Decode.
	pad := strings.Repeat("a", 2<<20)
	body := `{"ids":[1],"pad":"` + pad + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bulk", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body = %q", rr.Code, rr.Body.String())
	}
}
