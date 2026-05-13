package pathmap

import "testing"

func TestRemapperApplyAndInverse(t *testing.T) {
	r := Parse("/media/downloads:/books/downloads,/media:/books")

	if got := r.Apply("/media/downloads/A"); got != "/books/downloads/A" {
		t.Fatalf("Apply longest prefix = %q, want /books/downloads/A", got)
	}
	if got := r.ApplyInverse("/books/downloads/A"); got != "/media/downloads/A" {
		t.Fatalf("ApplyInverse longest prefix = %q, want /media/downloads/A", got)
	}
}

func TestRemapperApplyInversePrefersLongestLocalPrefix(t *testing.T) {
	r := Parse("/external/long:/books,/x:/books/downloads")

	if got := r.ApplyInverse("/books/downloads/A"); got != "/x/A" {
		t.Fatalf("ApplyInverse longest local prefix = %q, want /x/A", got)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("/media:/books, /abs:/audiobooks"); err != nil {
		t.Fatalf("Validate valid spec: %v", err)
	}
	if err := Validate("nodivider"); err == nil {
		t.Fatal("Validate invalid spec expected error")
	}
}
