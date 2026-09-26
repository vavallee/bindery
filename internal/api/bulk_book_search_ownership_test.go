package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
)

// #2668 puts a per book "Automatic search" button on the book page, and that
// button posts POST /book/bulk with one id rather than a route of its own,
// because BooksBulk is the only API entry point into
// scheduler.SearchAndGrabBook and already owns both guards. A reused guard has
// to be tested at the shape that now reaches it: the other IDOR matrices in
// bulk_test.go cover delete, monitor and exclude, but none covered
// action:"search", so nothing held the cross user check in place for the path
// this button uses.
//
// The durable invariant is the one that matters: not the per id error string,
// but whether a search was ever dispatched for a book the caller does not own.
func TestBulk_Book_Search_OwnershipMatrix(t *testing.T) {
	cases := []struct {
		name         string
		gateOn       bool
		callerIsBob  bool // false => Alice, who owns the targeted book
		admin        bool
		wantOK       bool
		wantSearched bool
	}{
		{"gate on, cross-user blocked", true, true, false, false, false},
		{"gate on, owner allowed", true, false, false, true, true},
		{"gate on, admin allowed", true, true, true, true, true},
		{"gate off, cross-user allowed", false, true, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.gateOn)
			f := seedTwoUserBulk(t)
			spy := newMockBookSearcher()
			h := NewBulkHandler(f.authors, f.books, nil, spy)

			caller := f.u1
			role := "user"
			if tc.callerIsBob {
				caller = f.u2
			}
			if tc.admin {
				caller = 99
				role = "admin"
			}

			body := fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.b1.ID)
			rec := postBulkAs(t, h.BooksBulk, body, caller, role)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := resultOK(t, rec, f.b1.ID); got != tc.wantOK {
				t.Errorf("per-id ok=%v, want %v", got, tc.wantOK)
			}
			if tc.wantSearched {
				got := spy.waitForCall(t, 2*time.Second)
				if got.ID != f.b1.ID {
					t.Errorf("searched book %d, want %d", got.ID, f.b1.ID)
				}
			} else {
				spy.assertNoCall(t, 200*time.Millisecond)
			}
		})
	}
}

// The kill switch half, at the same single id shape. BooksBulk already refuses
// visibly when autoGrab.enabled is false (bulk_autograb_refusal_test.go), and
// the #2668 button relies on exactly that: the code is what the web UI matches
// on to say nothing was searched instead of flashing a success.
func TestBulk_Book_Search_SingleID_RefusesWithCodeWhenAutoGrabOff(t *testing.T) {
	f := newRefusalFixture(t, "false")

	rec := postBulk(t, f.handler.BooksBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.bookIDs()[0]))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeBulk(t, rec)
	got := resp.Results[fmt.Sprintf("%d", f.bookIDs()[0])]
	if got.Code != autoGrabDisabledCode {
		t.Fatalf("code = %q, want %q (the web UI matches on this, not on the text)", got.Code, autoGrabDisabledCode)
	}
	if got.OK {
		t.Fatal("a refused search reported ok=true, which is the #2669 flash this button must not reintroduce")
	}
	f.searcher.assertNoCall(t, 200*time.Millisecond)
}
