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
	// Monitoring is the scheduled check's state for this series (#2523). Nil
	// until the series has been monitored or checked at least once.
	Monitoring *SeriesMonitoring `json:"monitoring,omitempty"`
}

// Series monitoring modes (#2523). Upcoming adds only books that are announced
// or newly released and leaves back-catalogue gaps to an explicit Fill. All
// adds every book the catalogue has that the library does not.
const (
	SeriesMonitorUpcoming = "upcoming"
	SeriesMonitorAll      = "all"
)

// Outcomes of a series check, stored in SeriesMonitoring.LastStatus.
const (
	SeriesCheckOK        = "ok"
	SeriesCheckNoLink    = "no_link"
	SeriesCheckDisabled  = "provider_disabled"
	SeriesCheckFailed    = "failed"
	SeriesCheckAmbiguous = "ambiguous_owner"
	SeriesCheckTimedOut  = "timed_out"
	// SeriesCheckPartial is a check that read the catalogue but could not add
	// every book it meant to. It does not advance last_ok_at, so the next check
	// still sees what this one missed.
	SeriesCheckPartial = "partial"
)

// SeriesMonitoring is what the scheduled series check records per series.
type SeriesMonitoring struct {
	Mode        string     `json:"mode"`
	OwnerUserID int64      `json:"-"`
	LastCheckAt *time.Time `json:"lastCheckAt,omitempty"`
	// LastOKAt is the start of the last check that read the whole catalogue.
	LastOKAt    *time.Time `json:"-"`
	NextCheckAt *time.Time `json:"nextCheckAt,omitempty"`
	LastStatus  string     `json:"lastStatus,omitempty"`
	LastMessage string     `json:"lastMessage,omitempty"`
	LastCreated int        `json:"lastCreated"`
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
