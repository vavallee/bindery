package audiblesync

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vavallee/bindery/internal/isbnutil"
)

const (
	// listPageSize is the largest page its wishlist and library serve.
	listPageSize = 200
	// maxListPages bounds a wishlist or library walk: 50 × 200 books is far
	// past any real account, and stops a misbehaving server from looping a
	// sync forever.
	maxListPages = 50
)

// Health calls GET /api/health. It needs no token, so success proves only
// that the URL reaches Audible Sync; Accounts proves the token.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var out Health
	if err := c.do(ctx, http.MethodGet, "/api/health", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Accounts lists the Audible accounts Audible Sync holds.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var out []Account
	if err := c.do(ctx, http.MethodGet, "/api/accounts", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Wishlist returns every book on the wishlist of accountID (zero means the
// client's account, or Audible Sync's default), walking every page. The
// account states come from the first page.
func (c *Client) Wishlist(ctx context.Context, accountID int) ([]WishlistItem, []WishlistAccount, error) {
	var (
		items    []WishlistItem
		accounts []WishlistAccount
	)
	for page := 1; page <= maxListPages; page++ {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(listPageSize)},
			"sort":      {"date_added"},
			"order":     {"asc"},
		}
		if accountID > 0 {
			q.Set("account_id", strconv.Itoa(accountID))
		}
		var out WishlistPage
		if err := c.do(ctx, http.MethodGet, "/api/wishlist", c.query(q), nil, &out); err != nil {
			return nil, nil, err
		}
		if page == 1 {
			accounts = out.Accounts
		}
		items = append(items, out.Items...)
		if len(out.Items) == 0 || len(items) >= out.Total {
			break
		}
	}
	return items, accounts, nil
}

// RemoveFromWishlist takes asin off the wishlist of accountID. A book that
// is not there counts as removed.
func (c *Client) RemoveFromWishlist(ctx context.Context, accountID int, asin string) error {
	asin = isbnutil.NormalizeASIN(asin)
	if asin == "" {
		return errors.New("asin required")
	}
	if accountID <= 0 {
		accountID = c.accountID
	}
	if accountID <= 0 {
		return errors.New("removing from a wishlist needs an Audible Sync account id")
	}
	path := "/api/wishlist/" + strconv.Itoa(accountID) + "/" + url.PathEscape(asin)
	err := c.do(ctx, http.MethodDelete, path, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Library returns the books in the library of accountID (zero: the client's
// account, or every account when neither names one), walking every page.
// status, when not empty, keeps only books in that state, such as
// LibraryStatusUnavailable.
func (c *Client) Library(ctx context.Context, accountID int, status string) ([]LibraryBook, error) {
	var items []LibraryBook
	for page := 1; page <= maxListPages; page++ {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(listPageSize)},
		}
		if accountID > 0 {
			q.Set("account_id", strconv.Itoa(accountID))
		}
		if status != "" {
			q.Set("status", status)
		}
		var out LibraryPage
		if err := c.do(ctx, http.MethodGet, "/api/books", c.query(q), nil, &out); err != nil {
			return nil, err
		}
		items = append(items, out.Items...)
		if len(out.Items) == 0 || len(items) >= out.Total {
			break
		}
	}
	return items, nil
}

// LibraryBook returns one book of a library by Audible Sync's id for it. A
// book that has left the library (refunded, say) is ErrNotFound.
func (c *Client) LibraryBook(ctx context.Context, id int) (*LibraryBook, error) {
	if id <= 0 {
		return nil, errors.New("a library book id is required")
	}
	var out LibraryBook
	if err := c.do(ctx, http.MethodGet, "/api/books/"+strconv.Itoa(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CatalogBook looks one book up in the Audible catalogue through the account
// accountID (zero: the client's, or Audible Sync's default), description
// included. Unlike the other reads this is a live request to Audible, which
// Audible Sync neither paces nor batches: call it for one book at a time. A
// book Audible does not know is ErrNotFound.
func (c *Client) CatalogBook(ctx context.Context, asin string, accountID int) (*CatalogBook, error) {
	asin = isbnutil.NormalizeASIN(asin)
	if asin == "" {
		return nil, errors.New("asin required")
	}
	q := url.Values{}
	if accountID > 0 {
		q.Set("account_id", strconv.Itoa(accountID))
	}
	var out CatalogBook
	if err := c.do(ctx, http.MethodGet, "/api/catalog/books/"+url.PathEscape(asin), c.query(q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WishlistTotal is how many books are on the wishlist of accountID, read with
// a one-book page.
func (c *Client) WishlistTotal(ctx context.Context, accountID int) (int, error) {
	q := url.Values{"page": {"1"}, "page_size": {"1"}, "account_id": {strconv.Itoa(accountID)}}
	var out WishlistPage
	if err := c.do(ctx, http.MethodGet, "/api/wishlist", q, nil, &out); err != nil {
		return 0, err
	}
	return out.Total, nil
}

// LibraryTotal is how many books of the library of accountID are in status
// (every book when status is empty), read with a one-book page.
func (c *Client) LibraryTotal(ctx context.Context, accountID int, status string) (int, error) {
	q := url.Values{"page": {"1"}, "page_size": {"1"}, "account_id": {strconv.Itoa(accountID)}}
	if status != "" {
		q.Set("status", status)
	}
	var out LibraryPage
	if err := c.do(ctx, http.MethodGet, "/api/books", q, nil, &out); err != nil {
		return 0, err
	}
	return out.Total, nil
}
