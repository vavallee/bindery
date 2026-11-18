package models

import "time"

// Import list types the syncers understand.
const (
	ImportListTypeHardcover   = "hardcover"
	ImportListTypeCSV         = "csv"
	ImportListTypeAudibleSync = "audiblesync"
)

type ImportList struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	APIKey           string `json:"apiKey"`
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
	// Account is the provider-side account identity the list belongs to
	// (#1489) — for Hardcover, the username reported by the token the list
	// was loaded with. Two accounts' built-in shelves share slugs, so list
	// identity is (URL slug, Account). Empty for legacy rows.
	Account          string `json:"account"`
	RootFolderID     *int64 `json:"rootFolderId"`
	QualityProfileID *int64 `json:"qualityProfileId"`
	MonitorNew       bool   `json:"monitorNew"`
	AutoAdd          bool   `json:"autoAdd"`
	Enabled          bool   `json:"enabled"`
	// MediaType pins the format that books synced from this list are created
	// as: "ebook", "audiobook", or "both". Empty means "unset" — keep the
	// media type derived from the source (e.g. Hardcover edition availability).
	MediaType string `json:"mediaType"`
	// OwnerUserID is the Bindery user who owns books and authors synced from
	// this list under multi-user tenancy. nil = global (visible to all users),
	// which is what legacy rows carry and what an admin picks for a shared
	// shelf. A user id scopes every synced book+author to that user, closing
	// the leak where scheduler-synced content was always NULL-owned/global.
	OwnerUserID *int64 `json:"ownerUserId"`
	// SkipOwned and RemoveOnImport are options of the Audible Sync wishlist
	// list (Type ImportListTypeAudibleSync); other types ignore them.
	// SkipOwned leaves a wishlist book the Audible account already owns alone,
	// since Audible Sync downloads it. RemoveOnImport takes a book off the
	// Audible wishlist once Bindery has imported it.
	SkipOwned      bool       `json:"skipOwned"`
	RemoveOnImport bool       `json:"removeOnImport"`
	LastSyncAt     *time.Time `json:"lastSyncAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// Sources of an Audible Sync list, stored in ImportList.URL (the field a
// Hardcover list keeps its slug in: which list on the provider it reads).
const (
	// ImportListSourceWishlist reads the account's wishlist. An empty URL
	// means the same, for rows written before there was a choice.
	ImportListSourceWishlist = "wishlist"
	// ImportListSourceUnavailable reads the books in the account's library
	// Audible no longer lets it download, typically titles withdrawn from the
	// Plus catalogue.
	ImportListSourceUnavailable = "unavailable"
)

// ImportListBook records that a list created a book.
type ImportListBook struct {
	ImportListID int64
	BookID       int64
	ASIN         string
}

type ImportListExclusion struct {
	ID         int64     `json:"id"`
	ForeignID  string    `json:"foreignId"`
	Title      string    `json:"title"`
	AuthorName string    `json:"authorName"`
	CreatedAt  time.Time `json:"createdAt"`
}
