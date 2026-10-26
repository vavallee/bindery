# Troubleshooting

Solutions to recurring problems, organised by symptom. Add new entries here as patterns come up in support.

## A release names a different book or author

Release matching rejects extra meaningful words inside a requested title and
preserves its numbers: `12 More Rules for Life` cannot satisfy `12 Rules for Life`,
even when both name the same author. An explicit trailing `by Author` or
`Title - Author` credit that conflicts with the requested author is also rejected.
Title-only releases, connecting words, file-format labels and narrator credits
remain supported. Unrecognised trailing text can be conservatively rejected;
inspect the release's title and author rather than relying only on shared words.

When a filter is wrong about a release you can see, you do not have to wait for
a fix. The book page's interactive search keeps what the title and author check
and the language filter removed in a **Filtered** section under the results. It
is collapsed by default, shows the reason on each row, and has the same Grab
button. Releases dropped as not a book (video, a movie or console category, a
raw usenet posting) are listed in the search details panel only.

### A release names the next book in the series

Matching also reads what comes AFTER your title. A release that has the whole
title and then goes on to name another book is dropped, with a reason in the
search debug view (`GET /search/last-debug`, and the interactive search panel).
Two things drop it:

- **A later series number, when the book has a stored series position.**
  `King of Scars 02 - Rule of Wolves` is not `King of Scars` (position 1). A
  number of 2 or more straight after the title (also `[Rotten School 12]`,
  `Series Book 3`, `Series #3`) that differs from the stored position drops the
  release with the reason `release names a different series number after this
  title`. It only counts when the number ends the name, is followed by
  punctuation (a dash, colon, comma, semicolon, bracket, or a period and a
  space), or sits behind a marker word (book, vol, volume, no, number, episode,
  tome, band, part) or a `#`. A number followed by an ordinary word is a count
  and never drops a release: `Title 12 files`, `Title 4 mp3`, `Title 24 bit`,
  `Title 3 Tapes`, `Title 2 vols`, `Title 2 CD`, `64 kbps`, `12 hours`. A
  capital roman numeral from `II` to `XXX` counts as a number in the same
  places (`Mistborn III` at position 2 is dropped). A lone `I`, `V` or `X` is
  never read as a number that drops a release (they are the pronoun and author
  initials, as in `V. E. Schwab`); they only count as the wanted number that
  keeps one, behind a marker word or at the end of the name (`Alpha Book V`),
  lowercase is never read, and nothing is read after a wanted title that itself
  ends in a roman numeral (`Henry V`, `Rocky II`).
- **Another of the author's titles.** If one of the author's other books in your
  library appears in the words after the title, the release is dropped with
  `release names another title after this one`. A title before yours
  (`Belgariad 2 - Queen of Sorcery`) is a series prefix and is fine. Only
  library titles of two or more significant words count, and the rule is off
  when the wanted book is itself a set (a set release lists its books). A
  library title is also not read as "another title" when it equals, contains or
  is contained in the book's own series title (releases write `Title (Series
  #N)` and `Title - Series N`), when it contains the wanted title (`Dune
  Messiah` for `Dune`), or when the wanted title contains it. A library title
  that is directly followed by a number, or by a marker word and a number
  (`Rotten School 12`, `The Dark Tower 1`, `Rotten School Book 12`), is a series
  label and not another book, even when the wanted book has no series link. So
  is one inside brackets or parentheses (`(The Dark Tower)`), one followed by a
  series word (`The Dark Tower Series`, Trilogy, Saga, Cycle, Chronicles), and
  one followed by a capital roman numeral (`The Dark Tower III`). A number of
  four digits is a year, not a label.

Numbers that are not series numbers never drop a release: `01` (the first book),
`Part 1 of 2` and `Book 2 of the Stormlight Archive` (a number followed by
"of"), a four digit year, and a number equal to the book's own position (or
inside a stored range such as `1-3`).

**The limit that matters most:** with no stored series position, a number after
the title is not judged at all, because nothing in the release name can tell
`Title 02 - Rule of Wolves` from `Title 02 - Scott Brick` or `Title 02 -
Dramatized`. Such a release is caught only through a library title after the
number. So `Rotten School 12` and `Percy Jackson and the Olympians 04` are
caught only when the book has a series position (it comes from the series data,
see the Hardcover series page) or the later book is in your library. Set the
series position on the book to turn the number check on.

Stored series position and release numbering can disagree, and Bindery cannot
tell which is right: Discworld book 4 stored as position 1 of the `Death`
series is judged against `Mort 04` and dropped. Only the book's primary series
position is used. If a book is wrongly dropped for this reason, correct or clear
its series position. A different book whose title really ends in a number (`Rule
of Wolves 2`) is read as a series label by the library title rule and is not
caught.

What else this does not catch: a later book that carries no number and is not in
your library (`Title - Some Other Book`), a library title that is the wanted
entry's own subtitle or member when the entry is a series (`Mistborn - The Final
Empire` for a bare `Mistborn` entry with `The Final Empire` in the library, `The
Lord of the Rings - The Fellowship of the Ring`), a number joined to the title
without a separator the filter can read (`Title2`) or written as a word (`Title
Two`), and a release that extends the wanted title into another book's title
(`Frank Herbert - Dune Messiah` is still approved for `Dune`; the title checks
above only stop it when the title is one significant word followed by a
connective and more words).

### A release has the main title but not the subtitle

A book stored as `Main: Subtitle` is also matched on its main title alone,
because releases often drop the subtitle (`The Hobbit` for `The Hobbit: Or There
and Back Again`). That reading is now held to the same checks as the full title,
and three more:

- If another result in the same search carries the whole title, a main title
  only release is dropped with `release carries only the main title, and
  another result carries the full title`. The other result counts only when it
  is the same media type (audio against audio, ebook against ebook, judged by
  the format words; a release with no format word, or with both kinds, never
  triggers the rule), is the book itself (nothing but release furniture after
  the title, and no summary, analysis, study guide, workbook, companion, review,
  notes, guide or highlights wording), and is approved on the full title. So a
  summary epub or a study guide pdf never hides an audiobook. Ranking is left
  alone: a main title only release that survives is ranked like any other.
  A release that names the subtitle too (`Prelude to Foundation`) is not a main
  title only release.
- A subtitle that only numbers the book as the first (`Dune: Part One`, `Foo:
  I`, `Foo: 1`) is exempt, because releases drop such a subtitle as a matter of
  course.
- A main title only release is dropped with `release carries only the main
  title, but this book is a later entry or the main title is another of the
  author's books` in two cases, both positive evidence: the main title is itself
  the title of another of the author's books in your library (`Legion` for
  `Legion: Skin Deep`), unless that library title equals, contains or is
  contained in the book's own series title; or the book's subtitle numbers it 2
  or more (`Dune: Part Two`) and the release names a different number (`Dune
  III`). A stored series position of 2 or more is NOT a reason by itself: the
  plain `Author - Caliban's War (epub)` release of `Caliban's War: A Novel` is
  kept at any position. A release is kept if it carries the wanted number right
  after the main title, written as `2`, `02`, `Book 2`, `#2`, `Vol. 2`, a
  capital roman numeral (`II`), a number word after a marker (`Book Two`), or a
  season or series form (`S1 Part 2`, `Series 02`, `Season 2`).
- The series number and other title checks above also read what follows the
  main title, so `The High Druid of Shannara 3 - Straken` is not `High Druid of
  Shannara: Jarka Ruus`.

- A bare series entry is not its numbered volumes. When the wanted title has no
  series position but your library already holds `Rotten School #12 - Battle of
  the Dum Diddys` under the same author, `R L Stine - [Rotten School 12] -
  Battle of the Dum Diddys (epub)` is dropped for the wanted `Rotten School`
  (`release names a different series number after this title`), and so is the
  plain `Rotten School - Battle of the Dum Diddys`. Only a library title that
  starts with the wanted title and a number of 2 or more counts, so titles that
  end in a number, such as `Catch-22` or `Apollo 13`, are unaffected.
  A stored series position switches this off, so a duplicate row never hides
  the wanted volume's own release. The same goes for a library title with the
  series prefix and no number: with `Rotten School - Punk'd and skunked` in
  the library (excluded books are not listed, so the standalone `Punk'd and
  Skunked` row does not help), `R L Stine - [Rotten School 11] - Punk'd and
  Skunked (epub)` is dropped for the wanted `Rotten School` (`release names
  another title after this one`), whichever way the apostrophe is spelled.
  The prefix must be followed by a dash, colon or bracket. A suffix that
  describes an edition is not read as a book: one that starts or ends with a
  word for edition (`German Edition`, `Edicion en Espanol`, `Deutsche Ausgabe`,
  `Norsk utgave`), starts with an ordinal or a language name (`10th Anniversary
  Edition`, `Second Printing`), mentions an anniversary, or is made only of
  words such as `Large Print`, `Movie Tie-in` and `A Novel`. A library title
  that is the same book written with its subtitle (`Sapiens: A Brief History
  of Humankind` next to `Sapiens`, a duplicate from another provider) never
  rejects the release that carries the whole title straight after the wanted
  one, in any punctuation (`Sapiens - A Brief History of Humankind`). Only
  connecting words (the, a, an, and, or, of, in, to, by, for, with, at, from,
  on) may sit between the two; a number, a roman numeral, a short word such as
  `Mr`, or a number before the wanted title (`11 - Rotten School - ...`) still
  counts as another book. If the library also holds the subtitle as a book of
  its own, that book still counts. Searching for `Punk'd and Skunked` itself
  still finds these releases.

