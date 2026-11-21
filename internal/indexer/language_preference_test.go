package indexer

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// goldenScoresBefore4130 is scoreResult for a fixed corpus, recorded on main
// at v1.53.0 before the language term existed (#4130): media type, release
// title, score. A criteria with no restricted language list must still score
// exactly these.
const goldenScoresBefore4130 = `
""|"FLAC soundtrack"|1011.1394335230684
""|"Colleen Hoover - Verity (2019) MP3"|730.9308998699194
""|"Colleen.Hoover.-.Verity.DE.2020.ABook.MP3-PaZ"|736.7003461136252
""|"Colleen.Hoover-Verity.(ungekürzt).DE.MP3-ABook"|716.1242268082221
""|"Colleen Hoover - Verity (2019) EPUB RETAIL"|600.363413502058
""|"Colleen Hoover - Verity azw3 german"|622.0630250076729
""|"Verity Unabridged M4B English"|962.8741180788643
""|"Verity Abridged mp3 French 2019"|694.2897000433602
""|"Verity pdf"|337.8981820797933
""|"Verity"|26.16179973983887
""|"Verity 9781538724736 epub"|739.6520170102592
""|"Dungeon Crawler Carl_EspañolNeutro m4b"|930.8209460269048
"ebook"|"FLAC soundtrack"|511.13943352306836
"ebook"|"Colleen Hoover - Verity (2019) MP3"|230.93089986991944
"ebook"|"Colleen.Hoover.-.Verity.DE.2020.ABook.MP3-PaZ"|236.7003461136252
"ebook"|"Colleen.Hoover-Verity.(ungekürzt).DE.MP3-ABook"|216.12422680822206
"ebook"|"Colleen Hoover - Verity (2019) EPUB RETAIL"|600.363413502058
"ebook"|"Colleen Hoover - Verity azw3 german"|622.0630250076729
"ebook"|"Verity Unabridged M4B English"|462.8741180788642
"ebook"|"Verity Abridged mp3 French 2019"|194.2897000433602
"ebook"|"Verity pdf"|337.8981820797933
"ebook"|"Verity"|26.16179973983887
"ebook"|"Verity 9781538724736 epub"|739.6520170102592
"ebook"|"Dungeon Crawler Carl_EspañolNeutro m4b"|430.8209460269048
"audiobook"|"FLAC soundtrack"|1011.1394335230684
"audiobook"|"Colleen Hoover - Verity (2019) MP3"|730.9308998699194
"audiobook"|"Colleen.Hoover.-.Verity.DE.2020.ABook.MP3-PaZ"|736.7003461136252
"audiobook"|"Colleen.Hoover-Verity.(ungekürzt).DE.MP3-ABook"|716.1242268082221
"audiobook"|"Colleen Hoover - Verity (2019) EPUB RETAIL"|100.36341350205791
"audiobook"|"Colleen Hoover - Verity azw3 german"|122.06302500767288
"audiobook"|"Verity Unabridged M4B English"|962.8741180788643
"audiobook"|"Verity Abridged mp3 French 2019"|694.2897000433602
"audiobook"|"Verity pdf"|-162.10181792020674
"audiobook"|"Verity"|26.16179973983887
"audiobook"|"Verity 9781538724736 epub"|239.65201701025913
"audiobook"|"Dungeon Crawler Carl_EspañolNeutro m4b"|930.8209460269048
`

func goldenCorpusResult(i int, title string) newznab.SearchResult {
	seeders := 12
	r := newznab.SearchResult{Title: title, Grabs: i * 7, Size: int64(i) * 90 << 20, Protocol: "torrent", IndexerPriority: i % 3}
	if i%2 == 0 {
		r.Seeders = &seeders
	}
	return r
}

// TestLanguageTermLeavesUnrestrictedRankingUnchanged proves an install with no
// language restriction ranks exactly as before: nil, empty, "any" and a list
// that contains "any" all reproduce the recorded scores to the last digit.
func TestLanguageTermLeavesUnrestrictedRankingUnchanged(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(goldenScoresBefore4130), "\n")
	if len(lines) != 36 {
		t.Fatalf("golden corpus has %d rows, want 36", len(lines))
	}
	for _, allowed := range [][]string{nil, {}, {"any"}, {"ger", "any"}} {
		for n, line := range lines {
			f := strings.Split(line, "|")
			mt, _ := strconv.Unquote(f[0])
			title, _ := strconv.Unquote(f[1])
			want, _ := strconv.ParseFloat(f[2], 64)
			c := MatchCriteria{Title: "Verity", Author: "Colleen Hoover", Year: 2019, MediaType: mt, ISBN: "9781538724736", AllowedLanguages: allowed}
			got := scoreResult(goldenCorpusResult(n%12, title), c, nil)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("allowed=%v %q %q: score %v, recorded %v", allowed, mt, title, got, want)
			}
		}
	}
}

func scoreOf(title string, c MatchCriteria) float64 {
	return scoreResult(newznab.SearchResult{Title: title}, c, nil)
}

