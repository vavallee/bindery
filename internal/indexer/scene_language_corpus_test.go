package indexer

import (
	"slices"
	"testing"
)

// sceneLanguageCorpus pins #3268 part 4. Every name is made up, in the shape of
// the real ones reported: scene names joined by separators, human-written
// spaced names, and spaced names that carry a code-like token as a word, a
// title, a narrator or a group. want is the complete tag set, from both the
// name markers and the scene reading. The point of the negative half is that
// an English release is never tagged by a token that merely looks like a code.
var sceneLanguageCorpus = []struct {
	title string
	want  []string
}{
	// Mangled umlaut markers (a).
	{"Kari Nordmann - Thron aus Asche und Gold 01 - Fjellvinden (ungekrzt)", []string{"ger"}},
	{"Kari Nordmann - Fjellvinden (gekrzt) [MP3]", []string{"ger"}},
	{"Kari Nordmann - Fjellvinden (Ungekrzt)", []string{"ger"}},
	{"Kari Nordmann - Fjellvinden (Ungekürzt)", []string{"ger"}},
	{"Kari Nordmann - Fjellvinden (Ungekurzt)", []string{"ger"}},
	{"Kari Nordmann - Fjellvinden ungekrztes Material", nil},

	// A code before the marker (b), scene shaped.
	{"Kari Nordmann-A Long Way Down-DE-AUDIOBOOK-4CD-FLAC-2005-oNePiEcE", []string{"ger"}},
	{"Kari Nordmann-Fjellvinden-3MP3CD-DE-2005-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden_Teil_2-4CD-DE-2010-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-2MP3CD-DE-2025-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-DE-Audiobook-CD-FLAC-2026-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-SE-AUDIOBOOK-2021-GRP", []string{"swe"}},
	{"Kari_Nordmann-Fjellvinden-DK-M4B-2021-GRP", []string{"dan"}},
	{"Kari_Nordmann-Fjellvinden-PT-AUDIOBOOK-2021-GRP", []string{"por"}},
	{"Kari_Nordmann-Fjellvinden-NL-AUDiOBOOK-2021-GRP iNT", []string{"dut"}},
	// IT and NO before a marker are English words too, so they are not read
	// there, even in a mixed case name. A miss, by choice.
	{"Kari_Nordmann-Fjellvinden-IT-AUDIOBOOK-2021-GRP", nil},
	{"Stephen_King-IT-AUDIOBOOK-WEB-2017-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-NO-ABOOK-WEB-2021-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-4CD-NO-2010-GRP", []string{"nor"}}, // a disc token is a marker, so this is the suffix reading
	// After a marker they still count (the #3272 reading).
	{"Kari_Nordmann-Fjellvinden-AUDIOBOOK-NO-2021-GRP", []string{"nor"}},
	{"Kari_Nordmann-Fjellvinden-FR-AUDIOBOOK-2021-GRP", []string{"fre"}},
	{"Kari_Nordmann-Fjellvinden-ES-AUDIOBOOK-2021-GRP", []string{"spa"}},
	{"Kari_Nordmann-Fjellvinden-DAN-AUDIOBOOK-2021-GRP", []string{"dan"}},
	// Code before AND after a marker: both are read.
	{"Kari_Nordmann-Fjellvinden-DE-AUDIOBOOK-WEB-EN-2021-GRP", []string{"ger", "eng"}},
	// -DE-EN- is only both AFTER a marker; before one, only the code next to it.
	{"Kari_Nordmann-Fjellvinden-DE-EN-AUDIOBOOK-2021-GRP", []string{"eng"}},
	{"Kari_Nordmann-Fjellvinden-EN-DE-AUDIOBOOK-2021-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-AUDIOBOOK-WEB-DE-EN-2021-GRP", []string{"eng", "ger"}},
	// A disc token is the right marker for the leading rule, but a bare CD is not.
	{"Kari_Nordmann-Fjellvinden-DE-4CD-FLAC-2005-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-DE-MP3CD-2005-GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-DE-CD-2005-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-CD-DE-2005-GRP", nil},
	// Separators: dot and underscore join, a doubled one does not.
	{"Kari.Nordmann-Fjellvinden.DE.AUDIOBOOK.2021.GRP", []string{"ger"}},
	{"Kari_Nordmann_Fjellvinden_DE_AUDIOBOOK_2021_GRP", []string{"ger"}},
	{"Kari_Nordmann-Fjellvinden-DE--AUDIOBOOK-2021-GRP", nil},
	{"Kari_Nordmann-Fjellvinden--DE-AUDIOBOOK-2021-GRP", nil},
	// The year must be a year.
	{"Kari_Nordmann-Fjellvinden-DE-AUDIOBOOK-1234-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-DE-AUDIOBOOK-9999-GRP", nil},
	// Disc token as marker for the suffix reading as well.
	{"Kari_Nordmann-Fjellvinden-4CD-DE-2010-GRP.nzb", []string{"ger"}},

	// English scene names, nothing to read.
	{"Kari_Nordmann-Fjellvinden-AUDIOBOOK-WEB-2021-GRP", nil},
	{"Kari Nordmann-A Long Way Down-AUDIOBOOK-4CD-FLAC-2005-oNePiEcE", nil},
	{"Kari_Nordmann-Fjellvinden-4CD-2010-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-WEB-SE-2021-GRP", nil}, // WEB is not a marker: known limit
	{"Kari_Nordmann-Fjellvinden-WEB-FLAC-2026-TR.nzb", nil},

	// Spaced, human written: a capitalised code right before a year or the
	// group is read, except IT, NO and HI before a final year, which are words.
	{"Author - Title Audiobook - FR 2019", []string{"fre"}},
	{"Author - Title [AUDIOBOOK] [DE] 2018", []string{"ger"}},
	{"Author - Title [AUDIOBOOK] [ES] 2018", []string{"spa"}},
	{"Author - Title Audiobook - DE - 2016", []string{"ger"}},
	{"Author - Title MP3 DE 2018", []string{"ger"}},
	{"Author - Title Audiobook 2018 DE GRP", []string{"ger"}},
	{"Author - Title Audiobook DE GRP", []string{"ger"}},
	{"Author - Title Audiobook DAN 2019", []string{"dan"}},
	{"Author - Title (MP3) 64kbps DE 2019", []string{"ger"}},
	{"Author - Title ABOOK 2020 DK GRP", []string{"dan"}},
	{"Author - Title ABOOK 2020 NO GRP", nil},
	{"Author - Title 3CD IT GRP", nil},
	{"Author - Title 3CD NO GRP", nil},
	{"Author - Title MP3 IT GRP", nil},
	{"Author - Title Audiobook 2018 NO GRP", nil},
	{"Author - Title Audiobook HI GRP", nil},
	// A true tag lost on purpose: neither a marker nor a year sits directly
	// before the code.
	{"Author - Title M4B 64kbps DE GRP", nil},
	{"Author - Title MP3 - 320 - DE - GRP", nil},
	// Known residual false positives, as on main: the group is a word.
	{"Author - Title Audiobook 2018 DE NIRO", []string{"ger"}},
	{"Author - Title Audiobook MP3 DE LUXE", []string{"ger"}},
	{"Author - Title - Audiobook - NO 2019", nil},
	{"Author - Title - Audiobook - IT 2019", nil},
	{"Author - Title - MP3 - Hi 2010", nil},
	{"AUTHOR - TITLE - MP3 - HI 2010", nil},
	{"Author - Title MP3 PT 2", nil},
	{"Author - Title (Audiobook) DE LUXE 2018 GRP", nil},
	{"Author - Title Audiobook DE 1234", nil},
	{"Author - Title Audiobook DE 9999", nil},
	{"Author - Title Audiobook DE 1234 GRP", nil},
	{"Author - Title Audiobook DE 9999 GRP", nil},
	// Known residual false positives, the same as on main, pinned so a change
	// is a decision: a word code before a year with a group after it.
	{"Author - Title (MP3) Stephen King IT 2018 GRP", []string{"ita"}},
	{"Author - Title (Audiobook) Dr. NO 2018 GRP", []string{"nor"}},
	{"Author - Title Audiobook - IT 2019 - GRP", []string{"ita"}},
	{"Author - Title (Audiobook) READ BY ROBERT DE NIRO", nil},
	{"Author - Title - DE - Audiobook - 4CD - 2010", nil},
	{"Author - IT - Audiobook - 2017 - Unabridged", nil},
	{"Author - No - Audiobook - 2017", nil},
	{"Author - Title DE MP3 2005", nil},
	{"Author - Title - NO MP3 - 2005", nil},
	{"Author - Title (SE) Audiobook 2017", nil},
	{"Author - Title PT 2 of 3 MP3 2010", nil},
	{"Author - Title (Narrated by Dan Castellaneta) FR Audiobook 2019", nil},
	{"Author - FR ES NL Audiobook MP3 2019", nil},
	{"Author - Title DAN Audiobook 2019", nil},
	// A hyphen-joined stretch around the code is still prose when a space
	// sits between the code and the year.
	{"Author - Title-DE-Audiobook-2019 Remastered edition-GRP", nil},
	{"Author - Title DE-Audiobook-2019-GRP", nil},
	// All-caps scene names: capitals carry no signal.
	{"STEPHEN_KING-IT-AUDIOBOOK-WEB-2017-GRP", nil},
	{"STEPHEN_KING-IT-4CD-2017-GRP", nil},
	// Titles that are a code word, in title case, never read.
	{"Stephen_King-It-AUDIOBOOK-WEB-2017-GRP", nil},
	{"Kari_Nordmann-No-AUDIOBOOK-2021-GRP", nil},
	// Capitals say nothing in an all-caps name, and a space between the code and the year means prose.
	{"JULES_VERNE-FR-AUDIOBOOK-2021-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-DE-AUDIOBOOK-Part 2-2019-GRP", nil},
	// A code with no year and group behind it, or first in the name.
	{"Kari_Nordmann-Fjellvinden-DE-AUDIOBOOK", nil},
	{"DE-AUDIOBOOK-Fjellvinden-2021-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-DE_AUDIOBOOK 2021 GRP", nil},
	// Not a code.
	{"Kari_Nordmann-Fjellvinden-WEB-AUDIOBOOK-2021-GRP", nil},
	{"Kari_Nordmann-Fjellvinden-DEU-AUDIOBOOK-2021-GRP", []string{"ger"}}, // a three letter ISO code is as good as a two letter one
	// Titles and group names holding the tokens, narrators.
	{"Kari_Nordmann-De_Fjellvinden-AUDIOBOOK-WEB-2021-NO", nil},
	{"Kari_Nordmann-Fjellvinden-AUDIOBOOK-WEB-2021-DE", nil},
	{"Narrated_by_Ola_De_Vries-Fjellvinden-AUDIOBOOK-WEB-2021-GRP", nil},
}