- Known trade-off of the two rules above: with a library title
  `Dune 2 - Illustrated Edition` and no stored position, the plain `Dune -
  Illustrated Edition (epub)` is dropped for the wanted `Dune`, because the
  words after the number count as another title. Known misses that stay kept,
  as on main: a number after the suffix (`Punk'd and Skunked - Book 11`,
  `#11`), a backtick used as the apostrophe, and a real book whose suffix
  starts with an ordinal or a language name or reads like an edition (`Second
  Foundation`, `Edition Zero`, `Anniversary Waltz`).

The limit: with no series position, no library title that matches the main
title, and no full title among the results, a lone `Legion (2012)` is still
approved for `Legion: Skin Deep`. Nothing in the release or the library says
otherwise, and Bindery drops releases only on positive evidence. Having the
main title's own book in your library turns the rule on.

Two more limits. A subtitle that numbers the book 2 or more (`Dune: Part Two`)
drops a release only when the release names a different number (`Dune III`);
the plain `Dune (Unabridged) MP3` is kept, the same as the plain release of
`Foundation and Empire: Book Two`. That number is read with the same count guard
as above, so `64 kbps` or `24 bit` is not a different number. And when the
book's series is linked, a library entry that carries the series name is not
read as "another book": `Frank Herbert - Dune (epub)` is approved for the wanted
`Dune: Messiah` at position 2 when the series is `Dune Chronicles` and the
library holds `Dune`.

### A title says "Book One" and the release says "Book 1"

A spelled out number straight after a volume word (book, volume, vol, part,
episode, season, year, arc; one to twenty) and its digit are treated as the same
position, in both directions. A book called `Path of the Deathless Book One`
matches a release named `Path of the Deathless Book 1`, and the reverse.

- **Search.** Indexers that need every query word in the release name return
  nothing for `Book One` on every tier. When a whole search finds nothing
  relevant, Bindery sends ONE more query per indexer with the digit form
  (`Path of the Deathless Book 1`, title only). A title without such a number
  word, and a search that already found its book, never pays for it. A book that
  stays unfound pays that one extra query per indexer on every scheduler cycle. The query
  shows in the debug panel's indexer row as `numberWordQuery`, with
  `numberWordResults` for what it returned, and `numberWordError` if that query
  failed (the search itself carries on).
- **Relevance.** A different position still does not match: `Book One` is not
  `Book 10`, `Book 2` or `Book Two`, and the part and set rules (`Book 1 of 2`,
  `Part 3 of 5`, box sets) judge a `Book One` title exactly as they judge `Book 1`.
- **Not covered.** Ordinals (`First Book`, `Book 1st`), numbers above twenty and
  compound numbers (`Book Twenty One`), and roman numerals.

### A search result is one chapter of an audiobook

Some usenet posts carry a single chapter, such as `Terry Goodkind - SOT06 Faith
of the Fallen Ch63 of 71.mp3`. Bindery drops these before relevance matching,
with the reason `looks like one chapter or part of a larger post (Ch NN, NN of
MM)` in the search debug view (stage `usenet-junk`). The same filter runs on the
free text Search page. A title is a fragment when it has either:

- a chapter marker, `Ch`, `Ch.` or `Chapter`, straight before a number (`Ch63`,
  `Ch. 5`, `Chapter 12`); or
- `NN of MM` with MM of 10 or more (`12 of 40`, `Part 3 of 12`), unless the word
  before NN numbers a book in a series: book, bk, vol, volume, series, no,
  number, novel, tome, band, season, episode, issue, disc, cd, or a `#`
  (`Book 3 of 12`, `Vol 2 of 10`, `Tome 03 of 41`),

and it names an audio format (mp3, m4a, m4b, flac, ogg, aac, wav, wma, opus) or
no format at all. A title that names an ebook format (epub, epub3, kepub, mobi,
azw, azw3, kfx, pdf, doc, docx, html, htm, ebook, lit, prc, djvu, cbr, cbz, fb2,
rtf, txt) is never dropped by this rule. An underscore counts as a space, so
`Author_Title_Ch63_of_71_mp3` is dropped like the dotted form. The rule is exact
about counters: a bare `NN of MM` with MM under 10, and a whole book `Part 1 of
2`, are kept; a chapter marker or `NN of MM` with MM of 10 or more is dropped
unless an ebook format or a series count word spares it.

One more shape is dropped for audiobooks: a single part of a set. Bindery
imports one grab as one book and cannot join several grabs, so `Part 3 of 5`,
`Pt 2 of 3`, `Disc 2 of 4` and `CD 3 of 8` are partial books. The reason shown
is `looks like one part of a multipart audiobook set (Part N of M), which
Bindery cannot assemble`. The rule is narrow:

- the counter must come straight after part, pt, disc, disk or cd, and the total
  must be 3 or more (`Part 1 of 2` is kept, since two parts are the likeliest
  shape of one recording split in two; `Book 3 of 5` and a bare `3 of 5` are
  not parts);
- it applies to a title that names an audio format, or to one that names no
  format when the search is for an audiobook. An ebook format in the name, or an
  ebook search with no audio format in the name, is never affected;
- a name with `all parts` or the word `complete` (`Disc 1 of 8 Complete`) is
  kept as the whole set. `Unabridged` does not count, because single parts are
  often labelled unabridged;
- a counter that the wanted book's own title contains is kept.

The rule applies to torrent results as well as usenet ones. The price is that
a recording posted as `Part 1 of 3` or longer is no longer eligible. Lone parts
it does not recognise, and so keeps: `Part 3/5`, `3/5`, `Part III of V`,
`Vol 3 of 5` and `Part One of Two`.

The file size is not used. A fragment is recognised by its name only, so an
ebook with a chapter counter and no format word in its name is dropped too.

What this does not catch: a fragment posted as a bare `3 of 8` with no part
word, a fragment with
only a bare number and no counter word, and a fragment named in another
language. It can wrongly drop a whole release whose own title contains `NN of
MM` with MM of 10 or more, no ebook format and no series count word in front
(`12 of 56 Stories`, `Discworld 03 of 41`).

These checks use the release name. They do not open the download. A separate
check does, once the download has finished: Bindery compares an EPUB's own title
and author with the book it was grabbed for, and for an audiobook compares the
audio tags of its files with the book. It is set per format under Settings,
General, File Naming (`import.ebook.content_check` and
`import.audiobook.content_check`), defaults to
`warn`, which imports the file and records what it found on the History row and
in webhooks as `contentCheck`, and only refuses a download in `block` mode. See
[the User Guide](User-Guide-Wiki.md) for what counts as a mismatch. Nothing
repairs an existing incorrect import. If a previously imported file is another
book, use **Fix match** to assign it to the correct book before requesting a
replacement for the original.

### An import was blocked because the file looks like a different book

The message names what was found and what the book is. For an ebook, for example
`the file says it is "Mary and Mr Eliot: A Sort of Love Story" by Mary Trevelyan,
but the book is "Mary, Mary: Alex Cross, Book 11" by James Patterson`. For an
audiobook, `the files contain Corralled (9), Coming Home to Myself (8), but the
book is "Chase" (2 of 19 tracks match)`, with the number of files in brackets.
The release is already on the Blocklist and the book is still Wanted, so the
next search picks another release.

- **The file really is the book** (a mislabelled or foreign language edition, a
  file whose metadata or tags are wrong): use **Match to book** on the Queue row.
  That keeps the file and skips the check.
- **The author is a pen name** (ebooks): add it as an alias on the author, and
  **Retry import** will accept the file.
- **An audiobook is the right book but its tags disagree with each other** (some
  files carry a retitled or regional album name): use **Match to book**. A
  folder whose every file carries the same other title is not blocked, since
  that is what a translated edition looks like.
- **You would rather be warned than blocked**: set the check to `warn`. The
  import goes ahead and the History row shows what was found.


### What the content check does not catch, and where it can be wrong

Use `block` with care. The check compares what the file says about itself, so it
inherits whatever that data gets wrong. Known limits:

