package pathmap

import (
	"strings"
	"testing"
)

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

// TestParseWindowsDriveLetter covers the drive-designator colon: splitting an
// entry at the first colon turned `S:\Downloads:/downloads` into from="S",
// to="\Downloads:/downloads", so no Windows download client could ever be
// remapped (Discussion #1971).
func TestParseWindowsDriveLetter(t *testing.T) {
	r := Parse(`S:\Downloads:/downloads`)

	if len(r.rules) != 1 {
		t.Fatalf("Parse produced %d rules, want 1: %+v", len(r.rules), r.rules)
	}
	if r.rules[0].from != `S:\Downloads` || r.rules[0].to != "/downloads" {
		t.Fatalf("Parse = {from:%q to:%q}, want {from:%q to:%q}", r.rules[0].from, r.rules[0].to, `S:\Downloads`, "/downloads")
	}
}

func TestApplyWindowsSource(t *testing.T) {
	r := Parse(`S:\Downloads:/mnt/Storage/Downloads`)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"backslash path", `S:\Downloads\bindery\Book`, "/mnt/Storage/Downloads/bindery/Book"},
		{"forward-slash path as qBittorrent reports it", "S:/Downloads/Book", "/mnt/Storage/Downloads/Book"},
		{"drive letter and path are case-insensitive", `s:\downloads\Book`, "/mnt/Storage/Downloads/Book"},
		{"exact prefix match", `S:\Downloads`, "/mnt/Storage/Downloads"},
		{"trailing separator", `S:\Downloads\`, "/mnt/Storage/Downloads"},
		{"sibling prefix must not match", `S:\DownloadsExtra\Book`, `S:\DownloadsExtra\Book`},
		{"different drive is untouched", `D:\Downloads\Book`, `D:\Downloads\Book`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.Apply(tc.in); got != tc.want {
				t.Fatalf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyInverseWindowsRoundTrip proves the reverse direction reconstructs a
// path the Windows client can actually open, in the separator style the
// operator configured. torrentSavePath sends this back to qBittorrent.
func TestApplyInverseWindowsRoundTrip(t *testing.T) {
	r := Parse(`S:\Downloads:/mnt/Storage/Downloads`)

	got := r.ApplyInverse("/mnt/Storage/Downloads/bindery/Book")
	if want := `S:\Downloads\bindery\Book`; got != want {
		t.Fatalf("ApplyInverse = %q, want %q", got, want)
	}
	if back := r.Apply(got); back != "/mnt/Storage/Downloads/bindery/Book" {
		t.Fatalf("round trip = %q, want /mnt/Storage/Downloads/bindery/Book", back)
	}
	if got := r.ApplyInverse("/mnt/Storage/Downloads"); got != `S:\Downloads` {
		t.Fatalf("ApplyInverse exact = %q, want %q", got, `S:\Downloads`)
	}

	// A spec written with forward slashes keeps forward slashes.
	fwd := Parse("S:/Downloads:/mnt/Storage/Downloads")
	if got := fwd.ApplyInverse("/mnt/Storage/Downloads/Book"); got != "S:/Downloads/Book" {
		t.Fatalf("ApplyInverse forward-slash spec = %q, want S:/Downloads/Book", got)
	}
}

// TestApplyWindowsDestination covers the mirror topology: Bindery on Windows,
// the download client somewhere POSIX.
func TestApplyWindowsDestination(t *testing.T) {
	r := Parse(`/downloads:S:\Downloads`)

	if got := r.Apply("/downloads/bindery/Book"); got != `S:\Downloads\bindery\Book` {
		t.Fatalf("Apply = %q, want %q", got, `S:\Downloads\bindery\Book`)
	}
	if got := r.ApplyInverse(`S:\Downloads\bindery\Book`); got != "/downloads/bindery/Book" {
		t.Fatalf("ApplyInverse = %q, want /downloads/bindery/Book", got)
	}
	if got := r.ApplyInverse("s:/downloads/Book"); got != "/downloads/Book" {
		t.Fatalf("ApplyInverse case-insensitive = %q, want /downloads/Book", got)
	}
}

// TestPosixRulesStayCaseAndSeparatorSensitive pins the pre-existing behaviour:
// Linux is case-sensitive and a backslash is an ordinary filename byte, so the
// Windows leniency must not leak into POSIX-only specs.
func TestPosixRulesStayCaseAndSeparatorSensitive(t *testing.T) {
	r := Parse("/media/downloads:/books/downloads")

	if got := r.Apply("/Media/Downloads/A"); got != "/Media/Downloads/A" {
		t.Fatalf("Apply wrong case = %q, want it unchanged", got)
	}
	if got := r.Apply(`\media\downloads\A`); got != `\media\downloads\A` {
		t.Fatalf("Apply backslash path against POSIX rule = %q, want it unchanged", got)
	}
	// A backslash inside a POSIX remainder is data, not a separator.
	if got := r.Apply(`/media/downloads/od\d`); got != `/books/downloads/od\d` {
		t.Fatalf(`Apply POSIX remainder = %q, want /books/downloads/od\d`, got)
	}
}

func TestValidateWindows(t *testing.T) {
	if err := Validate(`S:\Downloads:/downloads`); err != nil {
		t.Fatalf("Validate Windows pair: %v", err)
	}
	if err := Validate(`/downloads:S:\Downloads`); err != nil {
		t.Fatalf("Validate Windows destination: %v", err)
	}
	// The reporter's mistake shape: only the client-side path, no destination.
	err := Validate(`S:\Downloads`)
	if err == nil {
		t.Fatal("Validate drive path with no destination expected error")
	}
	if !strings.Contains(err.Error(), "not in 'from:to' format") {
		t.Fatalf("Validate error %q does not keep the existing message style", err)
	}
	if !strings.Contains(err.Error(), `S:\Downloads:/downloads`) {
		t.Fatalf("Validate error %q does not show a working example", err)
	}
	if err := Validate(`S:\Downloads:`); err == nil {
		t.Fatal("Validate empty destination expected error")
	}
}

func TestIsWindowsPath(t *testing.T) {
	for _, in := range []string{`S:\Downloads`, "S:/Downloads", `s:\d`} {
		if !IsWindowsPath(in) {
			t.Fatalf("IsWindowsPath(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "/downloads", "S:", "SS:/x", "1:/x", "http://x/y"} {
		if IsWindowsPath(in) {
			t.Fatalf("IsWindowsPath(%q) = true, want false", in)
		}
	}
}

// TestApplyShareDestination is the #2831 shape: a desktop Calibre on Windows
// opens books through a network share, because a mapped drive letter can be
// invisible to the running Calibre process. Treated as POSIX, the share got
// mixed separators, and path.Join collapsed `//nas/books` to `/nas/books`.
func TestApplyShareDestination(t *testing.T) {
	tests := []struct {
		name string
		spec string
		in   string
		want string
	}{
		{"backslash share", `/downloads/BOOKS:\\192.168.1.4\MEDIA\BOOKS`, "/downloads/BOOKS/Author/file.epub", `\\192.168.1.4\MEDIA\BOOKS\Author\file.epub`},
		{"forward-slash share keeps its leading //", "/books://nas/media/books", "/books/Author/file.epub", "//nas/media/books/Author/file.epub"},
		{"trailing backslash on the share", `/books:\\nas\media\books\`, "/books/Author/file.epub", `\\nas\media\books\Author\file.epub`},
		{"share root only", `/books:\\nas\books`, "/books/A/b.epub", `\\nas\books\A\b.epub`},
		{"extended UNC form is not double prefixed", `/books:\\?\UNC\nas\books`, "/books/A/b.epub", `\\?\UNC\nas\books\A\b.epub`},
		{"exact prefix", `/books:\\nas\books\`, "/books", `\\nas\books`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.spec).Apply(tc.in); got != tc.want {
				t.Fatalf("Parse(%q).Apply(%q) = %q, want %q", tc.spec, tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyShareSource covers a Windows download client that reports share
// paths: matched case-insensitively with either separator, like drive letters.
func TestApplyShareSource(t *testing.T) {
	r := Parse(`\\NAS\Downloads:/mnt/downloads`)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"backslash path", `\\NAS\Downloads\bindery\Book`, "/mnt/downloads/bindery/Book"},
		{"server and share fold case", `\\nas\downloads\Book`, "/mnt/downloads/Book"},
		{"forward slashes", "//nas/Downloads/Book", "/mnt/downloads/Book"},
		{"trailing separator", `\\NAS\Downloads\`, "/mnt/downloads"},
		{"sibling share must not match", `\\NAS\DownloadsOld\Book`, `\\NAS\DownloadsOld\Book`},
		{"other server untouched", `\\backup\Downloads\Book`, `\\backup\Downloads\Book`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.Apply(tc.in); got != tc.want {
				t.Fatalf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyInverseShareRoundTrip proves both directions rebuild the share in
// the separator style the operator configured, and that the share side is
// matched case-insensitively on the way back.
func TestApplyInverseShareRoundTrip(t *testing.T) {
	for _, tc := range []struct{ spec, local, remote string }{
		{`/books:\\nas\media\books`, "/books/Author/b.epub", `\\nas\media\books\Author\b.epub`},
		{"/books://nas/media/books", "/books/Author/b.epub", "//nas/media/books/Author/b.epub"},
	} {
		r := Parse(tc.spec)
		if got := r.Apply(tc.local); got != tc.remote {
			t.Fatalf("%s: Apply(%q) = %q, want %q", tc.spec, tc.local, got, tc.remote)
		}
		if got := r.ApplyInverse(tc.remote); got != tc.local {
			t.Fatalf("%s: ApplyInverse(%q) = %q, want %q", tc.spec, tc.remote, got, tc.local)
		}
		upper := strings.Replace(tc.remote, "nas", "NAS", 1)
		if got := r.ApplyInverse(upper); got != tc.local {
			t.Fatalf("%s: ApplyInverse(%q) = %q, want %q (share side is case-insensitive)", tc.spec, upper, got, tc.local)
		}
	}

	src := Parse(`\\nas\dl:/mnt/dl`)
	if got := src.ApplyInverse("/mnt/dl/Author/b.epub"); got != `\\nas\dl\Author\b.epub` {
		t.Fatalf(`ApplyInverse onto share source = %q, want \\nas\dl\Author\b.epub`, got)
	}
}

// TestPosixDoubleSlashStaysPosix pins that a POSIX path opening with `//` but
// lacking a share segment keeps POSIX semantics: case-sensitive, `/` only.
func TestPosixDoubleSlashStaysPosix(t *testing.T) {
	r := Parse("//data:/media")

	if got := r.Apply("//data/Book"); got != "/media/Book" {
		t.Fatalf("Apply = %q, want /media/Book", got)
	}
	if got := r.Apply("//DATA/Book"); got != "//DATA/Book" {
		t.Fatalf("Apply wrong case = %q, want it unchanged", got)
	}
	for _, in := range []string{"//data", "//data/", "///data/books", "/data/books"} {
		if IsUNCPath(in) {
			t.Fatalf("IsUNCPath(%q) = true, want false", in)
		}
	}
}

func TestIsUNCPath(t *testing.T) {
	for _, in := range []string{`\\nas\books`, `\\192.168.1.4\MEDIA\BOOKS`, "//nas/books", `\\?\UNC\nas\books\x`, ` \\nas\books `} {
		if !IsUNCPath(in) {
			t.Fatalf("IsUNCPath(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", `\\nas`, `\\nas\`, `\\?\C:\books`, `\\.\pipe`, `S:\books`, "//data", `\\?\UNC\nas`} {
		if IsUNCPath(in) {
			t.Fatalf("IsUNCPath(%q) = true, want false", in)
		}
	}
	// IsWindowsPath keeps its drive-letter-only meaning for its callers.
	if IsWindowsPath(`\\nas\books`) {
		t.Fatal(`IsWindowsPath(\\nas\books) = true, want drive letters only`)
	}
}

func TestParseExtendedDrivePath(t *testing.T) {
	r := Parse(`\\?\C:\Books:/books`)
	if len(r.rules) != 1 || r.rules[0].from != `\\?\C:\Books` || r.rules[0].to != "/books" {
		t.Fatalf("Parse extended drive path = %+v", r.rules)
	}
	if got := r.Apply(`\\?\c:\books\A`); got != "/books/A" {
		t.Fatalf("Apply = %q, want /books/A", got)
	}
}

func TestValidateShares(t *testing.T) {
	for _, ok := range []string{
		`/downloads/BOOKS:\\192.168.1.4\MEDIA\BOOKS`,
		"/books://nas/books",
		`\\nas\books:/books`,
		`/books:\\?\UNC\nas\books`,
		`\\?\C:\Books:/books`,
		`/downloads:S:\Downloads`,
	} {
		if err := Validate(ok); err != nil {
			t.Fatalf("Validate(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{`/books:\\nas`, `/books:\\nas\`, `\\nas:/books`, `/books:\\?\UNC\nas`, `/books:\\`} {
		err := Validate(bad)
		if err == nil {
			t.Fatalf("Validate(%q) accepted a share with no share segment", bad)
		}
		if !strings.Contains(err.Error(), `\\nas\books`) {
			t.Fatalf("Validate(%q) error %q does not name the working shape", bad, err)
		}
	}
	err := Validate(`\\nas\books`)
	if err == nil || !strings.Contains(err.Error(), `\\nas\books:/books`) {
		t.Fatalf("Validate share with no destination = %v, want a share example", err)
	}
}

// TestApplyMatchedReportsIdentityRules: an identity rule matches without
// changing the path, which only the matched flag can show (#2665).
func TestApplyMatchedReportsIdentityRules(t *testing.T) {
	cases := []struct {
		spec, path, wantFwd string
		fwdMatched          bool
		wantInv             string
		invMatched          bool
	}{
		{"/downloads:/downloads", "/downloads/book", "/downloads/book", true, "/downloads/book", true},
		{"/downloads:/downloads", "/other/book", "/other/book", false, "/other/book", false},
		{"/data:/downloads", "/downloads/book", "/downloads/book", false, "/data/book", true},
		{"/data:/downloads", "/data/book", "/downloads/book", true, "/data/book", false},
		{`S:\Books:/books`, `s:/books/a`, "/books/a", true, `s:/books/a`, false},
		{"", "/downloads/book", "/downloads/book", false, "/downloads/book", false},
	}
	for _, tc := range cases {
		r := Parse(tc.spec)
		got, ok := r.ApplyMatched(tc.path)
		if got != tc.wantFwd || ok != tc.fwdMatched {
			t.Errorf("Parse(%q).ApplyMatched(%q) = %q, %v; want %q, %v", tc.spec, tc.path, got, ok, tc.wantFwd, tc.fwdMatched)
		}
		if plain := r.Apply(tc.path); plain != got {
			t.Errorf("Apply(%q) = %q, disagrees with ApplyMatched %q", tc.path, plain, got)
		}
		got, ok = r.ApplyInverseMatched(tc.path)
		if got != tc.wantInv || ok != tc.invMatched {
			t.Errorf("Parse(%q).ApplyInverseMatched(%q) = %q, %v; want %q, %v", tc.spec, tc.path, got, ok, tc.wantInv, tc.invMatched)
		}
		if plain := r.ApplyInverse(tc.path); plain != got {
			t.Errorf("ApplyInverse(%q) = %q, disagrees with ApplyInverseMatched %q", tc.path, plain, got)
		}
	}
	var nilRemapper *Remapper
	if got, ok := nilRemapper.ApplyInverseMatched("/x"); got != "/x" || ok {
		t.Errorf("nil remapper = %q, %v", got, ok)
	}
}
