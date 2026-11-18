package rttest_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/rttest"
)

func TestFuncServesResponseAndError(t *testing.T) {
	boom := errors.New("boom")
	var calls int
	c := &http.Client{Transport: rttest.Func(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/fail" {
			return nil, boom
		}
		return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader("tea"))}, nil
	})}

	resp, err := c.Get("http://x.invalid/ok")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot || string(body) != "tea" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	failed, err := c.Get("http://x.invalid/fail")
	if failed != nil {
		failed.Body.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
