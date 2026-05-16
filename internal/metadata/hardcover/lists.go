package hardcover

import (
	"context"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// Hardcover documents user_books.status_id values in its official API docs:
// https://github.com/hardcoverapp/hardcover-docs/blob/31aaa75774ec560312222e5834322c71b79dbb5b/src/content/docs/api/GraphQL/Schemas/UserBooks.mdx#L12-L21
const (
	hcStatusWantToRead       = 1
	hcStatusCurrentlyReading = 2
	hcStatusRead             = 3
	hcStatusPaused           = 4
	hcStatusDidNotFinish     = 5
	hcStatusIgnored          = 6
)

// GetUserWishlist fetches the authenticated user's "Want to Read" books.
// Returns candidates suitable for list-cross recommendations.
// Requires the client to have an API token set via WithToken; returns nil if not configured.
func (c *Client) GetUserWishlist(ctx context.Context, limit int) ([]models.RecommendationCandidate, error) {
	if c.token == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	gql := `query GetWishlist($limit: Int!, $statusID: Int!) {
		me {
			user_books(where: {status_id: {_eq: $statusID}}, limit: $limit) {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	vars := map[string]any{"limit": limit, "statusID": hcStatusWantToRead}
	var resp struct {
		Data struct {
			Me []struct {
				UserBooks []struct {
					Book hcBook `json:"book"`
				} `json:"user_books"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, vars, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get wishlist: %w", err)
	}
	if len(resp.Data.Me) == 0 {
		return nil, nil
	}

	candidates := make([]models.RecommendationCandidate, 0, len(resp.Data.Me[0].UserBooks))
	for _, ub := range resp.Data.Me[0].UserBooks {
		b := c.toBook(ub.Book)
		cand := models.RecommendationCandidate{
			ForeignID:    b.ForeignID,
			Title:        b.Title,
			ImageURL:     b.ImageURL,
			Description:  b.Description,
			Rating:       b.AverageRating,
			RatingsCount: b.RatingsCount,
			ReleaseDate:  b.ReleaseDate,
			MediaType:    models.MediaTypeEbook,
			Genres:       []string{},
		}
		if b.Author != nil {
			cand.AuthorName = b.Author.Name
		}
		candidates = append(candidates, cand)
	}
	return candidates, nil
}

// HCList represents a Hardcover reading list or built-in shelf.
// Built-in shelves use negative IDs: -1 Want to Read, -2 Currently Reading,
// -3 Read, -4 Did Not Finish.
type HCList struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	BooksCount int    `json:"booksCount"`
}

// hcBuiltinShelves are the four Hardcover reading-status shelves Bindery exposes
// for list sync. Hardcover also defines Paused (hcStatusPaused), which is not
// exposed as a synthetic list because existing list sync behavior only surfaces
// DNF.
// They live in user_books (filtered by status_id), not in me.lists, so they
// are injected as synthetic entries using negative IDs to avoid collision with
// real list IDs.
var hcBuiltinShelves = []HCList{
	{ID: -1, Name: "Want to Read", Slug: "want-to-read"},
	{ID: -2, Name: "Currently Reading", Slug: "currently-reading"},
	{ID: -3, Name: "Read", Slug: "read"},
	{ID: -4, Name: "Did Not Finish", Slug: "did-not-finish"},
}

// hcShelfStatusID maps a synthetic shelf list ID to its Hardcover status_id.
func hcShelfStatusID(listID int) (int, bool) {
	switch listID {
	case -1:
		return hcStatusWantToRead, true
	case -2:
		return hcStatusCurrentlyReading, true
	case -3:
		return hcStatusRead, true
	case -4:
		return hcStatusDidNotFinish, true
	}
	return 0, false
}

// GetUserLists returns the authenticated user's reading lists, prepended by
// the four built-in Hardcover shelves (Want to Read, Currently Reading, Read,
// Did Not Finish). Built-in shelves always appear even when the user has no
// custom lists, which was the root cause of the "No lists found" report.
func (c *Client) GetUserLists(ctx context.Context) ([]HCList, error) {
	gql := `query GetUserLists {
		me {
			lists {
				id
				name
				slug
				books_count
			}
		}
	}`
	var resp struct {
		Data struct {
			Me []struct {
				Lists []struct {
					ID         int    `json:"id"`
					Name       string `json:"name"`
					Slug       string `json:"slug"`
					BooksCount int    `json:"books_count"`
				} `json:"lists"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, nil, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get user lists: %w", err)
	}
	var customLists []struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		BooksCount int    `json:"books_count"`
	}
	if len(resp.Data.Me) > 0 {
		customLists = resp.Data.Me[0].Lists
	}
	lists := make([]HCList, 0, len(hcBuiltinShelves)+len(customLists))
	lists = append(lists, hcBuiltinShelves...)
	for _, l := range customLists {
		lists = append(lists, HCList{
			ID:         l.ID,
			Name:       l.Name,
			Slug:       l.Slug,
			BooksCount: l.BooksCount,
		})
	}
	return lists, nil
}

// GetListBooks returns all books in the given list as Bindery models.
// Negative listIDs refer to built-in Hardcover shelves (see hcBuiltinShelves).
func (c *Client) GetListBooks(ctx context.Context, listID int) ([]models.Book, error) {
	if statusID, ok := hcShelfStatusID(listID); ok {
		return c.getShelfBooks(ctx, statusID)
	}
	gql := `query GetListBooks($id: Int!) {
		lists(where: {id: {_eq: $id}}, limit: 1) {
			id
			name
			slug
			list_books {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					default_audio_edition_id
					default_ebook_edition_id
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			Lists []struct {
				ListBooks []struct {
					Book hcBook `json:"book"`
				} `json:"list_books"`
			} `json:"lists"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"id": listID}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get list books: %w", err)
	}
	if len(resp.Data.Lists) == 0 {
		return nil, nil
	}
	books := make([]models.Book, 0, len(resp.Data.Lists[0].ListBooks))
	for _, lb := range resp.Data.Lists[0].ListBooks {
		books = append(books, c.toBook(lb.Book))
	}
	return books, nil
}

// getShelfBooks fetches all books on a built-in Hardcover shelf by status_id.
func (c *Client) getShelfBooks(ctx context.Context, statusID int) ([]models.Book, error) {
	gql := `query GetShelfBooks($statusID: Int!) {
		me {
			user_books(where: {status_id: {_eq: $statusID}}, limit: 500) {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					default_audio_edition_id
					default_ebook_edition_id
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			Me []struct {
				UserBooks []struct {
					Book hcBook `json:"book"`
				} `json:"user_books"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"statusID": statusID}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get shelf books: %w", err)
	}
	if len(resp.Data.Me) == 0 {
		return nil, nil
	}
	books := make([]models.Book, 0, len(resp.Data.Me[0].UserBooks))
	for _, ub := range resp.Data.Me[0].UserBooks {
		books = append(books, c.toBook(ub.Book))
	}
	return books, nil
}
