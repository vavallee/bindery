package models

import (
	"slices"
	"strings"
)

// DefaultMetadataProfileID is the ID of the seeded "Standard" profile created
// in migration 003. Authors with no explicit profile fall back to it so the
// language filter always has a value to consult.
const DefaultMetadataProfileID int64 = 1

// ParseAllowedLanguages turns a metadata profile's allowed_languages CSV
// (e.g. "eng,fre,ger") into the normalized lowercase set used when filtering
// metadata responses. Whitespace around codes is tolerated. An empty string
// or a single "any" entry returns nil — callers treat nil as "don't filter".
func ParseAllowedLanguages(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(csv, ",") {
		code := strings.ToLower(strings.TrimSpace(part))
		if code == "" {
			continue
		}
		if code == "any" {
			return nil
		}
		out = append(out, code)
	}
	return out
}

// The alias tables below are the only ISO 639 tables in the codebase, and
// NormalizeLanguageCode is the only thing that reads them (languageNames,
// further down, is a display table keyed by the codes they produce). There
// used to be four competing copies (here, in the indexer's release filter,
// and twice over in the Audible ingestion paths) and they disagreed with each
// other, so the same language filtered differently depending on which code
// path happened to be consulted. Add a language here and every caller learns
// it at once.

// iso639TwoLetterToB maps ISO 639-1 two-letter codes to the ISO 639-2/B
// three-letter vocabulary Bindery stores in Book.Language and metadata-profile
// allowed_languages. Anything not listed passes through unchanged so a rarer
// language still round-trips rather than being silently dropped.
var iso639TwoLetterToB = map[string]string{
	"en": "eng", "fr": "fre", "de": "ger", "nl": "dut", "es": "spa",
	"it": "ita", "pt": "por", "ja": "jpn", "zh": "chi", "ru": "rus",
	"sv": "swe", "no": "nor", "nb": "nor", "nn": "nor", "da": "dan", "pl": "pol", "cs": "cze",
	"tr": "tur", "hi": "hin", "ko": "kor", "ar": "ara", "fi": "fin",
	"el": "gre", "hu": "hun", "ro": "rum", "ca": "cat", "la": "lat",
	// The rest of ISO 639-1, so a file tagged "uk", "he" or "sk" is read as
	// the language it names rather than as no language at all (#2998).
	"aa": "aar", "ab": "abk", "ae": "ave", "af": "afr", "ak": "aka",
	"am": "amh", "an": "arg", "as": "asm", "av": "ava", "ay": "aym",
	"az": "aze", "ba": "bak", "be": "bel", "bg": "bul", "bi": "bis",
	"bm": "bam", "bn": "ben", "bo": "tib", "br": "bre", "bs": "bos",
	"ce": "che", "ch": "cha", "co": "cos", "cr": "cre", "cu": "chu",
	"cv": "chv", "cy": "wel", "dv": "div", "dz": "dzo", "ee": "ewe",
	"eo": "epo", "et": "est", "eu": "baq", "fa": "per", "ff": "ful",
	"fj": "fij", "fo": "fao", "fy": "fry", "ga": "gle", "gd": "gla",
	"gl": "glg", "gn": "grn", "gu": "guj", "gv": "glv", "ha": "hau",
	"he": "heb", "ho": "hmo", "hr": "hrv", "ht": "hat", "hy": "arm",
	"hz": "her", "ia": "ina", "id": "ind", "ie": "ile", "ig": "ibo",
	"ii": "iii", "ik": "ipk", "io": "ido", "is": "ice", "iu": "iku",
	"jv": "jav", "ka": "geo", "kg": "kon", "ki": "kik", "kj": "kua",
	"kk": "kaz", "kl": "kal", "km": "khm", "kn": "kan", "kr": "kau",
	"ks": "kas", "ku": "kur", "kv": "kom", "kw": "cor", "ky": "kir",
	"lb": "ltz", "lg": "lug", "li": "lim", "ln": "lin", "lo": "lao",
	"lt": "lit", "lu": "lub", "lv": "lav", "mg": "mlg", "mh": "mah",
	"mi": "mao", "mk": "mac", "ml": "mal", "mn": "mon", "mr": "mar",
	"ms": "may", "mt": "mlt", "my": "bur", "na": "nau", "nd": "nde",
	"ne": "nep", "ng": "ndo", "nr": "nbl", "nv": "nav", "ny": "nya",
	"oc": "oci", "oj": "oji", "om": "orm", "or": "ori", "os": "oss",
	"pa": "pan", "pi": "pli", "ps": "pus", "qu": "que", "rm": "roh",
	"rn": "run", "rw": "kin", "sa": "san", "sc": "srd", "sd": "snd",
	"se": "sme", "sg": "sag", "si": "sin", "sk": "slo", "sl": "slv",
	"sm": "smo", "sn": "sna", "so": "som", "sq": "alb", "sr": "srp",
	"ss": "ssw", "st": "sot", "su": "sun", "sw": "swa", "ta": "tam",
	"te": "tel", "tg": "tgk", "th": "tha", "ti": "tir", "tk": "tuk",
	"tl": "tgl", "tn": "tsn", "to": "ton", "ts": "tso", "tt": "tat",
	"tw": "twi", "ty": "tah", "ug": "uig", "uk": "ukr", "ur": "urd",
	"uz": "uzb", "ve": "ven", "vi": "vie", "vo": "vol", "wa": "wln",
	"wo": "wol", "xh": "xho", "yi": "yid", "yo": "yor", "za": "zha",
	"zu": "zul",
}

