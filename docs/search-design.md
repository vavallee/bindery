# Search and matching design

Why this document exists: the same class of bug kept recurring in Bindery, and
it was never a hard bug to fix once seen. Two sides of a string comparison were
reduced through *different* alphabets, so the match was silently always-false
(the user gets zero results) or silently wrongly-true (the user gets the wrong
book). #1648 named the pattern after it had produced #940, #871, #1608 and
#1642–#1647; #1660 and #2419 are what was left over.

Fixing instances one at a time did not stop it. What stops it is writing down
which alphabet is which, why they differ, and what evidence the choice rests on
— then testing the differences rather than the instances. `internal/textutil/fold.go`
holds the alphabets, `internal/normdrift` holds the properties, and this file
holds the reasoning and the sources.

## The alphabets

Six reductions exist. They differ **on purpose**; what was wrong before was that
the difference was accidental and undocumented.

| # | Function | Case | Marks | Non-decomposable Latin (ø ł ß æ) | Compatibility (NFKC) | Apostrophe | `&` | Stored? |
|---|----------|------|-------|-----------------------------------|----------------------|-----------|-----|---------|
| 1 | `textutil.FoldForTitleMatch` | lower | kept | kept | no | deleted | separator | no |
| 2 | `textutil.NormalizeAuthorName` | lower | stripped | kept | no | separator | separator | no |
| 3 | `db.authorSortKey` / `db.bookSortKey` | lower | stripped | folded | no | separator | separator | `authors.sort_key`, `authors.name_sort_key`, `books.sort_key` |
| 4 | `textutil.FoldForSlug` | lower | stripped for Latin/Greek only | folded | no | kept | kept | foreign IDs |
| 5 | `newznab.TransliterateQuery` | as-is | kept | kept | no | kept | kept | no (outgoing query) |
| 6 | `textutil.FoldForSearch` | **case-folded** | stripped for Latin/Greek only | folded | **yes** | deleted | `" and "` | `books.search_key`, `authors.search_key`, `author_aliases.search_key` |

Alphabet 1 expands German umlauts (ö→oe) because that is what German NZB
indexers write in release names. Alphabet 2 strips them (ö→o) because author
names arrive from providers in every romanisation and the goal is one identity
per human. That divergence is deliberate and is asserted by
`TestDiacriticSchemesAreTheDocumentedOnes`.

**Alphabets 3, 4 and 6 are lossy and must never decide identity.** 6 in
particular is a *recall* key: two distinct works may share a `search_key`, which
only means both are offered to someone who typed either spelling.

## Decisions, and what they rest on

### Fold in Go, store the result, match with `LIKE`

