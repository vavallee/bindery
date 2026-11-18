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