// iso639LegacyTwoLetter maps the ISO 639-1 codes withdrawn in 1989 onto the
// codes that replaced them. Older EPUB tools and Java locales still write
// them, and passed through raw they read as no language at all and relabelled
// a book to "iw".
var iso639LegacyTwoLetter = map[string]string{"iw": "he", "in": "id", "ji": "yi"}

// iso639TermToB maps the ISO 639-2/T (terminology) code onto the 639-2/B
// (bibliographic) code for the twenty languages where the two standards
// disagree. Both spellings are legal ISO 639-2 and providers emit either, but
// Bindery stores and filters on the /B form, so /T has to fold onto it or a
// profile allowing "ger" rejects every book a provider reported as "deu".
// This is a closed set: ISO 639-2 defines exactly these twenty pairs.
var iso639TermToB = map[string]string{
	"sqi": "alb", "hye": "arm", "eus": "baq", "bod": "tib", "mya": "bur",
	"ces": "cze", "cym": "wel", "deu": "ger", "ell": "gre", "fas": "per",
	"fra": "fre", "isl": "ice", "kat": "geo", "mkd": "mac", "mri": "mao",
	"msa": "may", "nld": "dut", "ron": "rum", "slk": "slo", "zho": "chi",
}

// iso639NorwegianToB folds the two Norwegian written standards onto the
// macrolanguage code. Bokmål and Nynorsk have their own ISO 639-2 codes, and
// Norwegian catalogues (EPUB dc:language, the National Library) use them, but
// a metadata profile offers only "Norwegian" and stores "nor". Without this a
// profile allowing Norwegian rejected every book tagged "nob" or "nno".
var iso639NorwegianToB = map[string]string{"nob": "nor", "nno": "nor"}

// iso639NameToB maps a language written out as a word onto its 639-2/B code.
// Audible reports languages this way ("english", "german"), and release names
// carry them in English and in the language's own spelling.
//
// Only genuine names of languages belong here. The release filter also
// recognises words that merely imply a language (the local word for
// "audiobook", say), but those are evidence read off a release title rather
// than a language a provider reported, so they stay in releaseLanguageTags in
// internal/indexer/searcher.go.
var iso639NameToB = map[string]string{
	"english": "eng",
	"french":  "fre", "francais": "fre", "français": "fre",
	"german": "ger", "deutsch": "ger",
	"spanish": "spa", "espanol": "spa", "español": "spa",
	"italian": "ita", "italiano": "ita",
	"dutch": "dut", "nederlands": "dut",
	"portuguese": "por", "portugues": "por", "português": "por",
	"japanese": "jpn",
	"russian":  "rus",
	"chinese":  "chi", "mandarin": "chi",
	"danish": "dan", "dansk": "dan",
	"swedish": "swe", "svenska": "swe",
	"norwegian": "nor", "norsk": "nor",
	"polish": "pol", "polski": "pol",
	"finnish": "fin", "suomi": "fin",
	"hindi":   "hin",
	"turkish": "tur", "turkce": "tur", "türkçe": "tur",
	"arabic": "ara",
	"korean": "kor",
	"czech":  "cze", "cestina": "cze", "čeština": "cze",
	"greek": "gre", "ellinika": "gre",
	"hungarian": "hun", "magyar": "hun",
	"romanian": "rum", "romana": "rum", "română": "rum",
	"catalan": "cat", "catala": "cat", "català": "cat",
	"latin": "lat",
}

// iso639ChineseToB folds the ISO 639-3 codes for the two Chinese languages
// books are written in onto the macrolanguage code a profile stores, the way
// iso639NorwegianToB does for Norwegian. EPUBs tag themselves "cmn" or "yue"
// as well as "zh".
var iso639ChineseToB = map[string]string{"cmn": "chi", "yue": "chi"}

