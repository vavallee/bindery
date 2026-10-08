package models

import "time"

type Series struct {
	ID          int64  `json:"id"`
	ForeignID   string `json:"foreignSeriesId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Monitored   bool   `json:"monitored"`
	// GenreOverride is the genre list applied and locked on every book created
	// in this series (#1709). GenreOverrideSet distinguishes "no override" from
	// "override deliberately set to no genres" — the column is nullable and the
	// two states behave differently on book creation, so the client needs both
	// to render the current state and to offer clearing it (#1711 follow-up).
	GenreOverride    []string  `json:"genreOverride,omitempty"`
	GenreOverrideSet bool      `json:"genreOverrideSet"`
	CreatedAt        time.Time `json:"createdAt"`

	// Joined data
	Books         []SeriesBook         `json:"books,omitempty"`
	HardcoverLink *SeriesHardcoverLink `json:"hardcoverLink,omitempty"`
	// SplitEditionPartBookIDs lists the books in this series that are split
	// edition parts of a whole the library already has (#3048), filled by the
	// series API only. Fill and the wanted sweep skip them.
	SplitEditionPartBookIDs []int64 `json:"splitEditionPartBookIds,omitempty"`
}

type SeriesHardcoverLink struct {
	ID                  int64  `json:"id"`
	SeriesID            int64  `json:"seriesId"`
	HardcoverSeriesID   string `json:"hardcoverSeriesId"`
	HardcoverProviderID string `json:"hardcoverProviderId"`
	// HardcoverSlug is the series' slug on hardcover.app, which is the only
	// identifier its public page routes on (#1708). Empty for links stored
	// before migration 080 and for any candidate Hardcover returned without
	// one; callers must treat "" as "no public link" rather than build a URL
	// from HardcoverProviderID, which resolves nowhere.
	HardcoverSlug       string    `json:"hardcoverSlug"`
	HardcoverTitle      string    `json:"hardcoverTitle"`
	HardcoverAuthorName string    `json:"hardcoverAuthorName"`
	HardcoverBookCount  int       `json:"hardcoverBookCount"`
	Confidence          float64   `json:"confidence"`
	LinkedBy            string    `json:"linkedBy"`
	LinkedAt            time.Time `json:"linkedAt"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type SeriesBook struct {
	SeriesID         int64  `json:"seriesId"`
	BookID           int64  `json:"bookId"`
	PositionInSeries string `json:"positionInSeries"`
	PrimarySeries    bool   `json:"primarySeries"`

	// Joined
	Book *Book `json:"book,omitempty"`
}
