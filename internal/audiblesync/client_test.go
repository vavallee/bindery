package audiblesync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "tok-123"

// newTestClient points a client at h with pacing off.
func newTestClient(t *testing.T, accountID int, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testToken, accountID)
	if err != nil {
		t.Fatal(err)
	}
	return c.withHTTPClient(srv.Client()).withPacing(0)
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "http://audible-sync:8080/", want: "http://audible-sync:8080"},
		{in: " https://example.org/audible-sync/ ", want: "https://example.org/audible-sync"},
		// A pasted URL ending in /api would double the prefix.
		{in: "http://10.0.0.5:8080/api", want: "http://10.0.0.5:8080"},
		{in: "http://10.0.0.5:8080/?x=1#f", want: "http://10.0.0.5:8080"},
		{in: "10.0.0.5:8080", wantErr: "missing a scheme"},
		{in: "audible-sync:8080", wantErr: "missing a scheme"},
		{in: "ftp://host", wantErr: "must use http or https"},
		{in: "", wantErr: "required"},
	}
	for _, tt := range tests {
		got, err := NormalizeBaseURL(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NormalizeBaseURL(%q) err = %v, want one containing %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestValidateBaseURLSecureRefusesMetadataAddress(t *testing.T) {
	if _, err := ValidateBaseURLSecure("http://169.254.169.254/"); err == nil {
		t.Fatal("a cloud metadata address must be refused")
	}
	if _, err := ValidateBaseURLSecure("http://192.168.1.20:8080"); err != nil {
		t.Fatalf("a LAN address must be allowed: %v", err)
	}
}

func TestNewClientRequiresToken(t *testing.T) {
	if _, err := NewClient("http://10.0.0.5:8080", "  ", 0); err == nil {
		t.Fatal("an empty token must be refused")
	}
	if _, err := NewClient("http://10.0.0.5:8080", "a\nb", 0); err == nil {
		t.Fatal("a token with a newline must be refused")
	}
	c, err := NewClient("http://10.0.0.5:8080", "Bearer abc", 0)
	if err != nil || c.token != "abc" {
		t.Fatalf("a pasted Bearer prefix should be stripped: %v %q", err, c.token)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized", 401, `{"detail":"Not authenticated"}`, ErrUnauthorized},
		{"not found", 404, `{"detail":"Not in the catalogue"}`, ErrNotFound},
		{"needs login", 409, `{"detail":"Main needs signing in again"}`, ErrNeedsLogin},
		{"refused", 409, `{"detail":"Audible refused Main; sign in again"}`, ErrNeedsLogin},
		{"no account", 409, `{"detail":"Add an Audible account, or sign in to one, to search the catalogue"}`, ErrNeedsLogin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.Accounts(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidationErrorDetailIsReadable(t *testing.T) {
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"detail":[{"loc":["query","page"],"msg":"Input should be greater than or equal to 1"}]}`)
	})
	_, err := c.Accounts(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 || !strings.Contains(apiErr.Detail, "greater than") {
		t.Fatalf("err = %#v", err)
	}
}

func TestRetriesBadGatewayThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(t, w, 200, Health{Status: "ok", Version: "1.2.3"})
	})
	h, err := c.Health(context.Background())
	if err != nil || h.Version != "1.2.3" {
		t.Fatalf("health = %+v, %v", h, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, _ = c.Accounts(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestWishlistSendsTokenAndAccount(t *testing.T) {
	c := newTestClient(t, 7, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/api/wishlist" || r.URL.Query().Get("account_id") != "3" {
			t.Errorf("%s %v", r.URL.Path, r.URL.Query())
		}
		writeJSON(t, w, 200, WishlistPage{Total: 0})
	})
	// An explicit account wins over the client's default.
	if _, _, err := c.Wishlist(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveFromWishlistTreatsMissingAsRemoved(t *testing.T) {
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/wishlist/4/B00ABCDEFG" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, 404, map[string]string{"detail": "B00ABCDEFG was not on Main's wishlist"})
	})
	if err := c.RemoveFromWishlist(context.Background(), 4, "B00ABCDEFG"); err != nil {
		t.Fatal(err)
	}
}

func TestWishlistWalksEveryPage(t *testing.T) {
	const total = 450
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		var items []WishlistItem
		for i := (page - 1) * size; i < min(page*size, total); i++ {
			items = append(items, WishlistItem{CatalogBook: CatalogBook{ASIN: "B" + strconv.Itoa(1000000000 + i)[1:], Title: "T"}, AccountID: 1})
		}
		writeJSON(t, w, 200, WishlistPage{Items: items, Total: total, Page: page, PageSize: size,
			Accounts: []WishlistAccount{{AccountID: 1}}})
	})
	items, accounts, err := c.Wishlist(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != total || len(accounts) != 1 {
		t.Fatalf("items = %d, accounts = %d", len(items), len(accounts))
	}
}

func TestLibraryBookByID(t *testing.T) {
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/books/12":
			writeJSON(t, w, 200, LibraryBook{ID: 12, AccountID: 1, ASIN: "B00ABCDEFG", Status: LibraryStatusDownloading})
		default:
			writeJSON(t, w, 404, map[string]string{"detail": "No book with id 13"})
		}
	})
	b, err := c.LibraryBook(context.Background(), 12)
	if err != nil || b.Status != LibraryStatusDownloading || b.ASIN != "B00ABCDEFG" {
		t.Fatalf("LibraryBook(12) = %+v, %v", b, err)
	}
	// A book gone from the library is ErrNotFound, which the caller acts on.
	if _, err := c.LibraryBook(context.Background(), 13); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LibraryBook(13) err = %v, want ErrNotFound", err)
	}
}

func TestOwnedByIsPerAccount(t *testing.T) {
	// Account 2 owns the book; account 1, on whose wishlist it is, does not.
	b := CatalogBook{InLibrary: true, Library: []LibraryCopy{{AccountID: 2, BookID: 7}}}
	if b.OwnedBy(1) {
		t.Error("a book only another account owns must not count as owned")
	}
	if !b.OwnedBy(2) {
		t.Error("the owning account must count it as owned")
	}
	// No account named, or no copies listed (an older Audible Sync): the
	// marketplace-wide flag is all there is.
	if !b.OwnedBy(0) {
		t.Error("with no account named, InLibrary decides")
	}
	if !(CatalogBook{InLibrary: true}).OwnedBy(1) {
		t.Error("without copies listed, InLibrary decides")
	}
}

func TestLibraryBookAsCatalogBookKeepsItsCopy(t *testing.T) {
	cb := LibraryBook{ID: 9, AccountID: 3, ASIN: "B00ABCDEFG", Status: LibraryStatusUnavailable, Language: "en"}.CatalogBook()
	if c, ok := cb.CopyIn(3); !ok || c.BookID != 9 || c.Status != LibraryStatusUnavailable {
		t.Fatalf("copy = %+v, %v", c, ok)
	}
	if cb.Language != "en" {
		t.Fatalf("language = %q, want it carried over", cb.Language)
	}
}

func TestCatalogBookSendsAccountAndReadsDescription(t *testing.T) {
	c := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/catalog/books/B00ABCDEFG" && r.URL.Query().Get("account_id") == "2" {
			writeJSON(t, w, 200, map[string]any{"asin": "B00ABCDEFG", "title": "T", "description": "Plain text.", "description_html": "<p>Plain text.</p>"})
			return
		}
		writeJSON(t, w, 404, map[string]string{"detail": "No book in the catalogue"})
	})
	b, err := c.CatalogBook(context.Background(), "b00abcdefg", 2)
	if err != nil || b.Description != "Plain text." {
		t.Fatalf("CatalogBook = %+v, %v", b, err)
	}
	if _, err := c.CatalogBook(context.Background(), "B00ZZZZZZZ", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ASIN err = %v, want ErrNotFound", err)
	}
}
