package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestGroupClaimValues_Shapes exercises the group-claim shape variance handled
// for OIDC role mapping (issue #688): a JSON array of strings, a single
// space/comma-delimited string, and the degenerate / absent cases.
func TestGroupClaimValues_Shapes(t *testing.T) {
	tests := []struct {
		name      string
		claimJSON string // the value stored under the "groups" key, as raw JSON
		want      []string
	}{
		{
			name:      "json array of strings",
			claimJSON: `["bindery-admin","staff"]`,
			want:      []string{"bindery-admin", "staff"},
		},
		{
			name:      "single bare string",
			claimJSON: `"bindery-admin"`,
			want:      []string{"bindery-admin"},
		},
		{
			name:      "space-delimited string",
			claimJSON: `"bindery-admin staff readers"`,
			want:      []string{"bindery-admin", "staff", "readers"},
		},
		{
			name:      "comma-delimited string",
			claimJSON: `"bindery-admin,staff,readers"`,
			want:      []string{"bindery-admin", "staff", "readers"},
		},
		{
			name:      "comma-and-space-delimited string",
			claimJSON: `"bindery-admin, staff , readers"`,
			want:      []string{"bindery-admin", "staff", "readers"},
		},
		{
			name:      "empty array",
			claimJSON: `[]`,
			want:      nil,
		},
		{
			name:      "empty string",
			claimJSON: `""`,
			want:      nil,
		},
		{
			name:      "json null",
			claimJSON: `null`,
			want:      nil,
		},
		{
			name:      "array with empty and whitespace entries",
			claimJSON: `["bindery-admin","","  ","staff"]`,
			want:      []string{"bindery-admin", "staff"},
		},
		{
			name:      "unsupported type (number)",
			claimJSON: `42`,
			want:      nil,
		},
		{
			name:      "mixed-type array keeps only strings",
			claimJSON: `["bindery-admin",1,true,"staff"]`,
			want:      []string{"bindery-admin", "staff"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{}
			if err := json.Unmarshal([]byte(`{"groups":`+tc.claimJSON+`}`), &raw); err != nil {
				t.Fatalf("unmarshal test fixture: %v", err)
			}
			got := GroupClaimValues(raw, "groups")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GroupClaimValues = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestGroupClaimValues_CustomClaimName verifies the configurable claim path.
func TestGroupClaimValues_CustomClaimName(t *testing.T) {
	raw := map[string]any{
		"groups":        []any{"ignored"},
		"roles/bindery": []any{"bindery-admin"},
	}
	got := GroupClaimValues(raw, "roles/bindery")
	if !reflect.DeepEqual(got, []string{"bindery-admin"}) {
		t.Errorf("GroupClaimValues custom claim = %#v, want [bindery-admin]", got)
	}
}

// TestGroupClaimValues_MissingClaimOrNilMap verifies the absent-claim and
// nil-map paths return nil rather than panicking.
func TestGroupClaimValues_MissingClaimOrNilMap(t *testing.T) {
	if got := GroupClaimValues(nil, "groups"); got != nil {
		t.Errorf("nil map: got %#v, want nil", got)
	}
	if got := GroupClaimValues(map[string]any{}, ""); got != nil {
		t.Errorf("empty claim name: got %#v, want nil", got)
	}
	if got := GroupClaimValues(map[string]any{"other": "x"}, "groups"); got != nil {
		t.Errorf("missing claim: got %#v, want nil", got)
	}
}

// TestGroupClaimValues_StringSliceShape covers the []string concrete type,
// which the json decoder will not produce but a direct caller might pass.
func TestGroupClaimValues_StringSliceShape(t *testing.T) {
	raw := map[string]any{"groups": []string{"bindery-admin", " staff "}}
	got := GroupClaimValues(raw, "groups")
	if !reflect.DeepEqual(got, []string{"bindery-admin", "staff"}) {
		t.Errorf("GroupClaimValues = %#v, want [bindery-admin staff]", got)
	}
}

func TestContainsGroup(t *testing.T) {
	groups := []string{"staff", "bindery-admin"}
	if !ContainsGroup(groups, "bindery-admin") {
		t.Error("ContainsGroup: want true for present group")
	}
	if ContainsGroup(groups, "Bindery-Admin") {
		t.Error("ContainsGroup: matching must be case-sensitive")
	}
	if ContainsGroup(groups, "missing") {
		t.Error("ContainsGroup: want false for absent group")
	}
	if ContainsGroup(nil, "anything") {
		t.Error("ContainsGroup: want false for nil slice")
	}
}
