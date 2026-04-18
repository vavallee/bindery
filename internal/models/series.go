package models

import "time"

type Series struct {
	ID          int64     `json:"id"`
	ForeignID   string    `json:"foreignSeriesId"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Monitored   bool      `json:"monitored"`
	CreatedAt   time.Time `json:"createdAt"`

	// Joined data
	Books []SeriesBook `json:"books,omitempty"`
}

type SeriesBook struct {
	SeriesID         int64  `json:"seriesId"`
	BookID           int64  `json:"bookId"`
	PositionInSeries string `json:"positionInSeries"`
	PrimarySeries    bool   `json:"primarySeries"`

	// Joined
	Book *Book `json:"book,omitempty"`
}
