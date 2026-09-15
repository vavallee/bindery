package filterengine

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func strPtr(s string) *string { return &s }

func TestAnyEditionHasISBN(t *testing.T) {
	tests := []struct {
		name     string
		editions []models.Edition
		want     bool
	}{
		{"nil editions", nil, false},
		{"empty editions", []models.Edition{}, false},
		{"no ISBN on any edition", []models.Edition{{}}, false},
		{"ISBN13 present", []models.Edition{{ISBN13: strPtr("9780000000000")}}, true},
		{"ISBN10 present, no ISBN13", []models.Edition{{ISBN10: strPtr("0000000000")}}, true},
		{"blank ISBN13 string doesn't count", []models.Edition{{ISBN13: strPtr("  ")}}, false},
		{"blank ISBN10 string doesn't count", []models.Edition{{ISBN10: strPtr(" ")}}, false},
		{"second edition carries the ISBN10", []models.Edition{{}, {ISBN10: strPtr("0000000000")}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyEditionHasISBN(tt.editions); got != tt.want {
				t.Errorf("AnyEditionHasISBN(%+v) = %v, want %v", tt.editions, got, tt.want)
			}
		})
	}
}