// NormalizeLanguageCode canonicalizes a language code from any source (an
// EPUB's dc:language, provider metadata, a hand-edited metadata profile) into
// the lowercased ISO 639-2/B form the language filter compares against. It
// resolves a written-out language name ("German", "Deutsch"), drops a region
// or script subtag ("en-US" to "en" to "eng", "zh-Hans" to "chi"), maps a
// known two-letter code to its three-letter equivalent, folds ISO 639-2/T onto
// 639-2/B ("deu" to "ger"), and passes anything unrecognised through
// lowercased so it still round-trips. Empty in, empty out.
func NormalizeLanguageCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	// Names are matched whole. A name is not a code carrying a subtag, so the
	// stripping below must not run before this lookup.
	if b, ok := iso639NameToB[code]; ok {
		return b
	}
	// Drop a region/script subtag: "en-US", "pt_BR", "zh-Hans".
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if b, ok := iso639ChineseToB[code]; ok {
		return b
	}
	if len(code) == 2 {
		if current, ok := iso639LegacyTwoLetter[code]; ok {
			code = current
		}
		if b, ok := iso639TwoLetterToB[code]; ok {
			return b
		}
		return code
	}
	if b, ok := iso639TermToB[code]; ok {
		return b
	}
	if b, ok := iso639NorwegianToB[code]; ok {
		return b
	}
	return code
}

// languageNames gives the English name of each language Bindery recognises,
// keyed by the ISO 639-2/B code NormalizeLanguageCode produces, so a message
// can say "Swedish" rather than "swe".
var languageNames = map[string]string{
	"aar": "Afar", "abk": "Abkhazian", "afr": "Afrikaans", "aka": "Akan",
	"alb": "Albanian", "amh": "Amharic", "ara": "Arabic", "arg": "Aragonese",
	"arm": "Armenian", "asm": "Assamese", "ava": "Avaric", "ave": "Avestan",
	"aym": "Aymara", "aze": "Azerbaijani", "bak": "Bashkir", "bam": "Bambara",
	"baq": "Basque", "bel": "Belarusian", "ben": "Bengali", "bis": "Bislama",
	"bos": "Bosnian", "bre": "Breton", "bul": "Bulgarian", "bur": "Burmese",
	"cat": "Catalan", "cha": "Chamorro", "che": "Chechen", "chi": "Chinese",
	"chu": "Church Slavic", "chv": "Chuvash", "cor": "Cornish",
	"cos": "Corsican", "cre": "Cree", "cze": "Czech", "dan": "Danish",
	"div": "Divehi", "dut": "Dutch", "dzo": "Dzongkha", "eng": "English",
	"epo": "Esperanto", "est": "Estonian", "ewe": "Ewe", "fao": "Faroese",
	"fij": "Fijian", "fin": "Finnish", "fre": "French",
	"fry": "Western Frisian", "ful": "Fulah", "geo": "Georgian",
	"ger": "German", "gla": "Scottish Gaelic", "gle": "Irish",
	"glg": "Galician", "glv": "Manx", "gre": "Greek", "grn": "Guarani",
	"guj": "Gujarati", "hat": "Haitian Creole", "hau": "Hausa", "heb": "Hebrew",
	"her": "Herero", "hin": "Hindi", "hmo": "Hiri Motu", "hrv": "Croatian",
	"hun": "Hungarian", "ibo": "Igbo", "ice": "Icelandic", "ido": "Ido",
	"iii": "Sichuan Yi", "iku": "Inuktitut", "ile": "Interlingue",
	"ina": "Interlingua", "ind": "Indonesian", "ipk": "Inupiaq",
	"ita": "Italian", "jav": "Javanese", "jpn": "Japanese",
	"kal": "Kalaallisut", "kan": "Kannada", "kas": "Kashmiri", "kau": "Kanuri",
	"kaz": "Kazakh", "khm": "Khmer", "kik": "Kikuyu", "kin": "Kinyarwanda",
	"kir": "Kyrgyz", "kom": "Komi", "kon": "Kongo", "kor": "Korean",
	"kua": "Kuanyama", "kur": "Kurdish", "lao": "Lao", "lat": "Latin",
	"lav": "Latvian", "lim": "Limburgish", "lin": "Lingala",
	"lit": "Lithuanian", "ltz": "Luxembourgish", "lub": "Luba-Katanga",
	"lug": "Ganda", "mac": "Macedonian", "mah": "Marshallese",
	"mal": "Malayalam", "mao": "Maori", "mar": "Marathi", "may": "Malay",
	"mlg": "Malagasy", "mlt": "Maltese", "mon": "Mongolian", "nau": "Nauru",
	"nav": "Navajo", "nbl": "South Ndebele", "nde": "North Ndebele",
	"ndo": "Ndonga", "nep": "Nepali", "nor": "Norwegian", "nya": "Chichewa",
	"oci": "Occitan", "oji": "Ojibwa", "ori": "Odia", "orm": "Oromo",
	"oss": "Ossetian", "pan": "Punjabi", "per": "Persian", "pli": "Pali",
	"pol": "Polish", "por": "Portuguese", "pus": "Pashto", "que": "Quechua",
	"roh": "Romansh", "rum": "Romanian", "run": "Rundi", "rus": "Russian",
	"sag": "Sango", "san": "Sanskrit", "sin": "Sinhala", "slo": "Slovak",
	"slv": "Slovenian", "sme": "Northern Sami", "smo": "Samoan", "sna": "Shona",
	"snd": "Sindhi", "som": "Somali", "sot": "Southern Sotho", "spa": "Spanish",
	"srd": "Sardinian", "srp": "Serbian", "ssw": "Swati", "sun": "Sundanese",
	"swa": "Swahili", "swe": "Swedish", "tah": "Tahitian", "tam": "Tamil",
	"tat": "Tatar", "tel": "Telugu", "tgk": "Tajik", "tgl": "Tagalog",
	"tha": "Thai", "tib": "Tibetan", "tir": "Tigrinya", "ton": "Tongan",
	"tsn": "Tswana", "tso": "Tsonga", "tuk": "Turkmen", "tur": "Turkish",
	"twi": "Twi", "uig": "Uyghur", "ukr": "Ukrainian", "urd": "Urdu",
	"uzb": "Uzbek", "ven": "Venda", "vie": "Vietnamese", "vol": "Volapuk",
	"wel": "Welsh", "wln": "Walloon", "wol": "Wolof", "xho": "Xhosa",
	"yid": "Yiddish", "yor": "Yoruba", "zha": "Zhuang", "zul": "Zulu",
}

