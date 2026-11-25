package api

import (
	"strings"
	"testing"
)

func TestValidForeignID(t *testing.T) {
	good := []string{
		"OL123W", "OL39307A", "hc:the-hobbit", "dnb:1234567", "gb:abc_DEF-9", "audnex:B0CXYZ1234",
		"ranobedb:12", "nb:author:123", "bnf:ark:/12148/cb118859183", "audible:B01N5AX61W",
	}
	for _, id := range good {
		if !validForeignID(id) {
			t.Errorf("validForeignID(%q) = false, want true (a real provider id)", id)
		}
	}
	bad := []string{
		"", "OL1W?q=x", "OL1W#frag", "OL1W/../../search.json", "gb:../../../books/v1/volumes",
		"dnb:1%20or%20tit=a", `dnb:1"`, "a b", "x\r\ny", "dnb:1;2", "a\\b", "é", strings.Repeat("a", requestForeignIDMaxLen+1),
	}
	for _, id := range bad {
		if validForeignID(id) {
			t.Errorf("validForeignID(%q) = true, want false", id)
		}
	}
}