- **Spelling variants of a name or title that Bindery does not fold**: a
  transliteration (`Fyodor Dostoyevsky` against `Fjodor Dostojewski`, `Schuld und
  Suehne` against `Schuld und Sühne`), a different English edition title
  (`Philosopher's Stone` against `Sorcerer's Stone`), `1984` against `Nineteen
  Eighty-Four`, or a real name against its pen name when no alias is recorded
  (`Eric Arthur Blair` for `George Orwell`). Aliases fix author spelling variants,
  and a variant title with a matching author is never flagged because both must
  disagree. Where it matters, leave the check on `warn`.
- **A download that is wholly one other book**: an audiobook folder whose every
  file carries one other title passes, because that is what a translated or
  retitled edition looks like. In `warn` and `block` the `contentCheck` object
  then carries `retitled` with the title and file count, and History shows a quiet
  line, but nothing is blocked.
- **`Dune` against `Dune Messiah`**: a short wanted title is supported by any
  title that contains it as whole words, by design, so this passes.
- **Multi-part single books** such as The Lord of the Rings or The Chronicles of
  Narnia, which are one catalogue book whose files may carry the parts' titles.
- **Transliterated and non-Latin editions** never flag; a wrong book in another
  script is not caught either.
- **Sampling**: more than 500 audio files are sampled evenly, and two adjacent
  foreign files can fall between samples.
- **Formats that are not read**: `.wma`, `.aac` and `.wav` audiobook files carry no
  tags the check reads, so they fail open, as do unreadable files.
- **A wrong book with a placeholder creator** (Unknown, calibre, a publisher) imports
  even in `block` mode, because a creator that names nobody can never disagree and
  both title and author must disagree to flag.
- **A small foreign share is treated as strays**: files that do not support the
  book must be at least 5 percent of the tracks read, so 30 foreign files in 700
  do not flag the folder.
- **A wrong book in another script never flags**, in either check.
- **Numeric titles and short number words**: a wanted title like `Slaughterhouse 5`
  is read as equal to `Slaughterhouse Five`, so titles that differ only by a number
  word and a digit are the same to the check.
- **Hung network shares can exceed the budget**: a read already in flight is not
  interrupted, so the 30 second audiobook budget is checked between files.
- **Publisher style creators that are not on the short list** (`Penguin Books`,
  `Baen Books`, `DAW Books`, `Scholastic Press`, `Penguin Group USA`, `Pearson
  Education`, `Pan Macmillan`, `Random House Audio`, `Harlequin`) are read as
  authors. Only the explicit publisher list and the endings Inc, Ltd, LLC, GmbH,
  Publishing, Publishers and Group count as no creator, so a wrong title with such
  a creator can flag.
- **Release group stamps in an audiobook album** such as `Downloaded from
  rutracker.org` or `[example.com] Dune` can count as another title, because only
  `www.`, a scheme, a word with two or more dots or a lone domain is read as a web
  address.
- **A release with more than 20 EPUBs** is not judged at all (the note says
  `truncated`), since a verdict on a prefix could condemn the right book.
- **Short story and compilation titles** that are not worded as one (a single word
  title that is really an anthology) are compared like any book. The compilation
  skip covers omnibus, anthology, short stories, collected and complete works,
  trilogy and similar, `Complete` plus two or three words, `X Y Collection`, and
  `The Best of <the book's author>`.
- **Retry import after a book was matched from the file name**: a download grabbed
  without a book is matched to one from its file or release name, and that match is
  saved on the download. Bindery does not record that it was a guess, so Retry
  import on such a download checks the saved book as if it had been grabbed for it.

## Bindery will not start after upgrading: "foreign_key_check found N violation(s)"

```
ERROR msg=failed to open database error=run migrations: migration 72: foreign_key_check found 2231 violation(s)
```

Affects **v1.30.1, v1.30.2 and v1.30.3**. Fixed in **v1.30.4** — upgrading is the whole fix, and nothing else is required.

**Your data is fine.** Migrations run in a transaction, so the failed migration rolled back and the database on disk is exactly as it was. Repulling the image does not help, because nothing is wrong with the image: the condition lives in your config volume.

