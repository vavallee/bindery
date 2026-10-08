package api

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Japanese mixes kanji, hiragana and katakana in one title, so the script
// check must read all three as one writing system. Otherwise a katakana heavy
// title such as "ノルウェイの森" is judged foreign to an author whose other
// titles are mostly kanji, loses the majority language, and is rejected under
// unknown_language_behavior = fail.
func TestApplyAuthorMajorityLanguageFallback_JapaneseTitlesShareAScript(t *testing.T) {
	books := []models.Book{
		{ForeignID: "1", Title: "騎士団長殺し", Language: "jpn"},
		{ForeignID: "2", Title: "風の歌を聴け", Language: "jpn"},
		{ForeignID: "3", Title: "羊をめぐる冒険", Language: "jpn"},
		{ForeignID: "4", Title: "世界の終りとハードボイルド・ワンダーランド", Language: "jpn"},
		{ForeignID: "5", Title: "ノルウェイの森"},
		{ForeignID: "6", Title: "海辺のカフカ"},
		{ForeignID: "7", Title: "ねじまき鳥クロニクル"},
		{ForeignID: "8", Title: "色彩を持たない多崎つくると、彼の巡礼の年"},
		// A translation in another script is still left unknown.
		{ForeignID: "9", Title: "Норвежский лес"},
	}
	applyAuthorMajorityLanguageFallback(books, nil)
	for _, b := range books[4:8] {
		if b.Language != "jpn" {
			t.Errorf("%q: Language = %q, want jpn from the author majority", b.Title, b.Language)
		}
	}
	if books[8].Language != "" {
		t.Errorf("%q: Language = %q, want it left unknown", books[8].Title, books[8].Language)
	}
}

// Hangul stays its own script: a Japanese or Chinese translation in a Korean
// author's catalogue is plainly not Korean.
func TestTitleScript_GroupsCJKButNotHangul(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"ノルウェイの森", "CJK"},
		{"騎士団長殺し", "CJK"},
		{"ねじまき鳥クロニクル", "CJK"},
		{"三体", "CJK"},
		{"채식주의자", "Hangul"},
		{"Norwegian Wood", "Latin"},
		{"1984", ""},
	} {
		if got := titleScript(c.title); got != c.want {
			t.Errorf("titleScript(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}
