# File naming templates

Bindery names the files and folders it imports from templates you set. This page covers every token, the brace rules, what happens when a value is missing, and a handful of layouts you can copy.

Every rendered example below was run through Bindery's own renderer, not typed by hand.

## The sample books

The examples use three books by Stephen King:

| Book | Series | Year | Language | ASIN | Genre |
|-|-|-|-|-|-|
| The Gunslinger | The Dark Tower #1 | 1982 | eng | B0EXAMPLE1 (made up) | Fantasy |
| The Stand | none | 1978 | none | none | none |
| The Drawing of the Three | The Dark Tower #2 | none | none | none | none |

The third one has its year stripped on purpose, to show what an empty `{Year}` does.

## Where the settings live

Settings, General, **File Naming**. The section is admin only. There are three templates:

| Setting | Names | Default |
|-|-|-|
| **Book Naming Template** (`naming.bookTemplate`) | the ebook file, with its folders, under the ebook library root | `{Author}/{Title} ({Year})/{Title} - {Author}.{ext}` |
| **Audiobook Folder Template** (`naming_template_audiobook`) | the audiobook folder, under the audiobook root | `{Author}/{Title} ({Year})` |
| **Audiobook file naming (per track)** (`naming.audiobook_file_template`) | each audio file inside that folder | empty, which keeps the download's own file names |

Until you save an ebook or audiobook folder template, the default applies. A saved template applies to the next import straight away, no restart.

The library root itself (which disk or share) is a root folder, set per author or as a default. The template only builds the path below it.

Defaults with the sample books:

```text
{Author}/{Title} ({Year})/{Title} - {Author}.{ext}
  Stephen King/The Gunslinger (1982)/The Gunslinger - Stephen King.epub
  Stephen King/The Stand (1978)/The Stand - Stephen King.epub
  Stephen King/The Drawing of the Three ()/The Drawing of the Three - Stephen King.epub

{Author}/{Title} ({Year})
  Stephen King/The Gunslinger (1982)
  Stephen King/The Stand (1978)
  Stephen King/The Drawing of the Three ()
```

Note the `()` on the book with no year. The defaults keep that for compatibility; the recipes below use `{ (Year)}` instead, which drops cleanly.

## Tokens

Tokens are case sensitive. `{author}` is not `{Author}`.

| Token | Renders | Empty when |
|-|-|-|
| `{Author}` | the author's name, `Stephen King` | never; a book with no author gets `Unknown Author` |
| `{SortAuthor}` | the sort form, `King, Stephen`. Handles particles and suffixes: `Le Guin, Ursula K.`, `King, Martin Luther Jr.`. A one word name stays as it is | never |
| `{Title}` | the book title | never in practice |
| `{Year}` | the release year, `1982` | the book has no release date |
| `{Series}` | the book's primary series, `The Dark Tower` | the book is in no series |
| `{SeriesNumber}` | its position in that series, as stored: `1`, `10`, `1.5` | no series, or a series with no position |
| `{Lang}` | the book's language as a three letter ISO 639 code, `eng`, `ger`, `fre` | the language is unknown |
| `{ASIN}` | the Audible ASIN | the book has none |
| `{Genre}` | the first genre on the book, best with Hardcover enabled | the book has no genres |
| `{Narrator}` | the audiobook narrator(s) | none recorded |
| `{ext}` | the file extension without the dot, `epub` | always empty in the audiobook folder template |
| `{Part}` | the track's position in playback order, `1`, `2`, `3` | only exists in the per track audiobook template |

There is no `{ISBN}` token. Anything Bindery does not recognise, like `{ISBN}` or a typo such as `{Titel}`, is written into the name literally, braces and all. The ebook and audiobook folder fields flag unknown tokens and will not save them. That check lives in the page only: the API accepts the template, and the per track field has no check at all.

A book in several series uses the one marked primary for `{Series}` and `{SeriesNumber}`.

## Braces

### Simple tokens

`{Title}` is replaced by the value. Text outside the braces is always written as it is:

```text
{Title} ({Year})
  The Gunslinger (1982)
  The Stand (1978)
  The Drawing of the Three ()
```

### Defaults: `{Token:text}`

Text after a colon is used when the token is empty:

