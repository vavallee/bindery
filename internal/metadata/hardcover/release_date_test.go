package hardcover

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// #3621: Hardcover books with a known exact release date must not be
// flattened to 1 January of the release year.
func TestToBook_ReleaseDate(t *testing.T) {
	c := &Client{}
	cases := []struct {
		name string
		json string
		want string
	}{
		{"exact date wins over year", `{"id":1,"title":"A","release_date":"2025-03-14","release_year":2025}`, "2025-03-14"},
		{"year only gives Jan 1", `{"id":2,"title":"B","release_year":2025}`, "2025-01-01"},
		{"empty date falls back to year", `{"id":3,"title":"C","release_date":"","release_year":2024}`, "2024-01-01"},
		{"malformed date falls back to year", `{"id":4,"title":"D","release_date":"soon","release_year":2023}`, "2023-01-01"},
		{"exact future date is honoured", `{"id":5,"title":"E","release_date":"2031-09-02","release_year":2031}`, "2031-09-02"},
		{"nothing known stays nil", `{"id":6,"title":"F"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hb hcBook
			if err := json.Unmarshal([]byte(tc.json), &hb); err != nil {
				t.Fatal(err)
			}
			got := c.toBook(hb).ReleaseDate
			if tc.want == "" {
				if got != nil {
					t.Fatalf("want nil, got %v", got)
				}
				return
			}
			if got == nil || got.Format(time.DateOnly) != tc.want {
				t.Fatalf("want %s, got %v", tc.want, got)
			}
		})
	}
}

func TestAuthorWorksProjectionRequestsReleaseDate(t *testing.T) {
	if !strings.Contains(authorWorksProjection, "release_date") {
		t.Error("authorWorksProjection lacks release_date")
	}
}