What happened: migration 72 rebuilds two Calibre tables, and its post-rebuild integrity check scanned the **whole** database instead of the tables it rebuilt. Long-running instances carry orphan rows left over from [#1727](https://github.com/vavallee/bindery/issues/1727), where foreign key enforcement could silently switch off and the schema's `ON DELETE CASCADE` rules stopped firing. Those old, unrelated orphans failed a migration that had nothing to do with them ([#1972](https://github.com/vavallee/bindery/issues/1972)). The more history an instance had, the more certain it was to hit this.

### Fix

Upgrade to v1.30.4 or later. The migration now checks only what it changed; pre-existing orphans are logged as a warning and the instance starts:

```
WARN database carries pre-existing foreign-key violations from before this upgrade ... violations=2231 tables="book_files=600, books=200, downloads=831, editions=600"
```

### Optional cleanup

The warning is harmless — nothing is broken and there is no rush — but you can clear the orphans when convenient. Stop Bindery, back up the database, then:

```bash
docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-check
docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-repair --yes
```

Bare-metal: `bindery db-check` / `bindery db-repair --yes` (add the database path as an argument if it isn't at `BINDERY_DB_PATH`).

`db-check` lists every affected row (table, rowid, missing parent) and changes nothing. `db-repair` replays the delete rule the schema declares — `ON DELETE CASCADE` orphans are removed, `ON DELETE SET NULL` references are cleared so download history keeps its rows — and prints exactly what it did. It refuses to run without `--yes`.

If your platform can't run a different command, set `BINDERY_DB_FK_CHECK=report` (or `=repair`), start the container once, read the log, and unset it.

## A grabbed book never imports

The book shows **grabbed** in History and reaches 100% in your download client, but Bindery never imports it. Usually one of the causes below.

### qBittorrent 5.x with an old Bindery

qBittorrent 5.x changed the reply it sends when a torrent is added: 4.x answered with the plain text `Ok.`, 5.x answers with a JSON object. Bindery **before 1.12.1** only understood `Ok.`, so it treats every add to a qBittorrent 5.x client as failed even though the torrent was accepted and downloads fine.

You may see `add torrent failed: {"added_torrent_ids":...}` or `failed to send to downloader`. Because Bindery believes the hand-off failed, it never records the download, so it is never imported and no `importFailed` event appears.

**Fix:** upgrade to Bindery **1.12.1 or later** (the current release is recommended). Bindery 1.11.0 and earlier cannot complete torrent grabs against qBittorrent 5.x at all — it is a hard incompatibility. After upgrading, re-grab anything that was stuck.

### The file is in a language your profile does not allow

The Queue row is **importBlocked** with a message like `file declares Swedish (swe), but the metadata profile "Standard" allows only English (eng)`. The release name did not say its language, so it passed the search filter, but the ebook inside (an EPUB, or a MOBI, AZW or AZW3 Kindle file) declares one the author's metadata profile does not allow. Bindery placed nothing, added the release to the Blocklist and left the book Wanted, so the next search picks a different release on its own. See [the file's language is checked at import](User-Guide-Wiki.md#from-wanted-to-your-library) for the full rule.

If the file is what you want after all, press **Match to book** on the row and pick the book, or import it through **Import → From a folder**; a manual import is never refused for its language. To accept more languages for this author from now on, add them to the metadata profile, or set it to any language. Releases already blocklisted for this stay there until you remove them in **Settings → Blocklist**.

### A foreign language audiobook was grabbed by an English profile

Bindery also checks the language tag of the audiobook files at import and refuses a release whose tagged language is not allowed, so a release whose name says nothing is caught when the files arrive. A file with no language tag is never refused. If a tag is simply wrong, use **Match to book** on the Queue row to import it anyway.

Scene audiobook names carry their language only as a short code, and the search filter reads those codes. It reads a code in the suffix after a marker (`audiobook`, `abook`, `mp3`, `m4b`, `flac`, `aac`, or a disc token such as `4CD` or `2MP3CD`), as in `-AUDIOBOOK-WEB-DE-2023-GROUP`, and a capitalised two or three letter code placed just before a marker, as in `Author-Title-DE-AUDIOBOOK-4CD-FLAC-2005-GROUP`. The mangled German words `ungekrzt` and `gekrzt` count as German as well. The code never counts from the title.

The rules are deliberately narrow, because an English release dropped by mistake is worse than a foreign one that slips through. "Capitalised" below means the code is all capitals.

- **A code before a marker** is only read in a scene shaped name: capitals joined by single separators to the token before it and to the marker, no spaces from the code to the year, a group after the year, and a lowercase letter somewhere before it (an all caps name tells nothing). Only `IT` and `NO` are excluded there, because they are English words and titles; other codes that are also words can still tag a release, for example `Title-SE-AUDIOBOOK` (Special Edition) reads Swedish, `Title-DE-AUDIOBOOK` (Deluxe Edition) German and `Mr_Mercedes-DK-MP3-2014-GRP` Danish. After a marker `IT` and `NO` still count.
- **A spaced, human written name** counts a capitalised code right before a year, or right before the group when a marker or a year sits directly before the code. `IT`, `NO` and `HI` are refused as words when the year is the last token (`Audiobook - IT 2017`, `Hi 2010`) and when the code sits right before the group (`3CD IT GRP`, `Audiobook 2018 NO GRP`), so a genuine `Audiobook - NO 2019` stays untagged. `read by Robert DE NIRO`, `DE LUXE` after a year or in running text, and `MP3 PT 2` are not read. A code before the group needs a marker or year directly before it, so a true `M4B 64kbps DE GRP` or `MP3 - 320 - DE - GRP` is no longer read. A group that is itself a word still fools it: `Audiobook 2018 DE NIRO` and `Audiobook MP3 DE LUXE` tag German, as they did. Spaced names with a group after the year are read as before, so `(MP3) Stephen King IT 2018 GRP`, `(Audiobook) Dr. NO 2018 GRP`, `Audiobook - IT 2019 - GRP` and `(Audiobook) (DE LUXE)` still tag wrongly, as they did.
- `-WEB-SE-` with no audiobook marker is not read, because `WEB` is not treated as a marker.
- **Two codes:** `-DE-EN-` after a marker is tagged both German and English, so an English only profile drops it. Before a marker only the code next to the marker counts: `Title-DE-EN-AUDIOBOOK` reads English only and `Title-EN-DE-AUDIOBOOK` German only.

Nothing here has been run against live indexers beyond made up names in the tests. If a foreign release still gets through, its name carries no marker this reading accepts. Blocklist that release in **Settings → Blocklist** so the next search picks another.

### Bindery cannot read the completed files

**Start with Diagnose.** In **Settings → Download clients**, press **Diagnose** on the client. It works out where Bindery's grabs actually land for ebooks and for audiobooks (the save path Bindery sends, the category's folder, or the client default, and it says which), applies the path remap the same way the importer does, checks that Bindery can read and write the result, and tries a hardlink into each library folder. The panel puts the first thing to fix at the top and shows the paths side by side: where the client puts the files, which remap applied, and where Bindery looks. **Copy report** copies the check results and paths without the host, port or username, so it is safe to paste into an issue or Discord.

A few answers need explaining:

* **Outside every folder it is configured to use.** The remapped path is not under `BINDERY_DOWNLOAD_DIR`, `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` or a library folder. Bindery does not look inside such a folder at all. Almost always a missing or wrong path remap.
* **Differs only in letter case.** Linux folder names are case sensitive, so `/Downloads` and `/downloads` are different folders. Fix the case in the remap.
* **SABnzbd did not share its folder settings.** SABnzbd only gives its folders to the full API key. With the NZB key the folder rows stay unknown; that is not a failure.
* **Did not respond within 10 seconds.** A stat on that folder hung, which is what a network mount that has stopped answering does. Check the mount; the request gives up rather than waiting.
* **Client reaching indexers: unknown.** This row is always unknown. Bindery fetches NZB and torrent files itself, but it cannot test the client's own network, VPN or DNS.

If Bindery and the download client see the storage at different paths (different container mounts), Bindery cannot find the finished download. This usually surfaces as `importFailed` in the Queue with *nothing at `<path>` on this host*.

That message lists three causes, because all three produce the same missing path and only you can tell them apart:

1. **The download hasn't finished.** With qBittorrent's temp/incomplete directory enabled (`Session\TempPathEnabled=true`), the final save path doesn't exist until qBittorrent finishes and moves the payload there. Nothing to do — Bindery imports it when it lands.
2. **The files moved or were deleted since.** Common after deleting a book (or its files) from the UI while the torrent stays in the client, and after an import in `move` mode. Re-download, or point the client back at the files.
3. **Bindery and the client see different filesystem roots.** This is the path-remap case: set a download-client path remap in **Settings → Download clients**, then use **Queue → Retry import**. See [Path remapping](./DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups) in `DEPLOYMENT.md`.

**Download client on Windows, Bindery on Linux or Docker?** The client reports a drive path like `S:\Downloads`, which can never exist inside Bindery, so a remap is mandatory. Write the pair with the drive letter on the left: `S:\Downloads:/mnt/Storage/Downloads`. The colon after the drive letter is part of the path, not a second separator. Bindery **1.32.1 and earlier** read that same string as `S` mapping to `\Downloads:/mnt/Storage/Downloads`, so the remap silently did nothing: torrents were added, author and book folders were created, and no file was ever copied. If the Test button warns about the path on one of those versions, upgrade; no spelling of the pair works there. See [Windows download client, Bindery on Linux](./DEPLOYMENT.md#windows-download-client-bindery-on-linux-or-docker) in `DEPLOYMENT.md`.

Bindery does not spend import retry attempts while there is nothing at the path, so a download in this state keeps its full retry budget for attempts that could actually work — it stays `importFailed` with the reason visible, and imports on its own as soon as the files appear.

If the files never appear, it does not wait forever: after about 30 minutes of finding nothing (120 poll cycles) the download flips to `importBlocked` with a message naming the path it checked. From there you can **Retry import** once you've fixed the path, or grab the release again from search — a blocked entry no longer blocks a re-grab.

**Stuck at `grabbed` rather than `importFailed`?** On Bindery 1.35.x and earlier, a qBittorrent download the client called complete while sending no content path sat at `grabbed` forever, with the log repeating *will retry next cycle*. Upgrade: from 1.36.0 that case fails the import with the missing path message above, retries if the files turn up, and blocks once they never do.

### "Waiting for … to be extracted"

The download is a RAR-packed release: its folder holds an archive (`Title.part1.rar`, or `Title.rar` and `Title.r00`…) and no book file yet. Bindery does not unpack archives, so it waits, without spending import retries, for an unpacker such as unpackerr to extract the files into the download's folder, and imports them as soon as they appear. If nothing extracts it within about 30 minutes, the download is blocked with *this download holds only an archive*. Set up an unpacker as described in [Storage and Hardlinks, RAR-packed torrent releases](Storage-And-Hardlinks-Wiki.md#rar-packed-torrent-releases), or extract the archive into the folder yourself, then use **Retry import**.

### "Already grabbed" when re-grabbing a release

Clicking **Grab** on a release you already have a Queue entry for is refused with *already grabbed*, and the message now names the state that entry is in.

- **`importFailed`** — the scanner is still working on that download. Use **Queue → Retry import** to re-run the import against the files it already has, or remove the Queue entry if you want to grab the release fresh. If its files are simply not there, it turns into `importBlocked` (see above) and becomes re-grabbable on its own.
- **`imported`**: you already have it. If that import left nothing usable, Bindery asks whether to **Grab anyway** (on the Search page, a book's search results and the Wanted page). Confirming reuses the old Queue entry and sends the release to your download client again. Nothing is removed from the client, and the grab does not touch the files the book already has. Use it only when the earlier files are missing: while they still exist, the importer's duplicate checks skip the new download, so it cannot replace them or give you a second copy. Over the API, send the same grab again with `"force": true`; the refusal says whether that is allowed with `"forceAvailable": true`, which is only ever set for your own imported release, never for one still downloading or importing. If you have since deleted that book (or its author), the release is no longer held at all: grabbing it again goes through without asking and reuses the old Queue entry, and automatic search will pick it too once you add the book back. Either way, usually you can leave your download client as it is; the two exceptions are under the table. What happens next depends on the client:

  | Client | If it still has the release |
  |---|---|
  | qBittorrent, Transmission, Deluge, rTorrent | Bindery picks up the torrent the client already holds and imports its files, as long as they are still in the client's download folder. Nothing downloads again. |
  | SABnzbd, NZBGet | The release downloads again. Bindery adds NZBGet jobs past its duplicate check, and SABnzbd's duplicate detection is off unless you turned it on, in which case SABnzbd may hold or refuse the job. |

  If you removed the torrent from the client, it downloads again like any new grab. In two cases you need to do exactly that: remove the torrent from the client first, then grab again.

  - **Import mode `move`.** The earlier import moved the files out of the client's folder, so the torrent Bindery picks up has nothing left to import. The Queue entry never imports, and every later grab picks up the same empty torrent.
  - **qBittorrent 5.1 or older.** Bindery recognises a torrent qBittorrent already holds from its `409 Conflict` reply, which qBittorrent sends from 5.2 on. Older versions answer with a plain `Fails.`, the same reply as any other refused add, so the grab fails.
- **downloading / grabbed / importing** — it's in flight; check the Queue.
- **`importBlocked`** — a re-grab is allowed and reuses the existing Queue row with a fresh retry budget. Use this when the original files are gone; use **Retry import** instead when they're still on disk.

### Automatic search skips a release you have a failed Queue row for

Automatic search applies a narrower rule than the Grab button. A Queue row stops the sweep from grabbing that release again when it is in flight, when it is imported into a book you still have, or when it is `importBlocked`. A `failed` row does not: six hours after it failed, the next sweep grabs the release again and reuses the same Queue row, with the old error message, import path and client id cleared.

The six hour wait counts from the failure itself, not from when the download was added or started. A torrent grabbed at noon that your client gives up on at ten in the evening is retried from ten in the evening. The wait is there because a release that fails at the download client often fails again the moment it is re-sent, so without the wait a bad release would be re-grabbed on every sweep. Clicking **Grab**, or **Retry download** on the row (below), has no wait and retries straight away.

When the download client says the release itself is broken, Bindery does not wait at all: it adds the release to the Blocklist, and the next search picks a different one. The Blocklist entry's reason names the client's verdict, for example `downloadFailed: FAILURE/PAR`. That covers these failures:

| Client | Blocklisted |
|---|---|
| NZBGet | `FAILURE/PAR`, `FAILURE/UNPACK`, `FAILURE/HEALTH`, `FAILURE/SCAN`, and `FAILURE/BAD` (marked bad by you or by a script) |
| SABnzbd | Missing articles (*Aborted, cannot be completed*, *Not on your server(s)*, *Download might fail*), a failed repair, an unpack that failed with a CRC error, a missing volume or a password, a corrupt or unusable RAR, failed verification, and a job aborted for encryption or an unwanted extension |

Everything else keeps the six hour retry, because it says nothing about the release: SABnzbd running out of disk, a failed move (NZBGet's `FAILURE/MOVE`), a job you deleted by hand, NZBGet's duplicate handling, a script error, or a grab that never reached the client. SABnzbd's messages are matched in English; with SABnzbd set to another language its failures all keep the retry. Remove an entry under **Settings → Blocklist** to let that release be grabbed again.

NZBGet does not keep its own problems apart from the release's. A missing or misconfigured unrar or 7z, a full disk while unpacking, a folder it cannot write, or a par2 memory error also end as `FAILURE/UNPACK`, `FAILURE/PAR` or `FAILURE/SCAN`, and then every job fails, not just one. `FAILURE/HEALTH` and `FAILURE/BAD` have no such cause, so they are always blocklisted. For the other three Bindery looks for evidence before it blocklists:

1. **The job's log in NZBGet.** A CRC error, a missing volume, too little repair data, or a par check skipped for poor health means the release is broken, and it is blocklisted. A line such as `Unrar: Could not start /usr/bin/unrar` (a post processing script that will not start does not count), `Unrar error code: 8`, `Could not move file` or a par2 `out of memory` means the problem is on NZBGet's side: the release is not blocklisted, and that step stops being blocklisted for the client until NZBGet completes it again. This needs NZBGet's per job log, which is on by default (`NzbLog=yes`).
2. **NZBGet's own tool check** (NZBGet 24 and later). When an unpack failure has nothing in its log, and when you add, save or test the client, Bindery asks NZBGet whether it can find UnRAR and 7-Zip, and reuses the answer for six hours (the check is not free: NZBGet runs the tools and looks up its public address to answer it). If UnRAR is missing, the client shows an error saying so, and unpack failures are not blocklisted until it is found. A missing 7-Zip on its own is only logged, since NZBGet's default 7-Zip setting is often not installed and RAR releases do not need it.
3. **A count, when the log says nothing.** When three different releases from one client fail the same step within two hours and none completes that step in between, Bindery stops blocklisting that step for that client.

Each of these is per step: a paused unpack step never stops `FAILURE/HEALTH` or `FAILURE/PAR` from being blocklisted. While a step is paused, the client shows a warning under **Settings → Download Clients**, the log has a `WARN` line, and its failed downloads are retried after six hours as usual. It resumes when NZBGet completes that step again (a download that NZBGet actually unpacked, for the unpack step; a completed plain epub does not count), or when you save or disable the client. When it was the count that paused it, the first two releases are already on the Blocklist; remove them there if NZBGet was at fault.

SABnzbd needs none of this: it refuses to start downloading without unrar, skips 7-Zip archives when 7-Zip is missing, and reports a full disk or a failed move with its own messages, so its content failures are always blocklisted.

`importBlocked` is deliberately left to you. Those files downloaded fine and are still on disk; what failed was the import. The sweep cannot tell whether they are still there, and re-downloading them would fetch bytes you already have and leave the old torrent in your client with nothing tracking it, so a blocked row is only ever re-grabbed when you ask for it. Use **Retry import** if the files are still in place, and **Grab** if they are gone.

Earlier releases let a failed row block the release permanently, and the skip was silent: the log showed `auto-grabbing book` and then nothing, as though the grab had gone ahead. Every skip now writes a line naming the release, its GUID, the blocking Queue row and its status, and the `book search finished` line for that book ends with an `outcome` that says the same thing.

### Retrying a download that failed

A Queue row in `failed` never produced a file: the grab did not reach the download client, or the client gave up on it. There is nothing to import, so **Retry import** does not apply and is not offered. Use **Retry download** on the row instead. It sends the same release to your download client again, which is the right move when the cause was transient (the client was down, the indexer answered 429 or 500). It deliberately does not search for a different release; when the release itself is gone, search the book and grab another one, or blocklist this one first. A release NZBGet or SABnzbd failed as broken is already on the Blocklist (see above), so a search moves past it on its own; **Retry download** still sends it if you ask.

To retry in bulk, tick the rows and use **Retry selected**, or use **Retry all failed** above the list, which covers both stages: an import failure has its import re-armed, a failed download has its release re-sent. Both ask for confirmation when a re-send is in the batch, because that hands work back to your download client.

### A grab fails with "indexer ... is pinned to download client ..."

The indexer has a **Download client** set in Settings → Indexers, and that client
cannot take the grab. The message says which of three things is wrong: the client
is **disabled** (enable it in Settings → Download Clients), it **no longer
exists** (choose another client on the indexer), or it is the **wrong protocol**
(a torrent client on a usenet indexer, usually because the client's type was
changed after the pin). If the client is enabled but the message says the
release could not be sent, the client is not answering: check that it is running
and reachable from Bindery.

The message names the indexer, and the client too when you are an admin; other
users see the indexer only.

Bindery will not send the release to a different client. That is deliberate: the
pin exists so a private tracker's torrents never reach your public client. To
send the indexer's grabs through the normal routing again, set its **Download
client** back to *Default*. A grab refused before anything was sent creates no
Queue row: from the search page the error appears on the page, and a scheduled
search records it in History (once a day per book and indexer, and as a
notification naming the indexer). Just search or grab again once the cause is
fixed. Only a grab that reached an enabled client and failed there leaves a
failed Queue row with **Retry download**.

Limits worth knowing:

* A scheduled search skips only the failing indexer's releases. Releases from
  other indexers for the same book are still grabbed, and nothing from the
  pinned indexer goes to another client.
* Which indexer a grab belongs to is decided from the `indexerId` sent and the
  release URL's host: a pinned `indexerId` always wins; an unpinned one is taken
  at face value if its host matches the URL (and refused if it contradicts the
  URL's host while a pinned indexer owns that host); an unknown or missing id is
  treated as no id, and then the indexers on the URL's host decide (none:
  unpinned, one: that indexer, several with one pinned: refused). Grabs from the
  Bindery UI and the scheduler always send the indexer.
* An id-less API grab can only be tied to a pinned indexer by host. The match
  ignores case, a trailing dot and the port, so `prowlarr` against
  `prowlarr:9696` works. It does not match aliases of one machine (`localhost`,
  `127.0.0.1` and a DNS name), `www.` or other subdomains, scheme-less indexer
  URLs or punycode spellings; such a grab is routed as if unpinned. Send
  `indexerId` to be sure the pin applies.
* On a shared Prowlarr or Jackett host, one pinned indexer makes id-less API
  grabs fail for every other indexer on that host, because such a grab cannot be
  told apart. Send `indexerId` (the UI and the scheduler already do).
* A pending (held) release from a pinned indexer whose client is down stays in
  Pending and is tried again on the next sweep. It is not discarded.

### The Queue looks empty, or shorter than it should be

Look for a banner above the list saying *Could not reach `<client>`. This list may be incomplete*. Bindery gives each download client a short deadline when it renders the Queue, and a client that misses it is dropped from that render, so its rows are missing until the next poll succeeds. The rows and the downloads themselves are fine. Check the client is up and reachable at the host and port in **Settings → Download clients** (its **Test** button is the quickest answer), and remember a client that is up but very slow to answer will do this intermittently.

### "Could not match any book to this download"

The files downloaded fine, but Bindery couldn't tie them to a book in your library, so the item sits in the Queue as `importFailed` with *could not match any book to this download*. This happens when a release was grabbed without a specific book (e.g. from the free-text Search page) or its title didn't parse to a catalogue book.

For a download grabbed without a book, Bindery tries, in order: the title and author embedded in an EPUB or a Kindle file (MOBI, AZW, AZW3), the file name, and the release title you grabbed. A file name is used only when it names the book exactly or its author agrees, and the track files of a multi-file audiobook (`01. Track 1.mp3`) are skipped, since their names are not titles. When none of these is certain, the item waits for you rather than guessing. If an item was imported under the wrong book, use **Fix match** on that book's page; if it is still in the queue, import its folder from **Import** and pick the right book there.

**Fix:** on the failed Queue item, click **Match to book**, search your library for the correct book, and select it — Bindery imports the already-downloaded files against it and the item flips to **Imported**. If the book isn't in your library yet, add it first with **Books → Add Book** (pick the book row, or its author's row to bring the whole catalogue in), then match. Once matched, an item shows **Matched to *&lt;book&gt;*** and its **Retry import** button re-runs the import against that book.

If the item was left unmatched long enough for the scanner to retry it a few times, it turns into `importBlocked` with *import retry limit reached*. That's the same situation — the files are still there — so **Match to book** and **Retry import** work exactly the same on a blocked item; matching it re-imports the recorded files, and Retry import re-arms the scanner with a fresh retry budget.

### qBittorrent files land in the download root instead of the category folder

The torrent shows the right **category** label in qBittorrent, but the files are written to the download root (e.g. `/data/downloads`) instead of the category's configured save path (e.g. `/data/downloads/torrents/audiobooks`). The poller can't find them there and the import never starts.

This happened on Bindery **1.22.1 and earlier**: Bindery sent the category **and** an explicit save path with automatic torrent management (auto_tmm) off. With auto_tmm off, an explicit save path overrides the category's save path, so qBittorrent dropped the files in the root.

**Fix:** upgrade to the current release. Bindery now enables auto_tmm and omits the explicit save path whenever a category is set, so qBittorrent places files at the category's configured save path (the source of truth for Bindery's health checks). On an older version, work around it by enabling **Automatic Torrent Management** for the category in qBittorrent, or by setting the category's save path to match Bindery's download root.

## A download client tests fine but every poll logs "invalid character '<'"

```
qBittorrent category path check failed: decode categories: invalid character '<' looking for beginning of value
```

The client answered with a web page where Bindery expected JSON. Almost always the **Host** field holds more than a hostname, so every API request lands on the client's own web UI index page instead of its API. That page is served with a perfectly healthy HTTP 200, which is why **Test** used to report the connection verified: something did answer.

The value behind the message above was `192.168.1.50:8080/#/`, copied straight out of a browser address bar. Bindery appended its own port and path to it, so requests went to `http://192.168.1.50:8080/#/:8080/api/v2/...`, and the `#` threw away everything after it (#2203).

**Fix:** open **Settings → Download clients**, edit the client, and leave a bare hostname or IP in Host. The port belongs in **Port**, a reverse proxy prefix belongs in **URL Base**, and `https` is the **Use SSL** checkbox. See [What goes in a download client's Host field](./DEPLOYMENT.md#what-goes-in-a-download-clients-host-field) for the full table.

Current releases refuse to save a Host like that, fail **Test** with a message naming the port and path to move, and turn the client's health dot red, so a client carried over from an older version reports the problem the moment you press Test rather than at the next poll.

If Host is already a bare hostname and the message persists, something in front of the client is serving its own page: check that **URL Base** matches the path your reverse proxy serves the client on.

## The job stays in the download client after Bindery imports it

The book imports fine and shows **Imported** in the Queue, but the job is still sitting in SABnzbd's history (or NZBGet's, or still listed in your torrent client). Once Bindery has taken ownership of the files it asks the client to forget the job, and the client is allowed to say no.

Bindery logs that refusal. Look for a `cleanup failed` warning naming the client:

```text
level=WARN msg="cleanup failed" error="SABnzbd rejected history delete: Job not found" clientType=sabnzbd remoteID=SABnzbd_nzo_abc123
```

Versions before this fix (#2192) produced no log line at all, because SABnzbd and NZBGet report a refused action as a normal HTTP 200 response with a failure flag inside it, and Bindery read only the HTTP status. Nothing about the client's behaviour changed, only whether Bindery notices it.

Common reasons a client refuses:

* **The job was already removed**, by hand or by the client's own retention setting. Harmless, and the message usually says so ("Job not found", or an unknown ID).
* **The client restarted between the download finishing and the import**, losing the history slot.
* **The API user has no permission** to modify history or the queue. Check the API key or the RPC username's rights.
* **The client is busy repairing or unpacking** something else and declines the edit. It normally succeeds on the next import.

Nothing else is affected. The import completed and the book is in your library either way, and removing a Queue item in Bindery still removes Bindery's own row even when the client refuses to drop its copy. If the warning repeats for every import, the job is genuinely piling up on the client side and the reason in the message is what to fix.

## Grab fails with "not allowed to download NZBs" (newznab error 203) on a Prowlarr-synced indexer

Searching works and the same release downloads fine from inside Prowlarr, but grabbing it in Bindery fails with something like:

```
fetch nzb: indexer refused the download (HTTP 400, newznab error 203:
This application is not allowed to download NZBs from NZBFinder.)
```

Some indexers (NZBFinder is the known case) restrict API access to a whitelist of approved applications, keyed on the client's identity rather than your API key. Prowlarr is on that list; Bindery is not yet. Prowlarr answers Bindery's grab with a redirect straight to the indexer (its per-indexer **Redirect** setting), so the indexer sees Bindery's own identity and rejects the download with error 203. The error message names both hosts when this hand-off happened.

There is **no user-side workaround**:

- Disabling Redirect in Prowlarr is not possible for Usenet indexers — Prowlarr requires it and no longer proxies NZB downloads itself (earlier versions of this page and of Bindery's error text suggested that setting; that advice was wrong, see #1424).
- Adding the indexer to Bindery directly doesn't help either: the whitelist covers the whole newznab API, so searches fail with the same error 203 even with a valid API key (#1404).
- Bindery always identifies itself honestly as `bindery/<version>` and will not impersonate Prowlarr or an arr to get around a whitelist.

**Fix:** the indexer has to add Bindery to its approved applications. For NZBFinder that request is underway (#1425 tracks it) — if you're a member there, asking them too genuinely helps. For other whitelisting indexers, point them at Bindery's stable User-Agent (`bindery/<version>`) and the request pattern (standard newznab caps/search/download on the user's own API key, same as Readarr).

## Grab fails with "SABnzbd rejected download" and the book goes back to Wanted

The grab fails immediately and the book returns to **Wanted**. On Bindery **1.32.0 and earlier** the error named the download client:

```
SABnzbd rejected download
```

while SABnzbd's own log — on the far side of the integration — said the file it was handed wasn't XML at all:

```
Invalid NZB file <release>.nzb, skipping (error: syntax error: line 4, column 0)
```

SABnzbd is right and it is not the culprit. Bindery fetches the NZB from the indexer itself and hands the download client the bytes, so what the client rejected is a response the **indexer** got wrong. Indexers land here by answering a refused, expired or rate-limited grab with an error page under HTTP 200 instead of a 4xx — a 4xx would have been reported against the indexer all along.

The same shape reaches NZBGet; both usenet clients share the fetch path.

**Fix:** upgrade to the current release. Bindery now checks that the fetched body is an NZB before the download client sees it, and reports what actually arrived:

```
fetch nzb: the indexer returned HTTP 200 with a body that is not an NZB:
You have reached your download limit for today.
```

Then act on the snippet — a spent download quota or an expired grab token is the common case, so wait it out or grab from another indexer; an HTML login or challenge page means the indexer session or API key needs attention. On an older release there is nothing to read: SABnzbd purges an invalid NZB before its own backup step, so the bytes are gone by the time you look.

## Deluge's connection test passes but every grab fails

**Test connection** in Settings → Download Clients reports success, and
Deluge's own log shows Bindery's request arriving at the Web UI JSON API:

```
json-request: {"method": "core.add_torrent_magnet", ...}
```

but no torrent is ever added, and the book goes back to **Wanted**. The same
magnet added by hand in the Deluge Web UI works fine.

Deluge runs as two processes. `deluge-web` serves the Web UI and its JSON API;
`deluged` is the daemon that holds the torrents. Logging in authenticates
against `deluge-web` alone, and a Web UI session has to be attached to a daemon
before the Web UI has anywhere to send a `core.` method. Where `deluge-web`
auto connects to a single local daemon on startup this step is invisible. Where
it does not, or where the **Connection Manager** holds more than one host, login
keeps succeeding while every `core.` call fails, which is exactly the pairing of
a passing test and failing grabs (#2204).

**Fix:** upgrade. Bindery now attaches the session to a daemon after logging in,
and the connection test fails with the reason when it cannot, rather than
reporting success.

Two cases still need you, and the error message names which one you are in:

- **No daemon host configured.** Open the Deluge Web UI, add your `deluged`
  under **Connection Manager**, and connect to it.
- **More than one host configured.** Bindery will not guess which daemon you
  meant, because picking the wrong one files downloads somewhere the importer
  never looks. Connect to the one you want in the Web UI and tick its **auto
  connect** box so the session comes back attached on its own.

## Ebook searches on Prowlarr-synced indexers return zero results

Audiobook searches work, indexer tests pass, and running the same query inside Prowlarr returns releases, but every ebook search in Bindery finds nothing. Open **Settings → Indexers** and look at the synced indexer's **Categories**: if it lists `7030` (Books/Comics) and no `7020` (Books/EBook), this is the cause. Every ebook query goes out as `cat=7030`, which is where comics live, not ebooks.

Prowlarr's indexer API does not report a per-indexer category list, so Bindery derives one from the **Sync Categories** of the applications registered in Prowlarr. In 1.32.1 and earlier, any application whose sync categories fell in the Newznab book (7xxx) or audio (3xxx) ranges counted, which meant **Mylar** contributed its comics category `7030` and **Lidarr** contributed the music 3xxx range. A Mylar-shaped scope is the damaging one: it leaves the ebook category list non-empty but wrong, which suppresses both the fallback to the indexer's own capabilities and the search-time `7020` default.

**Fix:** upgrade. Bindery now takes application scopes only from Readarr and LazyLibrarian, the two Prowlarr applications that actually sync books. With neither registered, the indexer's own advertised book and audio categories are used, minus the standard ones that never hold books (3010, 3020, 3040, 3050 and 3060 music, 7010 magazines, 7030 comics). Localised book categories such as 7120 still come through. Bindery also logs a WARN at sync time when an indexer advertises an ebook category that no registered application syncs.

On an older version, the workaround is the per-indexer **Include broad parent categories** toggle, which widens the ebook query to `7000,7030` and does return the 7020 releases at the cost of a broader audiobook query. Editing the indexer's categories by hand does not survive the next sync, which rewrites them.

## "Could not reach the metadata provider" / OpenLibrary timeout

```
metadata provider unavailable: search authors: Get "https://openlibrary.org/..."
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

Bindery waited 15 seconds for OpenLibrary (run by the Internet Archive) and got nothing back. Common causes:

- **VPN or datacenter IP** — the Internet Archive throttles or blocks many shared VPN and hosting IP ranges.
- **OpenLibrary outage** — the Internet Archive has intermittent downtime.

Bindery's primary metadata provider is OpenLibrary, DNB (the German national library), or Hardcover. If OpenLibrary is unreachable for you long-term, switching the primary provider to Hardcover (`Settings → Metadata Profiles → Library Defaults`, requires a Hardcover API token) is a working English-language alternative; otherwise the fix is to make OpenLibrary reachable.

**Fixes:**

- Behind a VPN: split-tunnel `openlibrary.org` out of the VPN. Metadata lookups do not need VPN protection — only torrent traffic does — so a paid dedicated IP is not required. Switching to a different VPN exit location also often helps, since some exit IPs are blocked and others are not.
- Not on a VPN: retry later, and check the status of `openlibrary.org` / `archive.org`.

## A book is on hardcover.app but doesn't show up in the Add to library search

Hardcover does not have to be the primary provider for its titles to show up: it always runs as a **search enricher** too. The Add to library dialog (behind both **Add Author** and **Add Book**) fans the query out to the primary provider **plus** Hardcover (and Google Books, if an API key is set), then merges in any titles the primary didn't return. Books that only exist on hardcover.app are exactly what that path is meant to surface.

The catch is that **Hardcover's GraphQL API requires an API token for every query, including search**: an unauthenticated request returns `{"error":"Unable to verify token"}`. With no token saved, Bindery skips Hardcover before sending anything, so it contributes nothing silently and you only see OpenLibrary / DNB results. Startup says so too: the log reads `hardcover enrichment idle: no api token configured` after the `metadata providers configured` line. Saving a token takes effect on the next lookup, with no restart.

**Fix:** add a Hardcover API token in `Settings → API Keys` (the same token used for [Enhanced Hardcover Series](./Hardcover-Series-Wiki.md) and wishlist features), then re-run the search. Hardcover-only titles should appear in the merged results.

If you want Hardcover to *define* author catalogues rather than only add to them, set it as the primary provider in `Settings → Metadata Profiles → Library Defaults`. The change applies to the next lookup, with no restart. The option stays disabled until a token is saved, and clearing the token is refused while Hardcover is primary, because a tokenless Hardcover primary would fail every lookup. If the token is removed out of band (direct DB edit, restored backup), Bindery logs a warning at startup and falls back to OpenLibrary rather than booting with dead metadata; saving a token again switches the primary back to Hardcover straight away.

If results still don't appear with a token saved, confirm the instance has outbound HTTPS access to `api.hardcover.app` and that the token is valid (a bad token produces the same "Unable to verify token" error, which is logged and skipped).

## Hardcover fails with HTTP 500, or the token test shows a Hardcover error page

```
hardcover search books: HTTP 500 (upstream returned a non-JSON response, likely an error page,
so this is a Hardcover-side failure rather than a token problem)
```

This means `api.hardcover.app` answered with something that wasn't JSON, almost always its own HTML error page. It's an outage on Hardcover's side: no token change, reinstall or setting fixes it. Wait for Hardcover to recover, then press **Test** again. Bindery deliberately doesn't print the page it received, because pasting Hardcover's markup into the Settings UI made an upstream outage read like a bad token (#2128).

The error tells you which side failed:

| Message | Meaning | What to do |
|---|---|---|
| `token rejected (HTTP 401: ...)` | Hardcover read the token and refused it | Reissue the token at [Hardcover API settings](https://hardcover.app/account/api) and save it again |
| `HTTP 400: ...` / `HTTP 403: ...` plus text from Hardcover | Hardcover rejected the request and said why | Act on the quoted reason; the token itself is fine |
| `HTTP 5xx (upstream returned a non-JSON response, likely an error page, ...)` | Hardcover served an error page | Nothing on your side. Retry later |
| `HTTP 5xx (upstream returned an empty response body, ...)` | Nothing came back at all | Usually Hardcover, occasionally a proxy in front of your instance. Check `BINDERY_OUTBOUND_PROXY` if you set one |
| `GraphQL: ...` | Hardcover answered normally but rejected the query | A Bindery bug. Please file an issue with the message |

**On token formats:** Hardcover issues `hc_pat_` personal access tokens alongside the older JWTs, and **both work**. Bindery sends whatever you save as `Authorization: Bearer <token>`, which is the scheme Hardcover accepts for either format, so there is nothing to configure per format. Pasting the whole header is fine too: a leading `Bearer ` or `Authorization: ` is stripped before the token is stored. Verified against the live API on 2026-08-24, a made up `hc_pat_...` value comes back as `401 invalid_token`, so a PAT that fails with 401 is a token to reissue, while a PAT that fails with 500 is an outage to wait out.

## Why does the metadata button say "Link metadata" on some authors and "Find better metadata" on others?

The button is on every author's page. Only its wording changes, and the wording tells you what Bindery thinks it is doing:

- **"Link metadata"** means the author is not linked to a metadata provider yet, or was created from an **Audiobookshelf / Calibre import** (those use `abs:` / `calibre:` foreign IDs). The button attaches a real provider record.
- **"Find better metadata"** means the author is already linked. The button searches the providers for another record to relink to, which is how you move an author whose catalogue came from the wrong record or the wrong provider.

Relinking is valid either way, so an author with a full description, image and ratings still offers it. Before v1.35.0 the button was hidden on authors whose record already looked complete, which left a well described author with a wrong catalogue reachable only through the API.

## Books are filed under the wrong author after a Calibre import

Symptom: a Calibre library imports, and an author you own dozens of books by
has no author page at all. Their titles are on somebody else's page, usually a
co-author or a joint pen name. On an affected version (before v1.32.2) the log
shows lines like:

```
DEBUG calibre import: alias record skipped error="alias \"Isaac Asimov\" already points at author 1125 (refusing to reassign to 105)" name="Isaac Asimov"
```

On a current version the import reports the same situation as it steps over the
bad alias instead of obeying it:

```
INFO calibre import: ignoring untrusted author alias name="Isaac Asimov" aliasAuthor="Janet Asimov" aliasAuthorID=1125
```

The scan then lists those books on **Import → In your library** as by an
author who is not in your library, because the name only exists as an alias.

What happened: for a book credited to several people, the import used to record
every co-author as an *alias* of the first credited author ([#1684](https://github.com/vavallee/bindery/issues/1684)).
An alias means "another name for this same person", so from then on every book
by that co-author resolved back to their collaborator, and they never got an
author row of their own. Which author survived came down to Calibre book id
order, which is why it looked arbitrary. Rolling the import back does not undo
it: aliases are shared entities, so a rollback leaves them in place.

### Fix

Upgrade, then re-import. Co-authors are no longer recorded as aliases, and an
import now ignores an existing alias unless something backs it up, so the real
authors get created on the next run. Nothing is deleted for you, because the
database cannot tell a co-author alias apart from a legitimate one.

To tidy up the leftovers, open the author page the books were wrongly filed
under, find the wrong names in the **Aliases** list, and use the remove button
on each. Books already attached to the wrong author stay there: move them with
**Merge authors** on the Authors page, or edit the book's author directly.

An alias is still honoured when it carries real provenance (an author merge, or
a provider record), when it is simply a spelling variant of the author's own
name, or when it is a latin-script name on a non-latin author (the "Murakami"
to "村上春樹" case). Those keep working exactly as before.

## An author has far fewer books than they should after a refresh

The catalogue sync filters the works the metadata provider returns before creating book rows, so a refresh can legitimately end with far fewer books than the author has written. After the refresh finishes, the author's page shows a note above the book list saying how many works were skipped and by which filter — reload the page if the refresh was still running when you last looked.

The usual culprit is the **allowed languages** list on the author's metadata profile (`Settings → Metadata Profiles`). Two halves of that setting drop books:

- **The language list itself.** A work whose language is outside the list is skipped. Foreign-language editions of an English author are the common case.
- **"When book language is unknown".** OpenLibrary carries no language on many *work* records, so a large tail of an author's catalogue arrives with no language at all. Set to **fail**, every one of those is skipped too — which is what turns "a few translations were dropped" into "most of this author is missing". Setting it to **pass** and refreshing again brings them back.

Two more settings on the same profile drop books by looking at the work's **editions** rather than the work itself:

- **Min pages.** A work is skipped when no edition of it reports a page count at or above the floor. A work whose editions carry no page count at all is treated as unknown and kept, not skipped.
- **Skip missing ISBN.** A work is skipped when no edition of it carries an ISBN. A work the provider returns no editions for counts as missing and is skipped too.

Both read the full edition list. Older versions fetched only the first 50 editions of an OpenLibrary work, in an order OpenLibrary does not sort, so a heavily reprinted title whose ISBN or page count happened to sit further down the list was skipped even though it qualified. If either setting is on and books went missing that way, refresh the author again after upgrading.

The skip counts are also in the log (`Settings → Logs`): the `author books synced` line carries `added`, `matched`, `failed`, `total` and one counter per filter, including `skipped_language`, `skipped_junk`, `skipped_media_type`, `skipped_part_books`, `skipped_missing_date`, `skipped_min_pages` and `skipped_missing_isbn`. It is logged at WARN when a filter or a failed write dropped something, and at Info when the only skips were ones you configured on purpose (`skipped_excluded`, `skipped_not_accepted`). Per-book detail (which title, which language) is at DEBUG.

## Ebooks lost their file extension after Rename files

A template without a dot before `{ext}`, such as `{Title}{ext}`, renames
`Title.epub` to `Titleepub`. Fix the ebook template (Settings → General → File
Naming) to `{Title}.{ext}`, then run Rename files again. Bindery gets the
extension back from the file's rename history or its contents and moves the
file to the corrected name. A file where neither answers is listed as Error and
left alone; rename it by hand or use the `from` path in its Rename history row.

Folders named `Title.` (and `Title. (2)`) come from `{ext}` in the audiobook
folder template, which has no extension to fill in. Remove it; Bindery now
refuses to save that.

## A book shows a file path that no longer exists

Symptom: you moved or renamed a book's file (or the folder holding it), scanned, and the book still shows the old path. It keeps saying **In library**, download and delete act on a file that isn't there, and rescanning changes nothing.

What happens: Bindery tracks files as rows, and registering a file at a new location *adds* a row rather than replacing the old one, so the book ends up owning both. Older versions always rendered the oldest row, so once the old path was dead it stayed on screen ([#2186](https://github.com/vavallee/bindery/issues/2186)).

Fixed (#2186): a book now renders whichever of its tracked files still exists on disk, and a **Scan Library** run also repairs books that were already stuck this way, so on a current version the fix is to scan once. The row for the old location is deliberately *not* deleted. To clear it, open the book, find the dead path in the **Files** list, and use **Forget this file**, which drops the tracking row only and never touches the filesystem.

Two things this does not do:

- It does not find the file for you. Bindery only knows about the new location once a scan (or an import) has registered it, and a scan only matches files to books already in your catalogue (see rule 1 in the [User Guide](User-Guide-Wiki.md#1-library-scan-is-a-reconciler-not-an-importer)). A file moved *outside* your library root is not found at all.
- It does not react to a storage outage. If a mount is temporarily unavailable then every path under it looks missing at once, so Bindery deliberately keeps showing what it showed before rather than acting on the absence. Nothing is deleted or reset, and the books come back as they were when the mount does.

If you reshape your library regularly, **Rename files** on the book or author page is the supported way to do it: it moves the file *and* repoints the same tracking row at the new location, so there is never a second row to clean up.

## "path is outside the configured library roots" or "Bindery cannot read this file"

These come from the Import page and the **Reassign file to another book** window, and they mean different things.

**"Bindery cannot read this file: NAME."** The path sits inside one of your library folders, but Bindery could not open it. The file was moved or renamed, the drive is not mounted, or the account Bindery runs as is not allowed to read the folder. A file that is itself a link to something missing gets the outside roots message below instead, deliberately. Older versions reported this case as "outside the configured library roots" even though the root they listed contained the file ([#3332](https://github.com/vavallee/bindery/discussions/3332)). Check from inside the container, because the path that matters is the one the container sees:

```
docker exec -it bindery ls -l "/books_drive/Audiobooks/Author/Book/file.mp3"
```

The image is minimal and may have no `ls`. If so, check the bind mount instead: the host folder must be mapped to the same path in the container, the drive must be up when the container starts, and `user: "UID:GID"` in Compose must be able to read it. On Docker Desktop with WSL a drive that was unmounted or slept can leave a stale mount, so restart the container after it is back. If the file really was renamed, use **Forget this file** on the book and let a scan register the new name.

**"path is outside the configured library roots: ..."** The path does not resolve under any listed root. That is what you get for a file outside them, for a link inside a library that points out of it, and also for a file that does not exist outside them, and for a link that points at nothing or loops back on itself, because Bindery deliberately answers the same way whether or not anything is there. The message does not mean the file exists. Either add the real folder in Settings, Root Folders, or replace the symlink inside your library with the file itself. A link that points out of a root is refused on purpose.

## A scan leaves files unmatched

Go to **Import → In your library**. Every book the last scan could not match
is listed there, one row per book, with a sentence saying what the scan found
and what to do: add the missing author and scan again, confirm a suggested
book, or choose one. Adopting registers the files where they are and can be
undone. The full walkthrough is in the user guide under [Adopting files
already in your library](User-Guide-Wiki.md#adopting-files-already-in-your-library).

A few things that look wrong but are not:

- **A file you expected is not listed.** Symlinks are never listed, and a file
  whose folder resolves outside your library folders is skipped. Replace the
  link with the file, or add the real folder as a root.
- **The list did not change after the volume came back.** A scan that finds no
  files at all changes nothing on the list, so an unmounted library does not
  erase your decisions. Scan again once it is mounted.
- **A folder ending in a dot, or with other capitals, shows as new on Windows.**
  Windows drops a trailing dot or space from a folder name and keeps an
  existing folder's capitals. Bindery now leaves those off when it names a
  folder, and a Windows install treats paths that differ only that way as the
  same file, so nothing needs rewriting. This applies to Bindery running
  natively on Windows. A Linux host or Docker container reading a Windows share
  still sees the names as different files, macOS case insensitive volumes are
  not folded, and reserved names such as `CON` are untouched.
- **A row you ignored is gone for good.** It is on the **Ignored** list, and
  **Unignore** brings it back.

If you file a bug about a row, include the reason code that shows when you
hover its sentence.

## Collecting logs for a bug report

`Settings → Logs` is the whole log store, so you don't need shell access to the container (rootless images give you nowhere to `cat` a file anyway). Entries age out on the retention window, fourteen days by default, so collect the logs while the problem is recent. The window is the **Log retention** setting on the General tab if you need longer.

Filter down to the problem first — level, component, a search term, and a date range around when it happened — then click **Download** next to *Clear filters*. You get a plain-text file named `bindery-logs-<timestamp>.txt` containing exactly the entries the table was showing, with a header block recording which filters produced it. Attach that to the issue.

Notes:

- One entry per line, `timestamp LEVEL [component] message key=value`, so it stays greppable and pastes cleanly into an issue.
- API keys, tracker passkeys, tokens and signatures that appear in logged URLs, and the token in a Discord, Slack, Telegram, Teams, Home Assistant, Apprise or ntfy.sh webhook URL, are replaced with `REDACTED` on the way out, as are passkeys and download tokens that private trackers put in the URL path and `user:pass@` credentials written into a URL. Still skim the file before posting, since paths and book titles are not redacted.
- An export stops at 50,000 entries and says so in the last line of the file. If you hit that, narrow the level or the date range.
- Admin-only, like the rest of the Logs tab.
- Turn the **Runtime level** up to `DEBUG` before reproducing if the default output isn't enough; entries are persisted, so the download picks them up after the fact.
