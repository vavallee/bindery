package decision_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/models"
)

// helpers

func release(opts ...func(*decision.Release)) decision.Release {
	r := decision.Release{
		GUID:       "test-guid",
		Title:      "Test Book",
		Format:     "epub",
		Protocol:   "usenet",
		Size:       1_000_000,
		AgeMinutes: 60,
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func withFormat(f string) func(*decision.Release) { return func(r *decision.Release) { r.Format = f } }
func withProtocol(p string) func(*decision.Release) {
	return func(r *decision.Release) { r.Protocol = p }
}
func withLanguage(l string) func(*decision.Release) {
	return func(r *decision.Release) { r.Language = l }
}
func withAge(a int) func(*decision.Release)      { return func(r *decision.Release) { r.AgeMinutes = a } }
func withSize(s int64) func(*decision.Release)   { return func(r *decision.Release) { r.Size = s } }
func withGUID(g string) func(*decision.Release)  { return func(r *decision.Release) { r.GUID = g } }
func withTitle(t string) func(*decision.Release) { return func(r *decision.Release) { r.Title = t } }

func emptyBook() models.Book    { return models.Book{} }
func bookWithFile() models.Book { return models.Book{FilePath: "/books/test.epub"} }

// --- QualityAllowed ---

func TestQualityAllowed_NilProfile(t *testing.T) {
	s := decision.QualityAllowed{}
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("nil profile should allow all")
	}
}

func TestQualityAllowed_EmptyItems(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{}}
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("empty items should allow all")
	}
}

func TestQualityAllowed_AllowedFormat(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}},
	}}
	ok, _ := s.IsSatisfiedBy(release(withFormat("epub")), emptyBook())
	if !ok {
		t.Fatal("epub should be allowed")
	}
}

func TestQualityAllowed_DisallowedFormat(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name:  "Strict",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}},
	}}
	ok, reason := s.IsSatisfiedBy(release(withFormat("pdf")), emptyBook())
	if ok {
		t.Fatal("pdf should be rejected")
	}
	if reason == "" {
		t.Fatal("should return rejection reason")
	}
}

func TestQualityAllowed_NotAllowedItem(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Items: []models.QualityItem{
			{Quality: "epub", Allowed: true},
			{Quality: "pdf", Allowed: false},
		},
	}}
	ok, _ := s.IsSatisfiedBy(release(withFormat("pdf")), emptyBook())
	if ok {
		t.Fatal("pdf is listed but not allowed")
	}
}

// TestQualityAllowed_AudiobookProfilePassesEbook is the #2307 report: a book's
// author has one quality profile, so an author whose audiobooks are tracked
// under an m4b/mp3/flac profile had every ebook release rejected with "format
// \"epub\" not in quality profile". The profile was never asked about ebooks, so
// it must not answer for them.
func TestQualityAllowed_AudiobookProfilePassesEbook(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "Audiobook",
		Items: []models.QualityItem{
			{Quality: "mp3", Allowed: true},
			{Quality: "m4b", Allowed: true},
			{Quality: "flac", Allowed: true},
		},
	}}
	for _, format := range []string{"epub", "azw3", "pdf", "mobi"} {
		if ok, reason := s.IsSatisfiedBy(release(withFormat(format)), emptyBook()); !ok {
			t.Errorf("%s rejected by an audiobook-only profile: %s", format, reason)
		}
	}
}

// TestQualityAllowed_EbookProfilePassesAudiobook is the same case the other way
// round, which is the commoner one: the default new profile is pdf/mobi/epub/azw3.
func TestQualityAllowed_EbookProfilePassesAudiobook(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "E-Book",
		Items: []models.QualityItem{
			{Quality: "pdf", Allowed: true},
			{Quality: "epub", Allowed: true},
			{Quality: "azw3", Allowed: true},
		},
	}}
	for _, format := range []string{"m4b", "mp3", "flac", "m4a", "ogg"} {
		if ok, reason := s.IsSatisfiedBy(release(withFormat(format)), emptyBook()); !ok {
			t.Errorf("%s rejected by an ebook-only profile: %s", format, reason)
		}
	}
}