```text
{Genre:Unsorted}/{Author}/{Title}
  Fantasy/Stephen King/The Gunslinger
  Unsorted/Stephen King/The Stand

{Series:Standalone}/{Title}
  The Dark Tower/The Gunslinger
  Standalone/The Stand
```

`{Year:2024}` is a default too, because four digits is not a width (next section). The Drawing of the Three, with no year, gets `2024`.

### Zero padding: `{Token:N}`

One or two digits after the colon pad a number with zeros, so `2` sorts before `10`:

| Value | `{SeriesNumber:2}` | `{SeriesNumber:3}` |
|-|-|-|
| `1` | `01` | `001` |
| `10` | `10` | `010` |
| `100` | `100` | `100` |
| `1.5` | `1.5` | `1.5` |
| `Prequel` | `Prequel` | `Prequel` |

Only whole numbers are padded. Anything else is left alone.

### Conditional groups: text inside the braces

Put literal text inside the braces, next to the token, and it only shows up when the token has a value. When the token is empty the whole group disappears, text included. This is how you avoid stray dashes and brackets:

```text
{Title}{ - Series}
  The Gunslinger - The Dark Tower
  The Stand

{Title}{ (Year)}
  The Gunslinger (1982)
  The Drawing of the Three

{(SeriesNumber:3) - }{Title}
  (001) - The Gunslinger
  The Stand

{Vol SeriesNumber - }{Title}
  Vol 1 - The Gunslinger
  The Stand

{Title}{ (Series #SeriesNumber)}
  The Gunslinger (The Dark Tower #1)
  The Stand
```

Widths work inside a group (`{SeriesNumber:3 - }` gives `001 - `). Text defaults do not: the colon becomes a `-` and the default text is written as literal text, so `{Title}{ - Series:Standalone}` gives `The Gunslinger - The Dark Tower-Standalone` and plain `The Stand` for the standalone.

**Several tokens in one group.** The group shows when at least one of its tokens has a value. Tokens that are empty just render nothing, and the literal text stays. So `{ (Series #SeriesNumber)}` on a book with a series but no position still shows the brackets and the `#`.

**Watch the words you use.** Inside a group, any word that is a token name is treated as the token. `{Book Title}` renders `Book The Gunslinger`, which is fine, but a literal word like `Title`, `Series` or `Part` inside the braces gets swapped for its value. In the per track template, write `{ - Pt. Part:3}`, not `{ - Part Part:3}`.

**Do not start the literal text with a colon.** `{Series: }` is read as "Series, with a default of a space", not as a group, so it renders `The Dark TowerThe Gunslinger`. Use `{Series - }` or similar.

### Without braces

If a token at the start of a name renders empty, the separator right after it is dropped, so you do not get a name starting with ` - `:

```text
{SeriesNumber} - {Title}
  1 - The Gunslinger
  The Stand
```

That only works at the start, and only when there is no literal text in front. `Vol {SeriesNumber} - {Title}` gives `Vol  - The Stand` for a standalone. Separators in the middle or at the end always stay, which is where `The Drawing of the Three ()` comes from. Conditional groups are the reliable way to do this.

## Folders: `/` and empty levels

Each `/` in a template starts a new folder. Always use `/`, on every platform, Windows included. Bindery turns it into the right separator when it writes the file.

