package filterengine

import (
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func fires(t *testing.T, s Signal, c Candidate, ctx *Context) bool {
	t.Helper()
	obs := s.Observe(c, ctx)
	if len(obs) == 0 {
		return false
	}
	if len(obs) != 1 {
		t.Errorf("signal %q returned %d observations, want at most 1 at v1", s.ID(), len(obs))
	}
	o := obs[0]
	if o.Signal != s.ID() {
		t.Errorf("observation.Signal = %q, want %q", o.Signal, s.ID())
	}
	if o.Weight != -vetoWeight {
		t.Errorf("observation.Weight = %v, want %v", o.Weight, -vetoWeight)
	}
	if o.Confidence != 1 {
		t.Errorf("observation.Confidence = %v, want 1 (v1 signals never grade confidence)", o.Confidence)
	}
	if o.Reason == "" {
		t.Error("observation.Reason is empty")
	}
	return true
}

func TestMediaTypeSignal(t *testing.T) {
	tests := []struct {
		name    string
		book    models.Book
		strict  bool
		def     string
		wantHit bool
	}{
		{"strict off never fires", models.Book{MediaType: models.MediaTypeAudiobook}, false, models.MediaTypeEbook, false},
		{"both default disables the clamp", models.Book{MediaType: models.MediaTypeAudiobook}, true, models.MediaTypeBoth, false},
		{"matching format passes", models.Book{MediaType: models.MediaTypeEbook}, true, models.MediaTypeEbook, false},
		{"mismatched format fires", models.Book{MediaType: models.MediaTypeAudiobook}, true, models.MediaTypeEbook, true},
	}
	s := NewMediaTypeSignal()
	for _, tt := range tests {
		ctx := &Context{StrictMediaType: tt.strict, MediaTypeDefault: tt.def}
		if got := fires(t, s, Candidate{Book: &tt.book}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}

func TestJunkTitleSignal(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantHit bool
	}{
		{"empty title fires", "", true},
		{"whitespace-only title fires", "   ", true},
		{"title matching author name fires", "Jared M. Diamond", true},
		{"real title with author's name embedded does not fire", "Jared's Journey", false},
		{"ordinary title passes", "The Way of Kings", false},
	}
	s := NewJunkTitleSignal()
	ctx := &Context{NormalizedAuthor: "jared m. diamond"}
	for _, tt := range tests {
		if got := fires(t, s, Candidate{Book: &models.Book{Title: tt.title}}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}

func TestLanguageSignal(t *testing.T) {
	tests := []struct {
		name        string
		language    string
		allowed     []string
		unknownFail bool
		wantHit     bool
	}{
		{"no allowed-language filter never fires", "fre", nil, true, false},
		{"allowed language passes", "eng", []string{"eng"}, false, false},
		{"disallowed language fires", "fre", []string{"eng"}, false, true},
		{"unknown language passes by default", "", []string{"eng"}, false, false},
		{"unknown language fires when unknownFail is set", "", []string{"eng"}, true, true},
	}
	s := NewLanguageSignal()
	for _, tt := range tests {
		ctx := &Context{AllowedLanguages: tt.allowed, UnknownLangFail: tt.unknownFail}
		if got := fires(t, s, Candidate{Book: &models.Book{Language: tt.language}}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
	if got := fires(t, s, Candidate{Book: nil}, &Context{AllowedLanguages: []string{"eng"}}); got {
		t.Error("nil Book should never fire")
	}
}

func TestPartBookSignal(t *testing.T) {
	tests := []struct {
		name          string
		title         string
		skipPartBooks bool
		wantHit       bool
	}{
		{"setting off never fires", "The Foo Trilogy: Books 1-3", false, false},
		{"ordinary single-book title passes", "The Way of Kings", true, false},
		{"boxed-set title fires", "The Foo Trilogy Boxed Set", true, true},
		{"books N-M naming fires", "The Foo Series, Books 1-3", true, true},
	}
	s := NewPartBookSignal()
	for _, tt := range tests {
		ctx := &Context{SkipPartBooks: tt.skipPartBooks}
		if got := fires(t, s, Candidate{Book: &models.Book{Title: tt.title}}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}

func TestMissingDateSignal(t *testing.T) {
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name            string
		releaseDate     *time.Time
		skipMissingDate bool
		wantHit         bool
	}{
		{"setting off never fires", nil, false, false},
		{"present date passes", &now, true, false},
		{"missing date fires", nil, true, true},
	}
	s := NewMissingDateSignal()
	for _, tt := range tests {
		ctx := &Context{SkipMissingDate: tt.skipMissingDate}
		b := &models.Book{ReleaseDate: tt.releaseDate}
		if got := fires(t, s, Candidate{Book: b}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}

func TestMissingISBNSignal(t *testing.T) {
	isbn := "9780345472199"
	tests := []struct {
		name            string
		editions        []models.Edition
		skipMissingISBN bool
		wantHit         bool
	}{
		{"setting off never fires", nil, false, false},
		{"edition with ISBN passes", []models.Edition{{ISBN13: &isbn}}, true, false},
		{"no editions fires", nil, true, true},
		{"editions with no ISBN fire", []models.Edition{{}}, true, true},
	}
	s := NewMissingISBNSignal()
	for _, tt := range tests {
		ctx := &Context{SkipMissingISBN: tt.skipMissingISBN}
		if got := fires(t, s, Candidate{Book: &models.Book{}, Editions: tt.editions}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}

func TestProviderNoiseSignal(t *testing.T) {
	s := NewProviderNoiseSignal()
	ctx := &Context{}

	clean := &models.Book{Title: "A Real Book"}
	if got := fires(t, s, Candidate{Book: clean}, ctx); got {
		t.Error("clean book with no provider observations should not fire")
	}

	flagged := &models.Book{
		Title: "Cliffsnotes on A Real Book",
		Observations: []models.FilterObservation{
			{Signal: models.SignalProviderOpenLibraryNoise, Reason: `title contains "cliffsnotes"`},
		},
	}
	if got := fires(t, s, Candidate{Book: flagged}, ctx); !got {
		t.Error("book carrying a SignalProviderOpenLibraryNoise observation should fire")
	}

	unrelated := &models.Book{
		Observations: []models.FilterObservation{
			{Signal: "some.other.provider.signal", Reason: "irrelevant"},
		},
	}
	if got := fires(t, s, Candidate{Book: unrelated}, ctx); got {
		t.Error("an unrelated observation on the book must not make this signal fire")
	}

	if got := fires(t, s, Candidate{Book: nil}, ctx); got {
		t.Error("nil Book should never fire")
	}
}

func TestMinPagesSignal(t *testing.T) {
	pages300 := 300
	pages50 := 50
	tests := []struct {
		name     string
		editions []models.Edition
		minPages int
		wantHit  bool
	}{
		{"zero floor never fires", nil, 0, false},
		{"edition meeting the floor passes", []models.Edition{{NumPages: &pages300}}, 200, false},
		{"edition below the floor fires", []models.Edition{{NumPages: &pages50}}, 200, true},
		{"unknown page count passes through unfiltered", []models.Edition{{}}, 200, false},
		{"no editions at all passes through unfiltered", nil, 200, false},
	}
	s := NewMinPagesSignal()
	for _, tt := range tests {
		ctx := &Context{MinPages: tt.minPages}
		if got := fires(t, s, Candidate{Book: &models.Book{}, Editions: tt.editions}, ctx); got != tt.wantHit {
			t.Errorf("%s: fires=%v, want %v", tt.name, got, tt.wantHit)
		}
	}
}
