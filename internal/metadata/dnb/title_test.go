package dnb

import "testing"

// TestCleanDNBTitle exercises the cleanDNBTitle helper against the real-world
// DNB MARC 245 bloat patterns reported in issue #1114. DNB routinely embeds
// promotional blurb chains (pipe-separated), MARC statement-of-responsibility
// attribution (slash-separated), genre markers, and edition labels in the
// title fields that should never reach the stored title.
func TestCleanDNBTitle(t *testing.T) {
	cases := []struct {
		name    string
		rawA    string
		rawB    string
		want    string
	}{
		{
			name: "promotional pipe chain in $a stripped",
			rawA: "Die Nacht der Bärin : Roman | SPIEGEL-Bestsellerautorin Kira Mohn von einer neuen Seite | Aufarbeitung der Vergangenheit und Trauma | Opfer häuslicher Gewalt | Mutter-Tochter-Beziehungen /",
			rawB: "",
			want: "Die Nacht der Bärin",
		},
		{
			name: "genre word in $b stripped (ungekürzte Lesung)",
			rawA: "Der Bademeister ohne Himmel",
			rawB: "ungekürzte Lesung",
			want: "Der Bademeister ohne Himmel",
		},
		{
			name: "promotional $b stripped (Erfolg aus Skandinavien), series parens preserved",
			rawA: "Die Tochter des Doktors (Die Geheimnisse von Engeløya 1)",
			rawB: "Der große Erfolg aus Skandinavien",
			want: "Die Tochter des Doktors (Die Geheimnisse von Engeløya 1)",
		},
		{
			name: "intro phrase Ein in $b stripped",
			rawA: "Das Lied der Dunkelheit",
			rawB: "Ein Roman aus dem Mittelalter",
			want: "Das Lied der Dunkelheit",
		},
		{
			name: "promotional $b with Bestseller reference stripped",
			rawA: "Die Tribute von Panem L. Der Tag bricht an",
			rawB: "Deutsche Ausgabe von Sunrise on the Reaping, dem neuen Band der dystopischen Bestseller-Reihe",
			want: "Die Tribute von Panem L. Der Tag bricht an",
		},
		{
			name: "genre/edition label Jubiläumsausgabe in $b stripped",
			rawA: "Harry Potter und der Stein der Weisen",
			rawB: "Jubiläumsausgabe",
			want: "Harry Potter und der Stein der Weisen",
		},
		{
			name: "trailing : Roman genre suffix stripped",
			rawA: "Ein einfaches Buch : Roman",
			rawB: "",
			want: "Ein einfaches Buch",
		},
		{
			name: "trailing : Thriller genre suffix stripped",
			rawA: "Der dunkle Wald : Thriller",
			rawB: "",
			want: "Der dunkle Wald",
		},
		{
			name: "short non-promotional $b kept",
			rawA: "Der Fänger im Roggen",
			rawB: "Roman für Erwachsene",
			// "Roman" keyword present — stripped
			want: "Der Fänger im Roggen",
		},
		{
			name: "statement of responsibility in $a stripped",
			rawA: "Gedichte / Rainer Maria Rilke",
			rawB: "",
			want: "Gedichte",
		},
		{
			name: "empty $b — title from $a only",
			rawA: "Stiller",
			rawB: "",
			want: "Stiller",
		},
		{
			name: "both $a and $b clean — real subtitle preserved",
			rawA: "Die Verwandlung",
			rawB: "und andere Erzählungen",
			want: "Die Verwandlung: und andere Erzählungen",
		},
		{
			name: "series in parens not stripped from $a",
			rawA: "Die Tochter des Doktors (Die Geheimnisse von Engeløya 1)",
			rawB: "",
			want: "Die Tochter des Doktors (Die Geheimnisse von Engeløya 1)",
		},
		{
			name: "pipe in $b stripped before promotional check",
			rawA: "Das Buch",
			rawB: "Guter Untertitel | Bestsellertext",
			want: "Das Buch: Guter Untertitel",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanDNBTitle(tc.rawA, tc.rawB)
			if got != tc.want {
				t.Errorf("cleanDNBTitle(%q, %q)\n  got  %q\n  want %q", tc.rawA, tc.rawB, got, tc.want)
			}
		})
	}
}
