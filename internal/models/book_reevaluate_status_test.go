package models

import "testing"

// ReevaluateStatus is the rule the book_files write path already applies inside
// refreshBookStatus, lifted onto the model so the callers that change MediaType
// without touching book_files can apply it too (#1634).
func TestBookReevaluateStatus(t *testing.T) {
	tests := []struct {
		name    string
		book    Book
		want    string
		wantWhy string
	}{
		{
			name:    "owned ebook widened to both becomes wanted",
			book:    Book{MediaType: MediaTypeBoth, EbookFilePath: "/l/a.epub", Status: BookStatusImported},
			want:    BookStatusWanted,
			wantWhy: "the audiobook slot is monitored and empty",
		},
		{
			name:    "both formats on disk stays imported",
			book:    Book{MediaType: MediaTypeBoth, EbookFilePath: "/l/a.epub", AudiobookFilePath: "/l/a.m4b", Status: BookStatusImported},
			want:    BookStatusImported,
			wantWhy: "nothing is missing",
		},
		{
			name:    "narrowed to a format already on disk becomes imported",
			book:    Book{MediaType: MediaTypeEbook, EbookFilePath: "/l/a.epub", Status: BookStatusWanted},
			want:    BookStatusImported,
			wantWhy: "the declared format is satisfied",
		},
		{
			name:    "skipped is a user decision and survives",
			book:    Book{MediaType: MediaTypeBoth, EbookFilePath: "/l/a.epub", Status: BookStatusSkipped},
			want:    BookStatusSkipped,
			wantWhy: "skipped is never derived",
		},
		{
			name:    "wanted book with no files anywhere stays wanted",
			book:    Book{MediaType: MediaTypeEbook, Status: BookStatusWanted},
			want:    BookStatusWanted,
			wantWhy: "nothing is on disk",
		},
		{
			name:    "imported book whose only file vanished becomes wanted",
			book:    Book{MediaType: MediaTypeEbook, Status: BookStatusImported},
			want:    BookStatusWanted,
			wantWhy: "the declared format has no path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.book
			b.ReevaluateStatus()
			if b.Status != tt.want {
				t.Errorf("status = %q, want %q: %s", b.Status, tt.want, tt.wantWhy)
			}
		})
	}
}