A level that renders empty is dropped instead of leaving an empty folder name. That answers the question in [Discussion #1311](https://github.com/vavallee/bindery/discussions/1311): you do not need to put the slash inside the token. Keep it outside and the whole level goes away when there is nothing in it.

Audiobook folder template from that thread, with `{ (Year)}` swapped in so the yearless book stays clean:

```text
{Author}/{Series}/{(SeriesNumber:3) - }{Title}{ (Year)}{ [ASIN]}
  Stephen King/The Dark Tower/(001) - The Gunslinger (1982) [B0EXAMPLE1]
  Stephen King/The Stand (1978)
  Stephen King/The Dark Tower/(002) - The Drawing of the Three
```

Ebook version with a language level. The thread had a literal `.ext` at the end; it has to be `.{ext}`, so that is changed here too:

```text
{Author}/{Series}/{Lang}/{(SeriesNumber:3) - }{Title}{ (Year)}{ [ASIN]}.{ext}
  Stephen King/The Dark Tower/eng/(001) - The Gunslinger (1982) [B0EXAMPLE1].epub
  Stephen King/The Stand (1978).epub
  Stephen King/The Dark Tower/(002) - The Drawing of the Three.epub
```

The Stand has no series and no language, so both levels vanish and the file sits right in the author folder. The Drawing of the Three keeps its series folder and loses only the language folder.

That loose file in the author folder is worth thinking about. Calibre's one book per folder add mode expects each book in a folder of its own, and when Bindery's library scan reads a file with no book folder above it, it has only the filename to match on. If either matters to you, end the template with a per book folder, like the series recipe below does.

Values never add folders. A `/` inside a title or author becomes `-`, so `AC/DC: A Life` is one name, `AC-DC - A Life`.

## Cleaning up names

Bindery cleans every token value before it goes into a name:

- `Title: Subtitle` becomes `Title - Subtitle` (also `Title : Subtitle`).
- `/`, `\` and any other `:` become `-`.
- `*`, `?`, `"`, `<`, `>` and `|` are removed: `Who Goes There?` becomes `Who Goes There`.
- Control characters are removed and spaces at either end are trimmed.

Literal text inside a conditional group gets the same character replacement, without the trimming, so the spaces you put there stay. Literal text outside braces is written exactly as you typed it.

**Length.** Each token value is capped at 200 bytes. Each finished folder or file name is capped at 214 bytes, which leaves room inside the usual 255 byte filesystem limit for the temporary name Bindery stages a file under and for a ` (2)` added on a clash. When a name is too long, the longest values are shortened first. The literal text and the extension are kept. A 480 character title in the default ebook file name came out as 214 bytes, still ending in `- Stephen King.epub`. Bytes, not characters: accented and non Latin titles hit the cap sooner.

**Windows.** When Bindery itself runs on Windows, it also drops trailing dots and spaces from every folder and file name, since Windows would strip them anyway. `H.R.H.` becomes `H.R.H`. A name that is only dots becomes `_`. On Linux and macOS names are left as they are. Bindery in Docker counts as Linux, even with the library on a Windows share.

## Recipes

### Author and book folders (Readarr style)

```text
{Author}/{Title}/{Title} - {Author}.{ext}
  Stephen King/The Gunslinger/The Gunslinger - Stephen King.epub
  Stephen King/The Stand/The Stand - Stephen King.epub
```

Or the default with a year that drops cleanly:

```text
{Author}/{Title}{ (Year)}/{Title} - {Author}.{ext}
  Stephen King/The Gunslinger (1982)/The Gunslinger - Stephen King.epub
  Stephen King/The Stand (1978)/The Stand - Stephen King.epub
  Stephen King/The Drawing of the Three/The Drawing of the Three - Stephen King.epub
```

Swap `{Author}` for `{SortAuthor}` for surname folders: `King, Stephen/The Gunslinger (1982)/The Gunslinger - Stephen King.epub`.

### Series folders

```text
{Author}/{Series}/{SeriesNumber:2 - }{Title}{ (Year)}/{Title} - {Author}.{ext}
  Stephen King/The Dark Tower/01 - The Gunslinger (1982)/The Gunslinger - Stephen King.epub
  Stephen King/The Stand (1978)/The Stand - Stephen King.epub
  Stephen King/The Dark Tower/02 - The Drawing of the Three/The Drawing of the Three - Stephen King.epub
```

### Language folders

```text
{Lang:und}/{Author}/{Title}{ (Year)}/{Title} - {Author}.{ext}
  eng/Stephen King/The Gunslinger (1982)/The Gunslinger - Stephen King.epub
  und/Stephen King/The Stand (1978)/The Stand - Stephen King.epub
```

`{Lang}` gives the three letter code (`eng`, not `en`). Without the `:und` default, books with no known language skip that level and land at the top next to your language folders, which is rarely what you want. `und` is the ISO code for "undetermined"; any word works.

### Audiobookshelf

Audiobookshelf reads `Author/Series/Vol N - Title` and an optional year before the title. Set this as the **Audiobook Folder Template**:

```text
{Author}/{Series}/{Vol SeriesNumber - }{Year - }{Title}
  Stephen King/The Dark Tower/Vol 1 - 1982 - The Gunslinger
  Stephen King/1978 - The Stand
  Stephen King/The Dark Tower/Vol 2 - The Drawing of the Three
```

Leave `{ext}` out: this names a folder. Audiobookshelf can also read a narrator wrapped in curly braces, but braces are template syntax here, and `{{Narrator}}` leaves an empty `{}` on books with no narrator, so it is not worth it.

### Calibre

If Calibre owns your library, let Calibre name the files. Hand books over with `calibredb`, the Bridge plugin or a drop folder ([Calibre integration](Calibre-Integration-Wiki.md)) and the template here only shapes Bindery's own copy.

If Bindery owns the library and you add it to Calibre with its add from folders option set to one book per folder, any layout with one book per folder works, and this one matches Calibre's own `Author/Title/Title - Author` shape:

```text
{Author}/{Title}/{Title} - {Author}.{ext}
  Stephen King/The Gunslinger/The Gunslinger - Stephen King.epub
```

### Audiobook tracks

The per track template is off until you fill it in. It needs a `{Part}`. `{ext}` here is the track's own extension.

```text
{Title} - Part {Part:3}.{ext}
  track 3 of a folder:     The Gunslinger - Part 003.mp3
  a single file audiobook: The Gunslinger - Part 001.m4b

{Title}{ - Pt. Part:3}.{ext}
  track 3 of a folder:     The Gunslinger - Pt. 003.mp3
  a single file audiobook: The Gunslinger.m4b
```

A bare `{Part}` numbers a lone file as part 1. Put it in a conditional group and a single file audiobook gets no part number at all.

### Drop folder (External import mode)

With Import Mode set to External and a drop folder, **Layout** decides the names:

- **Flat** (the default) ignores your templates and puts a fixed name in the folder root: `The Gunslinger - Stephen King.epub`, or a folder `The Gunslinger - Stephen King` for an audiobook.
- **Templated** builds your ebook and audiobook folder templates inside the drop folder.

Audiobooks can have their own import mode, and with External they can use their own **Audiobook drop folder** (`import.audiobook.drop_folder`), falling back to the ebook one when that is empty. Layout applies to both formats.

## Common mistakes

**Backslashes.** `{Author}\{Title}` is not two folders. On Linux and in Docker, the backslash is kept in the name and you get one folder called `Stephen King\The Gunslinger`. Use `/`.

**Two tokens with nothing between them.** `{Author}{Title}` gives `Stephen KingThe Gunslinger`, one folder. You need the `/`.

**A slash inside the braces.** `{/Series}` does not work. The template is split on `/` before braces are read, so `{Author}{/Series}/{Title}` makes a folder named `Stephen King{` with a folder named `Series}` inside it. Keep the slash outside. Empty levels drop on their own.

**`.ext` instead of `.{ext}`.** `{Title}.ext` names every file `The Gunslinger.ext`, whatever the format. Bindery accepts that, because `.ext` looks like a real extension, so nothing warns you. Write `.{ext}`.

**No dot before `{ext}`.** `{Title}{ext}` would give `The Gunslingerepub`. The settings page warns, and the import refuses to place a file with no extension.

**`{ext}` in the audiobook folder template.** It is always empty there, so `{Author}/{Title}.{ext}` makes a folder ending in a dot. Saving it is refused.

**No `{ext}` in the ebook template.** `{Author}/{Title}` has no extension and is refused at import time.

## Preview and applying changes

The ebook and audiobook folder fields have a **Preview** line that updates as you type. The per track field is a plain text box with no preview and no checks. The preview renders a fixed sample (Jane Doe, *Sample Book*, Demo Series #2, 2024), not one of your books, and the sample has every value filled in. So it shows your structure, but not what happens to a standalone or a book with no year. Use the examples on this page for that. It also does not apply the length cap or the Windows clean up. The sample's language is `en`, so a `{Lang}` level previews as `en` while your books render `eng`.

Changing a template only affects new imports. To move files you already have, use **Rename files** (the reorganize tool):

| Scope | Where |
|-|-|
| One book | the book page, **More**, Rename files |
| One author | the author page, **More**, Rename files |
| Whole library | Settings, General, Library, **Reorganize Library** |

All three are admin only. They show a preview first (Will move, Already correct, Destination exists, Not on disk, Error) and nothing moves until you confirm. Ebooks are renamed and moved, audiobook folders are moved as a whole, and a single file audiobook is also renamed by the per track template. Run **Scan library** first, since only files already attached to a book can be moved. More in the [User guide](User-Guide-Wiki.md#restyling-files-you-already-have).
