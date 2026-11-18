package qbittest_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/qbittest"
)

func post(t *testing.T, c *http.Client, u string, form url.Values) (int, string) {
	t.Helper()
	resp, err := c.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestRejectsRequestWithoutSessionCookie(t *testing.T) {
	called := false
	s := qbittest.New(t, func(http.ResponseWriter, *http.Request) { called = true })

	resp, err := http.Get(s.URL + "/api/v2/torrents/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if called {
		t.Fatal("handler ran for an unauthenticated request")
	}
}

func TestLoginIssuesCookieAndUnlocksHandler(t *testing.T) {
	s := qbittest.New(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) })
	jar := newJar(t)
	c := &http.Client{Jar: jar}

	status, _ := post(t, c, s.URL+"/api/v2/auth/login", url.Values{"username": {"u"}, "password": {"p"}})
	if status != http.StatusOK {
		t.Fatalf("login status = %d", status)
	}
	resp2, err := c.Get(s.URL + "/api/v2/torrents/info?category=books")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200", resp2.StatusCode)
	}
	if s.Logins() != 1 {
		t.Fatalf("Logins = %d, want 1", s.Logins())
	}
	reqs := s.Requests()
	if len(reqs) != 2 || reqs[0].Form.Get("username") != "u" || !reqs[1].Authenticated || reqs[1].Query.Get("category") != "books" {
		t.Fatalf("unexpected recorded requests: %+v", reqs)
	}
}

func TestWithCredentialsRejectsWrongPassword(t *testing.T) {
	s := qbittest.New(t, nil, qbittest.WithCredentials("admin", "pass"))
	c := &http.Client{Jar: newJar(t)}

	_, body := post(t, c, s.URL+"/api/v2/auth/login", url.Values{"username": {"admin"}, "password": {"bad"}})
	if body != "Fails." || s.Logins() != 0 {
		t.Fatalf("body = %q, logins = %d; want Fails. and 0", body, s.Logins())
	}
}

func TestWithNoContentLogin(t *testing.T) {
	s := qbittest.New(t, nil, qbittest.WithNoContentLogin())
	status, _ := post(t, &http.Client{Jar: newJar(t)}, s.URL+"/api/v2/auth/login", url.Values{})
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
}

func newJar(t *testing.T) http.CookieJar {
	t.Helper()
	j, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
