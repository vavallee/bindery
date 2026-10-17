# Third-party data terms

Bindery pulls metadata from several public APIs and stores it in your SQLite
database. This page records the usage terms that constrain what a *deployment*
may do with that data, as distinct from
[dependency licences](../CONTRIBUTING.md#dependency-licences), which constrain
what the *binary* may link.

Nothing here restricts ordinary self-hosted use. It matters if you run Bindery
as part of something you charge for, incorporate, or run ads against.

This page does not yet cover Google Books, Audnex or the DNB catalogue, which
Bindery also queries. Their terms have not been reviewed.

## Hardcover

Source: <https://hardcover.app/pages/policies> (section "API Rules"), plus the
rate limits in <https://docs.hardcover.app/api/getting-started/>. Reviewed
2026-08-14.

Hardcover asserts no new copyright over the database material and states that
it carries "the same licensing as OpenLibrary". There is **no attribution
requirement**, no retention limit, and no expiry clause, so caching Hardcover
responses in the local database indefinitely is fine. Bindery's list sync runs
every 1 to 168 hours, well inside the documented 60 requests per minute.

The API Rules draw one line that matters, between a *personal project* and a
*professional* one (charged for, incorporated, or ad-supported):

- **Personal projects** may use any data from the API.
- **Professional projects** may use only your own personal data and *facts*
  about books, editions, and series. Other users' reviews, ratings, lists, and
  user-generated content are excluded.

Two consequences for anyone running Bindery professionally:

**Aggregated ratings must be excluded.** `books.average_rating` and
`books.ratings_count` are filled by the metadata aggregator's enrichment merge
(`internal/metadata/aggregator_enrichment.go`), which takes Hardcover's
`rating` and `ratings_count` alongside OpenLibrary's. The Hardcover derived
share is other users' ratings, not facts about the book. Strip them from any commercial deployment: don't display
them, don't serve them over the API, and don't re-sync them. Title, author,
series, edition, publisher, narrator, description, and cover are facts and are
fine. The setting that controls the sync cadence is `hardcover.sync_interval`.

**Cover images must be self-hosted.** The API Rules permit hot-linking
Hardcover's images from a personal project but prohibit it for a professional
one: "download those images and host them on your own." Bindery does this on
both surfaces: the web UI routes covers through `/api/v1/images` and the OPDS
feed through `/opds/images`, which are the same handler and the same
`<dataDir>/image-cache/`. Reading apps and browsers alike fetch covers from
your instance, and Hardcover is contacted once per cover per 30-day cache
entry.

**Hardcover can be the primary provider, not only an enricher.** Setting
`metadata.primary_provider` to `hardcover` (the default is `openlibrary`) makes
Hardcover the source that *defines* an author's catalogue rather than one that
supplements OpenLibrary's. This does not move the line drawn above: titles,
authors, series, editions, publishers, narrators, descriptions, and covers are
facts about books either way, and facts are permitted for personal and
professional projects alike. What changes is proportion. In primary mode
substantially more of a library is Hardcover-derived, so for a professional
deployment the two obligations above stop being marginal and start covering
most of the catalogue — in particular the aggregated-ratings exclusion, because
`average_rating` and `ratings_count` arrive on the same records as the facts
that are fine to keep.

> The Hardcover Terms of Service page is unedited template boilerplate whose
> governing-law, arbitration, and liability clauses are literal blanks, and
> whose §5 read literally would prohibit the API use its own documentation
> describes. The API Rules section of the policies page is the operative
> document.

## OpenLibrary

Public domain catalogue data (CC0). Cover images come from
`covers.openlibrary.org` and are subject to their rate limits; Bindery serves
them through the same `/api/v1/images` cache.

## Nasjonalbiblioteket

Sources: <https://api.nb.no/catalog/v1/items/api-docs> (the catalogue search
API) and <https://authority.bibsys.no> (the Norwegian authority file). Reviewed
2026-10-04. Only contacted when Nasjonalbiblioteket is the primary provider.