// TestLanguageTermVerity is the #4130 report. Under a profile allowing only
// German the two German releases outrank the untagged English one, and they
// outrank the FLAC soundtrack too (it scores three format steps above any mp3
// by the format term alone; nothing here is soundtrack specific).
func TestLanguageTermVerity(t *testing.T) {
	cands := []string{
		"FLAC soundtrack",
		"Colleen Hoover - Verity (2019) MP3",
		"Colleen.Hoover.-.Verity.ABook.MP3.DE-PaZ",
		"Colleen.Hoover-Verity.(ungekürzt).DE.MP3-ABook",
	}
	c := MatchCriteria{Title: "Verity", Author: "Colleen Hoover", Year: 2019, MediaType: models.MediaTypeAudiobook, AllowedLanguages: []string{"ger"}}
	rs := make([]newznab.SearchResult, len(cands))
	for i, s := range cands {
		rs[i] = newznab.SearchResult{Title: s, GUID: s}
	}
	rankResults(rs, c)
	first := map[string]bool{rs[0].Title: true, rs[1].Title: true}
	if !first[cands[2]] || !first[cands[3]] {
		t.Fatalf("German releases must rank first, got %q, %q, %q, %q", rs[0].Title, rs[1].Title, rs[2].Title, rs[3].Title)
	}

	// Control: with no restriction the untagged English release still beats
	// the ungekürzt one, so the ordering above comes from the term.
	c.AllowedLanguages = nil
	if scoreOf(cands[1], c) <= scoreOf(cands[3], c) {
		t.Fatalf("control: unrestricted, untagged mp3 should outrank the ungekürzt release")
	}
}

func TestLanguageTermEbookAndAudiobook(t *testing.T) {
	for _, mt := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		c := MatchCriteria{Title: "Verity", Author: "Colleen Hoover", MediaType: mt, AllowedLanguages: []string{"ger"}}
		tagged, untagged := "Colleen Hoover - Verity deutsch epub mp3", "Colleen Hoover - Verity epub mp3 RETAIL"
		if got := scoreOf(tagged, c) - scoreOf(untagged, c); got <= 0 {
			t.Errorf("%s: tagged German should outrank untagged RETAIL, diff %v", mt, got)
		}
	}
}

func TestLanguageTermNeutralAndNegative(t *testing.T) {
	c := MatchCriteria{Title: "Verity", Author: "Colleen Hoover", AllowedLanguages: []string{"ger"}}
	for _, tc := range []struct {
		name, title string
		want        float64
	}{
		{"untagged is neutral", "Colleen Hoover - Verity (2019) MP3", 0},
		{"allowed language earns the bonus", "Colleen Hoover - Verity deutsch mp3", languageMatchBonus},
		{"scene suffix earns it", "Colleen.Hoover.-.Verity.2020.ABook.MP3.DE-PaZ", languageMatchBonus},
		{"language outside the list earns nothing", "Colleen Hoover - Verity french mp3", 0},
		{"one tag outside the list earns nothing", "Colleen Hoover - Verity german french mp3", 0},
		{"glued name earns it", "Colleen Hoover Verity DeutschUngekürzt", languageMatchBonus},
	} {
		if got := languageScore(tc.title, c); got != tc.want {
			t.Errorf("%s: %q = %v, want %v", tc.name, tc.title, got, tc.want)
		}
	}
	if got := languageScore("Colleen Hoover - Verity deutsch mp3", MatchCriteria{AllowedLanguages: []string{"de"}}); got != languageMatchBonus {
		t.Errorf("two letter profile code: %v", got)
	}
	if got := languageScore("Verity german english mp3", MatchCriteria{AllowedLanguages: []string{"ger", "eng"}}); got != languageMatchBonus {
		t.Errorf("both tags allowed: %v", got)
	}
}

// TestLanguageTermOwnNameIsNotATag covers the false positives: the wanted
// book's own title or author carrying a language word does not tag the release.
func TestLanguageTermOwnNameIsNotATag(t *testing.T) {
	cases := []struct {
		name  string
		c     MatchCriteria
		title string
		want  float64
	}{
		{"German book titled English Lessons, untagged",
			MatchCriteria{Title: "English Lessons", Author: "Anna Schmidt", AllowedLanguages: []string{"eng", "ger"}},
			"Anna Schmidt - English Lessons (2019) MP3", 0},
		{"same book tagged German still earns it",
			MatchCriteria{Title: "English Lessons", Author: "Anna Schmidt", AllowedLanguages: []string{"eng", "ger"}},
			"Anna Schmidt - English Lessons deutsch MP3", languageMatchBonus},
		{"author named German, untagged",
			MatchCriteria{Title: "Quiet Rooms", Author: "Anna German", AllowedLanguages: []string{"ger"}},
			"Anna German - Quiet Rooms (2019) MP3", 0},
		{"glued title word",
			MatchCriteria{Title: "ItalianoAudiolibro Tales", Author: "X Y", AllowedLanguages: []string{"ita"}},
			"X Y - ItalianoAudiolibro Tales MP3", 0},
		{"author alias",
			MatchCriteria{Title: "Quiet Rooms", Author: "Anna Schmidt", AuthorAliases: []string{"Anna German"}, AllowedLanguages: []string{"ger"}},
			"Anna German - Quiet Rooms MP3", 0},
	}
	for _, tc := range cases {
		if got := languageScore(tc.title, tc.c); got != tc.want {
			t.Errorf("%s: %q = %v, want %v", tc.name, tc.title, got, tc.want)
		}
	}
}

// TestLanguageBonusMagnitude pins the sizing documented on languageMatchBonus:
// above the widest format gap inside one media type plus RETAIL and the
// capped small terms, below the media type mismatch penalty.
func TestLanguageBonusMagnitude(t *testing.T) {
	mp3ToFlac := float64(models.QualityRank["flac"]-models.QualityRank["mp3"]) * 100
	const retail, smallTerms, mismatch = 50.0, 65.0, 500.0
	if languageMatchBonus <= mp3ToFlac+retail+smallTerms {
		t.Errorf("bonus %v does not clear format gap %v plus retail plus small terms", languageMatchBonus, mp3ToFlac)
	}
	if languageMatchBonus >= mismatch {
		t.Errorf("bonus %v must stay under the media type mismatch penalty %v", languageMatchBonus, mismatch)
	}
}