// TestQualityAllowed_StillRejectsWithinItsOwnMediaType: the narrowing must not
// become "allow everything". Within a media type the profile has an opinion,
// the allow-list is still authoritative.
func TestQualityAllowed_StillRejectsWithinItsOwnMediaType(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "Audiobook",
		Items: []models.QualityItem{
			{Quality: "m4b", Allowed: true},
			{Quality: "mp3", Allowed: false},
		},
	}}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("mp3")), emptyBook()); ok {
		t.Error("mp3 is listed and unticked in an audiobook profile; it must stay rejected")
	}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("flac")), emptyBook()); ok {
		t.Error("flac is absent from a profile that does list audiobook formats; it must stay rejected")
	}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("m4b")), emptyBook()); !ok {
		t.Error("m4b is ticked and must be allowed")
	}
}

// TestQualityAllowed_MixedProfileJudgesEachMediaTypeSeparately: QualityTab
// allows a profile that lists both, and such a profile has an opinion about
// both, so nothing about it changes.
func TestQualityAllowed_MixedProfileJudgesEachMediaTypeSeparately(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "Mixed",
		Items: []models.QualityItem{
			{Quality: "epub", Allowed: true},
			{Quality: "pdf", Allowed: false},
			{Quality: "m4b", Allowed: true},
			{Quality: "mp3", Allowed: false},
		},
	}}
	for _, tc := range []struct {
		format string
		want   bool
	}{
		{"epub", true},
		{"pdf", false},
		{"m4b", true},
		{"mp3", false},
	} {
		if ok, _ := s.IsSatisfiedBy(release(withFormat(tc.format)), emptyBook()); ok != tc.want {
			t.Errorf("mixed profile: %s allowed = %v, want %v", tc.format, ok, tc.want)
		}
	}
}

// TestQualityAllowed_UnknownFormatTokenUsesTheWholeList: a format the token
// vocabulary does not contain cannot be narrowed by, so the spec falls back to
// comparing against every item rather than silently passing everything.
func TestQualityAllowed_UnknownFormatTokenUsesTheWholeList(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name:  "E-Book",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}},
	}}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("cbr7")), emptyBook()); ok {
		t.Error("an unrecognised format token should not pass a profile that does not list it")
	}
}

// --- DelayProfileSpec ---

func TestDelayProfileSpec_Nil(t *testing.T) {
	s := decision.DelayProfileSpec{}
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("nil profile should pass")
	}
}

func TestDelayProfileSpec_UsenetDisabled(t *testing.T) {
	s := decision.DelayProfileSpec{Profile: &models.DelayProfile{EnableUsenet: false}}
	ok, reason := s.IsSatisfiedBy(release(withProtocol("usenet")), emptyBook())
	if ok {
		t.Fatal("usenet disabled — should reject")
	}
	_ = reason
}

func TestDelayProfileSpec_UsenetDelayNotMet(t *testing.T) {
	s := decision.DelayProfileSpec{Profile: &models.DelayProfile{
		EnableUsenet: true,
		UsenetDelay:  120,
	}}
	ok, _ := s.IsSatisfiedBy(release(withProtocol("usenet"), withAge(30)), emptyBook())
	if ok {
		t.Fatal("age 30 < delay 120 — should reject")
	}
}

func TestDelayProfileSpec_UsenetDelayMet(t *testing.T) {
	s := decision.DelayProfileSpec{Profile: &models.DelayProfile{
		EnableUsenet: true,
		UsenetDelay:  60,
	}}
	ok, _ := s.IsSatisfiedBy(release(withProtocol("usenet"), withAge(90)), emptyBook())
	if !ok {
		t.Fatal("age 90 > delay 60 — should pass")
	}
}

func TestDelayProfileSpec_TorrentDisabled(t *testing.T) {
	s := decision.DelayProfileSpec{Profile: &models.DelayProfile{EnableTorrent: false}}
	ok, _ := s.IsSatisfiedBy(release(withProtocol("torrent")), emptyBook())
	if ok {
		t.Fatal("torrent disabled — should reject")
	}
}

// --- BlocklistedSpec ---

func TestBlocklistedSpec_NotBlocked(t *testing.T) {
	s := decision.NewBlocklistedSpec([]models.BlocklistEntry{
		{GUID: "other-guid"},
	})
	ok, _ := s.IsSatisfiedBy(release(withGUID("test-guid")), emptyBook())
	if !ok {
		t.Fatal("guid not in blocklist — should pass")
	}
}

func TestBlocklistedSpec_Blocked(t *testing.T) {
	s := decision.NewBlocklistedSpec([]models.BlocklistEntry{
		{GUID: "blocked-guid"},
	})
	ok, reason := s.IsSatisfiedBy(release(withGUID("blocked-guid")), emptyBook())
	if ok {
		t.Fatal("guid in blocklist — should reject")
	}
	if reason == "" {
		t.Fatal("should return reason")
	}
}

