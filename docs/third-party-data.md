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

## Audible

`internal/metadata/audible` calls an unpublished Amazon endpoint, and Amazon's
Conditions of Use prohibit unauthorised automated access. This is unresolved and
tracked in [#2015](https://github.com/vavallee/bindery/issues/2015), which
proposes an operator opt-out.
