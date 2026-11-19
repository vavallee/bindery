// Package rttest provides an http.RoundTripper backed by a function, for
// tests that drive a client with canned responses or transport errors and no
// listening server.
package rttest

import "net/http"

// Func is an http.RoundTripper that calls itself.
type Func func(*http.Request) (*http.Response, error)

// RoundTrip calls f(r).
func (f Func) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