func TestBlocklistedSpec_Empty(t *testing.T) {
	s := decision.NewBlocklistedSpec(nil)
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("empty blocklist — should pass")
	}
}

// --- AlreadyImportedSpec ---

func TestAlreadyImportedSpec_NoFile(t *testing.T) {
	s := decision.AlreadyImportedSpec{}
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("no file — should pass")
	}
}

func TestAlreadyImportedSpec_HasFile(t *testing.T) {
	s := decision.AlreadyImportedSpec{}
	ok, reason := s.IsSatisfiedBy(release(), bookWithFile())
	if ok {
		t.Fatal("has file — should reject")
	}
	if reason == "" {
		t.Fatal("should return reason")
	}
}

// withMediaType tags a release with the per-format media type the dual-format
// search assigns to each result.
func withMediaType(mt string) func(*decision.Release) {
	return func(r *decision.Release) { r.MediaType = mt }
}

// TestAlreadyImportedSpec_DualFormat covers #1148: a media_type=both book with
// only the audiobook on disk must still allow ebook releases. The check is
// per-format via the release's MediaType, not the legacy whole-book FilePath.
func TestAlreadyImportedSpec_DualFormat(t *testing.T) {
	s := decision.AlreadyImportedSpec{}
	// Audiobook imported, ebook missing. FilePath is the legacy column the old
	// code rejected on; it points at the audiobook path here.
	book := models.Book{
		MediaType:         models.MediaTypeBoth,
		AudiobookFilePath: "/audiobooks/test",
		FilePath:          "/audiobooks/test",
	}

	if ok, reason := s.IsSatisfiedBy(release(withMediaType(models.MediaTypeEbook)), book); !ok {
		t.Errorf("ebook release must pass when only the audiobook is imported, got reject: %q", reason)
	}
	if ok, _ := s.IsSatisfiedBy(release(withMediaType(models.MediaTypeAudiobook)), book); ok {
		t.Error("audiobook release must be rejected when the audiobook is already imported")
	}

	// Mirror case: ebook imported, audiobook missing.
	book = models.Book{
		MediaType:     models.MediaTypeBoth,
		EbookFilePath: "/books/test.epub",
		FilePath:      "/books/test.epub",
	}
	if ok, _ := s.IsSatisfiedBy(release(withMediaType(models.MediaTypeEbook)), book); ok {
		t.Error("ebook release must be rejected when the ebook is already imported")
	}
	if ok, reason := s.IsSatisfiedBy(release(withMediaType(models.MediaTypeAudiobook)), book); !ok {
		t.Errorf("audiobook release must pass when only the ebook is imported, got reject: %q", reason)
	}
}

// TestAlreadyImportedSpec_UntaggedFallsBackToFilePath confirms single-format
// searches (no MediaType on the result) keep the legacy whole-book behavior.
func TestAlreadyImportedSpec_UntaggedFallsBackToFilePath(t *testing.T) {
	s := decision.AlreadyImportedSpec{}
	if ok, _ := s.IsSatisfiedBy(release(), bookWithFile()); ok {
		t.Error("untagged release on a book with FilePath must still reject")
	}
	if ok, _ := s.IsSatisfiedBy(release(), emptyBook()); !ok {
		t.Error("untagged release on a book with no file must pass")
	}
}

// --- SizeLimitSpec ---

func TestSizeLimitSpec_NoLimits(t *testing.T) {
	s := decision.SizeLimitSpec{}
	ok, _ := s.IsSatisfiedBy(release(withSize(500)), emptyBook())
	if !ok {
		t.Fatal("no limits — should pass")
	}
}

func TestSizeLimitSpec_BelowMin(t *testing.T) {
	s := decision.SizeLimitSpec{MinBytes: 1000}
	ok, _ := s.IsSatisfiedBy(release(withSize(500)), emptyBook())
	if ok {
		t.Fatal("below min — should reject")
	}
}

func TestSizeLimitSpec_AboveMax(t *testing.T) {
	s := decision.SizeLimitSpec{MaxBytes: 1000}
	ok, _ := s.IsSatisfiedBy(release(withSize(2000)), emptyBook())
	if ok {
		t.Fatal("above max — should reject")
	}
}

func TestSizeLimitSpec_InRange(t *testing.T) {
	s := decision.SizeLimitSpec{MinBytes: 100, MaxBytes: 2000}
	ok, _ := s.IsSatisfiedBy(release(withSize(1000)), emptyBook())
	if !ok {
		t.Fatal("in range — should pass")
	}
}