// LanguageName returns the English name of a language code in any spelling
// NormalizeLanguageCode accepts ("sv", "swe", "sv-SE" all give "Swedish"). A
// code with no known name is returned normalised, so the result is never
// empty for a non-empty input.
func LanguageName(code string) string {
	n := NormalizeLanguageCode(code)
	if name, ok := languageNames[n]; ok {
		return name
	}
	return n
}

// LanguageCodeVariants returns the provider-facing ISO spellings that are
// equivalent to any code in codes. The result includes ISO 639-1, 639-2/B,
// and 639-2/T variants where Bindery knows them. Metadata providers that can
// filter remotely should send all variants: a profile stores the /B form,
// while an upstream catalogue may expose the same language as /T or 639-1.
func LanguageCodeVariants(codes []string) []string {
	seen := make(map[string]struct{})
	add := func(code string) {
		code = strings.ToLower(strings.TrimSpace(code))
		if code != "" {
			seen[code] = struct{}{}
		}
	}
	for _, code := range codes {
		normalized := NormalizeLanguageCode(code)
		add(code)
		add(normalized)
		for two, bibliographic := range iso639TwoLetterToB {
			if bibliographic == normalized {
				add(two)
			}
		}
		for terminology, bibliographic := range iso639TermToB {
			if bibliographic == normalized {
				add(terminology)
			}
		}
		for standard, macro := range iso639NorwegianToB {
			if macro == normalized {
				add(standard)
			}
		}
	}
	variants := make([]string, 0, len(seen))
	for code := range seen {
		variants = append(variants, code)
	}
	slices.Sort(variants)
	return variants
}

// IsLanguageAllowed reports whether code passes the allowed-language filter.
// When allowed is empty the filter is disabled and everything passes. When
// code is empty (source didn't report a language — common with OpenLibrary
// work-level data), unknownFail controls behavior: false keeps the book,
// true rejects it. See issue #232.
//
// Both the incoming code and the allowed entries are run through
// NormalizeLanguageCode before comparing: providers hand us whatever
// vocabulary they use (Google Books returns ISO 639-1 "en"/"en-US"), and a
// profile allowing "eng" must not reject those spellings of the same
// language. See issue #1729.
func IsLanguageAllowed(code string, allowed []string, unknownFail bool) bool {
	if len(allowed) == 0 {
		return true
	}
	code = NormalizeLanguageCode(code)
	if code == "" {
		return !unknownFail
	}
	return slices.ContainsFunc(allowed, func(a string) bool {
		return NormalizeLanguageCode(a) == code
	})
}
