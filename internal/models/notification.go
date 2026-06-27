package models

import "time"

type Notification struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	Headers   string    `json:"headers"`
	// Topic, when set, makes the webhook POST to the server root with a "topic"
	// field instead of POSTing to URL directly. This is what ntfy needs to render
	// a JSON payload natively instead of printing it verbatim (#1323).
	Topic     string    `json:"topic"`
	OnGrab    bool      `json:"onGrab"`
	OnImport  bool      `json:"onImport"`
	OnUpgrade bool      `json:"onUpgrade"`
	OnFailure bool      `json:"onFailure"`
	OnHealth  bool      `json:"onHealth"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