// --- LanguageFilterSpec ---

func TestLanguageFilterSpec_NoFilter(t *testing.T) {
	s := decision.LanguageFilterSpec{}
	ok, _ := s.IsSatisfiedBy(release(withLanguage("fr")), emptyBook())
	if !ok {
		t.Fatal("no filter — should pass")
	}
}

func TestLanguageFilterSpec_EmptyLanguage(t *testing.T) {
	s := decision.LanguageFilterSpec{AllowedLangs: []string{"en"}}
	ok, _ := s.IsSatisfiedBy(release(withLanguage("")), emptyBook())
	if !ok {
		t.Fatal("empty language — should pass")
	}
}

func TestLanguageFilterSpec_Allowed(t *testing.T) {
	s := decision.LanguageFilterSpec{AllowedLangs: []string{"en", "fr"}}
	ok, _ := s.IsSatisfiedBy(release(withLanguage("FR")), emptyBook())
	if !ok {
		t.Fatal("FR should match fr (case-insensitive)")
	}
}

func TestLanguageFilterSpec_NotAllowed(t *testing.T) {
	s := decision.LanguageFilterSpec{AllowedLangs: []string{"en"}}
	ok, reason := s.IsSatisfiedBy(release(withLanguage("de")), emptyBook())
	if ok {
		t.Fatal("de not in allowed list — should reject")
	}
	if reason == "" {
		t.Fatal("should return reason")
	}
}

// --- CustomFormatScoreSpec ---

func TestCustomFormatScoreSpec_NeverRejects(t *testing.T) {
	s := &decision.CustomFormatScoreSpec{}
	ok, _ := s.IsSatisfiedBy(release(), emptyBook())
	if !ok {
		t.Fatal("score spec should never reject")
	}
}

func TestCustomFormatScoreSpec_Score_NoFormats(t *testing.T) {
	s := &decision.CustomFormatScoreSpec{}
	if s.Score(release()) != 0 {
		t.Fatal("no formats — score should be 0")
	}
}

func TestCustomFormatScoreSpec_Score_FormatCondition(t *testing.T) {
	s := &decision.CustomFormatScoreSpec{
		Formats: []models.CustomFormat{
			{Name: "epub-preferred", Conditions: []models.CustomCondition{
				{Type: "format", Pattern: "epub"},
			}},
		},
	}
	if s.Score(release(withFormat("epub"))) == 0 {
		t.Fatal("epub should match format condition")
	}
	if s.Score(release(withFormat("pdf"))) != 0 {
		t.Fatal("pdf should not match epub format condition")
	}
}

func TestCustomFormatScoreSpec_Score_TitleRegex(t *testing.T) {
	s := &decision.CustomFormatScoreSpec{
		Formats: []models.CustomFormat{
			{Name: "retail", Conditions: []models.CustomCondition{
				{Type: "releaseTitle", Pattern: "retail"},
			}},
		},
	}
	if s.Score(release(withTitle("Great Book Retail"))) == 0 {
		t.Fatal("title with 'retail' should match")
	}
	if s.Score(release(withTitle("Great Book"))) != 0 {
		t.Fatal("title without 'retail' should not match")
	}
}

func TestCustomFormatScoreSpec_Score_NegateCondition(t *testing.T) {
	s := &decision.CustomFormatScoreSpec{
		Formats: []models.CustomFormat{
			{Name: "not-pdf", Conditions: []models.CustomCondition{
				{Type: "format", Pattern: "pdf", Negate: true},
			}},
		},
	}
	if s.Score(release(withFormat("epub"))) == 0 {
		t.Fatal("negated pdf: epub should score")
	}
	if s.Score(release(withFormat("pdf"))) != 0 {
		t.Fatal("negated pdf: pdf should not score")
	}
}

// --- DecisionMaker ---

func TestDecisionMaker_AllApproved(t *testing.T) {
	dm := decision.New()
	releases := []decision.Release{release(), release(withFormat("pdf"))}
	decisions := dm.Evaluate(releases, emptyBook())
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}
	for _, d := range decisions {
		if !d.Approved {
			t.Fatalf("expected approved, got rejection: %s", d.Rejection)
		}
	}
}

