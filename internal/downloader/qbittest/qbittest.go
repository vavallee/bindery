// Package qbittest is an in-process fake of the qBittorrent WebUI API v2 for
// tests. It implements the login handshake and the session cookie check, so a
// test only supplies the behaviour of the endpoints it cares about.
//
// The fake refuses any non-login request that lacks the session cookie it
// issued at login, answering 403 as qBittorrent does. A client that skips or
// loses its login therefore fails the test instead of passing silently.
package qbittest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

const (
	loginPath  = "/api/v2/auth/login"
	cookieName = "SID"
	cookieVal  = "qbittest-session"
)

// Request is one request the fake received, recorded after parsing.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// Form holds the URL-encoded body for POST requests; it is nil for
	// multipart bodies.
	Form url.Values
	// Authenticated reports whether the request carried the session cookie.
	Authenticated bool
}

// Option configures a Server.
type Option func(*Server)

// WithCredentials makes login succeed only for the given username and
// password; any other pair gets 200 with the body "Fails.".
func WithCredentials(username, password string) Option {
	return func(s *Server) { s.username, s.password, s.checkCreds = username, password, true }
}

// WithNoContentLogin makes a successful login answer 204 No Content with an
// empty body, as qBittorrent 5.x does, instead of 200 "Ok.".
func WithNoContentLogin() Option {
	return func(s *Server) { s.noContentLogin = true }
}

// Server is a running fake. It embeds *httptest.Server, so URL and Close are
// available directly.
type Server struct {
	*httptest.Server

	handler        http.HandlerFunc
	username       string
	password       string
	checkCreds     bool
	noContentLogin bool

	mu       sync.Mutex
	logins   int
	requests []Request
}

// New starts a fake and closes it when the test ends. handler serves every
// authenticated request except login; nil answers 200 with an empty body.
func New(t testing.TB, handler http.HandlerFunc, opts ...Option) *Server {
	t.Helper()
	s := &Server{handler: handler}
	for _, o := range opts {
		o(s)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Logins returns the number of successful logins served.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// Requests returns a copy of every request received, in arrival order,
// including login and rejected requests.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	authed := err == nil && c.Value == cookieVal

	rec := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Authenticated: authed}
	if r.Method == http.MethodPost && r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec.Form, _ = url.ParseQuery(string(body))
	}
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	s.mu.Unlock()

	if r.URL.Path == loginPath {
		s.login(w, rec)
		return
	}
	if !authed {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
		return
	}
	if s.handler != nil {
		s.handler(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, rec Request) {
	if s.checkCreds && (rec.Form.Get("username") != s.username || rec.Form.Get("password") != s.password) {
		_, _ = w.Write([]byte("Fails."))
		return
	}
	s.mu.Lock()
	s.logins++
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: cookieVal, Path: "/"})
	if s.noContentLogin {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, _ = w.Write([]byte("Ok."))
}