func allCodesOf(title string) []string {
	return append(releaseLanguageCodes(NormalizeRelease(title)), sceneLanguageCodes(title)...)
}

func TestSceneLanguageCorpus(t *testing.T) {
	for _, tc := range sceneLanguageCorpus {
		got := allCodesOf(tc.title)
		slices.Sort(got)
		want := slices.Clone(tc.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%q: tags %v, want %v", tc.title, got, want)
		}
	}
}

// TestSceneLeadingCodeDropsFromEnglishProfile is the end to end half: the
// reported names reach the filter and an English only profile drops them.
func TestSceneLeadingCodeDropsFromEnglishProfile(t *testing.T) {
	for _, title := range []string{
		"Nick Hornby-A Long Way Down-DE-AUDIOBOOK-4CD-FLAC-2005-oNePiEcE",
		"Leigh Bardugo - Thron aus Asche und Gold 01 - King of Scars (ungekrzt)",
		"Kari Nordmann-Fjellvinden-2MP3CD-DE-2025-GRP",
	} {
		if got := FilterByAllowedLanguages(toResults(title), []string{"eng"}); len(got) != 0 {
			t.Errorf("%q: an eng-only profile must drop it", title)
		}
		if got := FilterByAllowedLanguages(toResults(title), []string{"ger"}); len(got) != 1 {
			t.Errorf("%q: a ger-allowed profile must keep it", title)
		}
	}
}