func TestDecisionMaker_OneRejected(t *testing.T) {
	dm := decision.New(decision.AlreadyImportedSpec{})
	decisions := dm.Evaluate([]decision.Release{release()}, bookWithFile())
	if len(decisions) != 1 {
		t.Fatal("expected 1 decision")
	}
	if decisions[0].Approved {
		t.Fatal("expected rejection")
	}
	if decisions[0].Rejection == "" {
		t.Fatal("expected rejection reason")
	}
}

func TestDecisionMaker_StopsAtFirstRejection(t *testing.T) {
	// Two specs that both reject — only first reason should appear.
	dm := decision.New(
		decision.AlreadyImportedSpec{},
		decision.SizeLimitSpec{MinBytes: 999_999_999},
	)
	decisions := dm.Evaluate([]decision.Release{release()}, bookWithFile())
	if decisions[0].Rejection != "book already imported" {
		t.Fatalf("expected first spec rejection, got: %s", decisions[0].Rejection)
	}
}

func TestApproved_FiltersCorrectly(t *testing.T) {
	dm := decision.New(decision.AlreadyImportedSpec{})
	releases := []decision.Release{release(), release()}
	all := dm.Evaluate(releases, emptyBook())
	approved := decision.Approved(all)
	if len(approved) != 2 {
		t.Fatalf("all should be approved, got %d", len(approved))
	}

	all2 := dm.Evaluate(releases, bookWithFile())
	approved2 := decision.Approved(all2)
	if len(approved2) != 0 {
		t.Fatal("all should be rejected when book has file")
	}
}

// --- PubDateToAge ---

func TestPubDateToAge_RFC1123Z(t *testing.T) {
	// 1 hour ago
	pubDate := time.Now().Add(-time.Hour).Format(time.RFC1123Z)
	age := decision.PubDateToAge(pubDate)
	if age < 59 || age > 62 {
		t.Fatalf("expected ~60 minutes, got %d", age)
	}
}

func TestPubDateToAge_InvalidReturnsZero(t *testing.T) {
	if decision.PubDateToAge("not a date") != 0 {
		t.Fatal("invalid date should return 0")
	}
}

func TestPubDateToAge_FutureReturnsZero(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC1123Z)
	if decision.PubDateToAge(future) != 0 {
		t.Fatal("future date should return 0")
	}
}

// TestQualityAllowed_UnparseableFormatPasses pins the deliberate fail-open case
// added when the spec was finally wired in (#1693).
//
// ParseRelease only sets Format when it finds a known token in the release
// title, and plenty of legitimate releases carry none — Usenet titles in
// particular are often just "Author - Title (Year)". Rejecting those would turn
// this filter into a near-total grab blackout the moment a user ticked any box,
// which is the opposite of what the UI promises. The filter can only speak to
// formats it can actually see.
func TestQualityAllowed_UnparseableFormatPasses(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "EPUB only",
		Items: []models.QualityItem{
			{Quality: "pdf", Allowed: false},
			{Quality: "epub", Allowed: true},
		},
	}}
	ok, reason := s.IsSatisfiedBy(release(withFormat("")), emptyBook())
	if !ok {
		t.Fatalf("a release with no parseable format must pass, got rejected: %q", reason)
	}
}

// TestQualityAllowed_RejectionNamesFormatAndProfile pins the rejection string.
// Interactive search surfaces it verbatim, so it has to say which format was
// refused and which profile refused it — otherwise the user cannot tell whether
// to change the profile or pick a different release.
func TestQualityAllowed_RejectionNamesFormatAndProfile(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name:  "EPUB only",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}},
	}}
	ok, reason := s.IsSatisfiedBy(release(withFormat("pdf")), emptyBook())
	if ok {
		t.Fatal("pdf should be rejected by an EPUB-only profile")
	}
	for _, want := range []string{"pdf", "EPUB only"} {
		if !strings.Contains(reason, want) {
			t.Errorf("rejection %q should mention %q", reason, want)
		}
	}
}

// TestQualityAllowed_ListedButNotAllowed guards the difference between "absent
// from the list" and "present and unticked" — both must reject.
func TestQualityAllowed_ListedButNotAllowed(t *testing.T) {
	s := decision.QualityAllowed{Profile: &models.QualityProfile{
		Name: "EPUB only",
		Items: []models.QualityItem{
			{Quality: "pdf", Allowed: false},
			{Quality: "epub", Allowed: true},
		},
	}}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("pdf")), emptyBook()); ok {
		t.Error("a listed-but-unticked format must be rejected")
	}
	if ok, _ := s.IsSatisfiedBy(release(withFormat("mobi")), emptyBook()); ok {
		t.Error("a format absent from the list must be rejected")
	}
}
