package api

import (
	"context"
	"io"
	"net/http"
)

// DefaultMaxRequestBody is the per-request body cap applied by MaxRequestBody
// when no per-route override is in effect. It is intentionally small:
// Bindery's JSON payloads are single records (an author, a book, a settings
// blob, a small ID list) so 1 MiB is generous in practice. Routes that
// legitimately accept larger payloads (file uploads, database dumps) opt in
// to a larger cap via WithMaxBody on the chi route.
const DefaultMaxRequestBody int64 = 1 << 20 // 1 MiB

// origBodyCtxKey carries the raw, un-wrapped request body so WithMaxBody
// can install a fresh MaxBytesReader against it. Without the original
// reference a per-route override would chain a larger MaxBytesReader on top
// of the default one and still clamp reads at the inner (lower) cap.
type origBodyCtxKey struct{}

// PreserveRawBody snapshots r.Body before any later middleware wraps it.
// Wired at the router root, before MaxRequestBody, so that WithMaxBody can
// retrieve the unwrapped reader and re-wrap with a higher per-route limit.
//
// The snapshot is only taken for methods that carry a body (POST, PUT,
// PATCH); other methods pass through without touching the context.
func PreserveRawBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasRequestBody(r.Method) && r.Body != nil {
			ctx := context.WithValue(r.Context(), origBodyCtxKey{}, r.Body)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// MaxRequestBody wraps r.Body in http.MaxBytesReader so any downstream
// json.Decode, io.ReadAll, or ParseForm call stops cleanly at the byte cap.
// Without this an authenticated client can POST a 10 GB body and pin the
// process inside json.Decode while Go grows its decode buffers.
//
// Only methods that carry a body (POST, PUT, PATCH) are wrapped. GET, HEAD,
// DELETE, and OPTIONS pass through untouched so the wrapper does not allocate
// for the read-mostly traffic that dominates the request mix.
//
// Per-route overrides use WithMaxBody. Because chi runs r.With middleware
// after r.Use middleware in the same chain, WithMaxBody cannot pre-empt the
// default by setting context; instead it grabs the original body that
// PreserveRawBody snapshotted at the router root and installs a fresh
// MaxBytesReader with the higher limit, replacing the default wrap entirely.
func MaxRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasRequestBody(r.Method) && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, DefaultMaxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// WithMaxBody returns a middleware that raises the per-request body cap to n
// bytes for a single route. Use it via chi's r.With for routes that
// legitimately accept larger payloads:
//
//	r.With(api.WithMaxBody(50 << 20)).Post("/migrate/readarr", h.ImportReadarr)
//
// The implementation discards the default wrap installed by MaxRequestBody
// and re-wraps the raw body (snapshotted by PreserveRawBody at the router
// root) with the higher limit. Without the discard a 1 MiB inner cap would
// silently clamp reads regardless of the outer limit.
func WithMaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasRequestBody(r.Method) || r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}
			raw, ok := r.Context().Value(origBodyCtxKey{}).(io.ReadCloser)
			if !ok {
				// PreserveRawBody was not in the chain (test fixtures, future
				// embedders). Fall back to wrapping whatever is on r.Body.
				// When the chain does not include the default cap this is
				// the correct outcome; when it does, the smaller inner cap
				// will still win but the developer is no worse off than
				// without WithMaxBody.
				raw = r.Body
			}
			r.Body = http.MaxBytesReader(w, raw, n)
			next.ServeHTTP(w, r)
		})
	}
}

// hasRequestBody reports whether the HTTP method routinely carries a request
// body. GET, HEAD, DELETE, and OPTIONS do not in any handler Bindery
// registers, so wrapping their body would only cost an allocation.
func hasRequestBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}
