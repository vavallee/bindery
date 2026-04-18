package models

import "time"

// UnknownLanguageBehavior controls what happens when a metadata source
// returns no language for a book while the profile has a non-empty
// allowedLanguages list. See #232.
const (
	UnknownLanguagePass = "pass"
	UnknownLanguageFail = "fail"
)

type MetadataProfile struct {
	ID                      int64     `json:"id"`
	Name                    string    `json:"name"`
	MinPopularity           int       `json:"minPopularity"`
	MinPages                int       `json:"minPages"`
	SkipMissingDate         bool      `json:"skipMissingDate"`
	SkipMissingISBN         bool      `json:"skipMissingIsbn"`
	SkipPartBooks           bool      `json:"skipPartBooks"`
	AllowedLanguages        string    `json:"allowedLanguages"`
	UnknownLanguageBehavior string    `json:"unknownLanguageBehavior"`
	CreatedAt               time.Time `json:"createdAt"`
}