SQLite cannot help here. `LIKE` and `COLLATE NOCASE` both fold the 26 ASCII
letters and nothing else — that is documented behaviour, not a bug
([lang_expr](https://sqlite.org/lang_expr.html): `'a' LIKE 'A'` is true but
`'æ' LIKE 'Æ'` is false; [datatype3](https://sqlite.org/datatype3.html):
"SQLite does not attempt to do full UTF case folding due to the size of the
tables required"). So the fold has to happen in Go, and the only question is
where the result lives.

We store it in a column. The alternatives were considered and rejected:

- **FTS5 with the trigram tokenizer.** Would add a real index, but it cannot
  answer queries shorter than three code points at all
  ([fts5](https://sqlite.org/fts5.html): "Substrings consisting of fewer than 3
  unicode characters do not match any rows"), and `三体` is two. It also brings
  shadow tables that every bulk write has to keep in sync. Worth revisiting if a
  very large library reports latency; the folded column is the content it would
  index either way.
- **FTS5's own `remove_diacritics`.** Latin-only, and maps to a single ASCII
  letter, so ø ł đ ß æ œ ı come through untouched at both option levels
  (verified against `ext/fts5/fts5_unicode2.c`). It also folds case from a
  Unicode 6.1 table, and setting it disables the LIKE optimisation.
- **A registered Go collation or scalar function.** Registration is
  driver-global and must precede every `Open`; it re-folds every row on every
  keystroke; and it makes the database file unreadable by any other SQLite tool.

`LIKE '%q%'` cannot use an index ([optoverview](https://sqlite.org/optoverview.html)),
but neither could the query it replaces — this is a scan that got shorter, not a
scan that got added.

### NFKC, then full case folding

NFKC folds the compatibility distinctions NFC keeps: full-width `ＴＯＫＹＯ`,
the `ﬁ` ligature, `Ⅷ`, halfwidth katakana. It is lossy, and
[UAX #15](https://www.unicode.org/reports/tr15/) §1.2 warns against applying it
to arbitrary text — which is exactly why it is confined to a key nobody displays.

Case folding is not lowercasing. The Unicode Core Specification §3.13 is
explicit that "a case folded string is not necessarily lowercase" and that
folding, not case conversion, is what caseless matching needs; `CaseFolding.txt`
is where ß→ss, ẞ→ss and ς→σ come from. `strings.ToLower` leaves ß alone, so
*Straße* and *STRASSE* would never meet. We use `golang.org/x/text/cases.Fold`,
built per call because a `Caser` is stateful (the same trap as #1374).

Turkic tailorings (`I`→`ı`) are deliberately **not** applied: they are a
locale-specific tailoring, and SQLite's own ICU `LIKE` makes the same call.

### Marks are stripped for Latin and Greek only

A combining mark is a diacritic in some scripts and part of the letter in
others. Stripping category Mn everywhere turns `ハード` (hard) into `ハート`
(heart) and merges `क़` with `क`. `FoldForSlug` already made this call for
foreign IDs after #1645; `FoldForSearch` shares the implementation
(`stripLatinGreekMarks`) so the two cannot drift. Meilisearch's tokenizer draws
the same line, stripping marks for Latin but not for Japanese or Korean.

Greek *does* fold: ά/ή/ώ are accented forms of one letter, Greek drops the tonos
in all-caps, and word-final sigma is normalised so `ΝΙΚΟΣ` and `Νίκος` reach one
key. Cyrillic does **not** fold as a script, because й and и are two letters —
with one deliberate exception, ё→е, because Russian is routinely typed and
printed with е for ё.

Two traps are worth naming because they were both live in draft code:

- **Spacing marks.** A Devanagari vowel sign (ा) is category **Mc**, not Mn. A
  fold that keeps "letters and numbers" and treats everything else as a
  separator therefore *deletes* it, folding कमला onto कमल — the #1645 collapse
  arriving through the separator branch instead of the diacritic branch. Marks
  are word characters in this alphabet.
- **Stacked marks.** Vietnamese ộ carries two. Per-rune NFD decomposition
  handles them; a single-level table does not, which is the bug SQLite documents
  in its own `remove_diacritics=1` and cannot fix without breaking indexes.

Recomposing with NFC at the end matters for Hangul, which NFD decomposes into
conjoining jamo that would never meet a composed syllable from a provider.

### Strip marks before folding the non-decomposable letters

`FoldNonDecomposableLatin` maps the Latin letters that NFD cannot take apart
(`ß æ ø œ ł đ ı þ ð`) onto an ASCII approximation. It keys on the bare letters,
so it must run **after** mark stripping, never before.

A precomposed `ǣ` (U+01E3, ae with macron) decomposes to `æ` plus a combining
macron. Fold the table first and the macron is still attached, the table does
not recognise the letter, and the ligature survives to the end. The result is
two unreachable spellings of one name:

| input | table first (wrong) | marks first (right) |
|---|---|---|
| `Ǣlfric` | `ælfric` | `aelfric` |
| `Ælfric` | `aelfric` | `aelfric` |
| `Nesbǿ` | `nesbø` | `nesbo` |
| `Nesbø` | `nesbo` | `nesbo` |

It also makes the fold non-idempotent, since folding the output again finally
reaches the table. Six code points are affected: U+01E2, U+01E3, U+01FC, U+01FD,
U+01FE, U+01FF. The same ordering applies in `FoldForSlug`, in `db.authorSortKey`
(where the ligature went on sorting after Z, the very bug `sort_key` exists to
fix) and in the TypeScript port.

`internal/normdrift` asserts idempotency over a shared corpus, which is what
catches this; the corpus has to contain a precomposed form for the assertion to
mean anything.

### `&` expands here and nowhere else

`Foundation & Empire` and `Foundation and Empire` should be one search. But
alphabet 1 feeds `indexer.ContainsPhrase`, which requires keywords to be
contiguous, so injecting an `and` token there would break every phrase hit on a
release named `Foundation.&.Empire`. The expansion is therefore in alphabet 6
only. Extending it to the dedup key is tracked separately, because that changes
stored keys and needs a revision bump.

### Ranking: exact word beats prefix

Matching answers "does this row match", not "how well". The tiers in
`internal/db/searchrank.go` are the ones Algolia (`exact` criterion),
Meilisearch (`exactness` rule) and Typesense (`prioritize_exact_match`) all
converge on, with one distinction a naive prefix/substring ladder misses:
matching a **complete word** is stronger evidence than matching the start of a
longer one. `thor` should offer Brad Thor before Thornton Wilder, even though
only the latter is a prefix of the field.

Within a tier the shorter title wins, which is what BM25's length normalisation
(`b`) does and for the same reason. FTS5's `bm25()` is not used: its `k1` and
`b` are hard-coded, and it would only rank rows the tiers have already separated.

### What we deliberately did not do

- **`golang.org/x/text/collate`.** Its tables are Unicode 6.2 / CLDR 23 and
  script reordering is unimplemented. UTS #10 is also clear that collation is
  for ordering, not identity.
- **UTS #39 confusables.** Built for spoof detection in identifiers, explicitly
  "not suitable for display", and it would merge distinctions readers rely on.
- **Phonetic keys (Soundex, Double Metaphone).** Christen (2006) measures them
  at F ≈ 0.30–0.41 against 0.59–0.89 for Jaro/Winkler, and recommends against
  comparing phonetic codes directly; they belong in blocking, not matching. The
  only Go Double Metaphone implementation is GPL-2.0 and unusable here anyway.
- **Stemming.** `Dune` and `Dunes` stay distinct keys.

## Fixtures

`internal/textutil/testdata/search_fixtures.json` is the shared corpus, read by
both the Go tests and (from Phase A2) the web suite, so the key written into the
column and the fold applied to the query cannot drift apart. Every row carries
the issue it came from: #1610 (Phönix), #1642 (Nesbø, 刘慈欣), #1645
(ハリー・ポッター, ハード, कमला), #2042 (Poseidon's Arrow, Foundation & Empire),
#1347 (Östergaard), #1646 (decomposed spellings), #1660 (the compatibility and
case-folding rows).

`internal/normdrift` then asserts the *properties* across every registered fold:
Unicode-form invariance, idempotence, keyword-findability, and that distinct
scripts keep distinct keys.

## Adding an alphabet

1. Add it to the header list in `internal/textutil/fold.go`, saying what it
   folds and, more importantly, **why it differs** from its neighbours.
2. Register it in `normdrift.stringFolds`.
3. If it is stored, give it a `Rev` constant and a `runBackfillOnce` pass in
   `internal/db/db.go`, and say in the constant's comment what must bump it.
4. Add a fixture row for whatever real report motivated it.
5. Add a row to the table at the top of this file.

## Sources

Primary sources, all read rather than cited from memory.

**Unicode.** [UAX #15 Normalization Forms](https://www.unicode.org/reports/tr15/);
Core Specification ch. 3 §3.13 Default Case Algorithms;
[CaseFolding.txt](https://www.unicode.org/Public/UCD/latest/ucd/CaseFolding.txt);
[UnicodeData.txt](https://www.unicode.org/Public/UCD/latest/ucd/UnicodeData.txt)
(that ß, æ, ø, đ, ł, œ, ı have no decomposition; that U+3099, U+093C and U+093E
are marks); [UTS #10 Collation](https://www.unicode.org/reports/tr10/);
[UTS #39 Security Mechanisms](https://www.unicode.org/reports/tr39/);
withdrawn [UTR #30 Character Foldings](https://www.unicode.org/reports/tr30/),
which is where the accent-removal vs diacritic-removal distinction comes from.

**SQLite.** [FTS5](https://sqlite.org/fts5.html),
[expressions](https://sqlite.org/lang_expr.html),
[datatypes](https://sqlite.org/datatype3.html),
[the query optimizer](https://sqlite.org/optoverview.html), and
`ext/fts5/fts5_unicode2.c`.

**Ranking.** Algolia ranking criteria; Meilisearch ranking rules and typo
tolerance; Typesense search parameters; Lucene `BM25Similarity`.

**Matching (for later phases).** Cohen, Ravikumar & Fienberg, *A Comparison of
String Distance Metrics for Name-Matching Tasks* (IIWeb 2003); Christen, *A
Comparison of Personal Name Matching* (ANU TR-CS-06-02, 2006); Winkler, *String
Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter Model*
(ASA 1990) and *Overview of Record Linkage* (US Census RRS2006-02); the Splink
documentation for the additive-evidence form; LC-PCC *Access Point for Person*
(2025-03-04) and MARC 245/100/700 for name particles and non-filing articles;
RapidFuzz (MIT) for `WRatio`; beets (MIT) for weighted title distance.

**Licence note.** Bindery is MIT. Picard, Calibre, Sonarr/Readarr and FuzzyWuzzy
are GPL and guessit is LGPL: their *ideas* are referenced here, none of their
code. `golang.org/x/text` is BSD-3-Clause and already a dependency; this work
added none.
