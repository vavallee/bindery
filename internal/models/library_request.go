package models

import "time"

// Request kinds.
const (
	RequestKindBook   = "book"
	RequestKindAuthor = "author"
)

// Request statuses. RequestStatusApproving is a claim held while an approval
// runs; clients see it as pending.
const (
	RequestStatusPending   = "pending"
	RequestStatusApproving = "approving"
	RequestStatusApproved  = "approved"
	RequestStatusDeclined  = "declined"
)

// Who made a request on its owner's behalf (migration 113). Empty means the
// owner asked themselves.
const (
	RequestCreatedViaAPIKey = "api_key"
	RequestCreatedViaAdmin  = "admin"
)

// LibraryRequest is one row of the requests table (migration 089): a
// requester asking for a book or an author to be added.
type LibraryRequest struct {
	ID          int64
	OwnerUserID int64
	// OwnerUsername is joined in for the admin queue only.
	OwnerUsername string
	Kind          string
	ForeignID     string
	MediaType     string
	// Title is the book title, or the author's name for an author request,
	// as the metadata provider reported it when the request was made.
	Title      string
	AuthorName string
	// PayloadJSON is the server built add payload replayed at approval.
	PayloadJSON    string
	Status         string
	DeclineReason  string
	DecidedBy      *int64
	ResultBookID   *int64
	ResultAuthorID *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DecidedAt      *time.Time
	// ClaimToken is set on the request Claim returns, and only there.
	ClaimToken string
	// CreatedVia is who made the request for its owner: "" the owner,
	// RequestCreatedViaAPIKey or RequestCreatedViaAdmin. CreatedByUserID is
	// the admin for RequestCreatedViaAdmin, and CreatedByUsername is joined in
	// for the admin queue.
	CreatedVia        string
	CreatedByUserID   *int64
	CreatedByUsername string

	// Derived at list time, never stored. For a book request, BookStatus is
	// the status of the book the approval created. For an author request,
	// AuthorBooks and AuthorBooksImported count that author's books.
	BookStatus          string
	AuthorBooks         int
	AuthorBooksImported int
}