- **The delivered records are CC0; the search API states no licence.** The
  National Library publishes its catalogue records under CC0 through its
  metadata delivery over OAI-PMH and SRU
  (<https://doc.aja.bs.no/hente/bibliografiske-data/mlnb.html>). Bindery does
  not use that delivery: it reads the catalogue search API, which publishes no
  licence, terms or rate limits of its own. What Bindery stores from it is the
  same bibliographic data (titles, authors, ISBNs, years, languages, series,
  genres, audiobook narrators and running times). Bindery keeps NB opt in so
  installs that do not choose it send no traffic.
- **Summaries are publisher copy.** A record's summary is usually the
  publisher's own description of the book, not the library's cataloguing, so
  CC0 does not reach it. Bindery stores it as the book description, the way it
  stores descriptions from the other providers.
- **Covers are excluded.** NB's image service refuses in-copyright books, and
  the cover images some records link to are licensed to library catalogues
  only. Bindery fetches no cover from NB or from those links.
- **The authority file is Sikt's, under NLOD 2.0.** Bindery reads an author's
  name heading from it to look up their catalogue, and the name's other forms,
  which may become aliases. NLOD asks for attribution when the data is
  redistributed, for example published as a dataset:

  > Contains data under the Norwegian licence for Open Government data (NLOD)
  > distributed by Sikt.

  A self-hosted install that shows the names to its own users does not
  redistribute them.

## Bibliothèque nationale de France

Sources: the BnF catalogue's general terms of use, sections 2 and 4
(<https://catalogue.bnf.fr/conditions.do>), BnF's page on reusing its data
(<https://www.bnf.fr/fr/conditions-de-reutilisations-des-donnees-de-la-bnf>),
the data.bnf.fr licence page (<https://data.bnf.fr/licence>), the Licence
Ouverte / Open Licence 2.0 text
(<https://github.com/etalab/licence-ouverte/blob/master/LO.md>), and the SRU
service description
(<https://catalogue.bnf.fr/api/SRU?version=1.2&operation=explain>). Reviewed
2026-10-11. Only contacted when BnF is the primary provider: an install that
does not choose it sends BnF no request at all, not even at startup.

- **The catalogue records are under the Licence Ouverte 2.0 (Etalab).** It
  allows reuse, including commercial reuse, modification and redistribution,
  with no copyleft, and is compatible with CC BY. Its condition applies to
  every reuse, showing the data in a self-hosted app included: name the
  source (at least "Bibliothèque nationale de France") and the date of the
  data. Bindery meets it with the credit and the date together:
  - Settings, Metadata, where BnF is chosen as the primary provider, carries
    the credit: "Contains data from the Bibliothèque nationale de France
    (BnF) under the Licence Ouverte / Open Licence 2.0, with the date each
    record was retrieved shown on its book and author pages."
  - A BnF book's Metadata source section and a BnF author's page show "Data
    from the BnF, Licence Ouverte / Open Licence 2.0, retrieved <date>".

  The date is the row's own, already stored: `last_metadata_refresh_at`
  when its metadata was last fetched from its provider (an author refresh
  stamps the author, a rebind stamps the book), else `created_at`, when it
  was added. Bindery keeps no fetch time per field, so a book's date is when
  its record was added or rebound, not when an author refresh last read it. A
  book's ID is the record's ARK (`bnf:cb48780493c` is
  <https://catalogue.bnf.fr/ark:/12148/cb48780493c>), so the source record of
  every BnF book can be found, and the Links menu opens it. Anyone who
  publishes the data further, for example as a dataset or on a public site,
  has to keep the credit line with the date as well.
- **No endorsement, nothing misattributed.** The licence forbids suggesting
  that BnF endorses the reuse, and BnF does not endorse Bindery. Values Bindery
  derives itself (how editions are grouped into works, which edition names the
  work, ratings or series from other providers) are Bindery's, not BnF's.
- **Only book records are used.** The terms carve out the third party records
  for music audio recordings, and the Dewey classification authority records,
  from the open licence. Bindery searches only bibliographic records of
  document type "printed text" (`bib.doctype any "a"`), drops any record whose
  own type says it is a sound recording, music, an image or a map, and asks the
  authority file only for persons (`aut.type any "pep"`). It never queries the
  Dewey or subject authorities and does not read subject or classification
  fields.
- **Covers are excluded.** Bindery fetches no image from BnF, data.bnf.fr or
  Gallica, and ignores the cover links some records carry: Gallica has its own
  terms, images reused on Wikimedia Commons carry each image's own licence, and
  no terms were found for BnF's cover image service. BnF books get covers only
  from the other providers.
- **Summaries are not claimed as open data.** A record's summary is often the
  publisher's text, or a commercial database's (Electre), not BnF's
  cataloguing, so the open licence may not reach it. Bindery stores a summary
  only as the book description, the way it does for the other providers, and
  only when the record names the publisher (330 $2 "éditeur") or gives no
  source at all; a summary attributed to any other third party is skipped.
  Keeping an unsourced summary is a judgment call: a 330 with no $2 is read
  as the cataloguer's own text, which may be wrong for some older records,
  and Bindery does not claim it as open data either way.
- **Records the package redistributes.** The test fixtures in
  `internal/metadata/bnf/testdata` are real BnF responses, credited in their
  `SOURCE.md`, with third party summaries replaced and every 856 field
  (cover image references and links to other catalogues) removed.
- **Names are personal data.** Authors and translators are people, and their
  names and dates come from BnF's authority file. BnF gives the people named
  in its catalogue and data.bnf.fr rights of access and correction, exercised
  with BnF. Bindery stores the names in the install's own database, like every
  other provider's, and sends them nowhere.
- **No published rate limit.** BnF publishes none, so Bindery paces itself: at
  most one request a second and two in flight for the whole process, a
  `Retry-After` is honoured, and a refusal holds every BnF request. See
  `internal/metadata/bnf/pacer.go`. What that costs: a search is 1 to 3
  requests; opening or adding one book is up to 4 (the record, up to 2 name
  lookups, the rest of its work); an author refresh is up to 15 (the
  author's record for the profile, then for the catalogue the authority
  record again, up to 10 pages of 100, up to 2 pages of records not yet
  linked and 1 namesake check), so about 10 to 15 seconds.
  The page count and ISBN filters judge BnF works on the editions the
  catalogue already returned, so they add no request.

## Wikidata, Wikimedia Commons and Wikipedia

Sources: <https://www.wikidata.org/wiki/Wikidata:Licensing>, the Wikimedia
Foundation User-Agent Policy
(<https://foundation.wikimedia.org/wiki/Policy:Wikimedia_Foundation_User-Agent_Policy>)
and API Usage Guidelines
(<https://foundation.wikimedia.org/wiki/Policy:Wikimedia_Foundation_API_Usage_Guidelines>,
version 1.0 of 2024-08-26). Reviewed 2026-10-15. Only contacted while
`metadata.author_enrichment.wikidata` is on (#3287): an install that has not
switched it on sends Wikimedia no request.

The author enricher in `internal/metadata/wikidata` looks an author up on
Wikidata by an authority ID Bindery already stores (NORAF for `nb:` authors,
GND, BnF, OpenLibrary), never by name, and fills a photo or bio the author's
primary provider left empty. Three sources, three sets of terms:

- **Wikidata's structured data is CC0.** The item lookup, its identifiers and
  the image file name need no credit.
- **Wikipedia text is CC BY-SA 4.0.** The bio is the article's lead summary.
  The author page credits it with "Bio from Wikipedia, CC BY-SA 4.0", linking
  to the article it came from. The text is stored as the author description,
  the way descriptions from the other providers are.
- **Commons photos are licensed per file** (CC BY, CC BY-SA, public domain and
  others). The enricher reads each file's author and licence from Commons and
  stores them with the image URL; the author page shows "Photo: <author>,
  <licence>, via Wikimedia Commons", linking to the file's page. A file whose
  licence Commons does not state is not used, because it cannot be credited.
  Photos are served through `/api/v1/images` like covers, so a browser never
  hot-links Commons.

The API Usage Guidelines ask an operator to identify their client, to obey
rate limiting replies, and not to send concurrent or bursty traffic. Bindery
sends its own User-Agent (`bindery/<version>`, with the project URL as the
contact; the guidelines' "operator" is whoever runs the install), honours
`Retry-After` on a 429 or 503 and then holds back (15 minutes when no
`Retry-After` is given), and asks one author at a time on the staggered
author refresh (#3275), so a library is spread over the day. Answers, a miss
included, are kept for 30 days, so an author with no photo is not asked about
every day.

## Audible

`internal/metadata/audible` calls an unpublished Amazon endpoint, and Amazon's
Conditions of Use prohibit unauthorised automated access. This is unresolved and
tracked in [#2015](https://github.com/vavallee/bindery/issues/2015), which
proposes an operator opt-out.
