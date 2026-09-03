# Changelog

All notable changes to Bindery are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com) and versions follow
[Semantic Versioning](https://semver.org).

## [v1.33.3] — 2026-09-03

**Six fixes for things Bindery was getting wrong without ever saying so.**

Every entry here is a silence, not an error message. A grab that worked reported as
failed. Books you excluded quietly coming back. An ebook that no quality profile
would ever allow, on a path with no human watching. A track disappearing from a
multi-file audiobook. The one thread running through most of them is a check
answering a question it was never asked, or an absence being read as an answer.

### Fixed

- **Grabs against rdt-client and other qBittorrent API emulators are no longer all marked failed** (#2304). Those clients answer `POST /torrents/add` with HTTP 200 and no body, where qBittorrent itself writes `Ok.`. Bindery read the empty body as a rejection and failed every grab with `add torrent failed:` and no message, while the torrent had in fact been accepted, downloaded to completion, and then sat there unimported. An empty body on a 200 is now an accept; a non-empty rejection body such as `Fails.` still fails the grab, and a response Bindery could not finish reading now fails loudly instead of being mistaken for an empty one. Thanks to Gamegenie13 for the report.
- **A quality profile no longer blocks the format it was never asked about** (#2307). An author gets one quality profile, so if you built yours around audiobook containers (m4b, mp3, flac) then every ebook release for that author was rejected as "format not in quality profile", and the reverse for an ebook profile and audiobook releases. On the Search page that only showed as a misleading warning, since Grab stayed enabled. On the automatic path it was silent and much worse: the book could never be auto-grabbed at all, and the same check runs at import, so a file in the other format would not import either. A release is now judged only against the profile entries of its own media type, and passes when the profile lists none of them. Within a media type the profile does describe, nothing changes: a format you unticked stays rejected, and a profile that deliberately lists both behaves exactly as before. A file whose content belongs to the other media type is still kept out of the wrong slot, which the profile had been doing by accident. Thanks to clebcleb for the report and to KadenTheHero for working out that the profile has no media type to reason with.
- **Series fill no longer re-acquires the books you excluded** (#2302). Every series query returned excluded books, so filling a series marked them wanted and searched for them again, which is the opposite of what excluding a book means, and applying genres to a series wrote them onto excluded books too. Series listings now skip excluded books the way the author listings already did. The one place that still counts them is an import rollback deciding whether a series is now empty, because an excluded book still occupies the series. Thanks to hoxtonia for the report and to thejdubb02 for the fix.
- **An audiobook no longer imports into a "Title (2)" folder when the ebook is already there** (#1959). Under the shared-folder layout, where `BINDERY_AUDIOBOOK_DIR` is unset so both formats resolve to the same folder, importing a book's audiobook after its ebook created a sibling duplicate instead of joining it. It now merges into the book's existing folder. A same-named file already there, cover art carried by both formats being the usual case, is skipped rather than overwritten, and the skipped names are recorded on the import's History entry and the `bookImported` notification. Libraries using the per-file audiobook naming template or multi-disc flattening still split, because those paths own the folder they create and merging them safely needs their rollback reworked first. Thanks to Daize for the request and to schmitzkr for the fix.
- **A "Books 1-4" pack is no longer auto-grabbed for a single book** (#2276). An automatic search for one book could select an explicit multi-book release, and since a download is linked to exactly one book, the importer then tried to place every file in the pack into that one book's folder. Automatic selection now skips releases that name themselves as a pack: a numbered range like `Books 1-4`, a box set, an omnibus, a "complete series". They still appear in interactive search so you can see and grab one deliberately, and if you do, the import is blocked with an explanation instead of running. **Queue, Manual import** is the way to take a pack. The wording is judged narrowly: `Part 1-2` is how a single long audiobook is split and `Trilogy` is how single books name their series, so neither counts, and if the book you track is itself a bundle then matching packs are still allowed for it. Thanks to magrhino for the report.
- **A multi-file audiobook could lose a track, silently, when two of its files shared a filename** (#2275). Downloads whose book files share no folder below the download root are placed one file at a time and flattened into the book's folder by filename, so two tracks from different source folders with the same name both claimed the same destination. In `hardlink` mode the second failed with "file exists" and left a half-imported folder the book never recorded; in `copy` and `move` mode it silently replaced the first, and in `move` that track's contents were gone for good. Bindery now projects every destination name before it creates anything and blocks the import, naming both source files and the path they share, with nothing written and both files left in the download folder. The drop-folder handoff had the same flatten and is covered by the same check. A placement error that cannot be predicted, a full disk or a revoked permission, now also undoes what it placed instead of leaving a partial folder that made the next retry build a second one. Hardlink failures also stopped blaming your filesystem layout for unrelated errors: every failed `os.Link` that was not a cross-device error reported "download dir and library must be on the same filesystem", so a destination collision on a single dataset sent you to check mounts that were fine. A missing source, a permissions problem and an existing destination now each say what they are. Thanks to magrhino for the report.

## [v1.33.2] — 2026-08-28

**A throttled Hardcover call was permanently moving your authors onto the wrong metadata provider.**

Four fixes for faults reported the day after v1.33.1. Two of them are the same
shape: something transient produced something permanent, quietly. A rate limit
that lasts one second decided which provider an author syncs from forever, and
a release-name filter that only knew the English names of languages let foreign
audiobooks through to be downloaded and imported under English books.

### Fixed

- **A rate-limited metadata provider no longer downgrades an author permanently** (#2271). With `metadata.primary_provider` set to Hardcover, a single HTTP 429 during an import left the author linked to OpenLibrary, and because the catalogue provider is read back off that stored link, the author then synced from OpenLibrary forever. One 72-item Audiobookshelf import put 38 of 42 authors in that state with nothing reported. The reason re-importing never repaired it is that an OpenLibrary identity is not replaceable, while the `abs:` or `calibre:` identity the author had before is, so the wrong link was the one state the import could not climb back out of. The Audiobookshelf import, the Calibre relink and the automatic author relink now refuse to write a provider link when the configured primary dropped out of the search and the only candidates came from a fallback. The author keeps the identity it had, the Audiobookshelf import summary says so per author, and the relink endpoint answers 503 instead of binding. A primary that answers and simply has no such record is unchanged: that link is a real answer and is still written. Thanks to jmerhar for the report and the log data.
- **Hardcover requests are paced and retried instead of storming its rate limit** (#2075). The Hardcover client sent one request and returned whatever came back, so a burst of parallel lookups had most of them refused: one 72-item import produced 86 rate-limit rejections across four call sites, and the "Try again in 1 seconds" the response itself carried was never read. Requests now pace themselves and a refused request is retried, honouring Hardcover's own hint. The pacing costs nothing until Hardcover actually refuses something, hardens if it keeps refusing, and relaxes again after a run of successes, so a paid-tier token is never slowed for a limit it does not have. Thanks to jmerhar for confirming this was still live and for isolating it to a configured, working token.
- **Foreign-language audiobooks are no longer auto-grabbed under an English-only profile** (#2273). The release-name language filter only recognised the English names of languages, so a scene release named in its own language carried no tag at all and passed `allowedLanguages: ["eng"]` untouched. `Luisterboek ... (NL Audiobook)` and `... (Ungekürzt)` were approved and, on the auto-grab path, downloaded and imported under English book entries. The filter now also recognises the word a release of that language actually uses: `Hörbuch`, `Ungekürzt`, `Gekürzt`, `Luisterboek`, `Hoorspel`, `Nederlands`, `Ljudbok`, `Svensk`, `Lydbok`, `Norsk`, `Lydbog`, `Dansk`, `Audiolivro` and `Livre Audio`. Untagged releases still always pass, which is deliberate. Thanks to 4pBdhJoZ3Xy3reVvBoU9C3YPzyXDDU for the report, the traces and a patch that was right about the mechanism.
- **Nine languages the filter could drop can now be allowed** (#2273). The metadata profile editor offered ten languages while the filter recognised nineteen, so a release tagged Korean, Arabic, Swedish, Norwegian, Danish, Polish, Czech, Turkish or Hindi was dropped by a profile that had no way to permit it. All nineteen are now in the editor, and a test fails the build if the two lists drift apart again.

### Security

- **The backup screen and the deployment docs now say what a backup actually contains** (#2269). Every credential Bindery holds is stored in the database in plain text, and a backup is a whole-database copy, so a backup file carries indexer and Prowlarr API keys, download client passwords, OIDC client secrets, the session signing secret, the Bindery API key and every external service token in the clear. Nothing said so, and the realistic way people get hurt by this is attaching a backup to a bug report or syncing one offsite without knowing what is in it. `docs/DEPLOYMENT.md` now also records why encrypting at rest with a key kept beside the database would be obfuscation rather than protection, and what an operator-supplied key would actually cost. The write-only credential APIs added in v1.33.0 are a different control: they stop secrets being served back to a browser, and have no bearing on a file on disk.

## [v1.33.1] — 2026-08-27

**A series fill could grab your whole library with auto-grab switched off.**

Ten fixes for faults reported within a day of v1.33.0, most of them found by
one person exercising Hardcover as the primary metadata provider harder than it
had been exercised before. Four are in series fill, which turned out to be the
one action that ignored the auto-grab kill switch, could add the wrong book,
created the box sets the previous release taught the catalogue to remove, and
never recorded the Hardcover link it had just resolved.

### Fixed

- **Auto-grab off now stops every grab, not most of them** (#2256). The kill switch was checked by each caller rather than at the point a download is dispatched, so three paths never consulted it at all: a bulk **Search** action, adding a single book with search on add, and adding a book from **Recommendations**. All three fanned out indexer searches and grabbed releases with **Auto-grab** switched off. The check now lives inside the scheduler's search and grab entry point, so it holds for every caller including any added later, and a test fails the build if a new dispatch site routes around it. Books are still created and still marked wanted and monitored, so nothing is lost from the Wanted page. Only the automatic download stops.
- **Series fill ignored the auto-grab kill switch** (#2242). With **Auto-grab** off in Settings, a fill still fanned out indexer searches and grabbed the whole series. One report had fourteen releases grabbed and imported a minute after the switch was turned off, seven of them already in the library. Fill now creates the books and marks them wanted and monitored but queues no searches, which is what the scheduled wanted sweep already did.
- **Series fill added the wrong book** (#2238). When a Hardcover series files several books at one position, a box set, a novella and the real volume all at position 1, clicking add on one of them created whichever Hardcover happened to list first. An explicit book id now wins over a position match.
- **Series fill created box sets, and an excluded box set blocked the real book** (#2239). v1.33.0 taught the author catalogue to drop titles that unambiguously name a bundle, but series fill created books on a different path that consulted no filter at all, so the box sets came straight back in through it. Fill now applies the same check. An excluded book also only blocks re-adding that same title, so an excluded box set no longer makes the volume it is named after impossible to add.
- **Series built from Hardcover metadata were never linked to Hardcover** (#2245). A series created from a provider series reference got the right `hc-series:` id and no link row, so it showed as having no Hardcover link, the catalogue diff was unavailable, and **Fill** silently did nothing. The link is now recorded as the series is created. Series created before this release stay unlinked until you link them from the series **Search** button.
- **A strict unknown-language rule emptied a Hardcover catalogue** (#2241). Hardcover's author works query cannot ask for a language, because language belongs to an edition rather than a book, so every work arrived with no language at all. A metadata profile with allowed languages set and unknown-language behaviour set to fail then rejected all of them, and an author with 23 Hardcover series synced to a catalogue of leftovers and no series, with nothing reported as wrong. The language is now derived from the book's default edition. Nothing was deleted by this: the books were never created, so a refresh brings them back. Thanks to kevinatlee for the fix.
- **Adding a book could bind it to a cue sheet in another book's folder** (#2240). The already-owned check that runs when a book is created never received the supplement ranking added in v1.32.2, so a `.cue.txt` sitting beside an audiobook could be recorded as the file for a book you do not own, and one cue sheet could be handed to two different books in a single author sync, each with its on-add search silently skipped. That check now applies the same two tier treatment as the library scan: a supplement-class file next to audio is the audiobook's material and never answers, and elsewhere a real container outranks a supplement while still matching when nothing better does, so text-only and PDF-only libraries keep working. Thanks to ianepreston for both reproductions, and for the reports behind most of this release.
- **The provider fallback when adding an author is no longer silent** (#2237). With `metadata.primary_provider` set to Hardcover, adding an author whose record Hardcover's search does not return created an author linked to another provider, and because the catalogue provider is derived from that link the author then synced from the other provider permanently. Nothing said so. The add dialog now flags the result, warns before the add that the catalogue will sync from the other provider, and the API response carries a `providerMismatch` object.
- **Relink endpoints honour tenancy enforcement being off** (#2243). `relink-upstream` and its candidates endpoint returned "author not found" for any caller whose user id differed from the author's owner, even with `BINDERY_ENFORCE_TENANCY` unset, while every other author endpoint let that same caller read and delete the author. Both now scope their lookup the way their neighbours do. Cross-user access stays blocked when enforcement is on.
- **Per-indexer seed ratios reach qBittorrent again** (#2205). qBittorrent 5.2 made `shareLimitAction` a required parameter of the share limits endpoint, so every attempt to apply an indexer's seed ratio was rejected with HTTP 400 and the torrent silently kept the client's global limit. Bindery now sends it, with the value that applies the client's own configured action when a limit is reached, so behaviour is unchanged where it was already working. Older qBittorrent versions ignore the extra field.

## [v1.33.0] — 2026-08-25

**Your indexer and download client credentials were being sent back to the browser.**

Every settings page load returned the stored keys and passwords in full, masked
on screen but plainly readable in the network tab, in page state, and to
anything else with a view of an admin's session. Both APIs are write only from
this release on. The rest of the release is the backlog: the oldest requests in
the tracker, several of them open since June, plus the box sets that fill a
default install and the fix match that quietly rewrote people's folder layouts.

### Added

- **Hardcover can be promoted to the primary metadata provider** (#2040). `metadata.primary_provider` now accepts `hardcover` alongside `openlibrary` (still the default) and `dnb`. Selecting it lets Hardcover's curated catalogue define what an author's book list is, instead of only enriching OpenLibrary's, which for English language libraries means far fewer translated editions, omnibus bundles, alternate title duplicates and non book entries landing in the wanted list. It requires a Hardcover API token: the selector stays disabled until one is saved, the setting is rejected without one, and a token removed out of band makes Bindery fall back to OpenLibrary with a startup warning instead of failing every lookup. Changing it is safe for an existing library, because the catalogue provider is chosen per author from the ID they are already linked to, so authors added under OpenLibrary keep syncing from OpenLibrary and are not duplicated. The new primary applies to authors added afterwards.
- **Indexers now show when they have stopped working** (#1935). An indexer answering every search with "Account suspended" looked completely normal in Settings: enabled, no badge, no warning, nothing sent anywhere. The only trace was the search details panel on a book page, and only if you happened to run an interactive search and expand it, so an expired subscription or a revoked API key could sit there for weeks quietly dropping that indexer out of every automatic grab. Bindery now remembers whether each indexer answered its last search and shows it on the Indexers tab, in red when the credentials were rejected and someone has to fix it, in amber when it is a rate limit that clears on its own. A hard rejection also sends a notification, once when it starts rather than once per search. The indexer is never disabled automatically: that switch stays yours.
- **The book page now says where its metadata came from** (#1707). A Metadata source section names the provider, shows the identifier the book is bound to with a copy button, and lists every other provider id the same book is known by. With OpenLibrary, Hardcover, Google Books and DNB all in play there was no way to tell which record you were looking at, or whether a book was worth re binding. Providers whose public page can be built from the stored id get a link out; the rest show the id on its own rather than a link that goes nowhere.
- **Linked Hardcover series now link out to hardcover.app** (#1708), both in the candidate list while you are picking a series and on the linked series itself. Light novel searches routinely return the novel, its manga adaptation and a spin off under near identical names, and there was no way to open any of them before choosing. Bindery records the series slug when a link is made and fills it in for series linked before this release the next time their Hardcover catalogue is checked. A series with no slug yet shows no link rather than one that 404s.
- **Create a book from Manual Import's unmatched files** (#1719). An unmatched file could only be pointed at a book already in the library, so a file for a book nobody had added yet dead ended on the page that found it. Unmatched rows now offer a metadata search that creates the book, and its author when new, then resolves the row against it.
- **Sort the Authors list by display name** (#2102). The list could only be ordered by sort name, so an author filed under a surname was hard to find by the name actually shown. First name A to Z and Z to A join the existing buttons, ordered by a new folded `name_sort_key` column so accented first names sort in place rather than after Z.
- **Clear a queue item without touching the download client** (#2167): `DELETE /api/v1/queue/{id}?removeFromClient=false`, and `"removeFromClient": false` on `POST /api/v1/queue/bulk-delete`. Removing a queue item has always told the client to drop the job, which for a torrent ends the seed, so a stale row left behind by an out of band import could not be cleared without losing the release. The default is unchanged, and `deleteFiles=true` alongside `removeFromClient=false` is rejected with a 400 rather than silently ignoring one of them.

### Changed

- **An automatic grab now records when part of the indexer pool never answered** (#1936). A failing indexer returns no results, which looked exactly like an indexer that had nothing to offer. So if two of your three indexers were down, auto grab quietly picked the best of whatever the third returned and nothing anywhere said that most of the pool was never asked. If the release you wanted was on one of the other two, it simply did not get grabbed. Grabs decided this way are now stamped with how many indexers failed and why, visible in History, and logged rather than buried at debug level. What gets grabbed is deliberately unchanged: this makes the decision auditable first, so that acting on the signal can be a separate and deliberate choice.

### Fixed

- **Box sets no longer fill an author's catalogue on a default install** (#1780). OpenLibrary files a box set as an ordinary work, so "The Lord of the Rings 3 Books Box Set" arrived looking exactly like a book, monitored and Wanted, and went looking for releases. Removing them used to need either a Hardcover token or the **Skip part books** switch in the metadata profile, and neither is on out of the box. Titles that unambiguously name a bundle (box set, boxed set, "(Boxed)", collection set, "3 Books Set", "Carton of 10 Signed Copies") are now dropped from every author catalogue on ingestion, including the Audible supplement, whatever your providers and settings are. The judgement calls stay with **Skip part books**: a bare trailing "Omnibus", slash separated titles like "Title A / Title B", and "Books 1-3" are still only filtered when you turn it on, because each of those has real single books it would wrongly catch. "Trilogy" is filtered by neither, since plenty of real books are subtitled "Book One of the X Trilogy". A bundle already in your library keeps its row and its files; the catalogue simply stops offering it back.
- **The quality profile editor now offers every format release parsing recognises** (#1700). It listed only 8 of the 17 formats the search pipeline can label, so once v1.28.2 made the allow list authoritative (#1693), AZW, DJVU, CBR, CBZ, FB2, LIT, RTF, TXT and OGG could never be allowed for an author with a configured profile. All nine are now available as "+ Add" chips in the editor, and new profiles still start from the familiar PDF, MOBI, EPUB, AZW3 seed. A drift test keeps the editor's vocabulary locked to the parser's so the two cannot diverge again.
- **Hardcover list sync no longer downloads books nobody asked for** (#2124, #2217). Each list now has a "Download books from this list" checkbox under Settings > Import. On, which is the default and the old behaviour, synced books are created monitored and wanted. Off, they land in your library unmonitored, so a 400 book Want to Read shelf becomes something you browse instead of a 400 book download queue. The setting is the list's existing `monitorNew` API field, which was persisted and settable but read by nothing. Authors created by a list also stop pulling their entire back catalogue in on their first metadata refresh, because the syncer now pins `monitorNewItems` to "none" alongside the monitor mode, so only the listed books are added. The install wide default for `monitorNewItems` proposed in #2217 is not part of this change.
- **Fix match now warns that it moves and renames the file, and shows where** (#2055). Reassigning a file to a different book runs the full import pipeline, so the file is relocated into the target book's folder and renamed from your naming template, and nothing said so. Clicking a candidate committed straight to it, the request returns before the move runs, and there is no undo, so people with an existing library discovered their folder layout had been rewritten only afterwards. Picking a book is now a selection rather than the commit: Bindery asks the server where the file would land, shows that exact path next to the current one, spells out that the file is moved and renamed and that the move cannot be undone from Bindery, and waits for you to confirm. When the file is already where the template would put it, it says so instead of warning about a move that will not happen. Reassigning the metadata link without relocating the file is still not possible, and that half of the issue stays open.
- **Refreshing an author after relinking them to a different metadata provider created a second copy of books you already had** (#1705). With a Hardcover linked series, using Find better metadata on the author, picking their OpenLibrary entry and refreshing produced two rows for the same volume, one from each provider, and the only way out cost you the series link. A book row could only ever record one provider's id, so a work arriving from a second provider looked like something new. Books now keep track of every provider id they are known by, the way authors have since v1.26, so the same volume seen through two providers resolves to the one book you already have. This stops new duplicates rather than merging existing ones, which still need removing by hand.
- **Hardcover series membership no longer disappears for books OpenLibrary also lists** (#2207). When a Hardcover work matched a work from your primary metadata provider, the merge kept its rating, cover, description and genres but silently dropped its series. The only books that ever gained a series were the ones Hardcover has and OpenLibrary does not, so anthologies and tie ins were linked while the actual novels were not. Books created from now on are linked into the series Hardcover puts them in, which also fills the `Edit author > By series` picker and lets the `By series` monitor mode catch new books on discovery.
- **Download client Host field rejects a pasted URL instead of saving something that cannot work** (#2203). Pasting a value like `192.168.1.50:8080/#/` out of a browser address bar used to save fine and test green, then fail on every poll with `invalid character '<' looking for beginning of value`. Bindery now says which part of the value belongs in the Port field and which belongs in URL Base, fails **Test** for a client already saved with one, and marks it unhealthy in the client list.
- **qBittorrent errors say what came back** (#2203). A response that is not JSON is now reported as an HTML page from the WebUI, along with the two settings that route a request away from the API, rather than as a JSON parser message about a stray `<`.
- **IPv6 download client hosts work in both spellings** (#2203). `::1` and `[::1]` now reach the same address whichever client type you use; previously each spelling worked for only half of them.
- **Deluge grabs failed while the connection test passed** (#2204). Deluge's Web UI and its `deluged` daemon are separate processes, and Bindery only logged in to the Web UI. A session that had not been attached to a daemon accepted the login and then failed every torrent operation behind it. Bindery now attaches the session to the configured daemon after logging in, and the connection test reports the problem instead of passing when no daemon is attached. With several daemon hosts configured Bindery names them and asks you to pick one in the Deluge Web UI rather than guessing.
- **Editing a download client no longer wipes fields the request left out** (#2213). An update now applies on top of the saved row, so a client that omits `enabled`, `useSsl`, `category` or `priority` leaves them as they were. Sending `false` explicitly still turns a setting off.
- **Corrected the queue removal doc comment** (#2167). It claimed removing an item "preserves the seed" for torrent clients. The data survives on disk, but the torrent itself was deleted from the client, so nothing was seeding it.

### Security

- **Indexer and Prowlarr API keys are no longer sent back to the browser** (#2212). The settings pages used to receive every stored indexer and Prowlarr key in full, so the credentials were readable from the network tab, from page state, and from anything else with a view of an admin's session. Responses now return an empty `apiKey` plus an `apiKeyConfigured` flag. The edit forms start blank: leave the field alone to keep the saved key, or type a new one to replace it. API clients get the same contract, with a new `clearApiKey: true` body field for deliberately removing a key.
- **Download client passwords and API keys are no longer sent to the browser** (#2213). The download client API used to return the stored qBittorrent, Deluge, Transmission, rTorrent and NZBGet passwords, and the SABnzbd and NZBGet API keys, in full on every list and fetch, so they sat in the settings page in plain text behind a password mask that only hid them visually. Responses now blank them and report `apiKeyConfigured` and `passwordConfigured` instead. The edit form starts blank: leave the credential field alone to keep the saved one, or type a new one to replace it. Changing a client's type still drops the credential it no longer uses.
- **Warn when `local-only` auth mode runs without a trusted proxy configured.** In `local-only` mode Bindery serves any client whose resolved IP is private without a login. With `BINDERY_TRUSTED_PROXY` unset that decision is made on the connecting peer, which behind a reverse proxy or a Kubernetes ingress is the proxy's own private address, so every proxied request counts as local. Bindery now logs a warning at startup and when the auth mode is changed if it sees that combination, **Settings > General > Security** shows the same text inline under the authentication mode control when you select the mode, and the deployment docs state the requirement. Instances reached directly on a LAN are unchanged.

## [v1.32.2] — 2026-08-25

**Books you already owned kept turning back up in Wanted.** Three separate
faults produced the same symptom, and this release fixes all three. An
Audiobookshelf import read an item listed without its files as an audiobook and
quietly made ebook only books dual format, so they wanted an audiobook that was
never real. An author refresh did the same thing from the other direction,
widening any owned book whose work a catalogue also listed in the other format:
one reporter had a single routine refresh flip 29 already downloaded books, some
added two months earlier. And a file that moved on disk left the book rendering
the old, dead path while still claiming to be imported, which no amount of
rescanning corrected. In each case the book's status is derived from the formats
still missing, so an invented want was enough to drop it off the shelf.

**Heavily reprinted books were disappearing from libraries entirely.** Bindery
read only the first 50 editions of an OpenLibrary work, in an order OpenLibrary
does not sort, and the Min pages and Skip missing ISBN metadata profile settings
decide from that list. A title whose qualifying edition happened to sit further
down was skipped on every refresh. One work in testing had 139 editions, 131 of
them carrying an ISBN, more than half of the page counts past the cut.

**Path remaps were impossible to configure with the download client on
Windows.** A remap written with a drive letter was split at the drive letter's
own colon, so it silently matched nothing and the Settings validator accepted it
anyway. Torrents were added, author and book folders were created, and no file
was ever copied. Anyone running qBittorrent on Windows with Bindery in Docker
had no working form to write.

**A co-author could swallow another author's whole catalogue.** Importing a
Calibre book credited to two people made the second one an alias of the first,
so every later book by that co-author was filed under their collaborator and
they never got an author page. Which author survived came down to Calibre book
id order, which is why the effect looked arbitrary. Libraries already affected
recover on the next import.

**Quality profiles were never checked against the file you actually received**,
only against the release name, and the check deliberately let through any
release whose title carried no recognisable format. So the one case it admitted
it could not judge was exactly the case that arrived unchecked.

**Download client health is now checked for every client type.** If a client
turns red in Settings after this update, that is almost certainly a problem that
was already there. Nothing about your setup changed; Bindery just started
looking. Only qBittorrent was ever checked before, and the other five stored a
green result without checking anything.

### Changed

- **Author refresh fetches Hardcover editions for new books a few at a time instead of one after another** (#1929). After the library walk fix in v1.30.3, edition hydration was the last provider call the sync made once per newly added book, waiting for each one to come back before starting the next. A 65 book author paid 65 round trips in sequence. The sync now knows the full set of books it created before hydrating any of them, so it fetches their editions in parallel and hands the results to the same hydration path as before. A refresh that adds nothing still fetches nothing, and a lookup that fails is retried live exactly as it was.

### Fixed

- **An Audiobookshelf import no longer sends owned books back to Wanted** (#2169). When ABS listed an item without its files, because the per item detail fetch was skipped or failed, Bindery read that silence as "audiobook" and widened the book to dual format. Status is derived from the formats still missing, so the book then wanted an audiobook that never existed, and the next import demoted it from Imported back to Wanted with its ebook still on disk and still attached. Re-importing repeated the demotion instead of repairing it; one reporter had 23 of 51 rows in that state. Only the formats an item actually exposes now take part in the merge. Books already widened keep their media type, so set one back to the format you want and Bindery re-derives the status.

- **An author refresh no longer changes the format of a book you already own** (#2096). When a catalogue listed the same work in the other format, the refresh merged the row to dual format whether or not the book was already on disk, which invented a want nobody asked for. The merge still runs for a book nobody has yet, which is the duplicate row problem it exists to solve. Books already widened keep their media type; set one back by hand and the status follows.

- **A moved book file left the book pointing at a path that was gone** (#2186). After a file moved on disk, a scan registered it at its new location but the book kept showing the old, dead path while still reporting Imported, and no amount of rescanning corrected it. A book now shows whichever of its tracked files still exists on disk, and a library scan repairs books that were already stuck this way. The entry for the old location is not deleted: it stays listed under the book's Files, where **Forget this file** clears it without touching anything on disk.

- **Heavily reprinted books no longer go missing from an author's library** (#1779). Bindery read only the first 50 editions of an OpenLibrary work, in an order OpenLibrary does not sort. The **Min pages** and **Skip missing ISBN** metadata profile settings decide from that list, so a title whose page count or ISBN happened to sit further down was skipped on refresh even though it qualified. The full edition list is now read, so those books come back on the next author refresh.

- **Path remaps now work when the download client runs on Windows** (#2191, reported in Discussion #1971). A remap written with a drive letter, such as `S:\Downloads:/mnt/Storage/Downloads`, was split at the drive letter's own colon and quietly did nothing, so nobody running qBittorrent on Windows with Bindery on Linux or Docker could configure a working remap at all. Drive letters are now understood on both sides of the pair, either slash works on the Windows side, matching there ignores case while Linux paths stay case sensitive, and the result comes back as a clean path for whichever platform it lands on. Settings now rejects a drive path with no destination instead of silently accepting it, and the download client Test button explains that a Windows client requires a remap rather than suggesting an identical mount that cannot exist.

- **Calibre import no longer files co-authors as aliases of the primary author** (#1684). A book credited to two people made the second one an alias of the first, so every later book by that co-author was filed under their collaborator and they never got an author page at all. Co-authors are simply not recorded now, since a Bindery book carries one author, and an import ignores an existing alias unless something backs it up, so libraries already affected recover on the next import. No alias rows are deleted, and author merges, spelling variants and non-latin alternate names keep resolving exactly as before. Leftover rows can be removed from the author page if you want them gone.

- **The Calibre library import now tracks the files it imports** (#1635). It reconciled titles, authors and series from `metadata.db` but never recorded where the files actually were, so a Calibre managed book was marked imported while Bindery tracked no file for it. Every format Calibre reports is now tracked, so an epub and an audiobook of the same book are both attached rather than one hiding the other, and re-running an import does not duplicate rows. This also gives Calibre libraries a supported way to correct a file path, which they previously lacked.

- **A quality profile's allowed formats are now checked against the file you actually got** (#1782). The check ran once, before downloading, against the release name, and deliberately let through any release whose title carried no recognisable format, which is common for Usenet posts named like "Author - Title (Year)". Nothing after that ever looked again, so a release that turned out to be a MOBI, with no file extension at all, imported cleanly into a library whose profile disallowed MOBI. Bindery now identifies the real format from the file's contents before importing it. A download whose files are all disallowed is held for review rather than imported, with the reason attached, and the release is blocklisted so the next search does not simply grab it again. A download containing a mix imports the format you allowed and leaves the rest. Manual imports are unaffected: if you pick the format yourself, that is the answer.

- **Download client health is now checked for every client type** (#2029). Previously only qBittorrent was ever checked. SABnzbd, NZBGet, Transmission, Deluge and rTorrent each stored a green "path check not required" without checking anything, and the Settings page would not have displayed the result even if they had. That is the mechanism behind the most common support report we get: the connection tests fine, the client accepts grabs, and nothing ever imports. Health is also re-checked every 15 minutes now, so a client that breaks after setup, a remounted seedbox or a rotated password, no longer stays green until someone thinks to press Test. The three client types that genuinely cannot be introspected say so plainly instead of claiming to be fine.

- **Download clients that refuse an action are no longer treated as having done it** (#2192). SABnzbd and NZBGet report a refused pause, resume, delete or history delete as an ordinary HTTP 200 with a failure flag in the body, and Bindery read only the HTTP status. The most visible effect was post import history cleanup: SABnzbd kept the finished job in its history, and the "cleanup failed" warning that was supposed to say so never fired. Deluge and Transmission had the same gap on torrent removal. All six now report the client's own reason, and a client that refuses to forget a job still never leaves a stale row in Bindery's queue.

- **An indexer's error page no longer fails the grab under SABnzbd's name** (#2105). SABnzbd and NZBGet grabs fetch the NZB from the indexer and hand the download client the bytes, so an indexer that answers a refused, expired or rate limited grab with HTTP 200 and an error page had that page forwarded to the client. The client refused to parse it and the grab failed with `SABnzbd rejected download`, naming the one component in the chain that behaved correctly, while the book dropped silently back to Wanted. Bindery now checks that the fetched body is actually an NZB before it goes anywhere, and reports what the indexer really sent, including the newznab error code where there is one. Indexers that serve the NZB as a gzip or bzip2 file rather than as a compressed HTTP response are unaffected.

- **A momentary network failure fetching an NZB no longer fails the grab for good** (#2157). A single failed attempt marked the download failed and dropped the book back to Wanted, where it looked exactly like a book no release had been found for, and nothing retried it until the next scheduled search. The fetch is now attempted up to three times, with a short backoff, for failures another attempt could plausibly clear. Failures that will not change are still reported immediately and after a single request. Only the fetch retries; the upload to the download client is still made once, so a retry can never produce a duplicate job.

- **Ebook searches on Prowlarr synced indexers no longer come back empty** (#2170). When an indexer carried no categories of its own, Bindery derived them from the Sync Categories of every application registered in Prowlarr, including ones that have nothing to do with books. A Mylar install contributing 7030 (Comics) made the ebook bucket non empty and wrong, so every ebook search went out asking for comics and returned nothing, with no error to explain it. Only Readarr and LazyLibrarian scopes are used now, and an indexer that advertises an ebook category no registered application syncs logs a warning instead of failing silently.

- **A hostname that resolves to several addresses no longer fails on the first unreachable one** (#2156). Every guarded outbound connection resolves the hostname itself so it can re-check each address against the SSRF policy, which is what stops a DNS rebind, but it then connected to the first address only. The visible case is a dual stack indexer reached from an IPv4 only container network, where the IPv6 address is an instant "network is unreachable" and the IPv4 one right behind it was never tried. Addresses are now tried in the order the resolver returned them until one connects, and the policy check that refuses the whole dial if any resolved address is forbidden is unchanged.

- **Two different authors publishing under the same name were merged into one** (#1734). Hardcover works were always fetched by author name, so two real people who happen to publish as "J.A. Andrews" looked like one person, and one of them picked up around 44 books that were not theirs. Every metadata refresh re-applied it. Once an author is linked to a specific Hardcover author, their works are now fetched by that link instead of by their name. Name matching is still used when first linking an author, which is the point where a name is genuinely all there is to go on. This prevents new merges rather than untangling existing ones.

- **Series fill now respects the format you picked** (#1802). With enhanced Hardcover series enabled, "add all" and the per row "add" created every book as a dual format book whenever Hardcover listed an audiobook edition, so choosing Ebook still queued and grabbed an audiobook as well. The chosen format is now kept, and only that format is searched for.

- **Library scan no longer lets a notes file take a book's ebook slot** (#2188). When a folder held a real ebook alongside a `.txt`, `.rtf`, `.pdf` or comic archive, whichever file the directory listing happened to yield first became the book's ebook, and the other was reported as unmatched. The real ebook now always wins, and the companion file is counted as already tracked instead of cluttering the Unmatched list. Libraries made up of `.txt`, `.rtf` or `.pdf` files still reconcile exactly as before: a supplement class file is only ever passed over when a better file for the same book is there to take its place.

- **Deleting a user failed for any account that had ever used the app** (#1899). The delete ran as a bare `DELETE FROM users`, and seven tables point at the user with no rule for what happens when they go, so SQLite rejected it. Adding one author was enough. Deleting a user now asks what happens to their library first, showing how many authors, books, downloads, profiles, root folders and import lists are involved: hand them to another user, make them visible to everyone, or delete them along with the account. Blocklist entries always stay, since a release that failed for one user still fails for everyone, and only the record of who added it is cleared. Installs that deleted a user back when this silently orphaned rows instead get those rows swept back into view on upgrade.

- **Bulk author import no longer storms OpenLibrary with unbounded requests** (#2075). A CSV or Readarr database import fired one goroutine per author with no cap, so a 19 author CSV opened 19 simultaneous catalogue fetches, and OpenLibrary's per user agent throttle turned that into 429s cascading into timeouts and connection refusals with nothing to slow the batch down. Catalogue fetches are now bounded to two at a time and paced three seconds apart, which is what actually prevents the storm. The OpenLibrary client additionally retries an occasional 429, 502, 503 or 504 or transient network failure, honouring `Retry-After` when the response sends one.

- **Adding a book by ISBN no longer loses the author** (#2187). An OpenLibrary edition that is not linked to a work now carries its author through the lookup instead of coming back with just a title and cover. The Add button also no longer refuses a result that has no author name: the backend resolves the author from the book itself, so the request goes through.

- **Add Author now honours the default root folder** (#2166). The dialog seeded its root folder picker from the first folder in the list rather than from the configured default, and posted that as an explicit per author choice. Since an author's own root folder is resolved ahead of the install default, the setting had no effect on anything added through the dialog. Invisible with one root folder configured; with two it silently filed authors under whichever was created first.

- **Hardcover errors now say which side failed** (#2128). When Hardcover returns an HTML error page, Bindery no longer pastes that page into the Settings test result and the logs. A rejected token now reads `token rejected (HTTP 401: Invalid or expired token)`, and a Hardcover outage reads as an upstream failure rather than a token problem. Hardcover's `hc_pat_` personal access tokens keep working exactly as before.

## [v1.32.1] — 2026-08-20

**Single sign-on could lock you out of your own instance.**

Bindery read OIDC group membership only from the ID token and never called the
provider's userinfo endpoint. Authelia, Okta and Auth0 do not put `groups` in
the ID token by default and serve it only from userinfo, so Bindery saw no
groups at all. With `BINDERY_OIDC_ADMIN_GROUP` set, that empty result read as
"not an admin" and demoted the user on every login, including the operator who
configured it. The last-admin guard is deliberately bypassed on this path, so
there was nothing to catch it. It also explains OIDC users whose Bindery
username was a `sub` UUID, since `preferred_username` is userinfo-only under
the same defaults. Bindery now reads userinfo as well and merges it underneath
the ID token, and a group claim missing from both leaves your existing role
alone instead of assuming the worst.

**Adding the second volume of a series did nothing.** With Hardcover as the
metadata provider, adding a book whose main title matched a volume already in
your library was silently folded onto that volume. The Add button flickered, no
book appeared, and no error was shown. Bindery has a guard for exactly this,
comparing the two volumes' series positions, but the query that fetches a
single book never asked Hardcover for its series, so the guard had nothing to
compare and waved the merge through every time.

**Migrating from Readarr turned every download client into SABnzbd**, unless it
was qBittorrent. A Readarr install on Transmission, Deluge, NZBGet or rTorrent
imported a client with the right host and port and entirely the wrong type. It
saved, it appeared in the list, and then every grab against it failed, with
nothing in the migration result admitting the type had been guessed.

**Bulk folder import timed out and returned nothing.** The scan matched each
file against the catalogue one at a time, at roughly 213 ms per file on network
or spinning storage, so a thousand file scan ran past three minutes, outlived
the server's write timeout, and died mid response. The page sat there and then
retried straight into the same wall. That scan now finishes in under half a
minute.

---

### Changed

- **SQLite now runs the connection pragmas that WAL expects** (#2142). Turning on WAL never changed `synchronous`, so every commit was still paying a disk flush under SQLite's FULL default, and the page cache and temp store were left at their untuned defaults. Writes on the import, scan and download poll paths get noticeably cheaper. The trade, stated plainly: with `synchronous=NORMAL` an OS crash or power loss can lose the last few committed transactions that have not been checkpointed. It cannot corrupt the database, and an application crash loses nothing. See [Database durability](docs/DEPLOYMENT.md#database-durability).

- **Audiobookshelf imports fetch Hardcover series catalogues concurrently** (#2144). A search returns up to five candidate series, and each one's catalogue was fetched only after the previous had returned, so a book whose series were not yet cached cost five sequential round trips. Most noticeable on the first import of a library, which is when the fewest series are cached.

- **Calibre cover downloads reuse one HTTP client** (#2144). Each cover fetch built its own, so two covers from the same host could not share a connection. Affects the first import of a library, before the on disk cover cache is warm.

- **`make check` now runs `govulncheck`** (#2140), pinned to the same revision CI installs. `CONTRIBUTING.md` described the target as running what the gating CI checks run, and listed the vulnerability scan among them, but the target left it out. A contributor who ran `make check` before opening a pull request had not actually run the scan CI would fail them on.

- **Contributor documentation corrected and extended.** `ARCHITECTURE.md` claimed SQLite reads run concurrently, which the single connection pool has never allowed. `CONTRIBUTING.md` gains a guide to adding a metadata provider, download client or indexer, the three likeliest outside contributions and previously the three least documented.

### Fixed

- **OIDC group mapping works with Authelia, Okta and Auth0 again, and a missing group claim no longer takes your admin rights away** (#2097). Bindery now reads the userinfo document as well and merges it underneath the ID token, so a claim carried by both keeps the signed ID token's value, and a userinfo document whose subject does not match the ID token's is discarded (OIDC Core 1.0 §5.3.2). A group claim missing from both leaves the existing role untouched and logs why; a claim that is present and does not list the admin group still demotes, because that is the provider actually saying so. `allowed_groups` now says which of the two it rejected a login for instead of giving the same message either way.

- **Adding a second volume of a series did nothing** (#2116). The query that fetches a single book never asked Hardcover for its series, so the duplicate guard had nothing to compare and always let the merge through. The same field was missing from the ISBN lookup.

- **Hardcover supplemented author catalogues had no series information** (#2121). Works pulled from Hardcover to fill out an author's bibliography arrived with no series membership, so those books were never linked into a series and an author set to monitor a specific series never picked them up. They now carry the series and volume number Hardcover holds. Existing books gain their links on the next author refresh.

- **Readarr migration turned every non qBittorrent download client into SABnzbd** (#1983). All six client types Bindery supports are now mapped by name, and a Readarr client with no Bindery equivalent, such as NZBVortex or a blackhole, is reported as a skipped row instead of becoming a SABnzbd client that cannot work.

- **Bulk folder import scan timed out and returned nothing** (#1638). Matching now runs up to eight files at once.

- **Import lists ignored their root folder** (#1864). The per-list root folder picker saved its value and the Hardcover list syncer never read it, so authors created by a list landed with no root folder at all. Same defect as the quality profile in #1781, one field over. A list with no root folder configured still leaves the author's unset rather than guessing, because root folders belong to a user and picking one for somebody else's authors is worse than leaving it empty.

- **Author imports kept creating rows with no metadata profile** (#1803). Six author creation paths, including ABS import, the Calibre importer, the Goodreads migration and the CSV importer, inserted authors with the metadata profile column empty. v1.31.0's migration cleaned up the ones that had accumulated, but the paths producing them were untouched, so the next import started refilling it. Nothing behaved differently in the meantime, since every reader already fell back to the default; the profile column in the author UI was simply blank when it should not have been.

- **Grabs signed by host match lost their indexer** (#2053). When an API client posts a release with no indexer id, Bindery works out which configured indexer the download URL belongs to and signs it with that indexer's key. It never recorded which one it picked, so the download row had no indexer attribution in the queue and any per indexer seed ratio override was skipped. Two indexers sharing a host and key still sign the URL but attribute nothing, since their seed ratios can differ and choosing between them would be a guess.

- **"SABnzbd rejected download" never said why** (#2120). SAB replies to a refused upload with its own explanation, and Bindery decoded only the success flag and discarded the reason. Since SAB deletes the uploaded file before its own backup step, that reply was the last place the explanation existed. The reason now reaches the queue and the history entry.

- **Audiobookshelf base URL rejected a port with an error about the scheme** (#2056). Typing `audiobookshelf:13378`, the natural form when everything runs under Compose on one host, produced "must use http or https". Go reads a scheme-less `host:port` as a scheme, so the complaint pointed at the wrong half of the input, and the obvious next move was to drop the port and land on port 80. Ports were always supported. The error now names the missing scheme and prints the exact value to use.

- **Strict media type never said it was letting explicit adds through** (#1759). The setting skips catalogue books in the wrong format and narrows dual format ones, but a book you add yourself has always been created in the format you picked, on both add paths. That is deliberate, since silently refusing something you explicitly asked for is worse than the row it prevents, but nothing said so anywhere. The setting's help text now states the boundary, and an add that goes past the policy is logged instead of passing unremarked.

### Removed

- **Three scratch files that were committed by accident**: `err.log`, `magazine-feature-prompt.txt` and `ui-browser-test-prompt.txt`. They shipped in every clone. Their siblings were already ignored; these were missed.

### Security

- **The login rate limiter no longer grows without bound** (#2137). The per address bucket map only expired entries for an address that came back, so a caller rotating source addresses, which is trivial inside a single IPv6 /64, could grow it indefinitely. Buckets are now swept once per window. Forged `X-Forwarded-For` was never the vector here, since forwarded headers are stripped from untrusted peers.

- **The telemetry server compares its stats token in constant time** (#2138). The `/api/stats` and `/api/backup` bearer check used a plain string comparison, whose timing leaks how much of a guess was correct. `/api/backup` returns a snapshot of the installs database, so that token is the only control in front of it. Every other secret in the main binary was already compared this way.

## [v1.32.0] — 2026-08-19

**Five metadata profile filters did nothing at all.** `skipPartBooks`,
`skipMissingDate`, `minPopularity`, `minPages` and `skipMissingIsbn` each had a
control in the UI, each saved its value, and not one of them was ever consulted
while cataloguing an author. Setting them changed nothing, and there was no way
to tell from the outside — the books they should have excluded simply arrived
anyway. All five now filter, and the author page reports what each one dropped
with sample titles, so a profile that is too aggressive is visible rather than
something you infer from a book that never showed up. Books already in your
library are exempt from every one of them: an owned book still reaches the
update path and keeps getting its ratings, genres and cover refreshed, so
turning a filter on cannot strip metadata from what you already have.

**A clean install with no Hardcover token hammered Hardcover anyway.** A bulk
CSV import of nineteen authors fired 447 Hardcover requests, of which 240 came
back "unable to verify token" and the remaining 205 were throttled. Hardcover
authenticates every query, search included, so not one of them could have
succeeded. Only one of the client's queries checked for a token first. All of
them do now, and an install without a token makes no Hardcover requests at all.
Startup used to log `hardcover enrichment enabled` regardless; it now says
`idle: no api token configured` when that is the truth.

**And CSVs saved by Excel, Google Sheets or Numbers imported a stranger's
library.** Those apps write a byte order mark at the start of the file, and it
stuck to the first cell — so the header row stopped looking like a header, got
imported as an author name, matched some unrelated person on OpenLibrary, and
pulled in their entire catalogue. That is most of the rest of the request
volume above, plus a bogus author in your library. Goodreads exports failed
differently and more honestly: the mark made the Title column unfindable and
the import refused to run.

---

**Upgrading from a version before 1.15 could fail to start**, with
`no such column: books.excluded`. A column added years ago never landed on
some databases, and every startup after that died on the first query touching
it. It is restored automatically now.

**Deleting one format no longer takes the other one with it.** Audiobook
imports register the destination *folder*, so a `?format=audiobook` delete
landed on a directory and removed all of it — including the ebook sitting
beside the audio files. The ebook's database row survived, so the book went on
advertising a file that was no longer there.

### Added

- **Bulk media-type editing gained a "Set Both" action, and works from the author page** (#2066) — the Books view's bulk bar could set Ebook or Audiobook but never the pair, because `POST /book/bulk` rejected `mediaType: "both"` even though the author-level bulk action accepted it and the underlying write is media-type agnostic. A batch of books owned in both formats had to be corrected one at a time from each book's edit page. `both` is now accepted, **📖🎧 Set Both** sits alongside the existing two buttons, and all three are also available on an author page's book list — where a media-type correction after a bad sync usually starts, and where the previous workaround (filter the global Books view by author) cost the author-scoped context the cleanup needs.

- **Author sync reports what each filter dropped, with examples** (#2032) — `AuthorSyncSummary` carries a count and a sample of titles for every skip reason, and the author page's sync notice lists them. Previously a metadata profile could silently discard most of an author's catalogue and the only evidence was a shorter book list than you expected.

### Changed

- **Books that share a main title are no longer merged** (#2042) — the canonical title key used to stop at the first `": "`, so `Star Wars: A New Hope` and `Star Wars: The Empire Strikes Back` were one identity and an import could bind one onto the other. Distinct subtitles now mean distinct books; a subtitle only one source spells out still matches. Keys are recomputed automatically on the next start, so no action is needed; libraries already holding duplicates keep them, as merging existing rows remains a separate piece of work.

- **Series Fill picks the best title match rather than the first** (#1969) — Fill now scans every existing candidate title and links the best-scoring one, not just the first past the confidence threshold. On a library where two different real books both clear the threshold against one catalogue title, the book Fill links can change from "whichever came first" to "whichever scores higher".

### Fixed

- **The five metadata profile filters that never filtered** (#1723) — `skipPartBooks`, `skipMissingDate`, `minPopularity`, `minPages` and `skipMissingIsbn` were each persisted and each ignored during cataloguing. All five now apply during author sync and refresh:
  - *Part books* removes box sets, omnibuses, signed-copy cartons and slash-separated multi-title anthologies.
  - *Missing date* removes works with no release date.
  - *Minimum popularity* removes works below the ratings-count floor, exempting works that have not released yet and so cannot have accumulated any.
  - *Minimum pages* and *missing ISBN* are checked against a real edition lookup. A work with no page data at all counts as unknown rather than zero and passes the floor either way.

  The edition lookup runs only for works that survived every free filter, only when one of those two settings is on, and is batched across the survivors with bounded concurrency rather than fired one at a time — so a profile touching neither setting pays nothing, and one that enables them does not add a serial round-trip to author sync (#1929). Books already in your library are exempt from all five and keep receiving rating, genre and cover updates.

- **Hardcover is no longer queried when no API token is configured** (#2075) — a bulk CSV author import on a fresh config fired hundreds of Hardcover requests that came back `401 Unable to verify token` and then `429 Throttled`, because only one of the client's queries checked for a token first. Every Hardcover query now short-circuits as "not configured" before any network call. Adding a token still takes effect immediately, with no restart.

- **The Hardcover line in the startup log is honest** (#2075) — startup always logged `hardcover enrichment enabled`, even with no token configured. It now logs `hardcover enrichment idle: no api token configured` in that case.

- **CSV imports of files saved by Excel, Google Sheets or Numbers** (#2075) — those apps put a byte order mark at the start of a UTF-8 CSV, and it used to stick to the first cell. The header row was no longer recognised as a header, so it was imported as if it were an author name, matched an unrelated person on OpenLibrary, and pulled in that stranger's whole catalogue — often hundreds of provider requests, rate limiting and a bogus author in your library. The mark is now stripped before parsing, for author CSVs (both the two-column and the plain name-per-line form) and for Goodreads exports, where it made the Title column unfindable and the import fail outright.

- **Upgrades from before 1.15 no longer die on a missing column** (#1932) — the legacy-missing `books.excluded` column is restored automatically at startup instead of failing every query that touches it.

- **Deleting one format no longer destroys the other format's file when both live in the same folder** (#2052) — audiobook imports register the destination *folder* in `book_files`, so a `?format=audiobook` delete landed on a directory. That branch was an unconditional `os.RemoveAll`, which discarded the format filter every other part of the delete path honours, and took the ebook sitting beside the audio files with it. The ebook's `book_files` row survived, so the book kept advertising a file that was no longer on disk and downloading it returned an error. The directory branch now walks the folder and removes only the files belonging to the format being deleted; cover art and other sidecars are removed once no book file of any format is left, and the folder itself only when nothing remains in it.

- **A folder delete can no longer unlink a file another book still tracks** (#1368, surfaced by #2052) — the `book_files` ownership guard was applied only to the tracked path handed to the delete, so any file nested under a deleted folder was unguarded. The same check now runs for every file the sweep reaches, including same-stem siblings.

- **Bulk "Set monitor mode" can now set whether authors accept newly discovered books** (#2065) — the dialog wrote `monitor_mode` and nothing else, so setting a whole library to *None* left every author still pulling in its back-catalogue on the next refresh. The two settings are independent, and the field was only reachable from the single-author edit form. The bulk dialog now carries a **Monitor newly discovered books** control alongside monitor mode, defaulting to *Leave unchanged* so an existing bulk action behaves as before. Mode *None* still does not imply it: that pairing is the supported "list the whole catalogue, monitor none of it" setup.

- **Library scan no longer rejects audiobooks when `BINDERY_AUDIOBOOK_DIR` differs from the ebook root** (#2033) — the scan's reconcile tiers matched a file by ASIN, fuzzy title, or series position and then checked the candidate against the ebook root regardless of the file's format. With a separate audiobook root, every correctly matched audiobook failed that containment check and fell through to the generic `no_title_match` reason, so the scan reported that it could not identify files it had in fact identified. Each file is now checked against the root for its own format.

- **Long titles and long authors together no longer fail the import with "file name too long"** (#2014) — the per-value cap added in #1982 limits each template field, but nothing limited the *name the template composes out of them*: with the shipped default `{Title} - {Author}.{ext}`, a title and an author each at the 200-byte field cap render a 408-byte filename and the write dies with `ENAMETOOLONG`, on plain ASCII. The rendered segment is now capped as a whole, and the budget reserves the bytes that get appended after it is rendered — the staging prefix, so a name that is legal at the final write can no longer fail earlier and more confusingly during staging, and the `" (2)"` collision suffix, so a name that just fits does not fail the second time the same title arrives. Trimming takes from the longest field first and never from the template's own text, so the extension, the `" - "` between title and author, `{Year}` and audiobook `Part NNN` all survive intact. Names already inside the budget are byte-for-byte unchanged, so existing libraries do not move.

- **A metadata profile set to "English Only" no longer drops genuinely-English books whose language OpenLibrary never reported** — OpenLibrary's edition records commonly omit the `languages` field, which is a data gap, not a signal that a work is foreign-language. A work the edition-sample backfill could not resolve was previously treated identically to a confirmed non-English work by a profile set to reject unknowns. Author sync now checks whether one language clearly dominates the rest of that author's already-resolved catalogue in the same sync before giving up on an unresolved work, and applies it — most authors write predominantly in one language, so this catches the common case (a sparse-metadata author, not a mixed-language one) without any extra provider round-trips.

- **Duplicate books from punctuation and subtitle disagreements** (#2042) — the canonical title key now folds apostrophes (`Poseidon's Arrow` and `Poseidons Arrow` are one book) and treats a colon as a separator rather than a truncation point (`Journey of the Pharaohs: Numa Files #17` matches the colon-less spelling), so a provider that punctuates differently from Calibre no longer creates a second `wanted` row beside a book you already own.

- **Series Fill no longer lets a box-set or omnibus title satisfy a real book's slot** (#1969) — `TitleScore`'s substring-aware components score a target title fully contained in a much longer omnibus title as a perfect match, tying an actual exact-title match. Fill took the first title over the threshold rather than the best one, so an omnibus record could win that tie and get linked to a slot the real book should have filled, leaving the real book silently unmatched with no error surfaced. Fill now keeps the best-scoring candidate and breaks same-score ties with a boundary-aware match against a non-substring similarity score, falling back to title-length closeness only as a last resort.

- **Grabs from API clients no longer fail with the indexer's 401** (#2039) — search and queue responses strip the indexer apikey out of `nzbUrl` and the grab handler puts it back from the `indexerId` the web UI sends along. A client that posts only `{guid, nzbUrl}` (scripts, `curl`, other API consumers) has no id to send, so the unsigned URL went to the download client and the indexer answered `401`. The key is now also recoverable from the configured indexer whose host matches the download URL.

- **rTorrent downloads no longer burn their retry budget while the files are absent** (#1884) — every other download client stopped counting a retry attempt against a download whose files are not on this host, but rTorrent's retry branch was added without that guard, so the one client that missed it spent all five attempts on polls that could not have imported anything and then blocked the download terminally. It now waits like the others and imports the moment the files appear.

- **Download client path warnings name your configured directories** (#1984) — a qBittorrent completed-downloads path that Bindery cannot see now reports Bindery's own configured ebook and audiobook directories alongside it, so the mismatch is legible without going to look them up.

- **Hardcover list sync** (#2035) — the same work on an ebook list and an audiobook list now becomes one wanted dual-format book instead of two rows.

- **Multi-user search debug** (#1859) — users can now access only their own most recent search details.

### Security

- **The grab response no longer echoes the indexer apikey back to the caller** (#2039) — the queue listing already stripped the shared indexer credential out of `nzbUrl`, but the grab response handed back the download URL that had just been signed, so a non-admin who grabbed a release read the key straight out of the reply. Indexer credentials are admin-only settings. The stored row keeps the key, so retries still reach the indexer authenticated.

- **The pending releases list no longer exposes the indexer apikey** (#2039) — a release held back by a delay profile is stored as the raw indexer result, whose `nzbUrl` was already signed, and `GET /api/v1/pending` returned that blob verbatim in `releaseJson`. It is redacted now. The stored blob keeps the key so force-grab still re-sends a signed URL.

## [v1.31.0] — 2026-08-14

Two things in this release could take the whole process down, and neither
needed anyone to do something unusual to trigger it.

**A malicious torrent file could kill Bindery.** The walk that reads a
torrent's infohash recursed once per level of nesting with no limit, so a
crafted `.torrent` of deeply nested lists exhausted the stack. That is not a
recoverable panic — the process dies, the container restarts, the grab retries,
and it dies again. The file only has to be about 36 MB, comfortably inside the
size Bindery will fetch, and it arrives from whichever indexer answered the
search. Nesting is now capped; a real torrent nests three or four deep.

**And a background job could do the same.** Every tracked job — Audiobookshelf
import, Grimmory, manual library scan, the startup syncs — ran without a panic
handler. Any one of them hitting an unexpected nil took the whole process with
it rather than failing on its own. That hole had been there the entire time and
was only found because the Hardcover sync moved out from behind the web
server's handler, which had been quietly absorbing it.

**The Go toolchain moved to 1.26.6** for six standard library advisories,
including one in the XML decoder that is the same class of bug as the torrent
one above: unbounded recursion on input from somewhere else. Bindery parses
indexer XML with it.

**Bindery was also shipping GPL-licensed code inside MIT binaries.** A fuzzy
string-matching library used for series title comparison is GPL-3.0, and Go
links everything statically, so every release binary and container image was a
combined work that could not honestly be offered under the licence in the
repository. That code has been replaced with an implementation that produces
the same scores — verified across nineteen thousand real title pairs, with no
matching decision changing anywhere — and carries no licence of its own. Every
release now also ships `THIRD_PARTY_LICENSES.md`, which had been missing
entirely, and CI fails if it drifts out of date.

---

**The part most likely to be felt** is quieter, and it is three separate bugs
that happened to land in one user's log file.

Bindery decided a torrent was finished by looking only for reasons to say yes.
A freshly added torrent reports 100% for a moment, before the client has worked
out what it is actually downloading, and that alone was enough. So the import
ran about fifteen seconds after the grab, against a folder the download had not
created yet — and if you run qBittorrent with a separate incomplete directory,
it never would, because the data was somewhere else. The three import retries
then spent themselves in forty-five seconds, well inside any real download, and
the download was failed for good. The error blamed your path mapping, which was
one of three things it could have been and the wrong one here.

During those doomed retries a second bug fired: if the same book's audiobook
finished importing in the meantime, the still-pending ebook grab was marked
imported and abandoned, because the check for "is this already in my library"
looked at the book rather than at the format slot the download was filling.
Nothing failed and nothing retried, because from Bindery's side the chain had
ended successfully. If you have books stuck missing one format for no visible
reason, that is the likely explanation.

And once a download had been failed this way you could not simply grab it
again — the release stayed claimed by the dead row and every attempt came back
"already grabbed" with nothing to click. A blocked download now releases its
claim, retries no longer burn attempts when there is nothing on disk to import,
and a download whose files never appear ends up visibly blocked with a message
naming the path rather than silently stuck forever.

**Library scans understand dual-format folders.** Several bugs stacked into one
very stubborn symptom: an audiobook that would not attach no matter how many
times you scanned. A scan claimed a book the moment any one file matched it, so
a folder holding an epub and an m4b attached one and left the other for the
next scan. The "already tracked" shortcut for an audiobook's sibling tracks was
absorbing every file in the folder, including the ebook. And an m4b whose Artist
tag carries a contributor list — the author plus their translator and narrator,
which is simply how Audible tags things — had that tag overwrite the author your
folder names had already resolved correctly, leaving a string no author in your
library could ever match. Those files came back Unmatched on every single scan,
forever. When a file still does not match, the Unmatched table now records why,
per file, instead of giving everyone the same advice to go refresh an author who
was never the problem.

**Imports also stopped failing on long non-ASCII titles.** A Japanese, Chinese,
Korean, Russian, Greek or heavily accented title of roughly 83 characters could
kill an import with "file name too long", because the path limit counted
characters while filesystems count bytes and one CJK character is three or four
of them.

**Adding one book adds one book.** Authors → Add Book created the author and
then quietly pulled in their entire bibliography behind it, which for one
reporter turned 75 books into over 500. Refreshing an author did the same even
with monitoring off. A refresh now refreshes in place, and the author page says
how many works it declined to add and why. An author with no books at all is
still populated, so bulk Refresh keeps repairing imports that landed an author
without a catalogue — but an author you deliberately emptied stays empty. Telling
those two apart needed a new column, so this release carries a schema migration
(`075_author_catalogue_populated`); it applies at startup and needs nothing from
you.

**Hardcover "Sync now" finishes.** It used to run inside the web request, so the
server's own sixty-second timeout cut it off partway; on a 1,660-book shelf that
meant roughly a third of the books imported and the rest died. It never
announced any of this. The sync is a background job now, the button answers
straight away, and the import list row shows progress and result. The nightly
cadence, hardcoded since it shipped, is a setting.

**rTorrent and ruTorrent are supported** as a download client alongside
qBittorrent, Transmission and Deluge, over both the HTTP XML-RPC endpoint that
ruTorrent and seedbox panels expose and rTorrent's own SCGI socket. Two protocol
limits are documented rather than papered over: rTorrent has no per-torrent
ratio limit over XML-RPC, so per-indexer seed-ratio overrides are ignored, and
"remove with data" deletes from Bindery's side because rTorrent leaves the files
in place by design.

Riding along, three things that exist mostly to make the next bug report easier:
logs download as a plain text file from Settings → Logs, filtered exactly the way
the screen was filtered and with keys and tokens redacted, so people on rootless
containers can attach them without a shell. Picking Hardlink on a setup that
cannot hardlink now says so inline where you make the choice, which is the usual
ending of "hardlinks are broken" support threads. And the `…` in the pager is a
dropdown instead of decoration, so page 30 of 40 is one click rather than
thirty. While wiring the first of those up it turned out the date pickers in the
Logs tab had been sending a timestamp the API could not parse, so narrowing a
log search by date had been doing nothing at all.

### Added

- **rTorrent / ruTorrent download client** (#1618) — rTorrent is now a first-class download client alongside qBittorrent, Transmission and Deluge: grabbing (magnet and `.torrent`), live queue status, label handling via `d.custom1`, import of completed torrents using rTorrent's own file list, removal with or without the data, and a Test button that checks both the connection and whether Bindery can actually read where rTorrent writes. Both transports work: the HTTP(S) XML-RPC endpoint ruTorrent and seedbox panels expose (`/RPC2`, or `/plugins/rpc/rpc.php`), and rTorrent's native SCGI listener over TCP or a unix socket for plain installs — set the URL Base to `scgi://`, `scgi://host:port`, or `scgi:///path/to/socket`. Two protocol limits are documented in QUICKSTART: per-indexer seed-ratio overrides are ignored (rTorrent has no per-torrent ratio limit over XML-RPC), and "remove with data" deletes the files from Bindery's side because `d.erase` leaves them on disk by design — resolving the path through the client's remap or the global `BINDERY_DOWNLOAD_PATH_REMAP`, and refusing anything reached through a symlink or resolving to a mount root. A seeding torrent whose tracker is grumpy shows as seeding rather than an error, and the client form warns when an `scgi://` URL Base means the username, password or Use SSL you filled in will be ignored.

- **Download logs from the UI** (#1903) — a **Download** button next to *Clear filters* in `Settings → Logs` saves the entries matching the current filters as a plain-text file (`bindery-logs-<timestamp>.txt`) you can attach to a bug report, no container shell needed. The file records which filters were actually applied, one entry per line; API keys, tokens and line breaks are stripped from every part of the line, and an export stops at 50,000 entries and says so. On installs with no persistent log store the file names the filters it could not apply instead of implying it honoured them. Admin-only, like the rest of the Logs tab.

- **Configurable Hardcover list sync interval** (#1848) — the Hardcover import-list sync no longer runs on a hardcoded 24h cadence. Settings → General now has a "Hardcover list sync interval" picker (1h to 7 days, 24h default), validated server-side and applied on the next restart like the wanted-search interval.

- **Jump straight to a page in long lists** (#2010) — the `…` in the pager was inert, so reaching page 30 of a 40-page Books or Authors list meant clicking Next thirty times. Each `…` is now a dropdown listing exactly the pages it hides, so any page is one click away. Every paginated screen shares the component, so Wanted, History, Queue, Blocklist and the Logs tab get it too.

- **Third-party license attribution ships with every artifact** (#1989) — the new
  `THIRD_PARTY_LICENSES.md` lists every Go module linked into the binary and every
  npm package embedded in the web UI, reproduces the NOTICE files carried by the
  Apache-2.0 dependencies (`coreos/go-oidc` and the four Prometheus modules), and
  includes each distinct license text once. It is included in the release archives
  and copied into the container image at `/THIRD_PARTY_LICENSES.md`, alongside
  `/LICENSE`. The file is generated by `make licenses`, and CI fails the build if a
  dependency change lands without regenerating it.

- **Privacy policy** — `PRIVACY.md` documents the telemetry ping in one place:
  what the payload contains, that IP addresses are used only for rate limiting
  and never stored or logged, the retention windows (60 days for install rows,
  400 for the activity ledger, aggregates indefinitely), the GDPR legal basis,
  and how to have an install row deleted. Nothing about telemetry behaviour
  changed — this documents what the code already does.

- **Contributions now carry a Developer Certificate of Origin sign-off** — every
  commit in a pull request needs a `Signed-off-by` trailer matching its author,
  added automatically by `git commit -s`. The DCO is a one-line assertion that
  you wrote the change and may submit it under Bindery's MIT licence; there is
  no CLA, no form, and no transfer of rights. `CONTRIBUTING.md` now also states
  explicitly that contributions are accepted under the same MIT licence Bindery
  ships under. A new `DCO / Sign-off` check enforces this on pull requests,
  exempting merge commits, bot commits, and anything authored before
  2026-08-20 — so the PRs already open are unaffected.

### Changed

- **GPL-3.0 dependency removed from the release binaries** (#1988) — series and
  title matching used `github.com/creditx/go-fuzzywuzzy`, which is licensed
  GPL-3.0. Because Go links statically, every published binary and every
  `ghcr.io/vavallee/bindery` image was a combined work with that code while
  `LICENSE` and the README offered MIT, which is not a licence we could grant
  for the combined work. Anyone redistributing Bindery on the stated MIT terms
  was exposed. The four similarity ratios are now a first-party implementation
  with no new dependency, so the binaries are genuinely MIT again.

  Match quality is unaffected: three of the four metrics reproduce the previous
  scores exactly, and across 19,306 pairs of real series and book titles no pair
  crossed any of the score thresholds that decide whether books are linked,
  deduplicated or created.

- **OPDS reading apps now fetch covers from Bindery, not from the metadata
  provider** — the catalogue feed emitted the provider's own image URL, so every
  KOReader or Moon+ Reader client hot-linked Hardcover's CDN directly, once per
  client, with none of the local caching the web UI has had since the image
  proxy shipped. Covers now come from `/opds/images`, the same handler and the
  same `<dataDir>/image-cache/` the browser uses, which is also what Hardcover's
  API rules require of any deployment that isn't a personal one. The new
  `docs/third-party-data.md` records those rules, including that a commercial
  deployment must exclude `average_rating` and `ratings_count` because they are
  aggregated user ratings rather than facts about a book.

- **"Monitor newly discovered books" → *Don't add them*** (#1815) — the setting that used to add refresh-discovered works as unmonitored rows now doesn't add them at all, which is what the people asking for it were describing. Library imports (Calibre, Audiobookshelf) already set it on every author they create — so for an imported library the first "Refresh metadata" now updates the books you have and adds no catalogue. Turn *Monitor newly discovered books* back on for an author whose full bibliography you do want.

- **The unmatched-files hint now tells you what actually failed** (#1958) — when
  files are found but nothing matches, Bindery used to give one answer: populate
  the author's book catalogue and refresh the author. That is right for exactly
  one of the four ways a file can miss. The scan now records a reason per file
  and shows it in the Unmatched files table, and when the parsed author matches
  no author in your library at all the hint names that author and points at the
  file's tags and folder name instead of sending you to refresh an author who
  was never the problem. A file whose name and tags yield no title at all now
  says so instead of claiming no book matched a title that was never there.

### Fixed

- **Downloads no longer fail to import while they are still downloading** (#1884) — with qBittorrent's temp/incomplete directory enabled, a grab could be declared "download completed" a fraction of a second after being sent, imported against a final save path qBittorrent had not created yet, and permanently failed about a minute later with a `PathRemap` error against a path mapping that was perfectly correct. A just-added torrent briefly reports 100% progress, and the completion check accepted any single signal like that with nothing to corroborate it; qBittorrent's own state and its outstanding-bytes counter now override it, so nothing is imported until the client says the payload is complete and in place.

- **The import retry budget is no longer spent on downloads that have nothing to import** (#1884) — three attempts fifteen seconds apart could never outlast a real download, so one early misfire permanently blocked a healthy grab. A retry is now only counted when the files are actually on this host; a download waiting on files stays visible in the Queue with the real reason and imports itself the moment they appear. The budget for genuine failures went from 3 attempts to 5, which also gives the startup sweep for interrupted imports two more attempts before it gives up on a download.

- **A download whose files never turn up is no longer stuck in the Queue forever** (#1884) — waiting costs no retry attempts, so a download the client keeps calling complete while nothing is on disk would have waited indefinitely: no automatic path would touch it, and re-grabbing the release was refused. After about thirty minutes of finding nothing it is now marked **Import blocked** with a message naming the path it checked and what to do about it, which both re-enables **Retry import** and lets you grab the release again.

- **"Download path not found" no longer blames PathRemap** (#1884) — the message asserted a path-mapping problem, which sent a reporter auditing a mapping that was correct while the real cause was that the download had not finished. It now states what was missing and lists the three things that actually cause it: the download still finishing, the files having been moved or deleted, and a genuine mount mismatch.

- **A deleted book's leftover torrent no longer produces three opaque import failures** (#1955) — the download client's file list describes what the *torrent* contains, not what is on disk, so a torrent whose files were moved into the library or deleted with the book still reported them. Bindery believed it had files to work with and failed deep inside the import instead of saying the files were gone. Files the client lists are now checked against the filesystem first.

- **"Already grabbed" no longer traps a release with a blocked download** (#1955) — after an import was terminally blocked, every Grab of that release answered `already grabbed` with no way forward from the search page. A blocked download now releases the release for a fresh grab (reusing the same Queue row), and every other "already grabbed" refusal names the state the existing download is in and where to act on it.

- **Importing one format no longer abandons the other format's download** (#1885) — on a book tracked for both an ebook and an audiobook, the "book already in library" short-circuit compared against the whole book row, so an audiobook that imported while the ebook torrent was still downloading marked the ebook download as imported and dropped it silently. The check is now scoped to the format slot the download is actually filling; when the format can't be determined it no longer short-circuits at all, so the download follows the normal retry path and the missing format stays eligible for a re-search.

- **An audiobook that ships with a PDF booklet is no longer treated as an ebook** (#1885) — release parsing keeps the first format token it recognises, and it looks for ebook formats first, so a title like *(Unabridged) [M4B + PDF]* was recorded as a PDF. On a book tracked for both formats that let an audiobook grab close itself out against the already-imported ebook. Which slot a download is filling is now decided from every format the release names, and a release naming both with nothing to tell them apart is treated as unknown rather than guessed.

- **A dual-format folder now attaches both files in one library scan** (#1957) —
  a scan claimed a book the moment any one file matched it, so a folder holding
  `Title.epub` and `Title.m4b` attached one and left the other in Unmatched
  until you ran a second full scan. Claims are now per format, which is how
  book files are stored anyway. Two files of the *same* format still can't both
  claim one book. An ebook sitting next to an already-attached audiobook is no
  longer skipped as "already tracked" either — that shortcut now only absorbs
  the audiobook's own sibling tracks, plus the supplement files an audiobook
  release ships (a companion PDF, liner notes, a stray `.txt`), which are never
  attached as the book's ebook edition. A PDF in a folder with no audio in it is
  still an ebook.

- **Audiobooks tagged with a contributor list now match on library scan** (#1956)
  — an m4b whose Artist tag names the author plus their translator and narrator
  ("Álvaro Enrigue, Natasha Wimmer - translator, Gabriel Porras") could never
  reconcile: the tag replaced the author your `Author/Title/` folders had
  already resolved correctly, and no author in your library matches a whole
  contributor list, so the file returned to Unmatched on every scan. The tag is
  still preferred when it matches, but it no longer destroys the folder author
  — when the tag matches nobody, the scan falls back to the folder author and
  then to the credited names in the list — every one of them that matches an
  author you have, so a book catalogued under the second name is still found.

- **Books with long non-ASCII titles no longer fail to import** (#1982) — a
  Japanese, Chinese, Korean, Russian, Greek or heavily accented title of roughly
  83 characters or more could kill the import with "file name too long". The
  importer was limiting each name to 200 *characters*, but filesystems count
  *bytes*, and one CJK character is three or four of them. The limit is now 200
  bytes, cut on a character boundary so a name never ends in half a character.
  Titles in plain ASCII are unaffected — nothing already in your library gets
  renamed.

- **Adding one book no longer imports the author's back catalogue** (#1816) — Authors → Add Book created the author and then quietly pulled their entire bibliography in behind it ("my collection just went from 75 imported books to over 500"). The picked book is now the only book added. When the provider's book endpoint fails, the fallback asks the author endpoint for that one work rather than the whole catalogue — and, like the direct add it stands in for, it is not vetoed by the strict media-type policy or the metadata profile's language filter, which used to make an explicitly-picked audiobook or translation impossible to add.

- **A metadata refresh no longer grows the library behind your back** (#1815) — refreshing an author with monitoring off, or with *Monitor newly discovered books* set to don't add them, updated the covers, ratings, genres and series links of the books you have and then inserted every other work the author ever wrote. It now refreshes in place and adds nothing; the author page reports how many works it declined to add and why. An author who has never been populated is still filled in, so bulk **Refresh metadata** keeps repairing imports that landed an author without a catalogue — but an author whose books you deleted, or excluded, stays as you left them. Migration `075_author_catalogue_populated` adds the column that tells those two cases apart.

- **Hardcover "Sync now" no longer stops at 60 seconds** (#1854) — a manual list sync ran inside the HTTP request, so the server's request timeout cut it off partway: on a 1,660-book shelf that meant roughly a third of the books imported and the rest lost to `context deadline exceeded`. The sync now starts as a background job and the endpoint answers immediately; the import list row shows the run's progress and its result, and one sync runs at a time. Shutting the server down stops an in-flight sync at the next book rather than letting it race the database closing, so the run ends early and reports `context canceled` instead of recording a successful sync it did not finish.

- **A panicking background job no longer takes the whole process down** (#1967) — every tracked job (Audiobookshelf import, Grimmory, manual library scan, the startup syncs) ran without a panic handler, so an unexpected nil anywhere inside one killed the server rather than failing that job. Jobs now recover, record the failure against the job row with its stack in the log, and leave everything else running. The gap only became visible when the Hardcover sync moved out from behind the HTTP handler that had been absorbing it.

- **Import Mode now warns when Hardlink can't hardlink** (#1720) — picking **Hardlink** on a setup where the download folder and library are on different filesystems used to look like it worked while imports quietly fell back to copying; the usual cause is separate Docker volume mounts that look like sibling paths. The selector now shows the amber warning and the specific reason inline under the mode buttons, where the choice is made, instead of only in the Storage section further down. **Auto** is unaffected — it already picks per download.

- **Hybrid v1/v2 magnet links are no longer refused** — a magnet listing its `urn:btmh:` topic before `urn:btih:` read as having no infohash at all, so rTorrent refused the grab outright and qBittorrent could not recover the hash from an "already present" reply. Every `xt` topic is now examined.

- **Log date-range filters were silently ignored** (#1903) — the From/To pickers in `Settings → Logs` sent a zone-less timestamp the API couldn't parse, so narrowing the range did nothing. They now send a real instant in your local time zone.

- **A To-only log date range produced a file that didn't match the table** (#1903) — setting only *To* left the on-screen table unbounded below while the download covered just the hour before *To*. The export now defaults its range on the same condition the table does.

### Security

- **A malicious torrent file could crash Bindery** — the bencode walk that
  extracts a torrent's infohash recursed once per nesting level with no bound,
  so a crafted `.torrent` of deeply nested lists exhausted the goroutine stack
  and took the whole process down. A stack overflow is not a recoverable panic,
  so the container restarted and the grab retried into another crash. Nesting is
  now capped at 64 levels (a real torrent nests 3-4) and an over-nested file is
  refused like any other malformed payload. Reachable from any indexer serving
  the response to a grab.

- **Go toolchain updated to 1.26.6** — picks up fixes for six Go standard
  library advisories affecting `net/http`, `crypto/tls`, `net/url`,
  `encoding/xml` and `encoding/asn1` (GO-2026-6218, GO-2026-6090, GO-2026-6089,
  GO-2026-6088, GO-2026-5972, GO-2026-5026). Applies to the release binaries and
  the Docker image alike.

## [v1.30.4] — 2026-08-13

One fix, and it is the reason to upgrade straight away: v1.30.1 through v1.30.3
could leave Bindery unable to start at all, on every restart, with no way back
in.

Migration 72 rebuilds two Calibre tables, and rebuilding a table in SQLite means
turning foreign-key enforcement off for the duration — so the migration runner
verifies referential integrity before it commits. That part is right. What was
wrong is the scope of the check: it asked SQLite about the *entire database*
rather than the two tables the migration had just rebuilt. Any orphaned row
anywhere, including drift that predated the migration by months, failed it.

That drift had a source. Until #1727, connection-pool replacements ran without
`foreign_keys` set, so the `ON DELETE CASCADE` rules the schema declares quietly
stopped firing on long-running instances and orphan rows accumulated. #1727
stopped new drift but never cleaned up what had already collected, and migration
72 is where the bill arrived. The unpleasant part is who it selected for: the
longer an instance had been running, the more orphans it carried, and the more
certain it was to fail. The oldest and largest libraries were the most exposed.

Nobody lost data. The migration runs inside a transaction and rolled back every
time, so affected databases are intact — they simply could not be opened by
v1.30.1 or later. This release opens them, and adds two offline commands for
inspecting and clearing the leftover drift on an instance that cannot start.

### Fixed

- **Bindery would not start after upgrading to v1.30.1–v1.30.3**
  ([#1972](https://github.com/vavallee/bindery/issues/1972),
  [#1974](https://github.com/vavallee/bindery/pull/1974)) — the instance exited
  on every restart with `migration 72: foreign_key_check found N violation(s)`.
  Migration 72's integrity check scanned the whole database instead of the two
  Calibre tables it rebuilt, so orphan rows left over from the pre-#1727
  foreign-key drift — accumulated long before that migration existed — aborted
  the upgrade. Longer-running instances were the most likely to hit it. The
  runner now compares per-table violation counts from before and after the
  migration and fails only on violations the migration itself introduced;
  pre-existing ones are logged with their table names and counts and no longer
  stop the instance. A migration that genuinely corrupts what it rebuilds still
  aborts and rolls back, as before.

### Added

- **`bindery db-check` and `bindery db-repair`**
  ([#1972](https://github.com/vavallee/bindery/issues/1972)) — offline database
  integrity tooling that runs without applying migrations, so it works on an
  instance that cannot start. `db-check` lists every row whose foreign key points
  at a missing parent and changes nothing; `db-repair --yes` replays the delete
  rule the schema declares (`ON DELETE CASCADE` rows removed, `ON DELETE SET
  NULL` references cleared, anything else skipped and reported) and prints what
  it did. `--yes` is required; without it the command refuses and names the file
  to back up. Also reachable as `BINDERY_DB_FK_CHECK=report|repair` for setups
  where the container command cannot be edited. See
  [Troubleshooting](docs/Troubleshooting-Wiki.md).

  ```bash
  docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-check
  docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-repair --yes
  ```

## [v1.30.3] — 2026-08-12

Six fixes. Three of them are one story: a book's media type records what Bindery
was told to go and fetch, but the book detail page was using it to decide what
to show you about the files already on disk. A book can legally hold both an
ebook and an audiobook, and when the two disagreed the page put an Audiobook
badge next to an epub's path, gave the second file no surface at all, and —
the part worth upgrading for — offered a Delete button that removed both
formats while naming one of them. The File section is now a list of what is
actually there, and every delete says exactly which paths it will remove.

The other three are things Bindery was doing repeatedly and silently: work it
had already done, requests it had already been refused, and a field it was
confidently reporting wrong.

The one most likely to be felt is the author refresh. Before queuing a search
for each book it creates, the sync checked whether the file was already on disk,
and that check walked the entire library — once per book. On local disk the OS
caches the directory tree and it hides; on a NAS mount it does not, and a
65-book refresh spent close to an hour doing nothing but re-reading the same
directories. It now walks each library root once per refresh.

The other two came from users noticing something odd and looking closer. An
indexer that answers "request limit reached, retry in 485 minutes" was being
asked again on the very next search, and every search for the following eight
hours, because nothing recorded what it had said. And a book whose file was
Spanish displayed as English forever, because the language shown came from the
metadata provider's description of the *work* and the file's own tag was only
ever read when the provider had supplied nothing.

### Fixed

- **The book detail page now lists every file on the book, and a delete can no
  longer remove a format you were not shown**
  ([#1948](https://github.com/vavallee/bindery/pull/1948)) — `media_type`
  records acquisition intent, what search and monitoring are told to hunt for.
  `book_files` records inventory, what is on disk. The File section rendered
  inventory through the intent value, so any file outside the declared type was
  invisible to display but still included in destruction. A book marked
  `audiobook` that also held an epub showed a 🎧 Audiobook badge (from the media
  type) next to the epub's path (from the legacy `file_path` column, which was
  kept ebook-first regardless of media type); the audiobook itself had no row at
  all, because the format switcher only appeared for books already marked
  dual-format; Download sent no format and served the epub; and Delete file sent
  a format-less `DELETE`, which enumerates every registered file and removes
  both formats, while the dialog named one path and described it as the other
  format's. The badge and the path came from different sources, and the
  confirmation and the request disagreed.

  The section is now a list built from the files themselves, grouped by format,
  each group badged by its own format and nothing hidden behind the declared
  type. Download and Delete live on the format group and are always scoped to
  it, which is the honest unit: both endpoints act on every file of that format,
  plus, for delete, the same-name sibling sweep. The confirmation dialog lists
  every path the request will remove and is built from the same state the
  request is, so the two cannot drift apart again. A format-less delete is still
  available but only as an explicit **Delete all files** action, and its dialog
  lists every path across both formats. **Fix match** now moves the file whose
  row you opened rather than whichever format the switcher was on, and the
  switcher is gone: hiding one format behind it is what made a registered file
  invisible in the first place. One thing the switcher did carry is kept: a
  format the book wants but has no file for still shows as **Not downloaded**,
  so a dual-format book with one file on hand says which half it is still
  waiting on. Books that predate the `book_files` migration and were never
  re-imported still render from the legacy columns, and because that legacy
  single path carries no format of its own, its Download and Delete deal with
  it as the book's only file rather than guessing a format the server might
  disagree with.

  Two smaller things fell out of the same work. A new per-file **Forget this
  file** action drops a stale path from Bindery's records without touching disk
  — the database-only mode added in
  [#1692](https://github.com/vavallee/bindery/issues/1692) had no interface at
  all until now, which is what you want when a file has already been moved or
  removed elsewhere and the old path is still being reported. And the media
  badge was a two-way ebook/audiobook check, so a dual-format book displayed as
  "📖 Ebook"; it now renders both.
- **A book holding both formats now declares itself dual-format**
  ([#1946](https://github.com/vavallee/bindery/pull/1946)) — the display fix
  above makes the page correct whatever the media type says, but the media type
  was also simply wrong, and it is what search and monitoring read. When both an
  ebook and an audiobook are registered against a book, its media type is now
  widened to `both` on the next file event, because a file on disk settles the
  question of what the book is. This is driven by inventory and is deliberately
  independent of the metadata-driven widening pinned in
  [#1732](https://github.com/vavallee/bindery/issues/1732): that pin exists
  because Hardcover lists an audio edition for most popular titles, so widening
  from metadata alone was widening on a claim. Here the audiobook is already
  imported. Widening only ever fires when both files are present, so it cannot
  flip a book back to wanted or start a download. Affected books heal on their
  next import, delete, rename, or library reorganize; nothing runs at upgrade.

  Two related corrections ride along. The legacy `file_path` column, which is
  what the format-less download endpoint and OPDS serve, now prefers the path
  matching the book's media type instead of always taking the ebook. And the
  book list's `mediaType=both` filter now works: the `ebook` and `audiobook`
  filters deliberately include dual-format books, so neither of them isolated
  them, and the literal value `both` fell through unhandled and returned the
  entire library. The Books page has a **📖🎧 Both** button to match.
- **Editing an unmonitored book no longer starts a download**
  ([#1947](https://github.com/vavallee/bindery/pull/1947)) — the book update
  endpoint fires an immediate indexer search whenever a book crosses into
  `wanted`: a status edit, a "Delete file", or a media-type change that exposes
  a format it does not have
  ([#1148](https://github.com/vavallee/bindery/issues/1148)). The only thing
  guarding that was the global auto-grab kill-switch, so widening a book to
  dual-format grabbed the missing format even when the book was explicitly
  unmonitored — the one per-book control for "keep track of this, do not go and
  get it". It now honours `monitored`. The status still changes and the book
  still appears on the Wanted page; only the search is suppressed, and it runs
  as normal once you monitor the book. The twelve-hour wanted scan already
  honoured this, so nothing there changes.
- **An author sync no longer walks the entire library once per new book**
  ([#1888](https://github.com/vavallee/bindery/issues/1888),
  [#1929](https://github.com/vavallee/bindery/issues/1929)) — before queuing a
  search for each book it creates, the sync checks whether the file already
  exists on disk, and that check did a full recursive walk of every library
  root, per book. A sync that added 65 books walked the whole library 65 times.
  On local disk the OS caches the directory tree and the cost hides; on an NFS
  or SMB mount every walk is real network round trips per directory entry, and
  at a few dozen seconds per walk this alone accounts for the reported
  hour-long refresh. The sync now takes one snapshot of the library per
  refresh: each root is walked once, on first use, and every per-book check is
  answered from memory with the same matching rules as before — same root
  selection per media type, same author-folder pre-filter, same title and
  author comparison, in the same order. The walk also now honours cancellation,
  which it previously ignored, so deleting an author mid-refresh stops the
  filesystem work instead of letting it run to completion. One-off checks
  (adding a single book, series add, recommendations) keep their per-call walk
  and see the library exactly as it is at that moment; only files copied in by
  hand while a refresh is mid-flight are invisible to that refresh's snapshot,
  and the next refresh sees them.
- **A rate-limited indexer is left alone until it says to come back**
  ([#1934](https://github.com/vavallee/bindery/issues/1934)) — when an indexer
  answers a search with a Newznab 500 (`Request limit reached. Retry in 485
  minutes.`) Bindery used to record nothing, so the next search and every search
  for the following eight hours sent it another request it had already refused.
  The rate-limit classification existed but was consulted only inside a single
  search, to stop the query cascade falling through to lower tiers. The retry
  hint is now parsed out of the indexer's own message and that indexer is
  skipped until the deadline passes, across the scheduled wanted scan, on-add
  and bulk searches, and interactive search alike — they share one searcher, so
  a limit hit by one is respected by all of them. An indexer that gives no hint
  gets an hour; a parsed hint is clamped to between a minute and a day so a
  malformed or absurd value cannot bench an indexer indefinitely. Editing the
  indexer clears the hold immediately, so a new API key or a different account
  takes effect on the next search rather than waiting out a lockout that
  belonged to the old configuration. Interactive search reports the held indexer
  as skipped with the deadline in the Search details panel, in the same place
  the original error appeared, rather than dropping it from the list.

  Two related gaps are deliberately untouched here and tracked separately:
  authentication failures (a suspended account, a revoked key) get no cooldown,
  because they never heal on a timer and a user who fixes their key must see it
  work immediately — those need visibility and a notification
  ([#1935](https://github.com/vavallee/bindery/issues/1935)) — and auto-grab
  still decides from whatever indexers answered without recording that the
  others were unreachable
  ([#1936](https://github.com/vavallee/bindery/issues/1936)). The cooldown is
  held in memory, so a restart costs one refused request per indexer per search
  before it re-learns the limit, which is exactly the old behaviour and never
  worse.
- **A book whose file is in a different language than the catalogue says now
  gets corrected on import**
  ([#1933](https://github.com/vavallee/bindery/issues/1933)) — the language on a
  book page came from the metadata provider, which describes the abstract
  *work*, not the file you actually hold. The embedded `dc:language` tag was
  read only when the provider had supplied nothing at all, so a Spanish EPUB
  imported against an English OpenLibrary record displayed "English"
  indefinitely, with the release name buried in a history row the only hint
  otherwise. The tag is now read on every EPUB import and, when it disagrees
  with the stored value, the file wins: a work has editions in many languages,
  but the file on disk is one specific edition and is the thing you open. The
  correction is recorded as a **Language Corrected** history event showing both
  codes, so "why does my English book read as Spanish" has an answer in the
  place you would look for it rather than only in a log line.

  Precedence is user, then file, then provider. A language you set by hand locks
  the field, and a locked field is left alone — the EPUB is not even opened.
  Filling a language the catalogue never had is unchanged and stays silent: it
  is a gap being filled, not a disagreement, and OpenLibrary routinely supplies
  no work-level language at all. Comparison normalises both sides first, so a
  provider's `en` against a file's `eng` is not mistaken for a conflict, and
  nothing is written when an import fails — a book that never landed must not
  rewrite its own catalogue entry.

### Internal

- **Race guard on the metadata test doubles**
  ([#1924](https://github.com/vavallee/bindery/issues/1924)) — `mockProvider`'s
  call recorders were written from several goroutines once the author refresh
  started fanning out, which the race detector caught intermittently. Test-only;
  no runtime behaviour changes.
- **Base image digests bumped** for the `telemetry-server` and `discord-stats`
  helper containers ([#1925](https://github.com/vavallee/bindery/pull/1925),
  [#1926](https://github.com/vavallee/bindery/pull/1926)). The Bindery image
  itself is unchanged.

## [v1.30.2] — 2026-08-11

A maintenance release, and most of it is the same shape: something that had
been quietly not working, in a way that looked exactly like it working.

The biggest one is a tenancy bug. On a multi-user install, every book an author
sync created was written with no owner, which per-user scoping reads as
"shared" — so one user's whole catalogue was listed for every other account.
A migration repairs the rows already written. If you run multi-user, expect
books to disappear from other people's views on upgrade; that is the fix.

The rest came out of a bug sweep. Books were being dropped from a catalogue with
nothing to show for it beyond a debug log line — one reporter lost 65 books from
a single author and only found out by going looking. Import notifications were
being rejected by Apprise before they were ever dispatched, because of a payload
key whose name collides with one Apprise reserves. A book search would stop at
the first indexer response even when that response was unrelated, and never try
the queries that would have found the book. Hardcover search results could still
file a book under its narrator. An author refresh was asking OpenLibrary for the
same URL twice per work and doing every request one at a time.

Three of these were reported by users rather than found in the code, and one was
fixed by an outside contributor.

### Added

- **Sortable column headers on the Authors page**
  ([#1349](https://github.com/vavallee/bindery/issues/1349)) — Name, Books,
  Rating and Monitored are now clickable, ascending on the first click and
  descending on a second, matching the Books page. This completes #1349, whose
  Books half shipped in v1.28.0 while the Authors half never did. Sort keys are
  whitelisted server-side and every new sort carries a name tiebreaker, so ties
  cannot shuffle rows between pages of a paginated list.
- **Optional broad indexer categories**
  ([#1571](https://github.com/vavallee/bindery/issues/1571)) — an indexer can
  now opt in to searching the Newznab Books (`7000`) or Audio (`3000`) parent
  category alongside its configured subcategories, which recovers releases from
  trackers that file things loosely instead of under a specific child. The
  parent is only ever added for a media type the indexer already carries
  subcategories under, so a books-only indexer never gets an audio query, and
  indexers with non-standard taxonomies (MyAnonaMouse-style `100xxx` IDs) are
  left alone. Off by default — broad categories also return comics, magazines
  and music. Set it when adding or editing an indexer; Prowlarr syncs preserve
  the choice.
- **An author refresh now says which books it skipped, and why**
  ([#1889](https://github.com/vavallee/bindery/issues/1889)) — the catalogue
  sync already counted the works it dropped, but the counts only ever reached a
  Debug log line per book plus one Info summary, so an author whose catalogue
  had been filtered down to a handful looked exactly like an author who only
  wrote a handful. One reporter lost 65 books from a single author to the
  allowed-languages filter and found out only by going looking in the logs,
  which a rootless container does not hand them. The author detail response now
  carries a `lastSync` summary — works returned, books added, and how many each
  filter dropped — and the author page shows a note above the book list naming
  the language set that was applied, whether the profile also rejects works with
  no reported language, and a few of the dropped titles. The run's summary log
  line moves from `INFO` to `WARN` when anything was skipped, so it also shows
  up in Settings → Logs at the default level. Nothing about the filtering
  changed: a metadata profile set to reject unknown languages still rejects
  them, it just no longer does it silently. The summary is kept in memory, so it
  reports syncs this process has run rather than surviving a restart.

### Fixed

- **Books created by an author sync now belong to the user who added the author**
  ([#1872](https://github.com/vavallee/bindery/issues/1872)) — `CreateForUser`
  wrote `owner_user_id` to the `authors` row but never set it on the struct it
  returned, so the catalogue sync stamped owner `0` onto every book it created.
  A `0` owner is stored as NULL, which per-user scoping reads as "shared", so on
  a multi-user install one user's whole catalogue was visible to every other
  account. The repo now reflects the persisted owner back onto the author, and
  the sync re-reads the author row before its insert loop so a stale snapshot
  can no longer carry the wrong owner into new books. Migration
  `074_backfill_book_owner_from_author.sql` repairs the rows already written: a
  NULL-owned book under an owned author inherits that author's owner. Books
  under a NULL-owned author are left alone, so deliberately shared content and
  pre-multi-user libraries are untouched. On a multi-user install this will
  remove books from other users' views — that is the fix working. The issue was
  reported as the allowed-languages filter dropping books; the language filter
  was not involved.
- **Import and upgrade webhooks reach Apprise again**
  ([#1886](https://github.com/vavallee/bindery/issues/1886), thanks @nathang21)
  — `bookImported` and `upgrade` payloads carried the media format under a
  `format` key, but Apprise's REST API reserves `format` for the *body markup*
  and accepts only `text`, `html`, or `markdown`. It rejected `ebook` and
  `audiobook` with HTTP 400 before dispatching anything, so an Apprise relay
  delivered every grab, failure, and health notification — none of which carry
  a `format` — and silently dropped every successful import. The reserved key
  is now omitted for Apprise targets only, identified by a `/notify` path
  segment in the webhook URL. **No other consumer is affected**: ntfy, Home
  Assistant and Discord-proxy relays still receive `format` exactly as before,
  so existing templates keep working. Every payload also carries the same value
  as `mediaFormat`, which is never stripped, so an Apprise template has a key to
  read and anyone else can migrate at their own pace. The report diagnosed this
  as an empty `body`; the body was in fact populated, and the reserved key was
  the real reason for the 400.
- **Author refresh no longer spends every metadata round trip in sequence**
  ([#1888](https://github.com/vavallee/bindery/issues/1888)) — a refresh that
  added 65 books for one author took close to an hour. The catalogue sync loop
  was not the problem: the cost was in the three per-work enrichment phases that
  run *before* it, each a strictly serial walk of the whole work list. A 65-work
  author paid 195 upstream round trips one after another before the first book
  row was written, so any slow or timing-out provider multiplied straight into
  wall clock. Two of those phases were also asking OpenLibrary for the *same
  URL* twice: the work-language sampler (#891) and the work-cover sampler
  (#1748) both fetch `/works/{id}/editions.json?limit=5` and each kept its own
  cache. They now share one sample, which halves OpenLibrary requests for the
  pass — measured at 130 → 65 requests for a 65-work author — and the sampling
  and cover-enrichment passes run four works at a time instead of one, matching
  the pace already used elsewhere for provider fan-out. On a 65-work author with
  a 20 ms provider the sampling pass drops from 1.31 s to 0.35 s; against a real
  provider, where a round trip is seconds rather than milliseconds, the saving
  scales with it. Per-book Hardcover edition hydration inside the sync loop is
  still serial and is tracked separately, and the original report has not yet
  been confirmed fixed.
- **A junk indexer response no longer ends a book search early**
  ([#1891](https://github.com/vavallee/bindery/issues/1891)) — an indexer search
  runs a cascade of increasingly specific queries and stops at the first one
  that works, but only the structured `t=book` query checked that what came
  back was actually about the book. The freeform tiers stopped on any response
  at all, so an indexer answering "author surname + title" with unrelated
  releases ended the cascade there, the relevance filter then discarded every
  one of them, and the search finished with nothing — never having tried the
  queries that would have found the book. Broad parent categories (#1571) make
  that response much more likely, so an indexer opted in to them could return
  fewer results than a narrow category list. Every tier now has to return
  something plausibly on-target before it stops the cascade, and if none of
  them do, the earliest tier's results are still what comes back. The extra
  queries this can cost are capped at one per cascade: once any tier has
  answered, the broadest query in the ladder (title with no author) is skipped,
  since two more specific tiers have already failed and its results would lose
  to the earlier ones anyway.
- **Hardcover search results no longer file a book under its narrator**
  ([#1892](https://github.com/vavallee/bindery/issues/1892)) — #1733 added a
  contribution-role filter so an audiobook's narrator stops being treated as its
  author, but it covered only the GraphQL book queries. The Typesense search
  documents carry the same `contribution` field and it was never decoded, so
  every search-sourced credit arrived with an empty role, which the filter reads
  as "this is the author", and the first credit won. Hardcover lists the narrator
  first on plenty of audiobook-bearing works, so anything resolved through search
  rather than through a book query kept the pre-#1733 behaviour. The field name
  was confirmed against the live API rather than guessed — a wrong guess would
  have decoded to empty and silently preserved the bug while looking fixed.
- **Calibre rollback previews show edition names**
  ([#1896](https://github.com/vavallee/bindery/issues/1896), thanks
  @floze-the-genius) — created-edition rollback actions looked their edition up
  by scanning the editions of book ID `0`, a row that cannot exist, so the name
  was always blank. They now fetch the edition directly, through the repository
  executor so the lookup still works from inside the rollback transaction.
- **Sortable column headers on the Books page now look sortable**
  ([#1349](https://github.com/vavallee/bindery/issues/1349)) — the Books table
  has supported sorting by author, type and status since v1.28.0, but nothing
  said so: the Sort toolbar above the table offers only title and date, the
  headers had no pointer cursor, and the ▲/▼ marker appeared only on the column
  already in use. An unsorted Type or Status header was indistinguishable from
  plain text, so the feature read as missing. Headers now show a pointer cursor,
  a muted ↕ when inactive, and a "Sort by …" tooltip.
- **The Books column on the Authors page shows a real count**
  ([#1349](https://github.com/vavallee/bindery/issues/1349)) — it rendered `—`
  for every author, in every version that has had the column. The list query
  selected no count and nothing on that path ever set the author's statistics,
  so the field was omitted from the API response entirely and the UI fell back
  to its placeholder. The count is now computed per author and scoped to the
  books the requesting user can see, so it cannot report rows that belong to
  another user.
- **`/authors` now resolves**
  ([#1349](https://github.com/vavallee/bindery/issues/1349)) — Authors is served
  from `/` because it was the first page that existed, while every nav entry
  added later (`/books`, `/import`, `/settings`, …) got a real path. `/authors`
  matched no route, and with no catch-all it rendered the site chrome around an
  empty page rather than a 404. It now redirects to `/`, the same way
  `/blocklist` redirects into Settings.
- **Directory-move containment check now folds case**
  ([#1809](https://github.com/vavallee/bindery/issues/1809)) — on a
  case-insensitive filesystem (APFS, a Windows bind mount) a destination
  differing from the source only in case is the same directory, so the move was
  still able to recurse into its own output. Symlink resolution does not
  normalise case, so the comparison now runs folded as well as exact.
- **A 403 from the CSRF guards now says why in the log**
  ([#1895](https://github.com/vavallee/bindery/issues/1895)) —
  `RequireXRequestedWith` and `RequireCSRFToken` rejected a mutating request
  with a bare `{"error":"forbidden"}` and wrote nothing at any level, not even
  debug. That is what made [#1849](https://github.com/vavallee/bindery/issues/1849)
  so hard to report: a client with a valid API key got a 403 and the container
  log had nothing tied to the request, so the cause had to be read out of the
  source rather than out of a log. Both guards now emit a debug line naming the
  guard that fired, the reason, the method, the path and the peer address, plus
  whether an `X-Api-Key` header, an `?apikey=` parameter (ignored on mutating
  methods, and a common cause of exactly this 403), a session cookie, an
  `X-CSRF-Token` and an `X-Requested-With` header were present. Only presence is
  logged, never a value — no API key, session cookie, CSRF token or
  `Authorization` header content reaches the log. Debug rather than warn because
  this guard also fires on genuine cross-site forgery attempts, and an
  unauthenticated caller controls how often it fires; set the log level to debug
  in Settings → Logs when chasing an unexplained 403.
- **The OPDS guard now checks the API key before the local-only bypass**
  ([#1894](https://github.com/vavallee/bindery/issues/1894)) — no user-visible
  change today; this closes off a repeat of
  [#1849](https://github.com/vavallee/bindery/issues/1849) before it can happen.
  In `OPDSAuth` the `auth.mode=local-only` bypass ran ahead of the `X-Api-Key`
  check, so a request arriving from an address local-only trusts was let through
  without its key ever being verified. That is byte-for-byte the ordering that
  caused #1849 in `auth.Middleware`, where the swallowed key meant a valid
  API-key mutation was rejected `403` by the CSRF header guard. It stays
  harmless in OPDS only because every `/opds` route is a read, so nothing
  downstream cares how the request was authenticated. The moment a mutating
  OPDS route exists, the same client with the same documented credential would
  have started getting the same unexplained `403`. The key check now runs first,
  matching `auth.Middleware`; a missing or wrong key still falls through to the
  bypass exactly as before, so local-only OPDS readers are unaffected.
- **`models.Book.ISBNs` is now `ProviderISBNs`, so it stops looking like stored
  data** ([#1893](https://github.com/vavallee/bindery/issues/1893)) — no
  user-visible behaviour changes; this is an internal rename plus documentation
  and a regression test. The field sat in a struct otherwise full of real
  columns, but there is no `isbns` column and nothing in `internal/db` ever
  wrote or read it: metadata providers fill it in during a search, the
  aggregator uses it to dedup results across providers, and it is empty on
  every book loaded from the database. Anything built on it compiles, reads
  correctly, and matches nothing at runtime — which nearly shipped as the fix
  for the ISBN search criteria in
  [#1724](https://github.com/vavallee/bindery/issues/1724) before the persisted
  ISBNs were traced to `editions.isbn_13` / `isbn_10`. The declaration now spells
  out the field's lifetime and points at `indexer.CriteriaISBN` as the way to get
  a stored ISBN, and a database round-trip test pins the "not persisted"
  property so anyone who later adds a column has to change the test on purpose.
- **The frontend package version no longer poses as a release version**
  ([#1897](https://github.com/vavallee/bindery/issues/1897)) — `web/package.json`
  had been frozen at `1.22.3` since that release while the app shipped 1.30.x,
  because nothing in the release pipeline bumps it. This is metadata only, with
  no user-visible effect: the version shown in the UI comes from the API
  (`/system/status`), which reports the Go build's stamped version, so the app
  never displayed the stale number. The risk was in tooling that reads the file
  — npm banners, the vitest header, an SBOM generated from the frontend tree —
  where `1.22.3` looked like a real, current answer. Rather than adding release
  machinery to bump a number nothing consumes, the field is now pinned to the
  `0.0.0` sentinel, which reads unmistakably as "not a version" and cannot drift
  again. A test keeps it pinned.

### Internal

- **The OpenSSF Scorecard workflow pinned an action SHA that does not exist
  upstream** — `ossf/scorecard-action` was pinned to a commit that is not in
  that repository, so Scorecard's own publish step rejected every run on `main`
  as an imposter commit. A fork's commit stays reachable through the upstream
  repository's shared object store, so the pin resolved and the action ran while
  looking properly pinned. Repinned to v2.4.4 at a SHA resolved from the tag.

## [v1.30.1] — 2026-08-11

The onboarding half of this release is about the same blind spot: until now the
only evidence that first-run setup had worked was a download turning up hours
later, so a mis-wired install looked exactly like a working one. There is now a
progress checklist that ticks through indexer → download client → author → grab
→ import, warnings on the three ways a first run fails silently, and an update
badge so you find out a release exists without going looking.

The fixes are mostly a search-and-metadata sweep, and several of them are the
same shape: a feature that was fully built except for the one call that would
have made it work. The ISBN ranking bonus could never fire because nothing put
an ISBN in the search criteria. A Hardcover import list's quality profile was
saved, displayed, and never read. Reorganize's copy fallback would walk into the
directory it was creating and fill the disk. And the Hardcover client never
asked which role a credit was for, so audiobooks filed themselves under their
narrator and put themselves back there on every refresh.

### Added
- **Setup progress checklist** on the Authors page — indexer → download client → author → grab → import, ticking off as each happens and disappearing for good once a book has imported. This is the "your setup works" confirmation the app never had: until now the only evidence that first-run setup succeeded was a download showing up hours later, so a mis-wired install looked identical to a working one. Backed by a new `GET /api/v1/system/setup-state`; it replaces the getting-started card on that page (the checklist says everything the card did, plus what comes next).
- **In-app update badge** — the header version link (and Settings → About) now turns into an amber `v1.28.0 → v1.30.0` badge when a newer release exists, linking to the release. The telemetry ping already carried the latest published version on every response; the app now actually uses it instead of discarding it. Installs with telemetry disabled see no badge (the app has no other way to learn a release exists), and dev/sha builds never compare. Telemetry fleet data shows why this matters: docker installs are 81% current within a week of a release, while binary installs are 35% — the gap is discovery, not willingness.
- **"Updated to vX — see what's new" toast** after an upgrade, closing the loop the update badge opens: the badge tells you an update exists, this confirms it landed and links to the release notes. Shows once per version, never on a first-ever load (a fresh install has nothing to catch up on), and never for dev/sha builds.
- **Setup-funnel telemetry** (anonymous, opt-out with the rest of telemetry) — the daily ping now includes whole-day offsets from install to first indexer, first download client, first author, first grab, and first import (e.g. `setup_indexer_day: 0` = configured the same day). Integers only, never timestamps. Fleet data showed installs that reach "indexer + download client" retain 66% at 7 days vs 16% for those that don't — these fields make the stall points in that funnel visible so onboarding changes can be measured. Documented at getbindery.dev/telemetry-fields.

### Fixed
- **German titles with umlauts find their releases** (#1610) — indexer queries were sent with literal ä/ö/ü/ß while Usenet release names use the ASCII convention (Phönix vs. Phoenix), so every umlaut-containing title returned (near-)zero results and never auto-grabbed. Book-search queries now transliterate German umlauts (ä→ae, ö→oe, ü→ue, ß→ss) before being sent; other Latin diacritics (é, ñ, ç) and non-Latin scripts are left unchanged, and free-text searches are untouched. When the transliterated query finds nothing at all, the search retries once with the original umlaut spelling to catch the rare releases that keep it.
- **Hardcover no longer files books under their narrator or translator** (#1733) — the client never asked Hardcover which role a credit was for, so it took whichever contribution came back first and matched author lookups on name alone. Books landed under an audiobook narrator (Will Wight's *Cradle* volumes showing Travis Baldree as the author), and because the per-author refresh matched every contribution role by name, correcting a book by hand only held until the next "Refresh All Metadata" put it back. Every book query now requests the contribution role, the author lookup only matches author-role credits, and metadata resolves to the actual author. Books whose only credit is an editor or illustrator keep that credit rather than losing an author entirely.
- **Reorganize no longer fills the disk when a book's new location sits inside its old one** (#1809) — a layout change that computes a destination nested under the source (a series folder named after the book, so `/library/Author/Title` moves to `/library/Author/Title/Title`, or a flat author folder that becomes `/library/Author/Book/`) cannot be renamed by the kernel, so it fell through to the copy-based move, which then read the source while the destination grew inside it: the copy descended into its own output and nested directories forever until the drive was full and the container had to be killed. Directory moves, copies and hardlink placements now check containment up front and refuse the move with an error naming both paths, and the reorganize preview flags it before anything is applied. Source and destination are resolved through symlinks and compared on path component boundaries, so a genuine sibling like `Book Two` next to `Book` still moves normally.
- **"Search all wanted" no longer floods an indexer with duplicate and empty-term queries** (#1814) — a 26-book author sent roughly 294 searches to one indexer over 15 minutes, and every search after the first minute failed with `context deadline exceeded` so nothing was grabbed. Three things multiplied: a book whose title normalised to nothing (a blank title, or a row whose title is only an edition qualifier like `(Unabridged)`) still ran the full four-tier query cascade and put `q=` on the wire with nothing after it; two catalogue rows for the same work produced byte-identical queries that were both sent, and a sweep overlapping an earlier one repeated every query that sweep had already made; and the bulk fan-out ran at twice the concurrency of every other search fan-out in Bindery. Unsearchable titles are now skipped outright, identical queries to one indexer are collapsed for 90 seconds, and the bulk fan-out runs at the same bound as the per-author auto-search. A repeat or overlapping sweep is now close to free.
- **Releases that name a book's ISBN now actually win the search** (#1724) — the ranker has always carried a large exact-match bonus for a release whose title contains the book's ISBN, but nothing ever put an ISBN into the search criteria, so the bonus could not fire on a single search Bindery has ever run. Both search paths (interactive search and the scheduler's auto-grab) built their criteria with title, author, year and ASIN and silently left the ISBN empty, which is why an ISBN-tagged release ranked no better than an untagged one and auto-grab could pick the wrong edition when the right one was sitting there labelled. Both paths now read the book's editions and populate the criteria, converting an edition recorded only as an ISBN-10 into its ISBN-13 form so it matches — a release name can only ever carry the ISBN-13.
- **One-word book titles no longer grab a longer book that merely starts with that word** (#1731) — a book titled *Treasure* auto-grabbed and silently imported `Clive Cussler - Dirk Pitt Universe Bk 29 - The Treasure of Khan`, a different book by the same author, which landed under the right book record with no error. The single-keyword match path only asked for the word at a word boundary plus the author somewhere in the release, and the author corroboration added for embedded titles cannot separate two books by the same author. The matcher now looks to the right of the matched word as well as the left, and rejects a release where the word runs on through a connective (`of`, `and`, `in`, `to`, …) into a further title word. Trailing years, formats, bitrate and edition markers, series labels, language tags and the author's own name are all still accepted, as are subtitles opening with an article (`Treasure - A Dirk Pitt Novel`), so correctly named releases keep matching.
- **Series volumes sharing a base title no longer collapse into one book** (#1785) — the canonical dedup key strips a `": subtitle"` tail, so a series titled `Series: Volume` (e.g. Tao Wong's *A Thousand Li*, the *Gears of War* novels, omnibus collections) mapped every volume onto a single key. On an Audiobookshelf import that merged the extra volumes (a 902-item library created only ~824 books, the rest linked or queued for review); on a manual add by provider id it left the requested id bound to nothing, so the request failed forever with "book not found after author sync — try again shortly". The dedup match is now series aware: a candidate that shares a series but sits at a different sequence number is treated as a distinct work, so each volume gets its own row. Same-sequence editions (an audiobook that drops the subtitle) still merge as before.
- **Hardcover list sync no longer stalls on large lists** (#1694) — syncing a list issued one fully-paginated GraphQL edition query per newly imported book, so a first sync of a big shelf ran out of the request's time budget after a handful of books and reported success anyway. The audiobook ASIN, language, and media type those queries were after now arrive inline on the list response itself (via the default-edition relations — the `books` type has no `language` field of its own). Audnex audiobook enrichment still runs for books whose ASIN arrived inline, without any edition fetch. Measured on a real 1,660-book shelf against v1.30.0: the scheduled (deadline-free) sync now completes the full list; the manual "Sync now" path imports ~4.7× more books per request (519 vs 111) with ~7× the language coverage.
- **A Hardcover import list's quality profile now applies to the authors it creates** (#1781) — the per-list quality profile picker wrote its choice to the list row, but the sync never read it back: `ensureAuthor` built each new author by hand and left `qualityProfileId` null, so `ResolveAuthorQualityProfile` returned "no profile" and both the scheduled grab and the interactive search let every format through for that author's books. A list configured as audiobooks-only would happily grab epubs. The syncer now stamps the list's configured profile onto the authors it creates, so the filter is enforced from the first sync. Existing authors keep the profile they already have — a re-sync never overwrites a choice made on the author itself — and a list with no profile configured still leaves the author unfiltered rather than guessing at a default.
- **API-key clients can mutate again when `auth.mode=local-only`** (#1849) — a `POST`/`PUT`/`DELETE` carrying a valid `X-Api-Key` from an address local-only mode trusts (RFC1918 or loopback) came back `403 {"error":"forbidden"}`, even though the same request was being treated as an authenticated admin. Integrations like Harpoon and *arr-style callers had to also send `X-Requested-With: bindery-ui` — the web UI's own private header — to get through. The local-only bypass ran ahead of the API-key check and returned early, so the request was never marked as API-key-authenticated and the CSRF header guard, which exempts API-key clients, did not exempt it. The key check now runs first: both branches grant admin, so the only thing that changes for a trusted-local caller is that a verified key earns its documented exemption. A request with no key, or a wrong one, still falls through to the local-only bypass and still needs the header on a mutation, so browser CSRF protection on the LAN is unchanged.
- **Calibre imports no longer log a constraint failure for every series link, and rollback can now unwind them** (#1635) — the run-tracking tables shipped with `entity_type` pinned to `author`/`book`/`edition`, but series persistence records book-to-series memberships as `series_link`, so every linked book produced a `CHECK constraint failed` warning and left no provenance behind. Rolling back that import then deleted the books and authors while quietly leaving the series memberships in place. A new migration widens the constraint on both tracking tables (rebuilt in place, existing rows and indexes preserved), and rollback now unwinds a series link explicitly, before the book it belongs to. Only memberships the run actually created are claimed, so a link you made yourself, or one an earlier import created, survives. Part 2 of the report — `book_files` rows never being written during a Calibre import — is still open.
- **Deleting an author with "delete files" now removes every tracked file** (#1811) — the sweep enumerated only the legacy `file_path` column, so a book's ebook/audiobook files tracked in `book_files` (multi-format books, audiobook folders) and any excluded book's files were left orphaned on disk. It now walks `book_files` per book (falling back to the per-format/legacy columns) and includes excluded books, matching the single-book delete path.
- **Retrying a failed import clears the old error instead of showing it forever** (#1633) — `retry-import` reset the retry count and the status but left `error_message` untouched, so a queue row that recovered kept rendering `Error: import retry limit reached (3 attempts)` next to an import that had demonstrably succeeded. Only the transition into `imported` cleared the field, and external-mode hand-offs settle in `importExternal` instead — a deliberately non-terminal state whose rows stay in the queue permanently, so the contradiction never aged out and made a working import look broken. The retry now clears `error_message` in the same statement that re-arms the row, so a retried item starts clean.
- **Three silent first-run failure modes now warn in the UI** instead of living only in QUICKSTART.md: (1) an enabled Torznab/Newznab indexer with no matching-protocol download client shows an amber warning on the Indexers/Clients settings tabs (searches would find releases nothing could download); (2) turning on auto-search when the pipeline is incomplete warns right in the Add Author dialog (the background search's failure was invisible); (3) a failed download-client connection test against localhost explains the Docker loopback trap and what to use instead.
- **Half-configured installs now get setup guidance.** The getting-started card required indexers AND download clients to BOTH be missing, so a user who had configured one of the two — the state where searches or grabs fail silently — saw no guidance at all. It now names the one missing step ("…every grab will fail") and links only to it. Guidance also no longer hides inside the Authors/Books empty states: a dismissible banner in the app shell shows while the pipeline is incomplete, so library-importers (whose pages are never empty) finally see it too.
- **The search debug panel no longer reports `mediaType: ebook` for a `media_type=both` book** (#1636) — a dual-format book is searched once per format so each leg uses its own category tree (7xxx for ebooks, 3xxx for audiobooks), and the two debug payloads are merged into the one panel the UI shows. The merge kept the ebook leg's query summary, so a search that had genuinely queried both trees — with the 3xxx indexer rows visible in the same panel — labelled itself "ebook", which reads as a media-type misconfiguration and sends you hunting for a bug that isn't there. The merged summary now reports the book's own media type, while the per-indexer rows below it continue to show each format's categories separately.
- **Helm chart `appVersion` no longer drifts from the deployed tag** — it had been frozen at 1.22.3 while `values.yaml` advanced to 1.30.0, because the release pipeline only bumped the latter. The prod-deploy step now updates both, and `appVersion` is corrected to 1.30.0.
- **The setup-funnel cohort is gated on the release that actually reports it** — the gate was pinned to 1.31.0 on the assumption the funnel fields would ship in a minor release, and would have excluded every install that reports them, leaving the /stats setup-funnel section permanently empty. It now gates on 1.30.1, and accepts a version with or without a leading `v`: the Docker image reports `v1.30.1` (CI builds it from `git describe`) while the GoReleaser binaries report the bare form, and the telemetry client sends whichever it was built with — so a bare-form-only gate would have dropped every Docker install out of the cohort, which is indistinguishable from an install that stalled before its first milestone.

## [v1.30.0] — 2026-08-07

Mostly a UI release for the Author and Book detail pages, which had drifted
into looking unfinished. Two of those fixes are the same bug wearing different
clothes: a Tailwind class that compiles to nothing, and a Tailwind class that
never compiles at all. Both had been shipping silently — the class name looks
right in the DOM and the build succeeds, so nothing in the toolchain noticed the
File card had collapsed to one column or that the author page had no width limit
at all. Two lint rules now fail CI on either pattern. The rest of the UI work is
structure: one shared width across both pages, overflow menus instead of
eight-button rows, selects instead of ten filter chips, and a cover placeholder
that no longer reads as a broken image.

Riding along: backups can carry a label instead of a bare timestamp, Audiobookshelf
imports stop re-querying the same author once per book, and Hardcover list-sync
authors finally get the default metadata profile every other path assigns.

### Added
- **Label a backup when you create it** (#1790) — the Backup panel takes an
  optional label, so a snapshot is saved as `bindery_<timestamp>_<label>.db`
  (e.g. `bindery_20260726_181731_pre-import.db`) rather than a bare timestamp
  you have to rename afterwards to recognise. Labelled backups restore and
  delete correctly from the UI, which previously accepted only the
  bare-timestamp filename; a backup renamed by hand to something outside the
  `bindery_*` shape still lists but cannot be restored or deleted from the UI.
  The label is sanitised before it reaches the filename — only `A-Za-z0-9_-`
  survive, everything else collapses to `-`, capped at 40 characters — so a
  label that reduces to nothing (an all-CJK one, for instance) is dropped and
  the snapshot keeps its plain timestamp name. `POST /api/v1/backup` accepts the
  optional `{"label": "..."}` body; sending none behaves exactly as before.
- **Series name and position on the book detail page** (#1795) — `series_books`
  has been populated since v0.7.0 and this page never surfaced it. A book that
  belongs to a series now shows it in the meta row (`Discworld #3`), once per
  series for books that belong to several. There is no book→series endpoint, so
  this reuses `GET /author/{id}/series`; the lookup runs after the book loads,
  never blocks rendering, and simply omits the row if it fails.
- **`Downloading` and `Skipped` filters on the author page** (#1795) — both
  values have been in the status filter's type since it was written, but the
  chip row only ever offered All / Wanted / Downloaded / Imported, so there was
  no way to see books in either state. The status control now offers every value
  it supports.
- **Books without cover art get a real placeholder** (#1795) — previously a flat
  grey box with small centred text, which read as a failed image load; on a
  library where half the covers are missing, that is most of the page. The
  placeholder now sets the title large over a ground colour derived from the
  book's id, so it is stable per book and consistent everywhere that book's
  cover is drawn. Every colour carries white text at 4.5:1 or better and sits at least 3:1 from
  both page backgrounds, enforced by tests.

### Changed
- **Both detail pages now use one container width** (#1795) — the author page
  was effectively `7xl` and the book page `4xl`, so following a link from an
  author to one of their books collapsed the content by 384px and shunted it
  left. Both are now `7xl`, matching every other page; descriptions are held to
  a readable measure individually rather than by narrowing the whole page.
- **The author page's action row is five controls instead of eight** (#1795) —
  it wrapped, which pushed Delete onto a line of its own and gave the most
  destructive action the most prominence by accident. Monitored, a primary
  "Search N wanted" that carries its count in the label, Refresh and Edit stay
  on the row; Rename files, Merge, Link metadata and Delete move into a **More**
  menu (keyboard-navigable, Escape and click-outside to close).
- **Author page filters are three selects on one line** (#1795) — replacing three
  labelled chip groups totalling ten buttons, plus a "Select all" that wrapped to
  a second row and read as though it belonged to the Published group.
- **"Show excluded" is now an option in the status filter, not a separate
  checkbox** (#1795) — **note the behaviour change**: it used to *add* excluded
  books to whatever you were looking at, and now *narrows to* only them,
  consistent with every other option in that list. It is also remembered between
  visits, which the checkbox never was.
- **The author page's stats are a fixed four-cell strip** (#1795) — the old
  run-on line dropped the audiobook count entirely when it was zero, so the row
  changed shape between authors and no figure ever appeared in the same place
  twice. Books / In library / Wanted / Audiobooks are now always all four.
- **Book detail: Edit moved to the header, and the File card's actions are
  ranked** (#1795) — Edit changes metadata, not the file, and was the one action
  in that row with nothing to do with bytes on disk. Download, Re-bind and Fix
  match stay visible; Exclude and Rename files move behind **More**. "Delete
  file" drops from solid red to an outlined destructive style: it is reversible
  by re-downloading, and it was louder than "Delete book + files", which is not.
  Solid red now appears exactly once per page, on the action with no undo.
- **Long book descriptions clamp with show more/less** (#1795), matching the
  author page, instead of running the full height of the page.

### Fixed
- **Audiobookshelf imports no longer re-query the same author once per book**
  (#1788) — the importer looked each book's author up against the metadata
  providers with no caching, so every book on the same shelf re-issued an
  identical provider search. When that search was slow or unreachable —
  OpenLibrary author search timing out on romanised-CJK pen names, Hardcover
  returning 401 — each repeat paid the full per-request timeout again, dragging
  a single author's shelf out to minutes. The lookup is now memoised for the
  duration of one import run. Note the trade: a provider that degrades
  mid-import stays degraded for that author until the run ends, where
  previously each book got a fresh attempt.
- **Hardcover list-sync authors now get the default metadata profile** (#1736,
  #1783) — they were created with no profile assigned instead of the default
  "Standard", and existing rows are backfilled by migration. No behaviour
  changed as a result, because every reader already fell back to the default;
  what it fixes is the profile shown in the UI and three separate fallbacks
  that had to stay in sync. Five other author-creation paths still insert no
  profile, so the migration is a one-shot cleanup rather than a permanent fix —
  tracked in #1803.
- **The book detail File card had collapsed to a single column** (#1791) — its
  label/value grid separated the two tracks with a comma, which Tailwind passes
  through verbatim into an invalid `grid-template-columns` declaration that every
  browser then drops. The rule was generated, so grepping the compiled CSS for
  the class found it and it looked fine; only the computed style showed a single
  track. A test now asserts the card resolves to two tracks, and a lint rule
  fails CI on a top-level comma in any arbitrary value.
- **The author detail page was rendering with no width constraint at all**
  (#1791) — its `max-w-5xl` sat directly against a `${…}` interpolation, and
  Tailwind v4 scans source text rather than runtime values, so it extracted the
  token `max-w-5xl${selected.size` and never emitted `.max-w-5xl`. Neighbouring
  widths were present in the CSS, which is what made it look like a framework
  bug rather than a source one. A second lint rule now fails CI on any class
  glued to an interpolation, and a sweep of the tree found no other instance.
- **Clicking a table row on the author page did a full page reload** (#1795) —
  the row navigated via `window.location.href` while the link inside that same
  row routed client-side, so one row had two different behaviours depending on
  where you clicked.
- **Grid cards no longer end at ragged heights** (#1795) — the text block is a
  fixed height, and the published year no longer appears and disappears between
  cards (the formatter already returns an em dash for a missing date, so the
  surrounding conditional only ever removed the row).

### Docs
- **`BINDERY_TRUSTED_PROXY` governs the forwarded scheme and host, not just
  proxy auth** (#1787) — the deployment reference now says so. Requests from a
  peer outside this list have every `X-Forwarded-*` header stripped, so behind
  a TLS-terminating reverse proxy the OPDS feed links come out `http://`, and
  `BINDERY_COOKIE_SECURE=auto` and the OIDC `redirect_uri` both see the wrong
  scheme, until the proxy's IP or CIDR is trusted here. Set it even if you are
  not using proxy auth.

### Security
- **Bumped js-yaml to 4.3.1** (#1793, GHSA-5p4m-2wfm-xmqj) — resolves a
  high-severity quadratic-CPU advisory in `!!omap` resolution, where the
  CVE-2026-59870 fix was never backported to the affected 4.x range. Build
  tooling only: js-yaml reaches the tree as a transitive devDependency of ESLint
  and never enters the browser bundle, so no running instance was exposed.

## [v1.29.1] — 2026-08-04

A patch release out of a codebase audit. Two of these are credential leaks that
only mattered once Bindery went multi-user: an indexer API key riding along in
every search and queue response, and profiles you could read or delete across
users because their owner was never stamped. The rest are the same shape as
v1.29.0 — settings that saved and then did nothing: a Calibre "Library import"
toggle no code read, naming templates that only applied after a restart, and
two clients quietly ignoring the outbound proxy. Plus one dead endpoint removed.

### Security
- **Metadata and quality profiles created via the API are now scoped to their creator** — `Create` never wrote `owner_user_id`, so with `BINDERY_ENFORCE_TENANCY` on, every API-created profile was owner-less and the per-user access check (`CheckOwnership`) treated it as shared, letting any authenticated user read, edit, or delete another user's profile. Both profile Create paths now stamp the caller's user id (`CreateForUser`), matching authors/books. Existing owner-less rows stay shared, as before.
- **Indexer API keys no longer leak to non-admin users in search and queue responses** — interactive indexer search signs the instance's indexer/Prowlarr apikey into each result's `nzbUrl`, and that download URL was returned verbatim to any authenticated user (search results and the queue list). The apikey is now stripped from every client-facing response and re-attached server-side at grab time from the release's indexer id, so grabbing still works while the shared credential stays off the wire.

### Fixed
- **Book covers now backfill on Refresh Metadata, and edition covers are consulted** (#1748) — a book row that imported without a cover used to stay blank forever: the author refresh path updated only ratings and genres on existing rows, never `image_url`, so clicking Refresh Metadata could not fill a missing cover even once one was available upstream. The refresh now fills an empty cover (never overwriting one you already have). Separately, OpenLibrary attaches covers to editions far more consistently than to works, and Bindery previously read only the work-level cover; cover-less works are now sampled against their editions (bounded and memoized, same as language sampling) so a work whose cover lives only on an edition still gets one on add and on refresh.
- **The Calibre "Library import" toggle now actually gates library imports** (opt-in) — the setting was UI-only: no backend code read it, so startup imports, the 24h scheduled sync, and the manual import ran off `calibre.library_path` regardless of the toggle. A user who saw the switch "off" was still being imported on every boot. All three import paths now honor `calibre.library_import_enabled`. Existing installs that already have a library path configured are backfilled to enabled by a migration, so no working import is disabled; a newly-configured library imports only after you turn the toggle on.
- **Hardcover list sync and Prowlarr sync now honor `BINDERY_OUTBOUND_PROXY`** — `hardcover.NewAuthenticated` (used by the import-list syncer and import-list browse) and the Prowlarr client were built without the proxy transport, so they dialed `hardcover.app` / the Prowlarr host directly while every sibling code path was proxied. On a locked-down egress they failed outright; on a VPN-only setup they leaked traffic outside the configured proxy. Both now use the shared proxy transport like the other clients.
- **Ebook/audiobook naming templates now take effect without a restart** — the destination templates (`naming.bookTemplate`, `naming_template_audiobook`) were read once at boot and baked into the renamer, so saving a new template in Settings did nothing until Bindery restarted — and Reorganize actively applied the stale boot-time template. Both templates are now re-read from settings per import and per reorganize, matching how the per-track audiobook template already worked.
- **Google Books API key and primary metadata provider now show a "restart required" hint** — both are read once at boot, so a change only takes effect after a Bindery restart. The settings fields now say so (matching the existing wanted-search-interval note), instead of silently appearing to save with no effect.

### Removed
- **Dead `POST /book/{id}/map` endpoint** — an undocumented metadata-map handler with no caller (the Fix Match UI uses `rebind`, and ABS review uses its own resolve endpoint). Removing it drops maintained authenticated surface that duplicated rebind's logic; the shared helpers it used remain in place for the audiobook ASIN-map path.

## [v1.29.0] — 2026-08-04

A release about failures that never said anything. Almost every fix here was a
bug you could only find by noticing an absence: an author's catalogue arriving
50 books short with zero skip counters and no log line, `ON DELETE CASCADE`
quietly switching itself off partway through a process's life, a language
filter dropping books at a log level nobody runs, dismissed recommendations
returning because two code paths were writing to two different users. None of
them errored. Several had been reported as "it just doesn't work" precisely
because there was nothing to quote.

Most of this came from the Discord bug reports, several with source-level
diagnoses attached. Credit is on the individual entries.

### Added
- **User guide** (#1721) — a concepts guide for new users covering the five
  things that account for most support traffic: catalogue first and files
  second, status versus monitored, how monitor modes decide what gets grabbed,
  what the importer will and won't do with an existing folder, and where
  metadata actually comes from. Written against a sweep of real issues and
  Discord threads rather than from the code.

### Fixed
- **Audible catalogue no longer truncates prolific authors** (#1751) — the
  Audible author lookup issued a single request capped at 50 products and never
  paged, so any author with a larger audiobook catalogue silently lost the
  remainder. There was nothing to notice: no error, no log line, and the sync's
  `skipped_language` / `skipped_junk` / `skipped_media_type` counters all read
  zero, because the missing books were never enumerated in the first place. One
  reported author with 105 titles imported 56. The reason it went unspotted for
  so long is that the endpoint's `page` parameter is 0-indexed, so an unpaged
  request and `page=1` return *disjoint* windows rather than the same one — an
  entire 50-item window was going unrequested, which looks nothing like a
  normal off-by-one. Pages are now walked to completion and deduplicated by
  ASIN. Reported by .v.e.g.a.
- **Foreign-key cascades no longer stop working on long-running instances**
  (#1727, #1728) — `foreign_keys` and `busy_timeout` were applied once at
  startup, but they are per-connection settings in SQLite. Whenever the pool
  replaced its connection, the replacement came up with foreign keys switched
  off, silently disabling every `ON DELETE CASCADE` in the schema for the rest
  of the process. They now travel in the connection string, so each new
  connection gets them. The visible symptom this fixes: deleting an author left
  its identifier rows behind, and re-adding that author then failed forever with
  "author already exists" even though the author was gone. Author deletes now
  remove those rows explicitly, so correctness no longer depends on pragma state
  at all, and an upgrade sweeps away any orphans an affected install is already
  carrying.
- **Adding a specific book to an author you already track no longer fails
  forever** (#1612, #1735) — the add only polled for a row the catalogue sync
  had already refused to create, so it returned "book not found after author
  sync — try again shortly" on every retry and no amount of waiting helped. The
  requested book is now fetched and saved directly, and if you already own it
  under another source (a Calibre import, or one half of a split
  ebook/audiobook record) the existing entry is reused instead of a duplicate
  being created. Diagnosed and originally fixed by helios57.
- **Dismissed recommendations no longer come back** (#1725) — requests that
  authenticate as the install itself, from a trusted local network or with an
  API key, were treated as an admin but carried no user identity, so a refresh
  triggered that way saved its recommendations against a user that does not
  exist while the nightly job saved them against a real one. Dismissals
  recorded on one batch never applied to the other. Both now resolve to the
  same operator account, and an upgrade hands any stranded recommendations and
  dismissals back to it.
- **Discover no longer recommends books you already have** (#1726) — ownership
  was matched on provider-specific IDs, so a book owned via OpenLibrary or
  Audiobookshelf still came back as a Hardcover recommendation (one reported
  batch was 15 of 22 owned books). The recommender now also matches by work
  identity (canonical title key plus author), and books on your wanted list
  count as owned too instead of being recommended back.
- **A profile allowing "eng" no longer rejects books that arrive as "en" or
  "en-US"** (#1729) — the allowed-languages filter compared provider codes raw,
  but providers speak different vocabularies: Google Books returns ISO 639-1
  (`en`, `en-US`) while profiles are written in ISO 639-2/B (`eng`), so those
  books were silently skipped at the default log level. The filter now runs both
  the incoming code and the profile's list through the same canonicaliser the
  EPUB importer already used, so any spelling of a language matches any other.
  Books previously skipped will appear on the next metadata refresh.
- **"Add all" on a light novel or manga series now adds every volume** (#1682)
  — filling a Hardcover-linked series created only the first volume and silently
  linked that same book to every other position, so a 5-volume series produced
  one book and a 13-volume series produced one book. The duplicate check
  compared titles by fuzzy similarity, and volume titles differ by a single
  number: `Trapped in a Dating Sim Vol. 1` scores **100** against `Vol. 13`,
  `The Mimosa Confessions Vol. 1` scores 96 against `Vol. 2`. An explicit volume
  number now vetoes a title match, so those are recognised as different books.
  Titles with no volume marker are unaffected, and a bare number in a title
  (`Fahrenheit 451`) is still not treated as one. The same cause made an author
  page show a single volume until you refreshed their metadata, which was the
  workaround people found.
- **Author metadata candidates are now ranked, and you can check one before
  linking** (#1754) — "Link metadata" listed candidates in whatever order the
  provider returned them. OpenLibrary emits a composite author record per
  anthology, one row naming every contributor and holding a single work, so
  those led the list while the record carrying the author's actual catalogue sat
  below them and the picker looked like it couldn't find the author at all.
  Candidates are now ranked by name match then catalogue size, reusing the
  ranking the normal author search already had. Records from different providers
  are still never merged, so picking one specific provider's record still works.
  Each candidate also gained a **View on OpenLibrary** link, since several can
  share a name and there was previously no way to tell them apart without
  leaving the app; it is omitted for providers whose IDs have no public page.
  Reported by nzvengeance.
- **Ebook-pinned Hardcover lists no longer create books as "both"** (#1732) —
  edition hydration ran right after a list-synced book was created and widened a
  pinned `ebook` media type to `both` whenever the work had any audio-shaped
  edition on Hardcover (true for most popular titles). Hydration now knows when
  the media type was deliberately pinned by the list and leaves it alone; books
  with no media type at all still promote to `audiobook` when an audio edition
  exists.
- **A series genre override can now be seen, edited, and removed** (#1709) — the
  override applied to books added later was stored but never sent to the
  browser, so the "Set genre" prompt always opened blank and there was no way to
  tell an override existed or to undo one. The series list now carries it, the
  prompt opens pre-filled, the button reads **Genre ✓** while an override is
  active, and submitting an empty box removes it. Clearing drops the rule for
  future books and leaves the genres already written onto existing books locked,
  since those were a deliberate edit. The override can also now be set *before*
  adding a series' books, and applies to the books created by that add.
- **Expanding one series card no longer stretches its neighbours** (#1682) — the
  series grid used the CSS default of stretching every card in a row to the
  tallest, so opening one made all three grow and it was hard to see which was
  actually open.
- **Stopped the qBittorrent poll logging a Debug line for every already-imported
  download** (#1730) — the 15-second poll walked all download rows and logged
  "download not found in torrent list" for long-imported downloads whose torrent
  had been removed after import, one line per download per poll forever (96% of
  debug output on a 53-download library). Terminal downloads are now skipped
  before the log, matching the no-hash branch; failed-import rows still log and
  still feed stale-source detection unchanged.

### Upgrade notes
- Two data migrations run on first start. `068` removes author-identifier rows
  orphaned by the cascade bug above. `069` re-parents recommendations and
  dismissals stranded under a non-existent user to the operator account. Both
  are one-way; the affected rows were unreachable before, so nothing you could
  see is changed.
- Audible author lookups now make one request per 50 titles instead of always
  exactly one. For a large library the first refresh after upgrading will do
  noticeably more outbound requests, and will find books it previously missed.
  A lookup that fails partway now reports an error rather than returning a short
  list, so an Audible outage degrades to "no audiobook supplement this run"
  instead of a silently truncated catalogue.

## [v1.28.2] — 2026-08-03

A bug-fix release about things that looked like they worked. Three of these
were features you could see in the UI, configure, and get no behaviour from:
the quality profile's **Allowed formats** checkboxes were never wired into the
search pipeline at all, a naming template couldn't actually express the
combination its own help text described, and the Audiobookshelf importer kept
appending file rows it never cleaned up. None of them failed loudly — they just
quietly didn't do the thing. The fourth is housekeeping on a security gate that
had been red long enough for everyone to stop reading it.

### Fixed
- **Quality profile "Allowed formats" is now actually enforced** (#1693) — the
  checkboxes read as a hard allow-list in the UI, but the rule implementing them
  was never constructed anywhere in the codebase, so nothing stopped a
  disallowed format being grabbed. What was running instead is a fixed 1–10
  format ranking, which orders candidates but never rejects one. The clearest
  symptom: `azw3` outranks `epub`, so an EPUB-only profile would still pick the
  AZW3 even when a perfectly good EPUB was sitting in the same result set.
  Automatic grabs now reject formats the author's profile disallows, and because
  the rule runs on the same evaluator that re-checks parked releases, a rejected
  release keeps failing instead of slipping through on a later sweep.
  Interactive search deliberately only *labels* — every result is still shown,
  with the reason — so you can knowingly take a disallowed format for one book
  rather than having it silently hidden. Three cases are untouched by design: a
  release whose format can't be read from its title (many Usenet names carry no
  format token, and rejecting those would turn one ticked box into a near-total
  grab blackout), authors with no quality profile, and profiles with no format
  list, which already meant "allow all".

  **If you have a configured quality profile, check it after upgrading.** The
  profile editor offers eight formats (`pdf`, `mobi`, `epub`, `azw3`, `mp3`,
  `m4a`, `m4b`, `flac`), but release parsing recognises nine more — `azw`,
  `djvu`, `cbr`, `cbz`, `fb2`, `lit`, `rtf`, `txt`, `ogg`. Those nine cannot be
  put on an allow-list that has no checkbox for them, so with a configured
  profile they are now rejected where previously they were grabbable. Plain
  `.azw` is the one most likely to bite: it is a real Kindle format that Bindery
  ranks alongside `mobi`. Profiles left with no format list are unaffected —
  that still means "allow all" — so this only applies if you actively ticked
  boxes. Widening the editor's vocabulary is tracked in #1700.
- **Naming templates: literal text after a zero-pad width now renders** (#1690)
  — `{SeriesNumber:3 - }{Title}` dropped both the padding and the trailing text,
  producing `2Sample Book` where the help text promises `002 - Sample Book`. On
  a book with no series it was worse and silent: the modifier text itself leaked
  into the filename, so every standalone book came out named `3 -Sample Book`.
  The multi-token spelling (`{Series SeriesNumber:3 - }`) always worked, because
  it took a different parsing path; the single-token form now agrees with it.
  All-digit modifiers such as `{Year:2024}` keep their existing meaning as
  default text. The live preview in Settings had the identical defect — it
  re-implements the same engine — so what you see there and what the importer
  writes now match again.
- **Stale file rows after an Audiobookshelf re-import** (#1692) — re-filing
  books into a different folder or ABS library and re-importing added a row for
  the new path but kept the old, dead one, so a book's reported file path could
  point at a file that no longer existed. Nothing cleaned it up afterwards
  either: a library scan skips any book that still resolves at least one file.
  There was also no safe way to fix it by hand, because the existing per-format
  delete removes *every* file of that format from disk — including the good one
  you just moved into place. The importer now prunes a book's other rows for the
  same format when their files are confirmed gone, and
  `DELETE /api/v1/book/{id}/file?path=…` deregisters a single tracked path
  without touching the disk, for instances that already accumulated dead rows.
  Pruning is deliberately narrow: only after a replacement path was verified,
  only for that book and format, and only on a definite "file does not exist" —
  a permission error or an unmounted share leaves every row alone, because
  "missing" and "gone" are not the same thing.

### Security
- **Frontend dependency audit is clean again** (#1639) — `npm audit` reported
  three high advisories, so the SAST job had been failing on `main` itself for
  weeks. A security gate that is permanently red trains everyone to ignore it,
  which is worse than not having the gate. `react-router-dom` 7.x is replaced by
  `react-router` 8.3.0: v8 folded the DOM package into the core one, and 8.3.0
  is the first release outside the advisory range for GHSA-qwww-vcr4-c8h2 (7.18.2
  is still inside it, and the automatic fix offers only a downgrade to a stale
  7.11.0). In v7 `react-router-dom` was already a re-export of `react-router`, so
  the change across the frontend is the import path and nothing else. Stated
  plainly: the advisory covers RSC mode, which Bindery does not use, so this was
  a failing gate rather than an exploitable path. The transitive
  `brace-expansion` advisory in the lint tooling clears with a lockfile bump.

## [v1.28.1] — 2026-07-27

A bug-fix release, and most of it is one bug wearing different hats: two sides
of a string comparison were being reduced through different alphabets, so they
could never match. That single shape made accented and non-Latin books
unfindable, split authors into duplicate rows, stopped the library scan
reconciling files it had already imported, and merged unrelated series
together. Those folds now live in one place, with a test suite that fails the
build when two of them disagree rather than waiting for the bug report.
Alongside that: the install-wide **Default monitor mode** is finally honoured
by the bulk importers that were ignoring it, the Search page stops offering
movie rips and usenet fragments with a Grab button next to them, and every
Settings field confirms when it saves.

### Fixed
- **Accented and non-Latin titles and authors return results again** (#1642) —
  searches for a book whose title or author had a diacritic at the start or end
  of a word ("Ölümün Sonu", "El mundo que Jones creó", "Édition collector"), or
  that used a non-Latin script (Chinese, Cyrillic, Greek, Hebrew, Arabic),
  matched nothing at all — even when an indexer returned a release named exactly
  like the book. Measured against a real library, 110 of 382 books with a
  non-ASCII title could never be found, and they stayed in the wanted list being
  re-searched on every sweep. The matcher's word-boundary rule only understood
  ASCII letters, so a keyword touching a non-ASCII character was impossible to
  match; it now understands the full Unicode alphabet. Single-word titles by an
  author with a non-Latin name (for example "Circle" by 刘慈欣) were affected
  too, because those searches require the author to match. Accents *inside* a
  word ("Miéville", "Stanisław") were never affected.
- **Authors with dotted initials match releases again** (#1608) — a name like
  "J.R.R. Tolkien" produced an author token ("j.r.r") that can never appear in a
  normalized release name, so searches for single-keyword titles ("The Hobbit")
  returned zero results even when indexers found dozens. Author names are now
  normalized the same way release titles are before tokenizing; hyphenated first
  names ("Mary-Kate") are fixed by the same change.
- **Books with an apostrophe or umlaut in the title no longer waste indexer
  queries** (#1643) — a search for a title like "Ender's Game", "The Handmaid's
  Tale" or "Der Prozeß" threw away a perfectly good indexer response, misreading
  it as an irrelevant category feed, and then re-queried the same indexer three
  more times. This happened on every search for those books, and on a
  rate-limited indexer it could turn into a hard failure that returned no
  results at all despite the correct release having already been found. The
  check now compares both sides of the match in the same form.
- **"Default monitor mode" now applies to every way an author gets added, not
  just the Add Author button** (#1666) — the setting says "Applied to newly
  added authors", but only the manual Add Author form ever read it. Every import
  created the author with no mode at all, which the database quietly turned into
  "all books", so the one setting people reach for before a bulk import did
  nothing on exactly the paths they were trying to protect. Two reports: a
  Calibre library imported from scratch with the default set to None still
  produced authors monitoring everything, and a CSV author import queued about
  1250 books for download. Calibre, Audiobookshelf, CSV, Readarr and Goodreads
  imports, the series backfill and the author created behind a manual Add Book
  all honour the setting now, including the latest-book count. Anything that
  already picks a mode on purpose keeps it, so Hardcover list sync still
  monitors only the books on the list, and existing authors are untouched.
- **The Search page no longer offers movies and usenet junk to grab** — the
  video and non-book guards added for #1591, and later extended to the
  search-details pipeline in #1644, were missing from the freeform search behind
  the **Search** page. A query could return movie rips (`1080p`, `x265`,
  `S01E02`, releases the indexer itself filed under Movies/TV) and raw
  per-article usenet postings (`.part03.rar`, `[12/22]`, `yEnc`), each with a
  Grab button next to it — and grabbing one imports the wrong file. The same two
  content guards the book searches use now apply here. Relevance filtering is
  deliberately not applied: a freeform query has no book to score against, so
  which results are relevant stays the user's call.
- **Interactive search no longer shows video releases the automatic grabber
  rejects** (#1644) — the guard that keeps movie and TV releases out of book
  results was applied to scheduled searches but was missing from the search you
  run yourself, which is the one the UI uses. Releases tagged 1080p/x264/WEB-DL,
  or filed by the indexer under Movies, TV or similar, could therefore appear in
  manual search results and be grabbed by hand. The search details panel now
  also reports how many results that stage removed.
- **Library scan reconciles books whose author folder writes initials without
  spaces** (#1646) — a folder named "J.R.R. Tolkien" never matched the
  catalogue's "J. R. R. Tolkien", because the run-together form produced a token
  no author name can contain. Audiobook releases write initials this way almost
  universally, so those books stayed unmatched across every rescan. Manual
  import was unaffected, which is why it looked like only the scan was broken.
- **Library scan matches accented names and titles regardless of how they are
  encoded** (#1646) — macOS reports filenames with accents stored separately
  from the letter they sit on, while every metadata provider returns them
  combined. Nothing in the scanner reconciled the two, so "Björn Andersen" on
  disk never met "Björn Andersen" in the catalogue. Titles had a worse version
  of the same bug: "Die Höhle" was being reduced to "die hle" before comparison.
- **Library scan matches German titles spelled either way** (#1646) — "Die
  Hoehle" on disk now matches "Die Höhle" in the catalogue, and "Der Prozess"
  matches "Der Prozeß", the same way the indexer already matched them.
  Apostrophes too, so an "Enders Game" file finds "Ender's Game".
- **Bulk folder import matches transliterated author names** (#1646) — "Boell,
  Heinrich" now resolves to "Heinrich Böll" instead of scoring far too low to be
  considered a match.
- **Calibre import stops creating duplicate authors** (#1647) — Calibre compared
  author names byte for byte, so an author already in your library from
  Audiobookshelf or OpenLibrary got a second row whenever Calibre spelled the
  name differently ("J.R.R. Tolkien" against "J. R. R. Tolkien", or an accented
  name written in a different Unicode encoding, which Calibre on macOS
  produces). The books then showed up split across two author pages, and every
  later import recreated the split.
- **Missing descriptions, covers, ratings and genres get backfilled again**
  (#1647) — the enrichment step matched candidate books by comparing raw author
  and title strings from two different providers, so any book whose providers
  disagreed about initial spacing or accent encoding was never enriched. Because
  the comparison always failed the same way, it never recovered on retry.
- **A German author from DNB no longer ends up alongside a duplicate from
  OpenLibrary** (#1647) — the check that exists to merge those two rows compared
  "Tolkien, J. R. R." against "Tolkien, J.R.R." and found no match, so it
  created exactly the duplicate it was meant to prevent. Case handling for
  non-English letters was broken in the same query.
- **German and Scandinavian authors no longer end up duplicated or stuck in the
  review queue** (#1647) — author matching stripped accents ("Müller" →
  "muller") while every title matcher expands them ("Müller" → "mueller"), so a
  name spelled with accents in one place and ASCII-ised in another ("Jörg
  Müller" vs "Joerg Mueller") scored just under the auto-accept threshold. The
  same gap affected "Nesbø"/"Nesbo" and "Łukasz"/"Lukasz". Both spellings now
  resolve to one author.
- **Non-Latin series no longer collapse into one shared series** (#1645) — a
  series whose title had no Latin letters (Japanese, Chinese, Russian, Greek,
  Hebrew, Arabic) produced an empty identity, so the first such series imported
  claimed a shared row and every unrelated non-Latin series afterwards was filed
  under it. Accented Latin could collide the same way ("Ödland" and "Ådland"
  were treated as the same series). Series identities are now built from the
  full Unicode alphabet, with accents folded to their base letters, and a title
  with no letters or digits at all is skipped instead of being given a
  placeholder identity. Creating a series with an empty identity is now rejected
  outright, so no future import can reintroduce the same merge.
- **Japanese, Hindi and Greek series no longer merge into each other**
  (follow-up to #1645) — the fix above folded accents by stripping every
  combining mark, which is only correct for Latin. In Japanese those marks are
  part of the letter, so "ハード" (hard) and "ハート" (heart) both became
  "ハート" and shared one series row. Hindi vowel signs are spacing marks, so a
  Devanagari title was chopped into fragments instead of being kept whole. And a
  Greek title in all-caps keyed differently from the same title in mixed case,
  splitting one series in two. Accents now fold for Latin and Greek only — the
  two scripts where they really are accents — and every other script keeps its
  letters intact. Latin letters that carry no accent to strip (ß, ø, ł, æ, œ, þ,
  ð) now fold in slugs too, so a series is no longer split by spelling
  ("Straße-Serie" and "Strasse-Serie" are one series). Existing ASCII series
  identities are unchanged.
- **Accented titles no longer produce two different indexer searches** (#1648) —
  the same title could be sent to indexers in two different encodings depending
  on where it came from, which also gave it two different deduplication keys.
  Found by the new consistency test, not by a bug report.
- **Books stored with a format tag in the title stop getting a duplicate row**
  (#1648) — a book saved as "Title [Unabridged]" was not recognised as the same
  work as OpenLibrary's "Title", so an author sync created a second row for it.
- **Series named "... Series" or tagged "[Audiobook]" can be upgraded to full
  Hardcover data** (#1648) — the lookup and the upgrade check disagreed about
  what counts as the same series name, so some series matched one and failed the
  other.
- **Every settings field now confirms when it saves** (#1668) — the little green
  "Saved ✓" only appeared on some fields. Saving the book naming template, the
  audiobook folder template, the per-track audiobook template, either API key,
  or the log retention days gave no feedback at all, so there was no way to tell
  a save had gone through short of reloading the page. All of them confirm now,
  and there is one shared button behind them rather than a copy per field, so
  the next one can't quietly go missing. Failed saves show "Error" instead —
  three of those fields were swallowing the failure entirely, which would have
  shown a green tick on a save that never happened.
- **OIDC callback URLs behind trusted proxies** — redirect auto-detection now
  retains the original trusted proxy identity after real-client-IP resolution,
  preventing HTTPS callbacks from incorrectly falling back to HTTP.

### Changed
- **A non-Latin word now breaks phrase matching the same way an English one
  does** (#1642) — previously a "contiguous" title phrase would silently skip
  over an intervening Cyrillic or CJK word, because the matcher treated every
  non-ASCII letter as punctuation. Such titles are now handled by the same
  gap-tolerant path that already handled their English equivalents, so nothing
  is lost.
- **Normalization rules are defined in one place** (#1648) — the character
  folding used to compare titles and release names was copy-pasted into three
  packages and had started to drift, which is what caused #1643 and made #1642
  possible. It now lives in a single shared helper, with the four
  legitimately-different comparison alphabets (title matching, author identity,
  sort keys, slugs) documented alongside it. No change to how releases match.
- **Author name handling is defined in one place** (#1648) — several helpers
  that build author search queries and sort names existed as identical copies in
  four packages. They now live in one, and a new test suite compares every
  normalization rule in the codebase against a corpus of awkward names and
  titles (dotted initials, possessives, umlauts, Nordic and Polish letters, CJK,
  Cyrillic, Greek, Hebrew) so that two of them disagreeing is a build failure
  rather than a bug report months later.

## [v1.28.0] — 2026-07-25

A feature release driven almost entirely by user reports. Getting an existing
library in gets a **recursive bulk import** and a **manual import wizard**;
private-tracker users get a **per-indexer freeleech policy** that holds
ratio-costing releases for approval instead of hiding them; an accidental mass
import can now be undone with **bulk queue removal**. Alongside those, a
multi-user ownership leak in Hardcover list sync is closed, and a batch of
durability fixes stop a failed database write from being reported as a
successful import and stop background jobs racing the database on shutdown.

### Added
- **Bulk select and remove queue items** (#1622) — the queue could only "clear
  all failed", so an accidental mass import (one report: a CSV author import
  with monitoring left on, which queued ~1250 books) had no practical undo:
  the Books and Wanted bulk actions select one page at a time, and removing a
  queue item resets its book to wanted — so with the books still monitored the
  scheduler simply grabbed them again on the next sweep. The queue page now has
  per-row checkboxes and a **select-all that spans the entire queue, not just
  the visible page**, plus a bulk **Remove selected** with two options: *Also
  stop monitoring these books* (on by default — this is what stops the
  re-grab) and *Delete downloaded files* (off by default, so data and torrent
  seeds are kept). Backed by `POST /api/v1/queue/bulk-delete`, which removes
  each item from its download client under a bounded fan-out and reports a
  per-id result so one stale id can't fail the whole batch.
- **Per-indexer freeleech policy: hold ratio-costing releases for approval**
  (#1624) — a private-tracker user near a ratio floor previously had to
  restrict the whole indexer to Freeleech/VIP upstream in Jackett, which also
  hid normal releases from interactive search (where they'd happily pay the
  ratio cost on a book they actually want) and did nothing for bulk
  multi-book search, which has no picker and is pure fire-and-forget. Indexers
  gain an opt-in **Only auto-grab freeleech releases**: automatic grabbing
  takes only releases the tracker reports as freeleech (torznab
  `downloadvolumefactor` of 0 — an attribute Bindery previously never read),
  and anything that would cost ratio is parked in the existing **Pending**
  queue for manual approval rather than hidden or grabbed blind. Interactive
  search is deliberately untouched and still shows everything. Per-indexer
  rather than global because ratio economics are per-tracker — public trackers
  and Usenet are unaffected. A release whose ratio cost the indexer doesn't
  report is held rather than grabbed, on the principle that a visible hold is
  recoverable and an over-spent ratio isn't; half-leech (0.5) is held too.
- **Recursive bulk folder import** (#1434, closes #1402) — the bulk scan
  enumerated only immediate children and treated every subdirectory as a single
  unit, so a `Author/Books/Title.epub` layout collapsed into one row and
  dropped files, and any directory was labelled "audiobook" regardless of
  contents. The scan now walks recursively, yields per-file units at any depth,
  decides the directory-as-unit boundary by inspecting what's actually inside
  (audio vs ebook), derives the author from the folder layout, and matches on
  embedded EPUB metadata first, then folder author, then filename. A lone loose
  single-title match with no author corroboration is demoted from *confident*
  to *ambiguous*, which is the box-set mismatch reported in #1402.
- **Manual import wizard** (#1236) — a new `/import` page scans a folder,
  groups the results into *confident* / *ambiguous* / *unmatched*, and lets each
  unit be resolved individually (accept the match, pick from candidates, or
  search the existing catalogue) with a per-unit format override, then imports
  the resolved set in one batch.
- **Books view sorting and filtering** (#1349) — clickable sortable column
  headers (author, type, status, title, date; ascending↔descending, whitelisted
  server-side) and an all/monitored/unmonitored filter, mirroring the Authors
  view. Additive — no migration.
- **Per-file audiobook renaming** (#1126) — ebook imports have always renamed
  per file, but audiobook folders were copied in verbatim, so a library with a
  naming template still ended up with whatever track names the release shipped.
  A new `naming.audiobook_file_template` (empty = off, unchanged behaviour)
  flattens the audiobook folder on import and renames each track in resolved
  playback order, with the `{Part}` token carrying the index. It shares one
  deterministic track-ordering and sidecar-carry path with the existing
  multi-disc flatten, and works across copy, hardlink, and move. A template
  without `{Part}` is rejected when saved — and defensively fails the flatten
  rather than collapsing every track onto one filename.
- **Ebook + audiobook drop-folder pair gating** (closes #942) — completes the
  drop-folder handoff started in #941. A new `import.drop_pair_gating` opts
  `media_type=both` books into hold-until-paired handoff: the first format to
  arrive is parked in a non-terminal held state (files left in place, and kept
  out of the re-grab loop) until its sibling completes, then both are dropped
  together so a paired-reader tool ingests them as one. If the sibling never
  arrives, `import.drop_pair_gating_timeout_hours` (default 72) releases the
  held format on its own.
- **Ebook language detection at import** (#1160) — `dc:language` is now read
  from the imported EPUB and canonicalised, along with provider-supplied codes,
  to the ISO 639-2/B vocabulary the metadata-profile language filter already
  uses (`en`/`en-US` → `eng`, `zh-Hans` → `chi`). At import the ebook branch
  backfills a book's language when the catalogue left it empty, so the library
  and the language filter reflect what the file actually is. Locked fields are
  never overwritten.
- **Strict media-type policy** (#1575) — a new `default.media_type_strict`
  setting (off by default). Under a single-format default, a work available in
  both formats is narrowed to the configured one, and a work available *only*
  in the other format is skipped at add/refresh time instead of being created
  as a row that can never be satisfied (Discussion #1572). Skips are counted
  into the author-sync summary log, so the outcome is visible in the Logs tab
  without a notification per book.

### Security
- **Hardcover list sync now stamps an owner on the books and authors it
  creates** (#1621) — the follow-up to the v1.25.0 ownership work (#1457),
  which covered every create path *except* this one. Because the list syncer
  set no `owner_user_id`, everything it created was NULL-owned — and a NULL
  owner reads as global, so books synced from one user's Hardcover list were
  visible to **every** user, while manually added books stayed correctly
  private. Import lists are admin-configured and sync on a background schedule
  with no request identity to stamp from, so ownership now lives on the list
  row: each import list gains an **Owner**, and the syncer stamps that owner
  onto the books and authors it creates. An existing author reused by a sync
  keeps whatever owner it already had, so a list can never reassign another
  user's — or a shared — author. Leaving the owner unset means *Global*, which
  is the previous behaviour and what every existing list gets, so nothing
  changes until an owner is chosen. Only affects deployments with
  `BINDERY_ENFORCE_TENANCY` enabled.

### Fixed
- **Usenet ebook imports no longer leave an empty job folder behind** (#1623) —
  a single-file ebook grabbed via SABnzbd/NZBGet imported cleanly but left its
  completed job folder sitting empty under `complete/`. Usenet clients report
  the storage path of a single-file job as the *file*, not a directory, and the
  move-mode cleanup only ever pruned directories at or below the download path
  — which a file's parent is not, so the folder was never even a candidate for
  removal. Cleanup now prunes from the parent directory when the download path
  is the imported file itself. The existing protections are unchanged: a folder
  still holding anything else is kept, and the category directory above the job
  folder is never touched. (Torrent imports and audiobook folder imports were
  never affected, which is why audiobooks looked fixed as of v1.26.0.)
- **Audiobook import no longer reports success after a failed database write**
  (#1459) — the audiobook branch moved or copied the folder, recorded the
  format path on a best-effort basis, and then marked the download imported
  regardless of whether that write landed. With the path unrecorded the book
  still read as wanted, so the next sweep re-grabbed it into a `Title (2)`
  duplicate — and in move mode the original source was already gone. The write
  is now retried on transient SQLite lock/busy errors and, if it still fails,
  the import is marked blocked rather than falsely completed.
- **Background jobs are drained before the database closes on shutdown**
  (#1458) — the ABS import, Grimmory sync, and manual library scan ran on
  detached contexts and raced `database.Close()` on SIGTERM, producing
  "database is closed" errors and making Grimmory re-upload everything after
  each deploy, since the push is only recorded once it succeeds. Those jobs now
  run on a shutdown-scoped context that is drained, within a bounded grace
  window, after HTTP shutdown and before the database is closed.
- **ABS import keeps its resume checkpoint across a shutdown** (#1472,
  partial) — the enumerator flushed its resume checkpoint on the very context
  that had just been cancelled, so the flush failed and the progress it existed
  to preserve was discarded. It now writes on a detached context. Previously
  unreachable, and made live by the shutdown work above.
- **NZBGet queue removal honours "keep files"** (#1456) — removing a download
  sent `DeleteFinal`, which isn't a valid editqueue command and ignored the
  flag entirely. Keeping files now sends `GroupParkDelete` and deleting them
  sends `GroupDelete`. Requires NZBGet 17 or newer.

### Changed
- **Shutdown grace defaults lowered** (part of #1458) — the HTTP shutdown grace
  drops from 30s to 10s and the background-job drain window is 15s, so the two
  serial waits total 25s and fit inside the chart's 30s
  `terminationGracePeriodSeconds` instead of being cut short by it. Operators
  who raised that value can raise these to match.

## [v1.27.0] — 2026-07-22

A feature release built around library management. Two new tools for keeping an
existing library tidy — **Rename files** (reorganize files to the current
naming template) and **Match to book** (rescue a completed download the
auto-matcher couldn't place) — plus a guard against auto-grab pulling in
unrelated movie/TV releases and a fix for German National Library catalogues
showing one book per printing.

### Added
- **Rename files: reorganize an existing library to the naming template**
  (#1181) — the naming template used to apply only at import time, so changing
  it later, importing a pre-existing library, or ending up with a half-renamed
  mix left no way to reconcile files short of deleting and re-importing. A new
  **Rename files** action (on the book detail page and the author page) previews
  every tracked file's `current → proposed` path under the current template and,
  once confirmed, moves the ones that differ — updating the library index and
  recording a rename in history. It is always a move within the library (never a
  copy), refuses to overwrite an existing destination, skips files already in
  the right place, and leaves anything not on disk untouched. Ebooks move as
  single files; audiobooks move as whole folders. Backed by
  `GET /api/v1/reorganize/preview` and `POST /api/v1/reorganize/apply` (both
  admin-only). Whole-library scope is available through the API; the UI exposes
  the per-book and per-author scopes.
- **Manually match an unmatched download in the queue** (#1589) — a download the
  auto-matcher couldn't tie to a book ("could not match any book to this
  download") used to sit in the queue as a dead-end import failure. Import-failed
  items now have a **Match to book** control: search your library, pick the book
  the files belong to, and Bindery imports the already-downloaded files against
  it — attaching the file and flipping the download to imported, with inline
  feedback. The scanner now records where an unmatched download's files are (it
  previously discarded that path), so the match can import them directly instead
  of hoping the download client still remembers the release; downloads without a
  recorded path fall back to a client re-poll. Downloads the scanner terminally
  blocked after exhausting their import-retry budget (the "stuck after three
  attempts" case) are recoverable too — the Match to book and Retry import
  controls now appear for `importBlocked` items, and a match re-imports the
  recorded files (or re-arms the scanner with a fresh retry budget) instead of
  leaving them permanently stuck.

### Fixed
- **Auto-grab no longer selects or imports unrelated video releases** (#1591) —
  an automatic audiobook search could pick a movie, TV, or music release that
  shared a few words with the book title, download it, and move the whole folder
  (video file included) into the audiobook library marked as imported. Two new
  guards close this: release names carrying video-only markers (`1080p`, `x265`,
  `WEBRip`, `S01E02`, and similar) and results the indexer filed under a
  movie/TV/console/PC category are now dropped from book search results; and at
  import time, a download whose largest file is a video file is blocked for
  manual review instead of being imported (an explicit format chosen through
  manual import overrides the block).
- **DNB author catalogues collapse editions into one book per work** (#1585,
  #1586) — the Deutsche Nationalbibliothek issues one MARC record per edition,
  printing, and volume with no work abstraction, so adding a DNB-primary
  author's catalogue produced a wall of near-duplicate books for the same work.
  The author-works path now groups records by work and volume, drops the
  combined volume-0 record when a work has numbered volumes, and rebuilds each
  representative's title from its series/volume statement, so one work becomes
  one book. Existing libraries keep any duplicate rows already imported (the
  author sync is add-only); remove them manually or via a future merge (#1358).
- **Security: the wanted/missing list is now scoped per user** (#1600) — under
  multi-user tenancy (`BINDERY_ENFORCE_TENANCY`), `GET /api/v1/wanted/missing`
  returned every user's wanted/missing books to any non-admin instead of just
  their own, the one book-list route that had missed the `owner_user_id` scope
  applied everywhere else. It now filters like the main book list; admins,
  API-key, and single-tenant deployments are unaffected.

### Changed
- The queue **Retry import** control now also revives downloads the scanner
  terminally blocked (`importBlocked`) after exhausting their retry budget, not
  only `importFailed` ones — previously it silently no-op'd on blocked items.
  (Part of #1589.)
- **Dependencies:** `modernc.org/sqlite` 1.53 → 1.54, the distroless base image
  for the main binary and the discord-stats/telemetry sidecars, and seven
  minor/patch web dependencies (#1582, #1583, #1584, #1554, #1555).

### Docs
- Clarified that `BINDERY_LIBRARY_DIR` is a scan/reconcile target in External
  import mode, not a destination Bindery writes to (#1558).

## [v1.26.2] — 2026-07-19

A patch release fixing two reported metadata bugs: profile language filters
that never applied to what actually got downloaded, and DNB-primary setups
silently importing English OpenLibrary catalogues.

### Fixed
- **Metadata profile languages now constrain downloads, not just the
  catalogue** (#1573, #1576, Discussion #1572) — the profile's
  `allowed_languages` filtered which books entered the library, but the
  release filter only activated for English-only profiles and the automatic
  grab path never loaded the profile at all, so an ITA/ENG profile still
  grabbed German or French releases whenever one ranked first. The
  foreign-tag list now maps each release marker to its ISO 639-2/B code so
  any profile language set can be checked against a release: tagged with a
  language outside the set drops it, untagged passes (the tag is only ever
  a negative signal). Auto-grab resolves the author's profile before
  grabbing, mirroring the interactive search, with the global
  `search.preferredLanguage` setting as the fallback. Closes #1573.
- **DNB as primary provider now keeps the DNB author identity** (#1574,
  #1577) — author search collapses same-name records from multiple
  providers to the most complete one, judged by work and rating counts
  that only OpenLibrary reports, so the `dnb:` record lost the collapse
  every time even with DNB promoted to primary. The added author kept an
  OpenLibrary foreign ID, and since the catalogue imports from the
  provider the foreign ID names, DNB-primary users got English OL work
  titles — and book searches built from those titles could never match
  German releases. The primary provider's record now wins the collapse
  ahead of the count comparisons; OpenLibrary-primary setups are
  unchanged. Authors already imported under the OL identity can be
  relinked once via **Link metadata** on the author page. Closes #1574.

## [v1.26.1] — 2026-07-18

A patch release fixing three reported bugs: dual-format downloads serving the
wrong file, combined Audiobookshelf libraries dropping their ebooks on import,
and the return of the author-sync foreign-key warning burst.

### Fixed
- **Downloading a dual-format book now serves the format you selected**
  (#1561, #1563) — the UI sends `?format=ebook|audiobook` when a book has
  both formats, but the download handler never read the parameter and always
  walked its legacy path chain, so selecting Audiobook still delivered the
  epub whenever an ebook file existed. `GET /api/v1/book/{id}/file` now scopes
  the file selection by the requested format, using the same query-parameter
  contract the delete endpoint already had. Requests without the parameter
  keep the old behaviour, and books imported before the dual-format schema
  satisfy a format-scoped request from their single legacy path only when its
  on-disk shape matches (directory = audiobook bundle, file = ebook). Closes
  #1561.
- **ABS import now picks up ebooks that Audiobookshelf marks supplementary**
  (#1565, #1566) — a combined item (epub stored next to the audio files in
  one ABS library item) imported only the audiobook when the ABS library has
  "Audiobooks only" enabled: ABS then never promotes the epub to
  `media.ebookFile`, exposing it only as a supplementary `libraryFiles` entry,
  and Bindery read `media.ebookFile` exclusively. The normalizer now falls
  back to the supplementary ebook (preferring `.epub`, mirroring ABS's own
  primary-ebook selection), so both editions import — no extra API calls,
  since the expanded detail fetch already carried the data. A promoted
  primary ebook still wins. Reported in Discussion #1556 on ABS v2.35.1;
  closes #1565.
- **Author catalogue sync stops mid-flight when its author is deleted**
  (#1559, #1564) — the async sync runs against a snapshot of the author and
  can spend minutes fetching a prolific author's catalogue. If the row was
  deleted in that window (the Add Book orphan cleanup after a poll timeout
  where #808's direct insert didn't land, or a user deleting the author
  mid-refresh), the insert loop failed the `author_id` foreign-key constraint
  once per work — the reported burst logged 180 warnings for a single
  "Michael Lewis" sync. The sync now re-checks the author row after the slow
  fetch phase and aborts on a foreign-key failure whose author has vanished,
  logging a single line instead. Harmless before and after (the failed
  inserts were never persisted), but the noise made real errors easy to miss.
  Closes #1559.

## [v1.26.0] — 2026-07-14

A correctness release. Two independent reports pinned real bugs in the parts of
Bindery that decide *what to grab* and *what to do with it afterwards*: the
relevance filter could grab a completely different book whose title merely
contained yours, and usenet imports were quietly leaking every completed
download folder to disk forever. Both are fixed. Around them, this release is
mostly about Bindery telling you the truth when something is wrong — storage
health, VPN-blocked connection tests, and Grimmory's connection probe all stop
hiding behind generic errors — plus a search-first Add Author flow and a Calibre
path-remap escape hatch for mismatched container mounts.

### Added
- **Calibre push path remap** (#1346) — Bindery hands the Bindery Bridge
  plugin the exact path it stores each book at, and the plugin opens that
  path on *its* side of the container boundary; when the two containers mount
  the library at different points (the recurring Unraid case), every push
  failed with "No such file or directory". A new **Settings → Calibre → Push
  path remap** field (plugin mode) translates Bindery's library prefix to the
  Calibre container's before the push, using the same `from:to[,from:to]`
  grammar as `BINDERY_DOWNLOAD_PATH_REMAP` — e.g.
  `/books:/mnt/user/media/books`. Malformed pairs are rejected at save time;
  empty means no translation, and aligning the mounts remains the preferred
  zero-config setup.

### Changed
- **Search-first author acquisition** (#1227, #1516) — select an author result
  before reviewing monitoring and download options, with consistent localized
  add dialogs and inline errors. Stage 1 of unifying the add and search
  acquisition flows; the issue stays open to gather feedback before the next
  stage. Community PR from @magrhino.
- **Base image and dependencies refreshed** (#1527, #1528, #1529) — the
  distroless `static-debian12` base moves to its current digest, alongside the
  usual grouped Go and frontend dependency bumps.

### Fixed
- **Usenet imports no longer leak completed job folders or library receipts**
  (#1542) — `import.mode` was applied with no protocol distinction, so usenet
  downloads inherited hardlink/copy behaviour whose only purpose is preserving
  torrent seeding. The completed job folder was left behind forever — and
  invisibly, since post-import cleanup removes the client's history entry but
  not its files (one report: 2.4 GB orphaned from three audiobook grabs).
  SABnzbd/NZBGet downloads now resolve `auto` and `hardlink` to `move`
  (explicit `copy` stays honoured for operators who want the client's
  retention to see finished files). Separately, directory placements copied
  the whole job tree verbatim, so `.nzb` receipts and `.par2` repair files
  landed in the library next to the media: hardlink, copy, move, and
  multi-disc-flatten placements now all skip download artifacts
  (`.nzb`/`.par2`/`.sfv`/`.srr`/`.srs`/`.diz` — `.nfo`, covers, and cue
  sheets are deliberately kept). Multi-disc flattening also works under move
  mode now (flatten via copy, then remove the source), so usenet downloads
  resolving to move don't lose it. Reported by cleb on Discord with both root
  causes correctly identified.
- **Wrong-author grabs from embedded title phrases** (#1539) — the release
  relevance filter accepted any release whose name contained the book's title
  words as a contiguous phrase, with no author check on that path, so a
  different work embedding the requested title could be grabbed and imported
  ("Reborn as an Assassin's Apprentice, Vol. 1 by okiuta" matched for Robin
  Hobb's "Assassin's Apprentice"; reported by cleb on Discord with the root
  cause pinned). Phrase and in-order title hits are now only trusted on their
  own when *anchored* — preceded by nothing but the author, a series index
  ("Book 1", "Vol. 2"), numbers, or filler words. When real foreign words sit
  in front of the title (usually another work's longer title), the requested
  author must appear somewhere in the release name. Releases titled with just
  the book title still pass, so the fix costs no recall on the common
  author-less naming shapes; the narrow tradeoff is that a release naming only
  a series *name* (no author, no "Book N" marker) before the title is now
  rejected rather than risk importing the wrong book.
- **Bulk searches no longer burst your indexers** (#1515) — "search all wanted"
  for a prolific author, filling a series, per-author auto-search, and the
  scheduled wanted loop now pace their indexer searches (a short gap between
  each) instead of firing as fast as slots free up. A 30-book author could
  previously flood a rate-limit-free Prowlarr into dropping requests, so
  nothing got grabbed; the fan-outs still run with the same concurrency caps
  but no longer sustain a tight query loop.
- **Transmission polling on large torrent histories** (#1524) — the download
  poller read at most 1 MiB of Transmission's `torrent-get` reply, so an
  instance with a few thousand torrents (one report: ~12 MiB for 5000+) had its
  response silently truncated and every poll failed with "unexpected end of
  JSON input", blocking imports. The RPC read cap is now 64 MiB, and a reply
  that somehow still exceeds it returns a clear "too many torrents to poll in
  one request" error instead of invalid JSON.
- **Grimmory "Test connection" against Grimmory v3.x** (#1485) — the connection
  test probed `GET /api/status`, a route current Grimmory (v3.x) no longer has.
  Its Spring security layer answers any unmapped `/api/**` path with a 401
  Whitelabel page, which looked like an auth wall (and was mistaken for one in
  #1448) but was really a missing route, so the test could never pass and failed
  with `invalid character '<'`. The probe now hits Grimmory's public
  `GET /api/v1/healthcheck` endpoint for reachability and version; credential
  verification stays with the separate login round-trip, so "Test connection"
  still reports whether your username/password actually work.
- **Storage health now says *why*, not just *that*** (#1427) — the
  "downloads and library can't hardlink" banner and the "not writable"
  warnings were generic, sending operators hunting through mount tables and
  permission bits blind. The hardlink probe now names the actual cause
  (different filesystems; same device ID but cross-mount EXDEV, typical of
  mergerfs pools, separate bind mounts, and Unraid `/mnt/user` shares; or a
  filesystem that refuses hardlinks, common on exFAT/NTFS/network shares) in
  Settings → General. And a failed writability check now reports the uid/gid
  the process actually runs as versus who owns the directory, with the
  `user: "UID:GID"` hint when they differ — the classic case being folders
  prepared for the stack's usual user while the container runs as the
  distroless default `65532` because `user:` was never set in Compose.
- **VPN-killswitch timeouts are named, not just reported** (#1474) — when an
  ABS or Calibre-plugin "Test connection" times out against a LAN-shaped host
  (private IP, bare Docker hostname, `.local`/`.lan` suffix), the error now
  points at the usual culprit: Bindery sharing a VPN container's network
  (`network_mode: service:gluetun`) whose killswitch drops LAN traffic, with
  the `FIREWALL_OUTBOUND_SUBNETS` fix named inline. Real upstream errors
  (auth failures, refused connections) and public hosts keep their unmodified
  message. Complements the "Running Bindery behind a VPN" deployment docs.
- **Go race CI no longer times out** (#1531) — split the race suite into six
  parallel shards so the large API and database test packages stay below their
  per-package deadlines. Community PR from @magrhino.

### Docs
- **Shared ebook + audiobook folder layout, and what `BINDERY_DOWNLOAD_DIR`
  actually does** (#1426) — the Storyteller-style shared folder (an ebook and
  its audio files in one directory) is Bindery's *default* behaviour when
  `BINDERY_AUDIOBOOK_DIR` is left unset, since both naming templates share the
  `{Author}/{Title} ({Year})` prefix; now documented with the resulting tree.
  And `BINDERY_DOWNLOAD_DIR` is **not** a watch folder — per-job import paths
  come from the download client's API, and the env var only feeds validation,
  storage health, the hardlink probe, and qBittorrent save paths — so the
  TRaSH split-tree layout (`/data/torrents` + `/data/usenet`) works as-is.

## [v1.25.0] — 2026-07-10

A contributor-driven release. The headline is manual metadata editing with
field locks — edit a book's title, genres, language, description, or release
date and every refresh path keeps your values — which also unlocks clean
opinionated genre taxonomies in folder paths. Around it: naming-template
conditionals and zero-pad widths, a `{Lang}` token, an ebook/audiobook/both
selector on Add Book, per-account Hardcover reading lists, an honest Import
Mode default (community PR), multi-user tenancy fixes for owner stamping and
the series views, and the next batch of audit bug fixes.

### Added
- **Manual metadata editing with field locks** (#1237, #1446) — the book page
  gains an Edit action for title, description, genres, language, and release
  date. Every field you edit is locked so the nightly metadata refresh,
  author-works sync, and ABS/Calibre re-imports keep your values (a 🔒 marks
  locked fields; one click unlocks them all). Genres can also be applied in
  bulk: an author-level override in Edit Author and a Set genre action per
  series stamp + lock the same genre list across all their books, so an
  opinionated genre taxonomy in folder paths (`{Genre}/...`) stays clean.
  Re-bind and metadata re-map clear locks — an explicit identity change asks
  for the new record wholesale. See the new
  [Metadata Editing wiki page](docs/Metadata-Editing-Wiki.md).
- **Conditional text and zero-pad widths in naming templates** (#1127) —
  literal text placed inside a brace group renders only when its token has a
  value (`{Title}{ - Series}` emits the dash only for series books, no more
  trailing separators), and numeric tokens accept a width modifier
  (`{SeriesNumber:2}` → `02`) so alphabetic filename sorts keep parts in
  order. Both are backward compatible: bare tokens, `{Genre:Unsorted}`-style
  defaults, and unknown-token passthrough behave exactly as before. One edge:
  a 1–2 digit modifier is now a width, so a numeric *default* that short is
  no longer supported (3+ digits, e.g. `{Year:2024}`, still works as a
  default).
- **`{Lang}` naming token** (#1175) — naming templates can now include the
  book's language code (e.g. `en`), so foreign-language books can carry the
  language in their folder or file name. Blank when the language is unknown,
  collapsing cleanly like the other optional tokens.
- **Format selector on Add Book** (#1397) — the Add Book modal (Authors/Home
  page) now has an ebook / audiobook / both selector, matching the Series
  page. Default keeps the previous behaviour (provider metadata, falling back
  to the `default.media_type` setting). Picking a format applies it to the
  added book even when it already existed in the library, re-evaluating
  wanted status so the missing format gets searched.

### Security
- **Multi-user tenancy: owners are now stamped and series views scoped**
  (#1457, #1416) — new books inherit their author's owner on every create
  path (add-book, author sync, series fill, ABS/Calibre imports), and new
  downloads carry the grabbing user (background grabs inherit the book's
  owner), so per-user scoping finally has data to scope on: a non-admin's
  queue is no longer empty under tenancy, and the series list/detail no
  longer let one user enumerate another user's titles, covers, and statuses.
  The author page's embedded book list now applies the same owner predicate
  as the book list, so the two views agree on co-authored books. Existing
  unowned rows keep their legacy world-visible behaviour; only affects
  deployments with `BINDERY_ENFORCE_TENANCY` enabled.

### Fixed
- **Import Mode UI no longer claims Move is the default** (#1444) — the
  selector pre-selected Move while the backend has defaulted to auto
  (hardlink on the same filesystem, else copy) since the seeding fix. Auto is
  now a first-class, selectable mode shown as the default, restoring a UI
  path back to the safe behaviour, and `import.mode` is validated so a typo
  fails loudly instead of silently behaving as auto. Contributed by
  @johnistheman.
- **Per-account Hardcover reading lists** (#1489) — Hardcover's built-in
  shelves ("Want to Read", …) share one slug per account, so loading a second
  person's lists with their token showed their shelf as the already-added
  first one and toggled the wrong list. List identity is now (slug, account):
  the picker reports which Hardcover account it's browsing, saved lists
  remember the account they came from (shown as an @username chip), and two
  households' "Want to Read" lists sync side by side, each with its own
  token.
- **Lists no longer silently truncate at 100 rows** (#1467) — the History
  page now pages through your full history on the server (with a working
  event-type filter that offers all event types), an author's page loads the
  complete catalogue even for authors with more than 100 books (so counts,
  filters, and select-all cover everything), and the Calendar loads every
  release in the month instead of stopping at 500.
- **Transmission/qBittorrent: healthy downloads no longer blocked as "source
  no longer available"** (#1461) — the stale-failure check treated a
  category-filtered torrent listing as a complete one. A failed-import
  download whose torrent was moved to another download directory, lost its
  label, or sat under a different qBittorrent category (while the unfiltered
  listing was unavailable) was terminally blocked even though the torrent was
  still seeding. Filtered or degraded listings now only block on retry
  exhaustion, never on "missing from the list".
- **Library scan single-flight** (#1460) — triggering a manual library scan
  while another scan is running (manual or the scheduled one) now returns
  409 Conflict instead of starting a second concurrent walk that could race
  on book creation and clobber the last-scan status.
- **Migration runner semicolon gotcha** (#1465) — the SQL migration runner
  split statements on `;` before stripping comments, so a semicolon inside a
  `--` comment (or a string literal) corrupted the statement list and aborted
  boot. The splitter is now aware of line comments, block comments, and
  quoted literals, so migration authors can write natural SQL comments.

## [v1.24.3] — 2026-07-10

A small security-driven patch. The Go toolchain moves to 1.26.5 to pick up two
stdlib vulnerability fixes that are reachable from Bindery code paths, and the
Grimmory connection test now says what it actually received when a server (or a
reverse proxy in front of it) answers with something other than JSON.

### Security
- **Go toolchain bumped to 1.26.5** (#1492) — rebuilds all binaries and images
  against a stdlib carrying the fixes for GO-2026-4970 (`crypto/tls`) and
  GO-2026-5856 (`os.Root`), both confirmed reachable in Bindery by
  `govulncheck`. v1.24.2 binaries were built with the affected stdlib; no
  Bindery code changes were needed. Workflow `go-version` pins and the main
  `Dockerfile` digest move together so CI and the published image agree on the
  toolchain.

### Fixed
- **Grimmory connection errors now name the content type** (#1493) — adding a
  Grimmory connection whose URL answers 2xx with a non-JSON body (typically a
  reverse proxy or SPA fallback serving an HTML page on the API path) failed
  with the bare JSON decoder error `invalid character '<' looking for beginning
  of value`. The client now reports the HTTP status and `Content-Type` it got
  back and points at the URL as the likely culprit, so misrouted proxies are
  diagnosable from the Settings screen. Refs #1485.

## [v1.24.2] — 2026-07-07

A fast follow-up patch. The headline fix is Bulk Folder Import, which stalled for
minutes and then failed with no logs on any library with a few hundred entries,
because it reloaded the whole book and author catalogue once for every item in
the folder. The rest is a batch of correctness and security fixes surfaced by an
internal audit: two data-safety races on the download pipeline, a nightly refresh
that wiped enriched author metadata, a Transmission queue mapping that failed
healthy downloads, and two hardening fixes covering system logs and indexer keys.

### Security
- **System logs are now admin-only** (#1451) — `/system/logs` and
  `/system/loglevel` were reachable by any authenticated user, exposing the
  app-wide log stream (other users' book and author names, OIDC usernames,
  download titles) and letting a non-admin flip the global log level. They now
  require admin, matching every other global-infra route.
- **Indexer API keys no longer leak into errors** (#1450) — when a torrent/NZB
  fetch failed at the transport layer (timeout, DNS, TLS), the underlying
  `url.Error` carried the full signed download URL, including the indexer's
  `apikey`, into the download row, history, and webhook/Discord notifications.
  All five download-client fetch paths now scrub the key before the error is
  wrapped.

### Fixed
- **Bulk Folder Import no longer times out on large libraries** (#1473) —
  scanning a folder used to reload the entire book and author catalogue once for
  every item, so a folder with a few hundred entries fired hundreds of
  full-table queries in a single request and ran past the server's request
  timeout, leaving the connection dead and no logs to explain it. The scan now
  loads the catalogue once and reuses it for every item, and it logs when a scan
  starts and finishes with how long it took and how many matches it found.
- **Download status races** (#1462) — two things updating the same download at
  once could slip an illegal state change through (re-completing an already
  imported download or double stamping timestamps). The status update is now
  applied atomically so only one writer wins and the row can't land in a bad
  state.
- **Transmission downloads queued behind others no longer fail to import**
  (#1452) — the RPC status enum was mapped wrong (`3` was treated as "seeding",
  `6` as "stopped"; they are actually "queued to download" and "seeding"). A
  torrent sitting in Transmission's download queue at 0% was treated as
  complete, import fired against an empty directory, failed, and after
  exhausting its retries the download was terminally blocked even though it was
  perfectly healthy. Completion is now keyed off real seeding / 100%-downloaded
  state.
- **The wanted-book sweep no longer double-grabs** (#1453) — a Wanted book stays
  Wanted until its file is imported and reconciled, so a book whose grab was
  still downloading (or working through the import pipeline) was re-searched on
  the next scheduled sweep and, if the indexer now ranked a different release
  best, a second download was grabbed for the same book. The sweep now skips
  books with a grab already in flight, while still re-searching books whose
  previous download failed.
- **Author metadata refresh no longer wipes enriched fields** (#1463) — the
  nightly refresh now keeps the existing description, ratings and rating count
  when the upstream record comes back sparse, instead of overwriting them with
  empty values.
- **qBittorrent category rejections are no longer ignored** (#1464) — Bindery
  now notices when qBittorrent rejects a category assignment (for example a 409
  because the category does not exist) instead of silently ignoring it, so the
  missing-category warning fires and a mis-categorized torrent no longer
  vanishes from the download poll.
- **Grimmory "Test connection" now authenticates before probing status** (#1448)
  — recent Grimmory guards `/api/status` behind a valid session, so the test
  button returned a 401 for anyone using username/password auth. Bindery now
  logs in first and presents the token when checking connectivity (retrying once
  if a cached token has expired), so the test succeeds instead of failing at the
  door.
- **Log search takes wildcards literally** (#1466) — searching the logs for a
  term containing `%`, `_`, or `\` now matches those characters as text instead
  of treating them as SQL wildcards, so you get the results you expect.
- **Faster library, book, and search queries** (#1454) — the query that finds
  each book's ebook/audiobook file ran twice per book listing and was doing a
  full scan of the `book_files` table because no index covered its `format`
  filter. A new composite index makes it an index seek, most noticeable on large
  libraries and on the paginated Books page. Applied automatically on upgrade.
- **Bulk "search" on the Wanted page is much faster** (#1455) — selecting a
  large batch and hitting search issued one of the heaviest book queries per
  book instead of a single batched fetch, so a 500-book bulk search ran hundreds
  of full-table aggregations. It now loads all selected books in one query.

## [v1.24.1] — 2026-07-06

A patch cut of fixes surfaced by the community in the days after v1.24.0: two
were Bindery giving actively wrong information (a dead-end fix for NZBFinder's
error 203, and Grimmory test failures rendering as an opaque "Bad Gateway"),
and two were the library scan and bulk import silently mishandling flat
`Author/Title.epub` layouts.

### Fixed
- **Error 203 guidance no longer points at a setting that can't be changed**
  (#1424) — the NZB grab error and the Troubleshooting wiki suggested
  disabling Prowlarr's per-indexer Redirect setting, which Prowlarr requires
  for Usenet indexers. Both now explain the real situation:
  application-whitelisting indexers (like NZBFinder) have to approve
  Bindery's identity, tracked in #1425.
- **Grimmory "Test Connection" failures now show the real error** (#1431) — a
  failed test displayed the bare HTTP status text ("Bad Gateway") instead of
  the actual diagnostic (connection refused, login rejected, upstream proxy
  error). The UI now surfaces the full message, upstream HTTP errors are
  labeled with their status code, and failures are logged server-side.
- **Bulk folder import shows the full source path on every row** (#1435) —
  previously only the basename was shown, so an ambiguous match gave no way
  to tell which file the "pick a book" dropdown referred to.
- **Library scan no longer hides untracked ebooks that share a folder with a
  tracked one** (#1436) — in a flat `Author/Title.epub` layout, one
  registered file made the scan silently skip every sibling ebook in that
  author folder, and the skipped files were missing from the result counts
  entirely. Folder-level suppression now only applies to audiobook folders,
  and the scan result shows an "Already tracked" count so files found always
  adds up.

## [v1.24.0] — 2026-07-04

The Grimmory integration goes from a settings tab nothing read to a working
push pipeline, and the UI can now be embedded in a dashboard iframe when an
operator explicitly opts in. A new per-author "Monitor newly discovered
books" policy defuses the refresh-mass-monitors-the-back-catalogue trap.
Three community bug reports filed this week are fixed in the same cut: books
silently stranded under the wrong author, the author-page filters mishandling
dual-format books, and unreadable NZB grab failures. Repo-side, the CI
pipeline gains AI triage/review bots and a hardening pass on the fork-facing
workflows.

### Added
- **Grimmory push pipeline — BookDrop upload on import plus bulk sync**
  (#1392, closes #826) — the Grimmory integration was configuration-only: a
  `Ping()` client and a Settings toggle no code ever read. It now works end to
  end. A new `internal/grimmory` client does JWT auth against Grimmory's API
  (login → access/refresh pair, rotation, one 401 retry after re-auth; a set
  `api_key` is honoured as a static Bearer token and bypasses login), and
  streams a multipart upload to `POST /api/v1/files/upload/bookdrop` on a
  dedicated 5-minute timeout. A `Pusher` is hooked into the importer alongside
  the Calibre/CWA handoffs — settings are read live per push (no restart) and
  pushes are best-effort by contract, so a Grimmory failure never fails the
  import. BookDrop has no server-side dedup, so idempotency is Bindery's:
  migration 059 adds a `grimmory_pushes` table keyed by file path, consulted
  on every push. Bulk sync (`grimmory.Syncer`, admin-only
  `POST /grimmory/sync` + `GET /grimmory/sync/status`) mirrors the Calibre
  single-job pattern (409 on concurrent start, polled progress, capped error
  list). Settings → Grimmory gains username/password fields, a real
  Test Connection login check, a "Push all to Grimmory" button with live
  progress, and the experimental banner is gone. Ebook files only for now —
  BookDrop takes one file per upload, which multi-part audiobook folders don't
  reduce to.
- **Opt-in iframe embedding via `BINDERY_FRAME_ANCESTORS`** (#1367) — the UI
  could not be embedded in an `<iframe>` because `SecurityHeaders` hard-coded
  `X-Frame-Options: DENY` and CSP `frame-ancestors 'none'`, blocking use inside
  dashboards like Organizr. That clickjacking lockdown stays the default; an
  operator can now set `BINDERY_FRAME_ANCESTORS` to a CSP `frame-ancestors`
  source list (`'self'` for same-origin, or a specific origin such as
  `https://organizr.example.com`) to allow framing per trusted origin. When
  set, `X-Frame-Options` is dropped, since it can't express an origin allowlist
  and `DENY`/`SAMEORIGIN` would override the more expressive CSP directive.
  Documented in `docs/DEPLOYMENT.md` and `charts/bindery/values.yaml`.
- **Per-author "Monitor newly discovered books" setting** (#1348) — "Refresh
  Metadata" shares the add code path, so on a default-config author (monitor
  mode `all`) a refresh that discovered the provider's full back-catalogue
  created every work monitored + Wanted, queueing a search storm the user
  never asked for. Each author now has a Monitor New Items policy in the edit
  modal: **Follow monitor mode** (default, previous behaviour) or **Add as
  unmonitored**, which applies to works discovered *after* the initial sync —
  the add flow and migrations still honour the monitor mode, and a genuinely
  wanted back-catalogue is one bulk-monitor away. Import-created authors
  (Calibre, Audiobookshelf) default to **Add as unmonitored**: they start
  with a partial catalogue, which made the first refresh after an import the
  classic detonation point.

### Fixed
- **Author sync no longer strands books under the wrong author** (#1405) — a
  work fetched during an author's sync could match an existing row created
  under a different author record (a duplicate OpenLibrary author key, a
  Calibre shell author, or an earlier book-level add). The sync refreshed that
  row's ratings forever but never re-linked it, and since the author page
  filters by `author_id` the book was permanently invisible under the real
  author — the reported case being Elantris missing from Brandon Sanderson
  despite being fetched on every sync. The sync now re-links such rows to the
  author being synced, using the provider's credited-author list as the
  safety check: a genuinely co-authored work (credited to both authors, e.g.
  the Wheel of Time books Sanderson finished for Robert Jordan) stays with
  its current owner so it can't ping-pong between authors on alternating
  syncs, and when authorship can't be determined the row is left untouched.
- **Author-page filters now respect the selected media type for dual-format
  books** (#1406) — a book wanted as Both was invisible under the
  Type: Ebook / Type: Audiobook chips (the filter compared `mediaType`
  exactly), and the Status chips judged the combined status, so a Both book
  whose ebook was already imported never showed under Type: Ebook +
  Status: Imported while its audiobook was still wanted. Both books now match
  either type chip, and with a type selected the status filter judges that
  format's own state (its file on disk → imported), so each side of a
  dual-format book filters correctly.
- **NZB grab failures now explain themselves — including the NZBFinder
  "error 203" case** (#1404) — when an indexer refuses the NZB download, the
  error now surfaces the parsed newznab code and description instead of raw
  XML, and when the grab was redirected off its original host (Prowlarr's
  per-indexer **Redirect** setting handing the fetch to an app-whitelisting
  indexer that rejects Bindery's identity) the error names both hosts and
  points at the Prowlarr setting to disable. Applies to both SABnzbd and
  NZBGet grabs; a matching entry was added to the troubleshooting guide.

### CI
- **AI triage, PR review, and nightly backlog-sweep bots** (#1222) — new
  `ai-triage`, PR-review, and `ai-sweep` workflows automate issue triage and
  first-pass PR review. The same change hardens the fork-facing notify
  workflows: the `pull_request_target` path no longer interpolates
  `github.event.*` inline (script-injection vector), permissions are cut to
  the minimum each job needs, and the ESLint security scan is pinned in the
  lockfile so the SARIF step actually runs instead of silently no-opping.

## [v1.23.3] — 2026-07-02

What began as a second same-day patch grew into a fuller batch: a
security-hardening sweep of the API surface, two author-metadata cleanups from
a first-time contributor, the naming-template fix behind several import
complaints, and the original cut's fixes (a concurrent-import crash, bulk
folder import rejecting its obvious targets, and a defence-in-depth guard
behind the v1.23.2 data-loss fix).

### Security
- **Recommendations are scoped to the signed-in user** (#1384) — every
  recommendation handler (list, dismiss, refresh, clear dismissals, author
  exclusions) hardcoded user id `1`, so on a multi-user install all users
  shared the admin's Discover feed and could read or mutate each other's
  dismissals and exclusions. It was the only handler family that ignored the
  session identity; all eight call sites now thread
  `auth.UserIDFromContext`, matching how authors and history scope their data.
- **Root-folder, Grimmory, and Calibre integration routes are admin-only**
  (#1387) — `POST/DELETE /rootfolder`, the Grimmory config/test endpoints, and
  the Calibre test/import/sync endpoints sat outside `RequireAdmin`, letting
  any authenticated non-admin register or delete server storage roots, rewrite
  an integration URL + credential, or trigger credentialed probes and bulk
  pushes. They now sit behind the same admin boundary the rest of the
  infrastructure routes (indexers, download clients, notifications, backups)
  already enforce.
- **Login timing no longer reveals which usernames exist** (#1386) — a login
  attempt against a non-existent username skipped the argon2id hash entirely
  and returned in microseconds, while a real username paid the full hash cost —
  a timing side-channel measurable over the network, on both the main login
  and the OPDS basic-auth path. A missing user now verifies against a dummy
  hash so every attempt costs the same.
- **500 responses no longer echo internal error detail** (#1385) — roughly 230
  handlers returned `err.Error()` straight to the client on internal failures,
  leaking SQLite table/column/constraint text and absolute filesystem paths.
  Server errors now return a generic `internal server error` body and log the
  full detail server-side (with the request method and path) instead.

### CI
- **Docs-only PRs can merge again** (#1380) — `Security Summary` is a required
  status check, but the security workflow path-ignores docs-only diffs, so a
  PR touching only Markdown/docs never received the check and sat permanently
  blocked. A companion workflow now runs on exactly the inverted path set and
  reports the check green — there is nothing to scan in a docs-only diff.

### Fixed
- **Concurrent imports no longer crash on author writes** (#1374) — the
  accent-folding sort key added in v1.23.1 shared a single
  `x/text/transform.Chain` across the process, but that transformer mutates
  internal buffers and is not safe for concurrent use. Two goroutines writing
  authors at once (an ABS import, parallel author work discovery, simultaneous
  API calls) could corrupt its state and panic with slice-bounds errors inside
  `x/text/transform`, killing the importing goroutine. The transformer is now
  built per call; a 16-goroutine regression test pins it under `-race`.
- **Bulk folder import accepts the library root and the download folder**
  (#1373) — the bulk import shipped in v1.23.0 rejected both of its obvious
  targets with `path is outside the configured library roots`: the containment
  check reused the delete path's rule that a root itself is never a valid
  target, so pasting `/books` failed, and the download dirs
  (`BINDERY_DOWNLOAD_DIR` / `BINDERY_AUDIOBOOK_DOWNLOAD_DIR`) were never in the
  allow-list at all — even though a migration backlog in `/downloads` is the
  exact use case the feature was built for. Scanning a configured root or a
  download dir now works; the delete handlers keep the stricter library-only
  containment.
- **Every book-file delete path now refuses to remove a file another book
  owns** (#1368 hardening) — v1.23.2 added the ownership check only in the
  book-delete legacy-column fallback. The invariant now lives in the shared
  delete chokepoint, so the per-file delete, author delete, and Fix Match
  cleanup are covered too: if a path is still registered to a different book in
  `book_files`, the on-disk delete is skipped (and it fails safe when ownership
  can't be determined).

- **Wanted and Queue pages no longer race their own polling** (#1382) — both
  pages poll every 5 seconds and wrote fetch results straight into state, so a
  slow response landing after unmount (or after a newer poll already resolved)
  could clobber fresher rows or set state on a dead component. Both effects now
  carry the same `cancelled` cleanup flag AuthorsPage already uses.
- **Author search collapses duplicated provider-noise names** (#1359, thanks
  @pcamp96) — OpenLibrary occasionally carries junk author records whose name
  repeats the whole token sequence (`Black, Chuck, Black, Chuck`). These now
  dedup into the clean author record instead of appearing as a second result,
  and the clean label wins when the two records merge.
- **Author refresh prunes same-name technical collisions** (#1360, thanks
  @pcamp96) — when OpenLibrary groups an unrelated same-name person's works
  under an author (a fiction catalogue suddenly containing a computer-networking
  textbook), the refresh now drops the obvious subject outlier instead of
  auto-creating it as a wanted book. Conservatively gated: only fires on
  fiction-heavy catalogues, and any fiction signal on the work exempts it.
- **The ebook naming template configured in Settings is honoured** (#1391,
  closes #1356) — the Settings UI has saved the ebook naming template under
  `naming.bookTemplate` since v0.2.0, but the importer read `naming_template` —
  a key nothing writes. Every ebook import (including Fix Match, where #1356
  caught it moving a correctly-placed file *out* of the configured series
  layout) silently used the built-in default layout, while the client-side
  preview showed the configured template applying. The importer now reads the
  key the UI writes (keeping the old key as a legacy fallback); as before, a
  template change applies from the next restart.

## [v1.23.2] — 2026-07-01

A data-loss patch for **Fix Match** (the file-reassign feature, #1238, shipped in
v1.23.0). Anyone whose library is reached through a symlink should update.

### Fixed
- **Fix Match no longer strands (and later deletes) a file on symlinked
  libraries** (#1368) — reassigning a file resolves the path through
  `EvalSymlinks` for its containment check, but `book_files` stores the path as
  it was recorded at import time. On a library mounted through a symlink (common
  in Docker/NAS setups) the resolved and stored paths differ, so the exact-match
  detach silently did nothing: the *old* record kept a live reference to the file
  the reassign had moved to the target. Deleting that "empty-looking" record then
  removed the file the target now needs. Bindery now detaches against the stored
  path (falling back to the resolved form), so the source record is reliably
  emptied. Closes #1368.
- **Reassign cleanup no longer deletes the source when nothing was imported**
  (#1368) — the post-reassign cleanup deleted the original file once it saw *any*
  file on the target book, treating a pre-existing unrelated file (e.g. an ebook
  the target already had) as proof the move succeeded. If the import placed
  nothing, the source was deleted anyway. It now snapshots the target's files
  before importing and only removes the source when a *newly added* file is
  present on disk.
- **Deleting a book can't remove a file another book owns** (#1368) — deleting a
  book with "also delete files" falls back to the legacy single-path columns when
  a book has no `book_files` rows, and did so without checking whether the path
  was still in use. A stale legacy column could therefore delete a file a
  different book actively tracks. The delete now skips any legacy path still
  present in `book_files` (the `path` column is globally unique, so a match means
  another book owns it).

## [v1.23.1] — 2026-06-29

A patch release: responsive mobile Settings, correct alphabetical ordering for
accented author names, and legible Calibre push failures.

### Fixed
- **Settings is usable on a phone** (#1344) — the Settings page rendered its
  fixed `w-44` sidebar beside the content at every viewport, crushing the content
  column to ~180px on a narrow screen, so indexer rows overlapped and the theme
  toggle and Regenerate button overflowed their cards. The sidebar now stacks
  above the content below the `md` breakpoint, giving forms the full width. A
  headless mobile audit caught and fixed horizontal overflow on eight settings
  tabs, not just the two originally reported; desktop and tablet layout is
  unchanged.
- **Authors list sorts accented names in their place** (#1347) — #1312 made the
  A–Z / Z–A sort case-insensitive with SQLite `COLLATE NOCASE`, but that folds
  ASCII only, so any author whose `sort_name` began with a diacritic (Ö, Á, Ł,
  Ø, Æ…) still sorted after "Z". Bindery now stores an accent-folded `sort_key`
  (migration 058, computed on every author write and backfilled once at startup)
  and orders by it, so Scandinavian / Polish / Spanish / Turkish names sort by
  their base letter. Follow-up to #1312.
- **"Push all to Calibre" reports why each book failed** (#1346) — a bulk push
  that failed on every book logged only `calibre sync complete failed=N` with no
  per-book reason, even in DEBUG, so a library path the Calibre container can't
  resolve (e.g. `/books` vs `/media/books`) was invisible. The sync now logs the
  first book to hit each distinct failure reason at WARN (deduped, so one
  library-wide mismatch logs once instead of once per book) and adds
  `distinctFailureReasons` to the completion summary. The per-book error list
  returned to the UI is capped at 50 so a fail-on-every-book run no longer bloats
  the status payload; `failed` still counts every book.

### Dependencies
- Routine `minor`/`patch` dependency bumps across the Go and npm modules and the
  Docker base images (Dependabot).

## [v1.23.0] — 2026-06-27

A feature release: correct a mis-matched file in place, links out to the
upstream metadata source, bulk monitor-mode changes, alias-aware author search,
and native ntfy notifications. One additive schema change
(`notifications.topic`, migration 057) defaults to the previous behaviour. If
you maintain a custom webhook/ntfy template, read the upgrade note in the ntfy
entry below — the event payload shape changed.

### Added
- **Fix Match: reassign a mis-matched file to the correct book** (#1238) — the importer can attach a downloaded file to the wrong book, and correcting it previously meant deleting and re-importing. The book detail page now has a **Fix match** action (next to Exclude) that searches your library, moves the file into the chosen book's folder, detaches it from the wrong book, and removes the stale copy at the old location so a later library scan can't re-attach it. Backed by `POST /api/v1/queue/manual-import/reassign`, which reuses the manual-import move/attach path and only deletes the original after confirming the file landed at a new tracked path, so a failed move never loses data.
- **Links to the upstream metadata source on detail pages** (#1296) — author and book detail pages now show an *arr-style **View on OpenLibrary / Google Books ↗** link (the equivalent of the *arr stacks' TMDB/IMDB links) when the stored foreign ID maps to a stable public page: bare OpenLibrary `OL…` keys and Google Books `gb:` IDs. Sources without a reliably constructible public URL (Hardcover, DNB, Calibre, ABS) show no link. Frontend-only — the foreign IDs were already in the API.
- **Bulk author actions can set monitor mode** (#1228, closes #1140) — the author list's bulk action bar can now change monitor mode for the selected authors and optionally apply the mode to their existing books, instead of editing each author one at a time.

### Fixed
- **Author search matches aliases and pen names** (#1176) — searching a name stored as an author alias (e.g. a pen name) returned nothing. The query now also matches `author_aliases`, so an AKA surfaces the canonical author that owns it.
- **ntfy notifications render natively instead of as a raw JSON blob** (#1323) — ntfy only parses a JSON body when it is POSTed to the server *root* with a `topic` field; POSTed to a topic URL it treats the body as plain text and prints the JSON verbatim, which is what users saw. Notifications now have an optional **topic** field (migration 057, additive, defaults to empty): set it and point the URL at the ntfy server root, and Bindery publishes a body ntfy formats. Independently, every event payload is now normalized so `title` is *what happened* (`Release Grabbed`, `Book Imported`, …) and `message` is the subject, instead of both repeating the item name, and `eventType` is included on every event (not just `test`) so a single template can branch per event. The payload schema is documented in `docs/API.md`. **Upgrade note:** if you built a custom webhook/ntfy template that read `title` as the item name, that value now lives in `message` (and the raw item name is preserved under `item`).

### Changed
- **Refreshed Bindery branding** (#1324, #1326) — a new logo in the app navigation and a matching favicon.

### Docs
- **README refresh: Readarr-migration hook, comparison table, and updated screenshots** (#1321, #1324) — the README now leads with the Readarr-migration story and a feature-comparison table, and the screenshots were regenerated against the current UI with the new branding (plus calendar and queue views and a short demo loop).

## [v1.22.3] — 2026-06-26

A patch release of import, download, and multi-user fixes, plus a per-list
media type for Hardcover import lists. The one schema change
(`import_lists.media_type`) is additive and defaults to the previous behaviour.

### Added
- **Per-list media type for Hardcover import lists** (#1296, #1314) — a synced book took its format from Hardcover's edition availability, so separate "Audiobooks" and "Ebooks" lists produced identical media types (most works report both editions). Each import list now has an Auto / Ebook / Audiobook / Both selector that pins the format its books are created as. Applied on create only: the syncer skips books that already exist, so a book on two single-format lists is never auto-promoted to Both and a manually-set media type survives re-sync.

### Fixed
- **Authors list sorts A–Z / Z–A case-insensitively** (#1312) — `sort_name` is stored case-preserving, so the default BINARY collation sorted all uppercase ahead of all lowercase and pushed lowercase-article names ("de Balzac") past "Z", which read as a jumble. The sort now uses `COLLATE NOCASE`, backed by a matching index (migration 055). Sorting by "recent" was unaffected.
- **Admins see all libraries in list and browse views** (#1310) — with multi-user / SSO enabled, an admin only saw rows they owned, so authors and books created by another account (or via API key) were invisible even though the global duplicate check still blocked re-creating them. List, browse, and OPDS now show everything to admins (and when tenancy is disabled), matching the existing per-item ownership checks.
- **Downloads of books stored under configured root folders no longer return "access denied"** (#1308) — the download path allow-list was only the two static library/audiobook directories captured at startup, so a book written under a user-configured root folder 403'd. Root folders are now resolved at request time and included in the allow-list, failing closed on error.
- **Cross-device imports fall back to copy instead of failing** (#1313) — when downloads and library are on separate mounts (e.g. distinct Docker bind mounts, or Unraid `/mnt/user` shares, which share a device id but reject cross-mount hardlinks), the import failed with "invalid cross-device link" and the storage panel falsely claimed they shared a filesystem. The hardlink-capability check now performs a real link probe (so the message and the auto import-mode are honest), and the hardlink path falls back to a seeding-safe copy on EXDEV.

### Changed
- **CI and tests run on Node 26** (#1201, #1230, #1315; thanks @magrhino) — the frontend test harness was updated for Node 26 (storage guards, MSW origin normalization, `act`-wrapped fake timers) and the CI/security workflows bumped 22 → 26. As part of this, `usePagination` now tolerates unavailable `localStorage` so a list never breaks on a storage error.

### Internal
- Path-safety test compares resolved file paths so macOS `/var` temp directories don't fail local runs (#1229; thanks @magrhino).

## [v1.22.2] — 2026-06-25

Adds bulk folder import for migrating an existing library, plus a batch of
import, sync, and download-client fixes. No config or schema changes that
affect existing installs (the one new column defaults to the previous
behaviour).

### Added
- **Bulk folder import: scan a directory and import everything matched in one pass** (#1292, #1293) — manual import was one path at a time, which made migrating a Readarr/Calibre backlog of hundreds of folders impractical. The Import settings tab now has a Folder Scan section that enumerates a directory's book units, pre-checks the confident matches, surfaces a picker for the ambiguous ones, and imports the selected set as a bounded background batch. Backed by new `GET /queue/manual-import/scan` and `POST /queue/manual-import/batch` endpoints.
- **Settings → About** (#1235, #1297) — the in-app update check and the bug-report template both pointed at a "Settings → About" screen that did not exist. There is now a real About tab showing the running version, commit, and build date (reusing `GET /system/status`), so users can read their version without digging through Docker logs.
- **The telemetry server advertises the current release automatically** (#1289) — the public version indicator (e.g. the Discord channel name) is now driven by a background poll of the GitHub Releases API every 30 minutes instead of a build-time constant, so it stops lagging behind the latest tag after a release.

### Fixed
- **qBittorrent grabs land in the category's save path, not the download root** (#1301) — Bindery sent an explicit `savepath` alongside the category with auto-TMM off, so qBittorrent honoured the explicit path and ignored the category's configured save directory. When a category is set, Bindery now enables automatic torrent management and omits the explicit `savepath`, so the category path wins; the same `setAutoManagement` is applied on the recategorise-on-recovery paths. Grabs with no category keep the explicit path as before.
- **Hardcover list sync no longer auto-wants an author's entire back-catalogue** (#1290, #1300) — a list-created author was left with an empty monitor mode, which the scheduler treats identically to "all", so syncing a 235-book list ballooned the wanted count ~6× by pulling every author's full catalogue. New list-sync authors are now pinned to monitor mode `none`; only the specific books on the list stay monitored and wanted.
- **"Refresh Metadata" refreshes the author's own profile, not just their books** (#1299) — for an already-linked author the refresh repopulated the catalogue but skipped the author's description and photo, so a linked OpenLibrary author stayed blank even after the data appeared upstream. Refresh now re-fetches and saves the author's bio, image, and disambiguation for non-Calibre authors (Discussion #1226).
- **Library scan parses Readarr-style series folders** (#1234, #1298) — folders laid out as `{Series} #{N} - {Title}` were parsed with the whole folder string as the title, so most of a series failed to match and the rest collided onto one title. The scanner now strips the `{Series} #{N} - ` prefix, recovers the real title, and surfaces the series and position.
- **Em-dash and other Unicode dashes in folder names parse correctly** (#1291) — filename and folder parsing normalises Unicode dash variants (em-dash, en-dash, figure dash, minus) to ASCII `-`, so `Author — Title` style folders split on the separator instead of being treated as one token.

### Changed
- **Bump `undici` to 7.28.0 and `@babel/core` to 7.29.7** (#1286) — frontend dependency updates to pick up upstream fixes.

### Docs
- **Documented the author "Find better metadata" button** (#1294) — the Troubleshooting wiki now explains when the Link / Find-better-metadata button appears (unlinked or sparse author records) and why well-populated authors do not show it; the duplicate predicates behind it were de-duplicated into a shared util.

## [v1.22.1] — 2026-06-22

Patch release: security hardening, bug fixes, documentation accuracy, and a
large test-coverage backfill. No new features, no config or schema changes.

### Security
- **Outbound NZB/torrent fetches are now SSRF-guarded at dial time** (#1262) — the indexer-supplied `.torrent` / `.nzb` download URL is data chosen by the indexer's response, not an admin-typed value, so the download clients (`deluge`, `nzbget`, `qbittorrent`, `sabnzbd`, `transmission`) now dial through `httpsec`'s guarded transport. A malicious or compromised indexer can no longer point a download link at loopback, link-local, or cloud-metadata. Loopback remains opt-in via `BINDERY_DOWNLOAD_ALLOW_LOOPBACK`.
- **Webhook notifications re-validate on redirect and guard the dial against DNS rebinding** (#1261) — `notifier` now revalidates the target after each redirect hop and pins the dialed IP, closing a TOCTOU window where a webhook URL that passed the initial SSRF check could be redirected (or re-resolved) to a private address.
- **The SSRF guard now blocks the unspecified address** `0.0.0.0` / `::` (#1259) — previously these slipped past the loopback/private checks and could reach services bound to all interfaces on the host.
- **`/migrate/*` import routes now require admin** (#1264) — the CSV / Readarr / Goodreads bulk-import endpoints were registered at the authenticated-route level, so any logged-in user could run them. They are now behind `RequireAdmin`, matching ABS import and Calibre rollback.
- **Bulk book delete enforces per-row ownership** (#1242, #1243) — the bulk-delete handler now runs the same `auth.CheckOwnership` gate as single delete, so a non-owner can no longer delete another user's books by ID when tenancy is enforced.
- **OIDC `allowed_groups` is enforced against the configured group claim** (#1244, #1245) — the login filter previously read a claim literally named `groups`, so deployments using `BINDERY_OIDC_GROUP_CLAIM=roles` (or similar) silently rejected every user. It now honors the configured claim, consistent with admin-group mapping.
- **Indexer infoUrl links are scheme-allowlisted in the UI** (#1267) — provider-supplied `infoUrl` values are now rendered only when they are `http(s)`, blocking `javascript:` / `data:` injection on Book Detail and Wanted pages.
- **Imported file discovery skips symlinks** (#1263) — the scanner no longer follows symlinked files when collecting books to import, preventing a crafted download payload from redirecting an import outside the intended tree.
- **`sanitizePath` strips control characters and caps component length** (#1266) — path components built from metadata are hardened against control-byte injection and pathologically long names.
- **Indexer and SABnzbd API keys are redacted from transport errors** (#1260, #1265) — newznab transport errors and SABnzbd `%w`-wrapped `url.Error`s no longer echo the API key into logs. The SABnzbd fix scrubs the URL in place so the typed-error chain (needed by `nethint`) is preserved.

### Fixed
- **Audiobook scans no longer misparse narrator / per-chapter tags as book titles** (#1239, #1240) — when the folder hierarchy already yields a real title, a per-track chapter tag (`"04 - Sinister Grey Mists…"`) no longer clobbers it, so multi-part audiobooks reconcile to one book instead of fragmenting.
- **Hardcover list-sync reconciles against the library instead of duplicating** (#1223, #1241) — authors *and* books already present are matched and updated rather than re-created on each sync.
- **Manual/interactive search propagates `IndexerPriority`** (#1246, #1247) — priority now affects ranking in interactive search, not just automatic grabs.
- **History filters AND `BookID` and `EventType`** (#1248, #1249) — supplying both filters now intersects them instead of letting the later one replace the earlier.
- **Plain `.azw` releases are ranked, not scored 0** (#1250, #1251) — `.azw` is treated as a valid ebook format in quality ranking.
- **Calendar buckets releases by UTC date** (#1252, #1253) — release days no longer shift across a day boundary for users in non-UTC time zones.
- **Auto import mode is decided against the real destination root** (#1254, #1255) — the hardlink-vs-copy choice for `AUTO` now evaluates the actual per-author / audiobook destination filesystem rather than the global library dir.
- **Hardcover numeric slugs resolve by slug** (#1256, #1257) — an all-numeric author/book slug is looked up as a slug instead of being misinterpreted as a database id.
- **OpenLibrary preserves the error chain** (#1279) — transport errors are wrapped with `%w` instead of flattened, so `errors.Is(context.Canceled)` and friends work again.

### Docs
- **Hardcover requires an API token for search** (#1258) — README and the Troubleshooting wiki now state that the live Hardcover GraphQL endpoint rejects unauthenticated queries (`Unable to verify token`), including search — not just user-specific queries.
- **Freshened stale docs for this release** (#1280) — corrected the QUICKSTART "single-administrator" line (multi-user shipped), bumped `Go 1.25 → 1.26` in ARCHITECTURE/AGENTS to match the shipped build, documented `BINDERY_OIDC_GROUP_CLAIM` as feeding both admin mapping and `allowed_groups`, and added the missing `BINDERY_ALLOW_LAN_OIDC` env-var row to DEPLOYMENT.

### Tests
Coverage backfill with no production behavior change: indexer ranker scoring terms (#1268); importer path-traversal/containment (#1269), `MoveFileCtx` cross-fs copy/verify (#1270), and library-scan series reconcile (#1276); scheduler seed-ratio precedence (#1271); cross-tenant DB isolation (#1272); metadata/download client error paths (#1273); destructive API handlers (#1275); prowlarr + grimmory handlers (#1277); BooksPage states (#1274); and a grab→import→history integration test (#1278).

## [v1.22.0] — 2026-06-20

### Added
- **Add Book searches by Audible ASIN** (#1189) — entering a 10-character ASIN such as `B0DBJBFHGT` in **Authors → Add Book** now resolves the Audible edition directly (via the existing ASIN resolver) and returns one addable audiobook result with the ASIN preserved, instead of falling through to an unreliable title search. ISBN and title searches are unchanged.

### Changed
- **CI now gates on frontend unit tests** — the `vitest` suite (`npm test`) is wired into the `validate (frontend)` PR check and the `test` release/merge gate, so a failing frontend test now blocks merges and releases. Previously `vitest` only ran locally via `make check`; no workflow invoked it, leaving 352 frontend tests ungated.
- **CI Go toolchain aligned with the shipped Docker image** — all GitHub Actions workflows now pin Go `1.26.4` (was `1.25.11`), matching the `golang:1.26.4-alpine` build stage in the Dockerfile, so CI tests on the same Go toolchain production ships. (The matching Node bump to `26` is deferred: Node 26 breaks the MSW-based frontend test harness; tracked separately.)

### Fixed
- **"Group by series" on the author page dumped every book into "Standalone"** (#1209) — the per-author series endpoint returned each series without its book membership (the `books` array was empty, and `omitempty` dropped it from the JSON entirely), so the frontend had nothing to group on and every book fell through to Standalone. `ListByAuthor` now joins through `series_books`/`books` and populates each series' book list (author-scoped, ordered by position-in-series), so books group under their series again.
- **Un-added series-page books that already exist in your library are now clickable** (#1210) — "missing" rows from the Hardcover diff that correspond to a book already in your library now render as links to the existing book instead of an "add" button. The match is ownership-scoped, so only books in your own library are linked. Previously the backend never populated the local book id on these rows, leaving the link inert.
- **Prolific authors capped at 100 books** (#1205) — adding or refreshing an author from OpenLibrary fetched only the first page of the `/authors/{id}/works` endpoint, silently truncating any catalogue larger than 100 works. The works endpoint is now paged through to completion (bounded at 2000 works to keep pathological responses in check), so authors with hundreds of titles import their full catalogue. This is unrelated to the frontend list pagination fixed in #1011.
- **Spurious logout on a database blip** (#1200) — a transient database error while checking a session's epoch no longer silently invalidates a valid session cookie. The auth middleware now distinguishes a real "session revoked" epoch mismatch from a failed lookup and returns a server error (500) on the latter, instead of dropping the request to unauthenticated and logging the user out.

## [v1.21.0] — 2026-06-17

### Added

- **Configurable wanted-search interval** ([#1097](https://github.com/vavallee/bindery/issues/1097)) — the automatic wanted-books search interval is now a setting (Settings → General) instead of a fixed schedule. Lengthen it to ease load on your indexers / stay under daily API caps for large libraries, or shorten it to find releases sooner. Defaults to the previous behaviour; takes effect after the next Bindery restart.
- **Storage path health in Settings** ([#1183](https://github.com/vavallee/bindery/issues/1183)) — Settings → General → Storage now shows each configured directory (download, library, audiobook, audiobook-download) as OK / Missing / Not writable with the failing reason, and warns when downloads and the library are on different filesystems ("imports will copy, not hardlink"). Surfaces the existence/writability checks Bindery already ran at startup but previously only logged, via a new admin-only `GET /api/v1/system/storage` health response.
- **Choose format when adding from a series** ([#1124](https://github.com/vavallee/bindery/issues/1124)) — the series page "add" and "add all" actions now have an ebook / audiobook / both selector, so missing books are created (and searched) as the chosen format instead of always defaulting to ebook.
- **Group an author's books by series** ([#1125](https://github.com/vavallee/bindery/issues/1125)) — the author page gains an opt-in "Group by series" toggle that organizes books under the series they belong to (ordered by position), with series-less books collected in a "Standalone" group. The flat grid/table list stays the default.
- **Select all / deselect all on the author page** ([#1172](https://github.com/vavallee/bindery/issues/1172)) — the author page filter bar gains a single toggle that selects or deselects every currently displayed book for bulk actions. It respects the active filters, so it composes with the Wanted filter and only ever sweeps up visible books.
- **Clickable un-added series rows** ([#1123](https://github.com/vavallee/bindery/issues/1123)) — on the series page, an un-added Hardcover book that already exists in your library now links straight to its book page, matching the added rows. Rows with no library match keep the "add" button.
- **Link to the indexer's release page from search results** ([#1122](https://github.com/vavallee/bindery/issues/1122)) — each interactive search result (book page and Wanted page) now shows a small "↗ indexer" link, opening the indexer's human-readable detail/info page in a new tab so you can inspect a release before grabbing. The link appears only when the indexer supplies a usable URL.

### Changed

- **Download-client test now checks path visibility** ([#1182](https://github.com/vavallee/bindery/issues/1182)) — the Test button no longer just verifies the API connection. After connecting it resolves the client's completed-downloads path (qBittorrent category save path, NZBGet destination directory), applies your path remap (the per-client remap first, then the global `BINDERY_DOWNLOAD_PATH_REMAP` as a fallback, matching how the importer resolves paths), and confirms Bindery can actually read it. If it can't, Test now warns loudly with the resolved path and a fix instead of showing a misleading green check, catching the most common silent "test OK but nothing imports" misconfiguration at config time. Client types whose completed path can't be introspected stay connection-only.
- **Manual Import is easier to find** ([#1184](https://github.com/vavallee/bindery/issues/1184)) — the empty Queue and Wanted pages now point you straight to Manual Import (for files already on disk) and Scan Library (for files already in your library folder), so "I have ebooks downloaded but nothing imports" no longer leaves you stuck.
- **Document the single-mount (hardlink) storage layout** ([#1170](https://github.com/vavallee/bindery/issues/1170)) — `docs/DEPLOYMENT.md` now explains the TRaSH-style single-mount layout required for hardlink imports/seeding, and the Helm `values.yaml` flags that the stock `BINDERY_*_DIR` defaults are placeholders that must point inside a real mounted volume.

### Fixed

- **Book page updates live while a download imports** ([#1161](https://github.com/vavallee/bindery/issues/1161)) — the book detail page now polls while a book is downloading/importing, so the file and "Imported" status appear on their own instead of requiring a manual page reload.
- **Wanted page updates live** ([#1161](https://github.com/vavallee/bindery/issues/1161)) — the Wanted list now refreshes on its own, so books grabbed in the background (auto-search) drop off without a manual reload. Polling pauses while you have a results panel open or a grab in progress so the list doesn't shift under you.
- **"Wanted" filter no longer shows unmonitored books** ([#1173](https://github.com/vavallee/bindery/issues/1173)) — on the author page, the "Status: Wanted" filter now lists only genuinely wanted books (monitored and not yet imported). An unmonitored book carrying a stale `wanted` status is excluded, matching the status badge and the backend's wanted filter.
- **Relevance filter keeps correct releases whose title has punctuation** ([#1179](https://github.com/vavallee/bindery/issues/1179)) — title-vs-release-name matching now treats all punctuation (e.g. a trailing `!`, `?`, `%`, `#`) as a word boundary, so a correct release whose name drops the punctuation is no longer rejected with "title/author keywords did not match release name".
- **Add Book modal no longer crashes when a search returns no results** ([#1188](https://github.com/vavallee/bindery/issues/1188)) — an empty metadata book search now serializes as `[]` instead of `null` from `/api/v1/search/book`, and the modal defensively coerces a null body to an empty list, so an empty search shows "No results found." instead of throwing `Cannot read properties of null (reading 'map')`.
- **Cover images load again when `BINDERY_OUTBOUND_PROXY` is set** ([#1177](https://github.com/vavallee/bindery/issues/1177)) — the cover-image proxy no longer applies its strict per-dial SSRF re-check to the outbound proxy itself, which had rejected a LAN/loopback proxy address and returned `502` for every cover (silently breaking the on-disk image cache). The DNS-rebind guard still applies on the direct, no-proxy path.

### Security

- **Metadata provider errors no longer leak the Google Books API key or internal infrastructure** ([#1144](https://github.com/vavallee/bindery/issues/1144)) — a transport failure while talking to a metadata provider used to echo the raw error back to the client, and that error embedded the full upstream request URL (including the Google Books `?key=` API key) plus the internal DNS resolver IP. The search/series endpoints now return a generic `metadata provider unavailable` message, logging the full detail server-side instead, and the provider clients redact secret query-string values from any error they construct.

## [v1.20.0] — 2026-06-16

### Added

- **Korean (한국어) translation** ([#1138](https://github.com/vavallee/bindery/pull/1138)) — full Korean locale for the web UI, selectable from the language switcher.
- **Inline save feedback across Settings** ([#1107](https://github.com/vavallee/bindery/pull/1107)) — settings controls now show saving → saved status inline, plus an experimental Grimmory preview badge and a direct link to the Hardcover token field from the import flow.

### Changed

- **Settings General tab decluttered** ([#1109](https://github.com/vavallee/bindery/pull/1109)) — the overloaded General tab's sections (API keys, metadata, logs) moved into their own domain tabs, and the Root Folders tab gained a default-root-folder selector.

## [v1.19.1] — 2026-06-15

### Fixed

- **OIDC provider config via API key** ([#1139](https://github.com/vavallee/bindery/pull/1139)) — `PUT /api/v1/auth/oidc/providers` returned `403 {"error":"admin role required"}` even with a valid `X-Api-Key` header, because the auth allowlist matched on path only and skipped the key check entirely. `GET` is intentionally public (login page needs the provider list); `PUT` now goes through normal auth so API-key-authenticated requests are correctly granted admin access.
- **Dual-format books can grab the missing format** ([#1150](https://github.com/vavallee/bindery/pull/1150), [#1148](https://github.com/vavallee/bindery/issues/1148)) — for a book monitored as both ebook and audiobook, having one format on disk no longer blocks the other. Interactive search results for the missing format are no longer rejected with "book already imported", changing a book to "both" re-evaluates it back to wanted, and a library scan now attaches an existing file for the missing format even when the other format is already tracked.

## [v1.19.0] — 2026-06-13

### Added

- **`{Genre}` file-naming token** — organise your library into top-level genre folders (e.g. `{Genre}/{Author}/{Title}`). Genres are sourced from Hardcover's curated taxonomy when Hardcover is enabled; without it the token falls back to OpenLibrary's noisier subject data. New books pick up genres immediately; **refresh an author** to backfill genres onto books imported before this release.
- **`{Token:default}` fallback syntax in naming templates** — any token can specify a fallback used when it renders empty, mirroring Calibre's `ifempty(...)`. For example `{Genre:Unsorted}/{Author}/{Title}` routes un-tagged books to an `Unsorted/` folder instead of dropping the folder level.

### Changed

- **Genres now prefer Hardcover's taxonomy over OpenLibrary subjects** — when a Hardcover match is available, its curated genres (`Fantasy`, `Science Fiction`) replace OpenLibrary's raw subject bag (`Fiction`, `American literature`, `Large type books`) for display and for the new `{Genre}` token. Non-Hardcover enrichers (e.g. Google Books BISAC categories) no longer overwrite genres.
- **OpenLibrary search results now carry ISBNs** ([#1121](https://github.com/vavallee/bindery/pull/1121)) — the ISBN data OpenLibrary already returns is no longer discarded, so cross-provider de-duplication can collapse OpenLibrary and Hardcover hits for the same edition.

### Fixed

- **DNB titles no longer carry promotional/genre bloat** ([#1115](https://github.com/vavallee/bindery/pull/1115), [#1114](https://github.com/vavallee/bindery/issues/1114)) — German DNB records often append marketing text and genre markers (e.g. `: Roman | SPIEGEL-Bestsellerautorin … / Author`). These are now stripped, so titles are clean and indexer searches match instead of failing with "title/author keywords did not match release name". (Books added before this release keep their stored title until re-added.)
- **Deluge grabs no longer fail in VPN/namespaced setups** ([#1116](https://github.com/vavallee/bindery/pull/1116), [#1110](https://github.com/vavallee/bindery/issues/1110)) — Bindery now fetches the `.torrent` itself and submits the bytes to the daemon, instead of having the Deluge daemon fetch the indexer URL (which fails when the daemon can't reach the indexer). The infohash is returned directly, so the add no longer relies on hash polling.
- **Naming templates drop a dangling separator when a leading token is empty** ([#1128](https://github.com/vavallee/bindery/pull/1128)) — a template like `{SeriesNumber} - {Title}` now yields `Title` rather than ` - Title` for a book with no series number. Interior/trailing separators are unchanged.
- **CSV author import no longer mis-parses long first lines** ([#1120](https://github.com/vavallee/bindery/pull/1120)) — the parser read past its buffer when a CSV's first line exceeded 4 KB, producing phantom rows; found by the nightly fuzzer.

### Notes

- `{Genre}` uses the first genre Hardcover lists. If Hardcover later re-categorises a book, new grabs follow the new genre, but already-imported files are not relocated.
- Per-book genre editing (your own vocabulary, like a Calibre custom column) is not yet supported; this release sources clean genres automatically but does not let you override them per book.

## [v1.18.0] — 2026-06-11

### Added

- **`BINDERY_DOWNLOAD_ALLOW_LOOPBACK`** ([#1062](https://github.com/vavallee/bindery/discussions/1062)) — opt-in env var to let Bindery fetch indexer-provided `.torrent` / `.nzb` links that resolve to loopback (`127.0.0.1`, `::1`). Fixes `url not allowed: points to loopback address` when Prowlarr / an indexer is co-located on loopback (e.g. `network_mode: host`). Off by default since the download URL is indexer-chosen data; link-local and cloud-metadata stay blocked regardless.
- **Book and author search now queries every configured metadata provider** (#1064) — searches fan out in parallel to OpenLibrary, Hardcover, Google Books, and DNB (each with its own timeout), then results are de-duplicated and ranked by relevance with primary-provider hits first. Books and authors that OpenLibrary lacks — recent releases, niche or foreign-language titles — now show up in search instead of coming back empty.
- **Indexers synced from Prowlarr now auto-populate their per-indexer seed-ratio override** (#1065) from Prowlarr's `seedCriteria.seedRatio`, unless you've set the ratio yourself (your value always wins).
- **Per-indexer seed-ratio override** (#883) — set a seed ratio (or "unlimited") on each indexer and Bindery applies it to torrents grabbed from it on qBittorrent, Transmission, and Deluge; leave it blank to keep the download client's global rule.
- **Optional multi-disc audiobook flattening** (#886) — when enabled (Settings → General, copy/hardlink mode only), a completed audiobook download split into `Disc 1`/`CD 2`/… folders is imported into one flat folder as `Part 001.ext`, `Part 002.ext`, … so audiobook players sort it correctly; the source is never moved, so torrents keep seeding. Off by default and single-disc imports are unchanged.
- **File-naming templates now have a live preview, a token picker, and inline validation** (#943) — Settings → General → File Naming shows the rendered sample path as you type, lets you insert any of the 8 supported tokens with one click, and flags unknown/invalid tokens before you save.
- **Log-rate Discord alerting (Helm)** (#1085) — new opt-in `logAlert` CronJob in the chart polls `/api/v1/system/logs` and pings a Discord webhook when WARN/ERROR counts in the lookback window cross configurable thresholds, so persistent log noise pages someone instead of sitting unseen.
- **Telemetry: coarse error-class counters** — the once-daily anonymous ping now includes an `errors` section: the number of ERROR and WARN log entries over the last 24 hours, plus up to 5 `{msg, count}` entries for the most frequent errors. `msg` is the fixed, developer-written log message string only (truncated to 120 chars); log details (attrs), which can carry titles, paths, or URLs, are never sent. This surfaces topology-specific breakage in aggregate without waiting on bug reports. Existing opt-outs (`telemetry.enabled: false` or `BINDERY_TELEMETRY_DISABLED=true`) apply unchanged.

### Changed

- **Settings tabs now load on demand** (#773) — each Settings tab is code-split and fetched the first time it's opened, shrinking the initial app bundle.
- **Audiobook metadata is now derived from Hardcover editions before Audnex enrichment** (#806) — when a confident Hardcover match is hydrated, the chosen audio edition's language and cover (and audiobook media type) are filled from Hardcover first, leaving Audnex to supply only what Hardcover lacks (narrator, refined duration, summary). Known fields are never overwritten.
- **Metadata enrichment now prefers stronger Hardcover rating signals** (#807) — when Hardcover carries a materially better-supported rating (at least 2× the current `ratings_count`, above a small floor), Bindery adopts its `average_rating` and `ratings_count` instead of keeping a sparse first-seen value; weaker or empty incoming ratings still never replace a known one.
- **UI accessibility and visual polish across the app** (#1037–#1042) — a coordinated pass on contrast and affordance: WCAG AA-compliant text-color tokens and badge colors in both themes; a redesigned queue with status chips, quieter error rows, and bulk actions; a shared button vocabulary with AA-safe destructive buttons; clickable toggles that no longer look like static badges (plus a status legend); blank book covers replaced by an accessible placeholder with a readable title; and author bios constrained to a readable line length.
- **Author refresh no longer clutters the works list with omnibuses/box sets** — when a Hardcover API token is configured, works Hardcover classifies as compilations (omnibuses, box sets, "complete" bundles) are pruned from an author's book list, so the same content stops appearing in several places. OpenLibrary itself carries no such signal, so Hardcover's classification is what drives the cleanup; genuine books, and installs without a Hardcover token, are unaffected.
- **Docker image now carries OCI metadata** — the published image declares its license (`org.opencontainers.image.licenses=MIT`), source, title, and description, so registries and `docker inspect` surface them.

### Fixed

- **Grabbing from indexers that fingerprint the User-Agent now works** (#1053) — all indexer-fetch paths (SABnzbd, NZBGet, Transmission, qBittorrent) now send `bindery/<version>` instead of Go's default User-Agent.
- **Releases whose title words match out of order are no longer grabbed** (#1063) — the weakest release-matching fallback now requires either the author's name in the release or the title words appearing in order.
- **Search results without an author ID can now be added** (#1069) — results carrying only an author name (typical for Google Books hits) were findable but rejected on add. Bindery now resolves the author by name before adding.
- **Foreign-language OpenLibrary works are now caught by the language filter** (#891) — when the active profile restricts language, Bindery now edition-samples each work without a work-level language (bounded to 5 editions) to derive its language before filtering.
- **Release-title ISBNs with exotic separators now normalise correctly** — the ISBN extractor accepted any whitespace separator but only stripped hyphens and spaces, so a title like `978<TAB>0449912553` produced an ISBN with an embedded control character that silently failed downstream comparisons. Found by the `FuzzParseRelease` target.
- **All enabled download clients are now polled each import cycle** (#1090) — previously only the highest-priority client was polled; a SABnzbd+qBittorrent user had secondary-client downloads permanently stuck at "downloading".
- **Transmission Category filter no longer silently drops all torrents on a path mismatch** (#1091) — path comparison is now normalised (trailing slashes stripped), Transmission 3.0+ labels are accepted as an alternative match, and a WARN is emitted when a non-empty Category matches zero torrents on an instance that does hold torrents.
- **Migration guard no longer crashloops when the legacy-index version 10 row is present** — a no-op `010_noop.sql` fills the sequence gap, so the row legacy runners wrote as version 10 is a valid filename-based version and the guard accepts it.

### Security

- **Hardened parsers against malformed input with Go fuzz tests** (#885) — added bounded native fuzz targets for the Calibre rollback-metadata decoder and the NZB/torrent indexer fetch-URL SSRF guards, asserting they never panic and never let a loopback, link-local, cloud-metadata, or non-http(s) target through.

## [v1.17.1] — 2026-06-09

A patch release: fixes from user reports plus a security toolchain bump. No breaking changes.

### Fixed

- **Setting the Calibre Bridge plugin API key now works** (#1036) — saving the plugin API key under Settings → Calibre failed with `403 use /auth/* endpoints for auth settings`, and no endpoint would accept it, so the key was impossible to set. It is a write-only secret (like the Hardcover token): the settings endpoint now accepts it while still hiding it from reads. The misleading "/auth/*" wording on the read-only-secret guard was also corrected, since it applies to non-auth secrets (ABS, Grimmory, Calibre) too.
- **Hardcover author metadata refresh no longer fails on an invalid language field** (#1048) — the author-works query requested `language` on Hardcover's `books` type, which has no such field, so Hardcover rejected the entire query (`field 'language' not found in type: 'books'`, validation-failed) and the author-works supplement failed for every author (seen as repeated "author works supplement failed" warnings). The invalid field is removed; the supplement works again. Per-book language for author works now needs to come from editions (a small follow-up), so the foreign-language filter treats supplemental books as "unknown: pass" in the meantime.
- **Ebook imports no longer block when the release filename mis-parses** (#1014) — a download grabbed from the free-text Search page carries no book association, so the importer fell back to parsing the release filename. For the common `Author - [Series NN] - Title (tags)` naming it split the name wrong (e.g. title="Peter F Hamilton", author="- Pandora's Star") and, worse, never actually tried to match a catalogue book, so the import retried three times and blocked. The importer now recovers the association by reading the EPUB's embedded metadata (`dc:title` / `dc:creator`) and matching the catalogue book — preferring embedded metadata over the unreliable filename — and only imports on a single confident match. The release-name parser also now understands the `Author - [Series NN] - Title` ordering, and the unmatched-import failure message reports both the parsed-filename and embedded-EPUB title/author so you can see why a match failed.
- **Deluge downloads now import** (#1019) — a torrent grabbed into Deluge reached seeding but Bindery left it stuck at "downloading" forever and never imported, with no failure log. Deluge had no completion poller: it fell through the download-client dispatch into the SABnzbd path, which errored against the Deluge host and was logged only at debug level. Bindery now polls Deluge for completion (recognising the seeding/finished state), requests the torrent's save path so the importer can find the files, and imports them. As a safety net, an unknown/unsupported download-client type now logs a clear warning instead of silently masquerading as SABnzbd.
- **Failed actions now show a message instead of a raw key** (#1026) — a missing `common.actionFailed` translation made some failure toasts render the literal i18n key; the key is now defined.

### Security

- **Bump the release image's Go toolchain to 1.26.4** (#1021) — the published Docker image was built with Go 1.26.3, which has known standard-library vulnerabilities (incl. the high-severity CVE-2026-42504) that the container scan flagged. The build now uses the patched Go 1.26.4 release. CI's `go.mod`/test matrix already tracked the patched 1.25.11 line; this aligns the shipped binary.

## [v1.17.0] — 2026-06-06

### Added

- **Post-import drop folder for external library tools** (#941) — a new way to hand finished downloads to a tool that *owns* your library (Calibre-Web-Automated, Calibre auto-ingest, Storyteller). With Import Mode set to **External**, Bindery now renames each completed download into a configurable **drop folder** (copy or hardlink — never a move, so torrents keep seeding) instead of leaving it in the download directory, then reconciles the managed copy the external tool produces on the next library scan. Previously this was impossible: normal modes wrote into the library *and* the CWA mirror (so the file landed in both places and CWA fought Bindery over the library dir), while plain External mode left the file unrenamed in the download dir. Configurable layout (`flat` file in the folder root vs `templated` `{Author}/{Title}/…` tree) and placement (`copy`/`hardlink`) under Settings → General → File Naming. New settings `import.drop_folder` / `import.drop_layout` / `import.drop_link_mode`; the existing `cwa.ingest_path` mirror is unchanged for the "Bindery owns the library" topology. Fixes the recurring CWA ingest-routing reports and is the foundation for Storyteller pair-gated handoff (#942). See docs/DEPLOYMENT.md → "Handing off to another library tool".
- **Outbound proxy support** (#986) — route Bindery's remote-facing outbound HTTP through an `http`/`https`/`socks5` proxy via `BINDERY_OUTBOUND_PROXY` (e.g. a VPN container's Privoxy on `:8118`, or `socks5://gluetun:1080`), the same capability Sonarr/Radarr expose. In scope: indexer searches, metadata/cover providers, webhook notifications, and the telemetry ping; download clients and OIDC discovery stay direct. LAN / loopback / single-label destinations (e.g. a Docker `prowlarr` / `jackett`) are dialled direct by default so a local indexer manager stays reachable — tune with `BINDERY_OUTBOUND_PROXY_BYPASS_LOCAL` (default `true`) and `BINDERY_OUTBOUND_PROXY_NO_PROXY` (comma-separated hosts / domain suffixes / CIDRs). Parsed once at startup into a shared, proxy-aware transport in `internal/httpsec`; credentials travel in the URL userinfo and are never logged. Env-var only — no new dependency (stdlib dials all three schemes).
- **Refresh metadata for every author** (#863) — a one-click "Refresh all metadata" background job on the Authors page that re-fetches each author's metadata and catalogue, with progress that survives a page reload. Useful after a first import to backfill descriptions, covers, and missing books.
- **Per-indexer priority is now editable in the UI** (#1009) — the add/edit indexer form exposes the priority the searcher already uses to break ties when the same release is found on multiple indexers (e.g. prefer your Usenet indexers over torrent ones). Manually-added indexers previously had no way to set it, so it was stuck at 0; default is unchanged.

### Changed

- **Loopback URLs are now allowed for admin-configured service endpoints** — download clients, indexers, Prowlarr, the Audiobookshelf base URL, and the Calibre plugin URL now accept `http://127.0.0.1:…` / `localhost` (new `PolicyLANLoopback` SSRF tier). Previously the SSRF guard blocked all loopback with no escape hatch, which made a legitimate, common topology impossible: a companion service bound to `127.0.0.1`, or both containers on `network_mode: host`, could not be reached (e.g. SABnzbd on `127.0.0.1:50155` was rejected with "url not allowed: points to loopback address"). These endpoints are admin-only and CSRF-gated, so the loopback block bought ~no security. **Untrusted paths are unchanged**: proxied cover images and outbound webhooks still block loopback, and link-local + cloud-metadata (e.g. `169.254.169.254`) remain blocked everywhere. The release/torrent download URLs returned by indexers also still block loopback (a malicious indexer must not be able to point Bindery at internal services).

### Fixed

- **Transmission: indexer links that redirect to a magnet now work** (#1006) — public trackers (The Pirate Bay, Knaben, …) surfaced via Prowlarr/Jackett often serve an `http(s)` download link that 30x-redirects to a `magnet:` URI. Bindery fetched that link with Go's HTTP client, which tried to follow the redirect and failed with `unsupported protocol scheme "magnet"`, so the grab never reached Transmission. The Transmission client now follows redirects manually (re-validating each hop against the SSRF policy) and hands a redirected magnet straight to Transmission's `filename` arg — matching how the qBittorrent client already behaves (`internal/downloader/transmission/client.go`).
- **Library scan now records a result on early returns** (#965) — when the library directory was literally unset, or the book listing failed mid-scan, `ScanLibrary` returned without persisting anything, so the Settings → General "Library Scan" section kept showing a stale prior scan instead of reflecting the failure (and the #962 "no files found" warning never fired for an unset directory). Both paths now persist a scan result carrying a `scan_error` message ("library directory not configured" / "scan failed: …") that the UI renders the same way as other scan-outcome warnings. Additive, backward-compatible result field (`scan_error`); the matching logic is unchanged (`internal/importer/scanner.go`).
- **Retired the dead CSV `searchOnAdd` column** (#966) — after CSV import began always fetching each author's catalogue (#963), the optional third `searchOnAdd` column no longer gated anything and never triggered downloads. It is now dropped from the documented format and treated as ignored. The parser stays lenient, so existing users' three-column files keep importing unchanged; the dead field/plumbing is removed (`internal/migrate/csv.go`).
- **Backlist books no longer show a misleading "Wanted" pill** (#977) — `status` and `monitored` are orthogonal (every book starts `status=wanted`; backlist siblings are added unmonitored), but the status pill rendered `status` alone, so an unmonitored backlist book read "Wanted" while correctly never appearing on the Wanted page (which lists `status=wanted AND monitored`). A shared `bookStatusBadge` helper now makes the pill monitored-aware across the book detail page, book lists, and author rows: `wanted` + monitored → "Wanted"; `wanted` + unmonitored → "Not monitored" (muted). No status/model/DB change.
- **Authors and Books pages now reach past the first 100 entries** (#1010) — the list, search, sort, and filters are applied server-side and paginated, so libraries with more than 100 authors or books are fully browsable. Previously only the first page loaded, author/book name search was limited to that page, and the footer always read "1–100 of 100". Author/book name search is now matched on the server (book search also matches the author's name).

## [v1.16.1] — 2026-06-03

A getting-started / onboarding friction pass plus a batch of fixes from user reports. No breaking changes.

### Added

- **Test a download client / indexer before saving** — the Add and Edit forms for download clients and indexers now have an inline **Test** button that probes the unsaved host/port/URL/credentials, so a wrong value can be caught and fixed in place instead of having to save a broken entry, find Test on the saved row, reopen the editor, fix, and re-save. Backed by two new admin-only test-by-config endpoints (`POST /downloadclient/test` and `POST /indexer/test`) that validate and probe a posted config without persisting it; the response shape mirrors the existing test-by-id endpoints so the UI reuses one rendering path.
- **Bulk "Refresh metadata" action on the Authors page** — refresh the catalogues of many selected authors at once (metadata fetch only, never an auto-download). Recovers authors imported with empty catalogues without clicking per-author Refresh one at a time (`internal/api/bulk.go`, `web/src/pages/AuthorsPage.tsx`).
- **First-run onboarding guidance on empty Authors/Books pages** — when a brand-new instance has no authors/books *and* no indexers or download clients configured, the empty state now shows a short "Getting started" block linking to **Settings → Indexers** and **Settings → Download Clients**, explaining they must be configured first (without them, adding an author or searching silently does nothing). The block only appears when both are empty and falls back to the normal empty state if the checks fail. Settings tabs are now deep-linkable via `?tab=indexers` / `?tab=clients`.

### Changed

- **A page crash no longer blanks the whole app** — page render errors are now caught by a route-scoped error boundary that shows the error inline while keeping the nav/header usable, and clears itself when you navigate to another page (no reload needed). Previously the only boundary was the root one, whose full-screen dark-mode fallback took over the entire viewport and required a manual reload to recover.

### Fixed

- **Re-bind metadata "Re-bind anyway" override was unreachable** — when re-binding a book to a provider whose record belongs to a different author, the API returns a `409` carrying `force_required` so the UI can offer an override. The web API client discarded the HTTP status and JSON body on error, so `RebindModal` never saw the flag and rendered the raw `author mismatch` text with no way forward. The client now throws a structured `ApiError` (status + body); the amber "Re-bind anyway" confirmation works as intended. Note: forcing past the mismatch re-points the book's metadata to the new record but keeps it filed under its current author.
- **Audiobook downloads could hang in "downloading" forever (qBittorrent)** — the main import poll (`checkQbittorrentDownloads`) queried only the client's ebook `Category`, so torrents grabbed under `CategoryAudiobook` were never returned and their downloads never matched (logged as `download not found in torrent list`), leaving them stuck. The #700 fix that polls both categories had only been applied to the stall/health adapters, not this poll. It now polls every category the client may have grabbed under via `CategoriesToPoll`. Transmission and Deluge are unaffected (Transmission does not split audiobooks by category/dir; Deluge polls all torrents).
- **Grab errors name the missing download-client protocol** — grabbing a usenet (NZB) release when only a torrent client is enabled (or vice versa) returned a generic "no enabled download client configured", sending users to re-check the client they had already verified. The error now names the release's protocol and, when a client of the *other* protocol is enabled, spells out the mismatch and which client to add (`internal/api/queue.go`).
- **SABnzbd connection test validates the configured category** — the SABnzbd test only checked reachability and discarded the category list, so a typo'd category passed; downloads then landed silently in SAB's default category and the poller never found them. The test now verifies that each configured category (and audiobook category, if set) exists in SABnzbd, returning an actionable error otherwise — mirroring the existing NZBGet behaviour (`internal/downloader/sabnzbd/client.go`, `internal/downloader/adapter.go`).
- **CSV author import now always populates each newly-created author's book catalogue** — instead of only doing so for rows with an explicit `searchOnAdd=true` third column. Plain-name and two-column rows previously created authors with empty catalogues, so the library scan matched no files and the library looked empty after import. The fetch never auto-downloads (`internal/migrate/csv.go`).
- **Library scan surfaces the paths it walked and explains zero/unmatched results** — the manual library scan now records the resolved library and audiobook roots, plus an explicit zero-files signal, in its persisted result. The Settings → General "Library Scan" section shows which paths were scanned, warns by name when no book files were found under the configured directory (a common `BINDERY_LIBRARY_DIR` misconfiguration), and hints when files were found but none matched a book (the author's book catalogue needs populating first). Additive, backward-compatible result fields (`library_dir`, `audiobook_dir`, `scanned_paths`, `no_files_found`); the matching logic is unchanged.
- **Helm chart shipped unusable defaults** — `values.yaml` defaulted `BINDERY_LIBRARY_DIR` to a maintainer-specific path (`/media/BOOKS/incoming`) and enabled a `/downloads:/media` path remap, so a fresh `helm install` pointed the library at a path that didn't exist in the pod and silently rewrote download paths. Library dir now defaults to `/books` (matching the Docker image) and the remap is commented out by default. The chart's ingress was also Traefik-`IngressRoute`-only; it now supports `ingress.type: standard` to render a portable `networking.k8s.io/v1` Ingress (with `className`/`annotations`/`tls`) for nginx-ingress, GKE, EKS, etc. `type` defaults to `traefik`, so existing installs are unchanged.

### Docs

- **Added `docs/QUICKSTART.md`** — an in-repo zero-to-first-download walkthrough (run → first login → indexer → download client → author → grab) with the SSRF `localhost`-rejection gotcha and protocol-matching/category troubleshooting callouts. Linked from the README documentation table.
- **Fixed doc/code drift:** Unraid template Overview now says MIT (was "Apache 2.0"); `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` added to the README env table; `BINDERY_TRUSTED_PROXY` and `BINDERY_TELEMETRY_DISABLED` added to the DEPLOYMENT env-var reference; the broken `#reverse-proxy` anchor in `DEPLOYMENT.md` now points at the Reverse-proxy wiki; README "full reference" wording corrected to point OIDC/forward-auth vars at `docs/auth-oidc.md` / `docs/auth-proxy.md`.

## [v1.16.0] — 2026-06-03

Security and hardening release. The bulk of this version is an audit-driven hardening pass (the **D1–D4** access-control findings and the **Wave 2–5** robustness sweep), opt-in per-user data isolation, a batch of performance work, and a long tail of import/scheduler correctness fixes. No breaking config changes, but two behaviour changes worth noting before upgrading: list endpoints are now paginated and request bodies are capped at 1 MiB by default (see **Changed**).

### Added

- **Opt-in per-user data isolation via `BINDERY_ENFORCE_TENANCY`** (#898, #899) — a new environment variable (defaults **off**) that turns on real per-user scoping. With it set, each user sees only their own authors, books, profiles, and root folders (Tier-1 resources, #899), and the join-scoped queue, history, pending grabs, and OPDS catalogue are scoped to the requesting user (Tier-2, #898). Left unset, Bindery behaves exactly as before — a single shared library view across all accounts — so existing single-user and trusted-multi-user installs are unaffected. Admins still see all data regardless of the flag. Documented in `docs/multi-user.md`.

- **Blocklist entries record who created them** (#929) — a `created_by_user_id` audit column on the blocklist so manual blocks are attributable in multi-user installs. Part of the D4 audit follow-up.

### Changed

- **List endpoints are now paginated** (#902) — the `List` API surface (books, authors, queue, history, etc.) returns paginated results with new sort/lookup indexes backing them, rather than unbounded full-table responses. This keeps large libraries responsive but is a **response-shape change** for any external script that assumed a single call returned every row; such callers must follow pagination. The React UI already paginates.

- **Request bodies are capped at 1 MiB by default** (#901) — every API handler now rejects bodies larger than 1 MiB to bound memory use from hostile or malformed requests. Normal API and UI traffic is well under this; only unusually large custom payloads are affected.

- **Webhook notifications emit an Apprise-compatible `body`/`title` payload** (#888) — the generic webhook now includes top-level `body` and `title` fields alongside the existing structured payload, so Apprise, ntfy, and similar relays render notifications without a custom template.

- **Database backups now use `VACUUM INTO`** (#900) — backups capture a consistent snapshot that includes outstanding WAL pages, rather than copying the main database file and risking a torn or stale backup under load.

- **Sessions are invalidated on password change** (#896) — changing a user's password now revokes that user's existing sessions, so a rotated credential actually logs out the old session everywhere instead of leaving it valid until expiry.

- **Performance sweep (Wave 3)** — pooled HTTP clients for indexers and downloaders so each request no longer spins up a fresh transport (#917); bounded goroutine fan-out on bulk, queue, and series operations to stop large libraries spawning unbounded concurrency (#918); capped the metadata TTL cache and cached `enrichBook` results (#915); debounced Audiobookshelf enumerator checkpoint writes (#916). Build noise reduced by quieting the Vite build warnings (#913).

- **Removed the dormant tag surface** (#927) — the unused tag UI/columns flagged in the audit (D4a) were removed rather than left as dead, unreachable surface.

### Fixed

- **Unmatched audiobook imports no longer panic the library scan** (#946) — a completed download detected as an audiobook but with no resolved book row (no `BookID`, a lookup error, or a row deleted between grab and import) dereferenced a nil book while computing its destination and crashed the scan goroutine. It now fails that download with an actionable status, matching the ebook path. Found in a code sweep.

- **Connection-refused diagnostics stop blaming a Docker subnet** (#944) — the download-client "connection refused" hint now points at the real common causes (a service bound to `127.0.0.1` refusing a LAN-IP URL, or a host firewall) instead of asserting a Docker subnet that host-networked deployments don't have.

- **History tab is no longer empty for auto-grabs** (#938) — scheduler-initiated grabs now record a `Grabbed` history event like manual grabs always did, so monitored-author auto-search produces a visible audit trail. Reported by ThatGuyHere.

- **Calibre import persists series and series membership** (#936, half of #905) — importing from a Calibre library now writes `series` and `series_books` rows, so series populated in Calibre survive the import instead of being dropped.

- **Torrent imports only take the torrent's own file list** (#933, closes #903) — single-file torrents sharing a download root no longer drag unrelated sibling files into the library; the importer operates on the torrent's file list rather than the whole directory.

- **Legacy date formats are tolerated on read** (#921, closes #914) — `Scan` on book/author rows now parses pre-existing non-RFC3339 date strings instead of erroring, and writes back RFC3339 going forward, so databases carried over from older versions load cleanly.

- **Library-scan completion log names every walked root** (#937) — the log line now lists all scan roots instead of only the primary library directory, so multi-root setups can see what was actually covered.

- **Hardcover author-works filter respects canonical-book language** (#890, #889) — the canonical book's language is propagated so the author-works language filter actually applies.

- **`refreshMetadata` guards against `(nil, nil)` from the aggregator** (#892) — a provider returning no book and no error no longer trips a nil dereference.

- **`book.Author` is populated in `List` and `Get` responses** (#884, closes #882) — responses now carry the author object rather than leaving it nil for clients to resolve separately.

- **App-lifetime context plumbed through background goroutines** (#932, #934, closes #846) — bulk, author, calibre, and the four goroutines #932 missed now observe the application lifetime context and shut down cleanly instead of leaking past process exit.

- **`ttlCache` janitor no longer leaks a goroutine per instance** (#881, root cause of #73) — the metadata cache's cleanup janitor is now stopped with its cache, fixing a per-test (and per-instance) goroutine leak.

- **Download/import state-machine and atomicity hygiene (Wave 4)** (#920) — assorted state-transition and atomicity corrections across the downloader and importer.

- **OIDC discovery releases its lock and reads `failedEntry` race-free (Wave 5)** (#919) — the OIDC provider no longer holds a lock across the network discovery call and the failed-entry read is synchronised.

### Security

- **Closed cross-user IDOR on Tier-1 per-user resources** (#899, D1, env-gated) and **scoped Tier-2 join resources** queue/history/pending + OPDS (#898, D3) — see the tenancy entry under **Added**. Both are gated behind `BINDERY_ENFORCE_TENANCY`.
- **Shared deployment-config routes are gated behind `RequireAdmin`** (#897, D2) — indexer/download-client/system configuration endpoints that were reachable by non-admin users are now admin-only.
- **OIDC SSRF, redirect-revalidation, and trusted-proxy scheme guards** (#894) — the OIDC discovery probe and redirect handling reject private/loopback targets (override with `BINDERY_ALLOW_LAN_OIDC`) and re-validate redirect URLs; the trusted-proxy path validates the forwarded scheme.
- **Plugged a settings-endpoint secret leak** (#893) — `auth.oidc.providers` and other settings no longer return stored secrets in API responses (write-only).
- **Library-root containment on file deletion** (#895) — book/author file-deletion endpoints reject paths outside the configured library roots, with symlink resolution, so a crafted path can't delete arbitrary files.
- **StepSecurity best-practices sweep** (#904) — pinned actions, hardened runners, and related CI supply-chain hardening.
- **Go toolchain bumped to 1.25.11** (#945) — picks up the stdlib fixes for `GO-2026-5037` (crypto/x509) and `GO-2026-5039` (net/textproto). CI-toolchain only; the runtime image was already on a patched Go.

### Docs

- **Documentation currency sweep** (#949) — corrected the OPDS path (`/opds/`, not `/opds/v1.2/`), clarified that per-user isolation is opt-in via `BINDERY_ENFORCE_TENANCY`, refreshed the SECURITY.md supported-version table, marked editable quality profiles as shipped, completed the ARCHITECTURE.md package list, and documented previously-undocumented environment variables. The wiki's auth/OIDC recovery and secret-rotation recipes were also corrected.

## [v1.15.3] — 2026-05-29

Patch release. Three user-facing bug fixes plus a Hardcover edition-hydration feature and the client side of the telemetry redesign.

### Fixed

- **Library scan now reconciles Calibre-imported books with missing file paths** (#875, #878) — Calibre import sets every book to `Status=Imported`, but in container setups where Calibre's library mount differs from Bindery's view, `FilePath` stays empty. The library scan's candidate filter required `Status=Wanted` so all imported epubs found zero reconciliation targets, and users had to refresh metadata per author to nudge the rows back into scope. The candidate filter now includes Imported books whose recorded paths are either empty or point at locations that no longer exist, so the canonical Calibre-import-then-scan flow just works. Also covers the related "user moved their library and re-ran a scan" case where Imported rows used to be orphaned forever. Thanks to @Jashun44 for the precise diagnosis.

- **Single-word hyphenated titles no longer have every release dropped at the relevance filter** (#871, #876) — titles whose entire significant content was one hyphenated token (e.g. **Slaughterhouse-Five**, **Mother-to-Mother**) failed the relevance filter against every indexer result because `SigWords` kept the hyphen in the keyword while `NormalizeRelease` on the release side replaced it with a space. `SigWords` now pre-converts the same separator set as `NormalizeRelease` (`._-()[]|`) so hyphenated titles tokenise the same way multi-word titles already do. Reported with root cause and fix shape by @eliseban.

- **Transmission grabs no longer time out when the daemon is behind a VPN container** (#873, #877) — when Transmission runs inside `haugene/transmission-openvpn` or similar, the daemon's outbound traffic routes through the VPN tunnel. Bindery was passing the indexer URL via Transmission's `filename` arg, which makes the daemon fetch the URL itself; through the VPN that fetch never returns and Bindery's 15s deadline trips even though Test-Connection works fine. The Transmission client now fetches the `.torrent` file through Bindery's own HTTP client and submits the content via `metainfo` (base64), matching the same shape SAB (#864) and NZBGet (#837) ship since v1.15.2. Magnet links still pass through unchanged. Same `httpsec.PolicyLAN` SSRF guard and 50 MB cap as the other clients. Reported by @Bclark117 with the exact fix shape this release implements.

### Changed

- **Hardcover editions hydrate into the local library when a book has a confident Hardcover identity** (#822) — when a book carries an `hc:` foreign ID (created via Hardcover lookup, list sync, rebind, recommendations, or series fill), Bindery now fetches the full Hardcover edition list and persists those rows alongside the book. Edition fields use a COALESCE NULLIF upsert so user-curated and import-time values are never overwritten, and edition rows that already belong to another book are silently skipped (no re-parenting). When the matched audiobook edition carries an ASIN and the book has none yet, the ASIN is promoted onto the book and (if Audnex is configured) the audiobook is enriched automatically. Gated on Enhanced Hardcover being enabled (token configured + admin opt-in + env enabled), so installs without a Hardcover token see no behaviour change. Thanks to @magrhino.

- **Telemetry client sends per-subsystem feature counters** (#872) — the daily anonymous ping now carries an optional `features` block with counts of enabled indexers, download clients, notifications, and users, plus booleans for whether Calibre / Audiobookshelf / Grimmory / Hardcover / OIDC / multi-user is configured. Strictly numeric or boolean, never names or values. Lets the maintainer prioritise feature work against actual adoption rather than Discord vibes. Documented at [getbindery.dev/telemetry-fields](https://api.getbindery.dev/telemetry-fields) and opt-out remains unchanged (`BINDERY_TELEMETRY_DISABLED=true` env var or `telemetry.enabled=false` setting).

### Internal

- Closed #870 with a pointer to the existing `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1` env var from v1.15.1 #853; same feature, just hadn't been discovered yet.

## [v1.15.2] — 2026-05-28

Patch release. Two download-client fixes for users on v1.15.1 with broken SAB/NZBGet submissions, a UI refactor that locks the clipboard-fallback pattern from v1.15.1 into a reusable hook, plus housekeeping (gosec annotation, chi dep bump).

### Fixed

- **SABnzbd submissions no longer hang in "Waiting" with a resetting countdown** (#864) — Bindery was using SAB's `mode=addurl`, which makes SAB itself fetch the NZB from the indexer URL. In containerised setups where Bindery and SAB sit on different Docker networks (or only Bindery has DNS/route for the indexer), SAB can't reach the URL and parks the job in retry-backoff forever; Bindery's `sent to downloader` log line was misleading because addurl returned `status: true` regardless. Bindery now fetches the NZB itself through its own HTTP client (which holds the indexer credentials and network path) and submits the content via SAB's `mode=addfile` multipart upload — the same shape the NZBGet client has used since #837. The same `httpsec.PolicyLAN` SSRF guard and 50 MB cap apply. Thanks to @ibsfox for the precise repro (manual `.nzb` upload to SAB worked; the URL-handoff did not).

- **NZBGet rejections now name the actual problem** (#861, #862) — when NZBGet's `append` JSON-RPC returns id 0 (rejection), Bindery's error was `NZBGet rejected download (returned id 0)` with nothing more, which gave users no path forward. The most common cause is that the category configured in Bindery's download-client (e.g. `Audiobooks`) isn't defined in NZBGet's own Settings → Categories — NZBGet silently rejects in that case. Bindery now preflights the category list via NZBGet's `config` RPC before submitting; on mismatch the error names both the missing category and what NZBGet actually has configured. The same check runs at Test-Connection time so the misconfig surfaces when saving the client, not on the first grab. If preflight passes but append still returns 0 (disk full, write-permission on intermediate dir, NZBGet paused with quota reached, invalid NZB content), the fallback error now enumerates those causes and points the user at NZBGet's own log. Thanks to @BraynArts for the report.

### Changed

- **Clipboard-fallback handling is now a shared hook** (#860) — extracts `useClipboardCopy` + `ClipboardManualFallback` from the v1.15.1 inline fix in `SearchDebugPanel` (#850) and applies the same pattern to copy buttons for book file paths, API keys, OPDS feed URLs, and OIDC callback URLs. When the modern Clipboard API is unavailable (plain-HTTP LAN install) and the legacy `document.execCommand('copy')` fallback also fails, the UI now renders the text in a focusable read-only textarea so the user can always copy. Thanks to @magrhino.

### Dependencies

- Bump `github.com/go-chi/chi/v5` from 5.2.5 to 5.3.0 (#839).

### Security (housekeeping)

- Extend `// #nosec` directives on the three `os.Remove` call sites in `removeBookPathScoped` (`internal/api/books.go`) to cover gosec G703 (path-traversal taint analysis) in addition to G304. The paths reaching these calls are already constrained by the importer's `sanitizePath` (strips `..`, `/`, `\`, `:`, etc.), so the alert was a false positive in current code, but the suppression is now explicit and references #865 which tracks the defense-in-depth root-folder containment check.

## [v1.15.1] — 2026-05-27

Patch release. Five security/correctness fixes from a post-v1.15.0 review pass plus two user-visible bug fixes that affect every install (OpenLibrary author-search 403, notifications silently inert for everything but manual grabs).

### Fixed

- **Notifications actually fire on real events now** (#849, follow-up to #799) — before this release the notifier was only wired into the manual-grab path. Auto-grab (Wanted searches, series fill, recommendations, bulk monitor), import success, import failure, and download-client health-check failures all looked configured (`Test` worked, settings saved) but never produced webhooks. `EventGrabbed` now fires on every successful auto-grab, `EventBookImported` on every clean import, `EventDownloadFailed` from the importer's failure paths, and `EventHealth` is edge-triggered when a download client enters the error state (suppressed for the `checking → error` transient and for repeated `error → error` polls so a persistently-broken client doesn't spam every refresh cycle). `EventUpgrade` is intentionally deferred — Bindery does not currently have a distinct upgrade-grab code path. Thanks to @wirecutter313 for the original report.

- **`BINDERY_CONTACT` env var lets each install advertise its own User-Agent contact** (#848) — OpenLibrary's `/search/authors.json` endpoint applies per-User-Agent rate-limiting that the shared default contact pointer (the Bindery project URL) was tripping across the entire fleet, leaving users with HTTP 403 on every "Add author" attempt even though name/title book searches still worked. Setting `BINDERY_CONTACT` to a per-instance email or URL — e.g. `BINDERY_CONTACT=you@example.org` — makes each install's User-Agent distinct and lifts the block. Bare email addresses are auto-prefixed with `mailto:`. The default (project URL) is unchanged for installs that don't set the env var; users hitting the 403 should set it. Thanks to @wirecutter313 and a Reddit reporter for the independent confirmations.

- **Notification webhook URL on a private network can be set with a clear escape hatch** (#799 follow-up) — the bare `url not allowed: points to private network` error now appends a hint pointing at the `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1` env var, so users running ntfy / Gotify / Home Assistant on the same Docker network know how to unblock the save. Thanks to @joncrangle.

- **Custom HTTP headers are now editable in the notification UI** (#799 follow-up) — the `Headers` field has always existed in the model and the notifier honoured it, but the UI hardcoded `'{}'` so users had no way to provide an `Authorization` header for ntfy / Gotify / webhook routing. Both Add and Edit forms now expose a "Custom headers (JSON)" textarea with client-side validation and an ntfy auth placeholder. Thanks to @wirecutter313.

### Security

- **Indexer / Prowlarr / Download-client list endpoints behind RequireAdmin** (#844) — before this release, any authenticated user (role `user`, not just `admin`) could `GET /api/v1/indexer/{id}` and read the indexer's API key, or `GET /api/v1/downloadclient/{id}` and read the qBittorrent password. The entire `/api/v1/prowlarr/*` subtree (including Create/Update/Delete/Test/Sync) was ungated and a non-admin could delete an admin's Prowlarr instance. All these routes now require admin role. Tested by `TestSensitiveRoutesRequireAdmin`. Found in the post-v1.15.0 security review.

- **Notification routes behind RequireAdmin** (#799 follow-up) — same shape as the indexer/prowlarr leak: `Notification.Headers` carries arbitrary HTTP auth tokens (ntfy auth, Discord routing), but `GET /api/v1/notification` was ungated. Now admin-only across the whole subtree.

- **Backup endpoints behind RequireAdmin** (#845) — the `POST /api/v1/backup/{filename}/restore` endpoint overwrites the live SQLite database with the named backup file. Before this release, any authenticated user could roll the instance back. Now admin-only.

- **OIDC promote-first-admin race condition fixed** (#845) — two concurrent first-time OIDC logins against an admin-less instance with local auth disabled could both pass the "no admins exist" check and both be auto-promoted to admin. The decision is now atomic via `SettingsRepo.SetIfAbsent` (SQLite `INSERT … ON CONFLICT DO NOTHING`): exactly one concurrent first-time login wins; any other simultaneous login falls back to the default role.

- **Calibre import rollback is now transactional** (#643 follow-up, #847) — when a rollback hit a per-entity failure partway through, prior deletes/restores were already committed but the run wasn't marked rolled back, so retrying re-applied successful actions against shifted state and `restore_*` ops could mis-revert. The entire rollback now runs inside a single `sql.Tx` — any failure rolls back every prior write atomically, and `Stats.Failed > 0` is impossible on a successful return.

- **Migration runner refuses duplicate version numbers at boot** (#845) — the 043 collision incident during the v1.15.0 cycle was silently lost on every existing install (the apply loop skipped the second `043_*.sql` because the version was already recorded). A new startup guard fails with a clear error when two migration files share a numeric prefix, so the failure mode can't recur.

- **ABS / Grimmory / Calibre plugin base URLs validated against SSRF policy at save** (#845) — the admin-input boundary for these provider URLs now blocks link-local (169.254/16, AWS IMDSv4) and cloud-metadata endpoints via `httpsec.ValidateOutboundURL(PolicyLAN)`, matching the existing indexer/prowlarr/downloadclient policy. Loopback and RFC1918 are still allowed for typical homelab deployments. NewClient callers continue to use the format-only `NormalizeBaseURL` so test fixtures with `httptest` (loopback) still construct clients.

- **File handler path check fails closed when no library roots are configured** (#845) — previously allowed any path when `BINDERY_LIBRARY_DIR` was unset (intended for test fixtures, but a silent prod misconfiguration). Now returns 403 unless the path falls under a configured root; tests seed an explicit `t.TempDir()` root.

- **Trusted-proxy `0.0.0.0/0` boot warning** (#845) — operators sometimes set `BINDERY_TRUSTED_PROXY=0.0.0.0/0` to silence the proxy-mode safety gate, but in that shape every client's `X-Forwarded-For` is honoured, defeating the login rate-limiter and any per-IP decision. A boot-time `slog.Warn` makes the misconfiguration visible in logs.

## [v1.15.0] — 2026-05-26

Six feature drops plus a sweep of fixes for default-install breaks that v1.14.2 didn't catch.

### Added

- **Editable Quality Profiles** (#768) — quality profiles existed in the model and had a read-only Settings tab, but no way to create, edit, or delete them existed. The `/api/v1/qualityprofile` endpoint gains CRUD behind `RequireAdmin`, and Settings → Quality Profiles is now a full editor: reorderable preference list, per-format allow toggle, cutoff `<select>` restricted to allowed items, audiobook badge on m4b/mp3/flac/m4a entries. Delete-in-use returns 409 with the conflicting author count. Closes the obvious migration story from *arr that the read-only UI implied but didn't deliver.

- **Configurable author monitor modes** (#792, #809, #810) — adding a prolific author no longer floods Wanted with the full back catalogue. The Edit Author dialog gains a `MonitorMode` selector with five values: `all` (existing behaviour), `future` (only books with a future release date), `latest N`, `none`, and `series` — the last lets the user pick one or more of that author's series and monitor only books in those series. `BINDERY_AUTHOR_DEFAULT_MONITOR_MODE` sets the global default for new authors (any mode except `series`, which is per-author by design). Existing books can be retroactively re-monitored to match the new mode via an "apply to existing" checkbox. Thanks to @anthonysnyder for the original ask and @magrhino for implementing #809.

- **Bulk monitor/exclude/delete on Author detail** (#791) — per-row checkboxes + select-all header with indeterminate state + sticky bulk-action bar. Monitor / Unmonitor immediate, Exclude / Delete behind a confirm. Selection auto-prunes when filters hide rows. Pairs with the new monitor modes for a coherent "add prolific author" workflow. Thanks to @anthonysnyder.

- **Per-media-type download client category** (#700) — a download client now accepts a second `CategoryAudiobook` field. When set, audiobook grabs go under that category and validate against `BINDERY_AUDIOBOOK_DOWNLOAD_DIR`; ebook grabs use the existing `Category` against `BINDERY_DOWNLOAD_DIR`. The fuzzy `strings.Contains(category, "audio")` heuristic is gone. Multi-client setups can now use `PickClientForMediaType` to route by explicit field. Thanks to @strenkml.

- **Calibre import/sync rollback** (#643) — Calibre import gains run tracking + entity snapshots + rollback preview + rollback execute, modelled on the existing ABS rollback. A bad Calibre import is now revertible without a database-level snapshot. Metadata-only revert — on-disk files are not touched. New endpoints under `/api/v1/calibre/runs[/{id}/rollback[/preview]]` behind `RequireAdmin`; Settings → Calibre gains a "Recent imports" panel with per-run Rollback. Sync-side (push to Calibre) intentionally excluded — it only mutates `calibre_id`, no data damage. Thanks to @magrhino.

- **Edit existing Prowlarr connection** (#820) — the Prowlarr card in Settings → Indexers gained Add/Test/Sync/Delete but never Edit, so rotating the Prowlarr API key required deleting and recreating the instance (which cascade-deleted every synced indexer row and the user's local toggles/priorities). New Edit form is wired to the existing `PUT /api/v1/prowlarr/{id}`. Key rotation propagates immediately to every indexer row managed by the instance, so synced indexers never authenticate with a stale key. URL changes still need a manual Sync to rebuild per-indexer torznab URLs; the form warns when URL is edited. Thanks to @magrhino.

- **Hardcover series refs populated during list sync** (#805) — Hardcover GraphQL queries for custom lists and built-in shelves now request `featured_series` fields; the list syncer wires those refs into `SeriesRepo.LinkSeriesRefs` best-effort after each book create. Series links appear immediately for books imported from Hardcover lists rather than requiring a later manual rebind. Thanks to @magrhino.

### Fixed

- **OpenLibrary name/title searches no longer fail with HTTP 403** (#834) — OpenLibrary's API now blocks requests whose `User-Agent` does not include a contact pointer (email or URL), and Bindery's previous `bindery/<version> (<os>)` UA matched that block. Name/title book additions failed immediately against OpenLibrary while ISBN lookups (which fall through to Hardcover enrichment) still worked, so the breakage was easy to miss in smoke checks. The User-Agent now appends the project URL — `bindery/<version> (<os>; https://github.com/vavallee/bindery)` — which satisfies OpenLibrary's policy and unlocks its higher rate limit. Thanks to @thetic for the precise repro and root-cause analysis.

- **qBittorrent on Windows reports paths with backslashes; Bindery now normalizes them** (#800 follow-up) — a qBittorrent instance running on Windows reports `SavePath`, `ContentPath`, and category `SavePath` with backslash separators (e.g. `N:\Torrents\complete\library\book`). Bindery's downstream Linux path code (`filepath.Walk`, `PathRemap.Apply`, `pathIsAtOrUnder`) cannot process those, so a Docker-on-Windows user's import failed with `no book files found in N:\...` even though their PathRemap looked right. Backslashes are now normalized to forward slashes at the qBit API boundary; PathRemap configuration becomes predictable across deployments. Reported by PixieApples on Discord.

- **qBittorrent path remap is now verified against the on-disk filesystem** (#800 follow-up) — the PathRemap suggestion shipped in v1.14.2 only checked whether the remapped string was at-or-under `BINDERY_DOWNLOAD_DIR`, never that the resolved path actually existed. Linux is case-sensitive, so a Windows/WSL/Docker user with their drive mounted at `/N/` but Bindery configured with `/n/` (or vice versa) got a textually-correct remap suggestion that silently pointed at nothing. The health check now stat()s the resolved path; if missing but a case-variant exists, the error names the divergent segment so the user knows exactly which letter to fix. Also reported by PixieApples.

- **qBittorrent import diagnostics now surface real failure modes** (#824) — three gaps made torrent import failures near-invisible even with debug logging on: `GetTorrents` API errors were debug-level, loop-level skip-on-no-match was silent, and the "no book files found" error didn't distinguish path-doesn't-exist from path-exists-but-empty. API errors now log at Warn, the poll loop emits per-skip and per-match debug context, and when the download path doesn't exist a separate Warn names PathRemap as the likely fix. Thanks to @statte.

- **Docker → bare-metal host firewall is now named in the ECONNREFUSED hint** — `(service may not be running on that port)` was misleading when the service was running and a host firewall was REJECTing traffic from the Docker bridge subnet (REJECT sends RST → ECONNREFUSED). The hint now reads `(connection refused — service may not be listening on that port, or a host firewall is rejecting traffic from the Docker subnet)`. Reported by Daize on Discord.

- **Grimmory integration is now honest about being configuration-only** (#818) — Grimmory v3.x has no API keys (confirmed upstream at grimmory-tools/grimmory#1487), but the Bindery UI implied the API key was required, sending users on wild-goose chases when they tried to find one. The field is now optional with a clear note, the empty `Authorization` header is suppressed when no key is set, and a banner at the top of the tab acknowledges that Bindery does not yet push books to Grimmory — that's tracked in #826. Reported by Merijeek.

- **Migration version collision (043)** (#832) — internal hotfix during the v1.15.0 cycle: two unrelated PRs both shipped a `043_*.sql` migration and the runner skips any version it has already applied, so on existing installs only the first 043 would run and the second's schema change would be silently lost. The author-monitor-mode migration was renumbered to 045 before any release shipped the collision.

### Changed

- **Per-media-type expected-dir resolution** (#700 ripple) — `ExpectedDownloadDirForClient` now takes a `mediaType` parameter rather than guessing from category name. The fuzzy `strings.Contains(category, "audio")` heuristic is gone.

### Chores

- Dependency bumps via dependabot (#815, #817) and vulnerable Go modules update (#819).
- IndexersTab i18n migration (#838) — Settings → Indexers strings now route through `t()` so they translate per locale.
- Sweep of 113 `t.Fatal` nil-check sites in test files to satisfy staticcheck's SA5011 analyzer (#841); no runtime behaviour change.

## [v1.14.2] — 2026-05-24

### Fixed

- **Hardcover import lists paginate and reuse the saved global token** (#789) — large custom lists previously stopped at the first page, built-in shelves (To Read / Currently Reading / Read / Did Not Finish) did not page through all books, and a global-token deployment had to duplicate the Hardcover token per list to make sync work at all. Built-in shelves now expose item counts in the list picker, both custom lists and shelves paginate fully, and an import list with no per-list override now falls back to the workspace-wide saved Hardcover token. Thanks to @magrhino.

- **qBittorrent re-grab of an already-imported book now resolves to imported instead of looping or being blocked** (#769) — the v1.14.0 hash-recovery fix landed the download row in `StateGrabbed` when qBittorrent returned 409 for a duplicate add, but three gaps prevented it from completing on its own: a missing `content_path` in move-mode left the download polling forever; an empty download directory tripped `StateImportFailed → StateImportBlocked` after three retries; and the `grabbed → completed` transition was rejected by the state-machine guard. Bindery now detects "already in library" via `book_files` + `os.Stat`, walks the import states through to `StateImported`, and the missing transition has been added. Thanks to @statte.

- **"Local-only" auth bypass now grants the admin role to trusted-LAN requests** (#799) — admin-only endpoints (auth mode change, user CRUD, settings writes) returned `admin role required` 403 for requests on a trusted private network, even though local-only mode is meant to grant frictionless access from one. The trusted-LAN bypass now sets the request's role to `admin`, mirroring the API-key bypass.

- **Webhook notification saves surface their errors instead of failing silently** (#799) — saving a webhook notification under Settings → Notifications dropped errors on the floor, so a URL rejected by the default-strict outbound policy gave the user no feedback at all. Add and edit forms now display the error inline and disable the Save button while the request is in flight.

- **qBittorrent category-path mismatch error now tells you how to fix it** (#800) — when a qBittorrent category's save path resolves outside Bindery's expected download directory, the health-check error named the problem but never the remedy. The error now suggests a derived `PathRemap` value (e.g. `/torrents/complete:/downloads`) and the alternative of mounting the same directory inside Bindery to match `BINDERY_DOWNLOAD_DIR`.

- **Adding a new author no longer risks deleting the author row before the requested book is saved** (#804) — for prolific authors the async catalogue sync that follows `AddBook` can exceed the 15s poll budget; when it did, the orphan-cleanup defer treated the just-created author as orphaned and deleted it, and the async goroutine's later inserts cascaded into FOREIGN-KEY failures. The handler now directly persists the requested book before the cleanup defer runs whenever the author was newly created, mirroring the existing DNB path from #667. The async sync continues to do catalogue backfill — it just no longer races for the requested book.

- **Stale ABS no-match review items are auto-reconciled on rescan, plus per-run bulk dismiss** (#767) — fixing metadata in Audiobookshelf and re-running the import previously left old no-match rows in the review queue forever, indistinguishable from genuine no-matches. Bindery now flips pending review rows for any item that matched on the current run (preserving any user-applied decisions on rows that have already left pending), and a new "Dismiss all from this run" button on the ABS review page lets you clear the residue from a previous run.

## [v1.14.1] — 2026-05-22

### Fixed

- **Prowlarr no longer syncs zero indexers** (#763, #788, #794) — since v1.13.x the Prowlarr sync read each indexer's categories from a top-level field that Prowlarr's API does not populate (the category list lives under the indexer's `capabilities`). Every indexer was rejected as having "no book/audiobook categories", and — worse — previously synced indexers were then deleted on each sync, wiping indexer config. The sync now reads categories from `capabilities`, scoped against a registered book application (Readarr/LazyLibrarian) where one exists and falling back to the indexer's own book/audiobook capability categories for standalone Prowlarr setups. The syncer also no longer removes indexers when a sync matches nothing at all, so a future filter regression cannot wipe indexer config again.

- **qBittorrent 5.1.4 category health checks no longer fail** (#793) — qBittorrent 5.1.4 can return a boolean `download_path` flag in category payloads, which made Bindery fail to decode the categories response (`cannot unmarshal bool into Go struct field`) and fail the category health check. Non-string category path fields are now ignored while the string save path is still read.

- **Readarr migration imports author names correctly** (#784) — the Readarr import queried a non-existent `Authors.Name` column; in Readarr's schema the author name lives in `AuthorMetadata`, joined via `Authors.AuthorMetadataId`. The import now performs that join, so migrated authors keep their names.

- **Telemetry server stages its database backup next to the database** (#777) — the telemetry server wrote its backup to `/tmp`, which could fail or cross a filesystem boundary; the backup is now staged alongside the live database.

## [v1.14.0] — 2026-05-21

### Added

- **Audiobookshelf import can reconcile multiple libraries from one source** (#670) — a single ABS source can now import from several selected book libraries (for example a separate Books and Audiobooks library) instead of just one. Libraries are imported in an ordered, per-library sequence, each producing its own run record with independent checkpoint, rollback, and provenance, and the settings UI gains a multi-select for choosing them. Existing single-library configurations are migrated and behave unchanged. Closes #580.

- **Per-author audiobook root folder** (#579) — an author can be assigned a dedicated audiobook destination, separate from the global audiobook directory and from the ebook root folder. Set it in the Edit Author dialog; when unset, audiobooks fall back to the global audiobook directory. It is honoured by both the import scanner and the Audiobookshelf import path. Thanks to @j-tt for the original report and patch.

- **One-shot Goodreads library import** (#585) — a migration aid for users coming from Goodreads or Readarr. Export your library CSV from Goodreads, upload it under Settings → Import, and Bindery resolves each row against the metadata providers, shows a dry-run preview (added / skipped / failed to resolve), and lets you commit. Rows that fail to resolve are downloadable as a CSV to fix and retry. A shelf filter (default: `to-read`) selects which books to bring across. It is a one-time import, not a live sync — Bindery never contacts Goodreads.

- **OIDC role mapping** (#688) — three new opt-in, backward-compatible env vars remove the manual-promotion friction (and lockout trap) of SSO-only deployments. `BINDERY_OIDC_DEFAULT_ROLE` (`user`/`admin`, default `user`) sets the role assigned at OIDC auto-provision time. `BINDERY_OIDC_ADMIN_GROUP` makes the IdP authoritative for the admin role: when set, every login promotes the user to `admin` if the configured group claim contains that group and demotes to `user` if absent — overriding the manual role-promotion API for OIDC users. `BINDERY_OIDC_GROUP_CLAIM` (default `groups`) selects the claim path and tolerates both shapes IdPs emit (a JSON array of strings, or a single space/comma-delimited string). Finally, when local auth is disabled and Bindery has zero admins at OIDC-provision time, the first provisioned OIDC user is auto-promoted to admin — guarded by a one-shot settings flag so deleting all admins later cannot silently re-promote.

### Changed

- **The Wanted page has been redesigned** (#760) — the wanted-books list now uses the same card styling and consistent action buttons as the refreshed book-detail page, presenting each book and its search action more clearly.

- **Audiobookshelf import now imports unmatched items instead of queuing them for review** (#781) — previously every author or book without a confident local match was sent to the manual review queue, so a first import of a large folder-backed ABS library left almost everything (often 90%+) parked for review. Unmatched authors and books are now created directly — a faithful import of your own library — and only genuinely *ambiguous* matches (a close but uncertain candidate) are queued for review. If you re-run an Audiobookshelf import, expect far more items to import directly than before.

### Fixed

- **Readarr import no longer fails CSRF validation** (#765) — the Readarr/CSV migrate upload bypassed the standard API request wrapper and sent no CSRF token, so it was rejected once CSRF enforcement was tightened. The upload now goes through a helper that attaches the CSRF and `X-Requested-With` headers; this also fixes the upload on sub-path deployments.

- **Hardcover series and shelf handling hardened** (#691, #776) — built-in reading-shelf mapping (Want to Read / Currently Reading / Read / Did Not Finish) is corrected against Hardcover's documented `status_id` values, series mapping is more robust, and ISBN, genre, and media-type are now parsed from Hardcover responses.

- **qBittorrent re-grabs no longer fail when the torrent is already present** (#769) — re-grabbing a book whose torrent qBittorrent already holds returned `add torrent HTTP 409: Conflict` and failed the grab outright. A 409 means the content is already available, so Bindery now recovers the existing torrent's hash and proceeds to import instead of erroring.

### Docs

- **Documentation deep-dive cleanup** (#782) — removed a stale orphaned multi-user doc, corrected the multi-user guide (a wrong API route, a non-existent config key, and a wrong env var), updated the Audiobookshelf docs for the new import behaviour, added how-tos for the Goodreads import and the per-author audiobook root folder, and completed the README documentation index.

## [v1.13.2] — 2026-05-20

### Changed

- **The library scan is now linear in library size instead of quadratic** (#757) — reconciling on-disk files against the database ran a Jaro-Winkler title comparison for every *(file, book)* pair, an O(files × books) cost that made the first scan of a large migrated library take minutes and allocate gigabytes of memory. The title comparison is now scoped to the file's author and the loop-invariant title normalisation is hoisted out of the inner loop, so a realistic per-author library scans in time proportional to its size — a 2,000-book library drops from ~3.6 s to ~0.08 s, allocating 98% less memory.

### Fixed

- **The library scan no longer transposes author and title for Readarr- and Calibre-organised libraries** (#754) — the scan inferred author and title by splitting the filename on `" - "`, assuming a `Title - Author` order, but Readarr's default naming and Calibre both write `Author - Title`, so every migrated book scanned in with the two swapped and downstream metadata matching failed. The scan now derives author and title from the `{Author}/{Book}/` folder hierarchy — unambiguous and dash-safe — and falls back to filename parsing only when no such hierarchy is present.

### Docs

- **Added wiki pages for troubleshooting, storage and hardlinking, and migrating from Readarr** (#755) — new guides under `docs/` covering common setup problems, how Bindery's storage layout and hardlinking behave, and the path for bringing an existing Readarr library across.

## [v1.13.1] — 2026-05-20

### Changed

- **The book detail page has been redesigned** (#751) — the scattered action links are now consistent buttons, the file path and per-format on-disk status are surfaced clearly, history entries are humanised, and deleting a book and its files moved to a dedicated "Danger zone" that requires an explicit checkbox confirmation. The media-type control sits with the indexer-search action it scopes.

## [v1.13.0] — 2026-05-20

### Added

- **Session-secret rotation** (#746) — an admin can rotate the session-signing secret from Settings → Security. Rotation keeps the previous secret valid for a one-rotation window so existing logins are not dropped immediately; rotating twice fully invalidates sessions signed with the old secret.

### Security

- **OIDC account-linking now requires a verified email and enforces `AllowedGroups`** (#736) — email-based linking of an OIDC identity to an existing account is rejected unless the IdP marks the email verified, closing an account-takeover vector; a provider's configured `AllowedGroups` is now actually enforced (login is refused when the user belongs to none of them).
- **CSRF protection can no longer be switched off with a bogus `?apikey=`** (#734) — the CSRF and `X-Requested-With` exemptions now key off a *verified* API-key authentication rather than the mere presence of an `apikey` parameter; the API key is also no longer accepted from the URL query string for state-changing requests.
- **`local-only` auth mode is no longer spoofable via `X-Forwarded-For`** (#737) — the client IP is resolved by walking the forwarded-for chain right-to-left and peeling trusted-proxy hops, so a client-supplied leftmost address can no longer masquerade as local; session cookies also carry a key-id to support secret rotation.
- **Indexer requests re-validate the resolved IP on every connection** (#738) — prevents a DNS-rebinding attack where an indexer hostname is repointed at an internal or cloud-metadata address after the initial create-time check.

### Fixed

- **Move-mode imports no longer destroy un-imported or still-seeding files** (#740) — a partial multi-file import no longer marks the download complete and deletes the source of a file that never landed; the import mode is resolved once per run; a cancelled directory copy no longer continues in the background and deletes its source; and move cleanup removes only the specific imported files instead of `RemoveAll`-ing a path that can be a shared torrent save root.
- **Imports interrupted mid-move are recovered instead of wedged forever** (#741) — downloads stuck in `importing` after a crash are swept back to a retryable state on startup; retries are idempotent and no longer double-add files; external-handoff imports use a dedicated state so they no longer cause a silent re-download loop; and a download whose source has vanished ends in a terminal blocked state instead of retrying invisibly forever.
- **Database migrations now run in a transaction** (#733) — a crash mid-migration no longer leaves partially-applied DDL; migration versions are keyed to the filename number (with a one-time reconciliation of older databases) so the numbering gap can no longer cause a migration to be skipped.
- **Scheduled jobs honour the shutdown signal and no longer leak goroutines** (#739) — cron jobs run under the process-lifecycle context so a graceful shutdown can cancel them; background goroutines are tracked and drained on stop; per-format pending releases are no longer dropped when a dual-format book's other format is grabbed first.
- **Indexer errors are classified and no longer wasted on the tier ladder** (#735) — a hard indexer rejection (auth failure, rate limit) on an early search tier now aborts immediately instead of retrying the same indexer through three more tiers; searches also have an overall timeout so one hung indexer cannot stall the whole query.
- **Background recommendation work stops on shutdown** (#732) — the recommendations goroutine is tied to the app lifecycle instead of an uncancellable context.
- **Deleting one format of a dual-format book no longer removes the other** (#742) — a format-scoped file delete only removes files of that format, not the same-named sibling of the other format.
- **Audiobookshelf settings are saved atomically** (#742) — the ABS config is written in a single transaction, so a mid-save failure no longer leaves a half-applied configuration.
- **Book status updates are validated** (#742) — the API rejects an unknown `status` value instead of writing it verbatim.
- **qBittorrent categories that save to a sub-folder of the download directory are accepted** (#744) — the category-path health check no longer requires the category's save path to equal the configured download directory; a path at or under it is valid. A category that saves entirely outside the download directory still warns.

### Changed

- **Removing a queue item keeps the downloaded files by default** (#742) — the queue "remove" action no longer deletes data and stops seeding unless you explicitly opt in via a checkbox in the confirmation dialog.
- **Non-admin users no longer see admin-only Settings sections** (#745) — the Settings page hides sections a non-admin account cannot use (download clients, indexers, auth, system, …); a non-admin still sees Appearance and can still change their own password. Backend authorization is unchanged.

## [v1.12.3] — 2026-05-20

### Security

- **API-key regeneration and OIDC provider management now require an admin account** (#717) — `POST /auth/apikey/regenerate` and `PUT /auth/oidc/providers` sat outside the admin-only route group, so any signed-in non-admin user could rewrite OIDC config or regenerate the API key and read it back — and the API key grants admin access. Both routes are now behind the admin check.
- **NZBGet downloads validate the NZB URL before fetching; SABnzbd API key redacted from errors** (#724) — the NZBGet NZB fetch now runs the same outbound-request (SSRF) policy check qBittorrent already applied, and the SABnzbd API key is no longer interpolated into error messages.
- **Session cookies and CSRF tokens fail closed on a missing or too-short signing secret** (#726) — a missing or under-32-byte session secret previously still produced "valid" HMAC tokens; signing and verification now reject it instead.

### Fixed

- **Saving OIDC provider settings no longer breaks login** (#716) — the provider reload ran on the already-cancelled request context, so discovery aborted and every provider was marked failed until a later retry. It now runs on a non-cancelled context.
- **Scheduled jobs no longer overlap themselves** (#718) — a slow run (e.g. `check-downloads` on slow storage) is now skipped rather than run concurrently with the next tick, which previously could double-import a download. Two swallowed scheduler errors are now surfaced.
- **The hardlink import mode is reachable again** (#719) — the same-filesystem check stat'd the not-yet-created destination path and always failed, so first imports silently fell back to copying (doubling disk use) even when the download and library shared a filesystem.
- **Import retries record a blocked status correctly** (#720) — the `import-failed → blocked` and `→ importing` state transitions were rejected, so a retry that hit a blocking condition burned the retry counter with no state change or recorded reason.
- **The search debug panel no longer shows fabricated relevance rejections** (#721) — the debug relevance path skipped the query-title normalization the live search applies, so titles with edition qualifiers like "(German Edition)" were reported as dropped when the real search kept them.
- **Removing or demoting an admin is now atomic** (#722) — two simultaneous requests could each pass the "is there another admin?" check and both proceed, leaving the instance with zero admins.
- **Approve, Fill series, and blocklist actions are now idempotent** (#723) — re-approving an already-imported Audiobookshelf review item no longer re-imports the book, "Fill series" no longer re-grabs books already downloading or downloaded, and blocklisting the same release twice no longer creates duplicate rows.
- **Download-client tracking and concurrency fixes** (#725) — a SABnzbd job accepted without a trackable NZO id now surfaces as an error instead of becoming silently untrackable; concurrent torrent adds to one Deluge client no longer cross-assign info-hashes; and a qBittorrent session-refresh retry now checks the response status instead of decoding an error page as data.

### Changed

- **Torrents reported as having missing files now show as errored in the queue** (#725) — the queue's error-state check now recognises qBittorrent's `missingFiles` status.

## [v1.12.2] — 2026-05-20

### Fixed

- **Searches for titles containing a common word no longer return zero results** (#699) — The tier-1 canned-feed detector accepted a Jackett/AudioBookBay category feed whenever *any* significant query word coincidentally appeared in *any* result. For a title like *Life Ascending*, the common word "life" matched an unrelated canned title, so the canned feed was accepted as a tier-1 hit and the relevance filter then rejected all of it — leaving the user with zero results instead of falling through to text-search tiers. The detector now requires every significant query word to appear in at least one result before accepting a tier-1 response.
- **Newznab/Torznab indexer errors are surfaced with their own code and description** (#698) — When an indexer returns a top-level `<error code="N" description="...">` element (bad API key, rate limit, site disabled, …) instead of an `<rss>` feed, Bindery reported the raw XML decoder error `expected element type <rss> but have <error>`. The Search debug panel now shows the indexer's actual error, e.g. `indexer error 500: Request limit reached`.

## [v1.12.1] — 2026-05-19

### Added

- **Hardcover edition metadata lookup** (#686) — Hardcover-backed books can now return edition-level metadata, including ISBNs, ASINs, publisher, format, page count, cover, language, and audiobook duration, using Hardcover's current editions query shape.

### Fixed

- **Torznab indexers returning canned category feeds no longer block searches** (#665, #687) — Tier-1 `t=book` results that ignore the title/author params (Jackett/AudioBookBay pattern) are detected by a keyword-relevance check; the search now falls through to text-search tiers instead of returning the same canned results for every query.
- **qBittorrent 5.x grabs no longer surface as failed when the add succeeded** (#690) — qBittorrent v5 returns a JSON body from `POST /api/v2/torrents/add` (`{"success_count":1,"added_torrent_ids":[...]}`) instead of the plaintext `Ok.` v4 returned; Bindery was treating the JSON as a failure. The client now accepts either shape and uses `added_torrent_ids[0]` as the infohash directly when available.
- **Prowlarr sync no longer imports unrelated, disabled, or non-search indexers** (#675) — Indexers disabled in Prowlarr, without `supportsSearch`, or with no ebook/audiobook categories are now skipped during sync. Previously every indexer Prowlarr returned was created in Bindery and would respawn if deleted.

## [v1.12.0] — 2026-05-18

### Added

- **Calibre metadata handoff** (#668) — Calibre pushes now carry Bindery book, author, edition, series, identifier, language, rating, description, and cover metadata through both `calibredb` and metadata-capable Bindery Bridge plugin syncs, with legacy plugin fallback preserved.

### Fixed

- **qBittorrent imports recover from mismatched container paths without re-downloading** (#641) — Download clients now support per-client path remaps, qBittorrent grabs are sent with the expected category save path, and Settings surfaces qBittorrent category path health warnings. Queue items stuck in `importFailed` can also be retried after fixing storage/path settings.
- **Calibre ID reuse** (#668) — Plugin sync no longer reuses a stored source-library Calibre ID when pushing into a different Calibre library.
- **Hardcover supplemental author sync restored** (#669) — Hardcover removed fields from its GraphQL `books` shape and blocked `_ilike` searches, causing author page enrichment to fail with validation or 403 errors. Bindery now uses Hardcover's current search operation and avoids the removed author-work fields.

## [v1.11.2] — 2026-05-17

### Fixed

- **DNB add-by-ISBN no longer fails with "book not found after author sync"** (#667) — The DNB flow used to create the author row, kick off a 15-second background reconcile, and poll for the book to appear from a second SRU query that DNB's index can't actually answer (the synthetic `dnb:gnd:*` / `dnb:author:*` IDs aren't queryable in DNB's bibliographic `num=` or `per=` indices). The poll always timed out, the user saw "try again shortly" forever, and the author row was orphaned on the way out. The AddBook handler now resolves the DNB record synchronously, inserts the book in the same flow as the author, and only falls back to the reconcile poll for non-DNB providers.
- **German titles no longer render with garbage characters around leading articles** (#667) — DNB's MARC 21 records wrap the non-sorting prefix (e.g. "Der", "Die", "Das") in U+0098 (MARC Non-Sorting Begin) and U+009C (MARC Non-Sorting End) C1 control characters. These were passing through `marcClean` untouched and showed up as box/replacement glyphs in titles like *Der war's* by Juli Zeh. The cleaner now strips both control characters at the front of the pipeline, matching the pattern in calibre-dnb's `remove_sorting_characters` helper.
- **Failed adds no longer leave orphan author rows** (#667) — When AddBook timed out polling for a book to land, the author row created earlier in the flow stuck around with zero books. The handler now tracks whether the book was actually written and rolls the author back if not, eliminating the "add author first, then add book" workaround.
- **DNB MARC 100/700 author selection now follows the `aut` relator** — Author resolution falls back through MARC 100 → 700 with `$4 aut` (or German `$e Verfasser` / `Verfasser*in`) → first 700 with any name. Translations and audiobooks where the original author sits only in 700 (e.g. Harry Potter audiobooks where J.K. Rowling is cataloged as `ctb`) now resolve to a usable author instead of dropping the record.
- **Synthetic DNB author IDs short-circuit `GetAuthorWorks`** — When the foreign ID is `dnb:gnd:*` or `dnb:author:*` (a Bindery-internal synthetic, not addressable in DNB's SRU index), the works lookup now returns empty immediately instead of issuing a 15-second nonsense query against the `num=` and `per=` indices.

### Added

- **MVB cover image fallback for DNB-sourced books** — German books that aren't in OpenLibrary or Hardcover were ending up with no cover image. Bindery now consults DNB's public MVB cover service (`portal.dnb.de/opac/mvb/cover?isbn=<X>`) as a fallback when no other provider returned an image URL. Cheap HEAD probe to verify the service is actually serving an image for that ISBN before persisting the URL. Pattern lifted from calibre-dnb (#667).

### Internal

- **Live DNB SRU regression test** — New `BINDERY_LIVE=1` integration test exercises 25 zippoking-reported ISBNs (the three from #284 plus the twelve in #667/#608) plus 50 deterministic random samples against the real DNB SRU endpoint. Asserts each lookup returns a title with no U+0098/U+009C residue and a resolvable author. Wired into a separate nightly workflow so DNB upstream flakiness doesn't block PR merges.
- **`stripMARCNonSortingBrackets` + `extractAuthor` helpers** — DNB title cleanup and author resolution are now isolated for direct unit testing.
- **AddBook orphan-cleanup deferral** — A pointer-tracked flag arms a `defer` that deletes the author row if the book write didn't land. Idempotent: skipped when the author already had books before this AddBook call.

## [v1.11.1] — 2026-05-15

### Fixed

- **NZB Finder indexer searches no longer return Cloudflare 403** — nzbfinder.ws runs a WAF rule that case-sensitively rejects any User-Agent containing the substring `Bindery`. Every search through that indexer was returning HTTP 403 "Attention Required" instead of results. Bindery now sends a single canonical, lowercase User-Agent (`bindery/<version> (<os>)`) on every outbound HTTP request, matching the convention used by Sonarr/Radarr/Prowlarr. Other indexers (NZBGeek, NZB Planet) are unaffected.

### Internal

- **Single source of truth for User-Agent** — New `internal/useragent` package centralises the outbound User-Agent. Twelve HTTP clients (newznab indexers, OpenLibrary, DNB, Hardcover, Audnex, Audible, Google Books, Discord notifier, image proxy, Prowlarr, telemetry, Audiobookshelf, Grimmory) now share one identity. Previously some sent `Bindery/0.1`, some `Bindery/1.0`, three sent Go's default `Go-http-client/2.0` (which is on Cloudflare's bot blocklist), and existing helpers used their own format. All converged on `bindery/<version> (<os>)`.

## [v1.11.0] — 2026-05-14

### Added

- **SSO-only mode and OIDC account controls** (#654) — Three new env vars: `BINDERY_LOCAL_AUTH_ENABLED` (default `true`) disables password login entirely when set to `false`; `BINDERY_OIDC_AUTO_PROVISION` (default `true`) prevents automatic account creation for unknown OIDC users when set to `false`; `BINDERY_OIDC_EMAIL_LINK` (default `false`) links an unknown OIDC identity to an existing account by email on first login. Deployments that don't set these vars are unaffected.

- **Indexer priority now applied to release scoring** (#656) — The `Priority` field on indexers (Settings → Indexers) previously had no effect. It now adds directly to the composite release score, so a higher-priority indexer wins ties and can outweigh small quality differences. Set Usenet indexers to a higher priority than torrent indexers to prefer Usenet when both have matching releases.

### Fixed

- **Recommendations now return empty genre arrays instead of null** — Recommendation storage normalizes missing and legacy `null` genre values to `[]`, keeping API responses consistent for clients.

- **qBittorrent Docker hostname fix** (#640) — Bindery now fetches .torrent content itself and submits it to qBittorrent via multipart/form-data. Fixes setups where qBittorrent runs in a separate container and cannot resolve the Prowlarr/indexer internal hostname (e.g. `prowlarr:9696`). No config change needed.

- **NZBGet Docker hostname fix** (#531) — Bindery now fetches the NZB file itself and sends it to NZBGet as base64-encoded content. Same fix as #640 for Usenet: NZBGet in a separate container no longer needs to reach the indexer URL directly. No config change needed.

- **Non-standard indexer category passthrough** (#636) — Category IDs outside the standard 7xxx/3xxx ranges (e.g. MaM-style IDs) are now forwarded to the indexer unchanged instead of being dropped. Fixes searches returning no results on niche indexers with custom category numbering.

- **Backup delete button in Settings → General → Backup** (#638) — Backup entries now have a delete button in the UI. Previously backups could only be removed by SSHing into the container.

- **Log level toggle now propagates to log viewer** (#651) — Switching to DEBUG in Settings → System now immediately affects the log viewer. Previously only `BINDERY_LOG_LEVEL=debug` at startup had any visible effect.

- **Discover page no longer crashes on books with no genre metadata** (#645) — The Recommendations page handles `genres: null` from the API gracefully instead of throwing a runtime render error.

### Improved

- **Book list query performance** (#648) — Book list queries now use a single CTE JOIN instead of correlated subqueries (previously 2N SQLite queries for N books). Noticeable speedup for libraries with more than a few hundred books.

- **Download client form shows inline errors on save failure** (#649) — The Save button in the Add/Edit Download Client forms now displays an inline error message when the API call fails, rather than appearing to do nothing.

### Internal

- **Test coverage for grimmory and hardcoverlistsyncer** (#652) — Added unit and HTTP test coverage for `internal/grimmory` (0% → 93%) and `internal/hardcoverlistsyncer` (13% → 20%).

### Chores

- **Local check cleanup** — Restored downloader lint compliance and aligned the WantedPage test with optimistic unmonitor behavior so local checks can pass.

## [v1.10.0] — 2026-05-13

### Added

- **Discord stats voice channels** — A k8s CronJob in `deploy/discord-stats.yaml` updates three Discord voice channels every 10 minutes with live active-install count, latest released version, and GitHub star count. Powered by a new `/stats.json` JSON endpoint on the telemetry server. Setup steps in `deploy/README.md`.
- **Live ISBN provider integration tests** — New torture-corpus tests (`BINDERY_INTEGRATION=1` to run) exercise the aggregator's ISBN fallback chain against real DNB / OpenLibrary / GoogleBooks / Hardcover endpoints. Useful for catching upstream schema drift; skipped by default to avoid CI flake. Extracted from #515.
- **Library scan status auto-refresh** (#544) — The Settings page now polls the scan endpoint every 2 s while a scan is running and shows an inline progress banner. No more F5 to see when the scan finishes.
- **Expanded frontend test coverage** (#485) — Auth flow, Login page, History page, Queue page, WantedPage, full SettingsPage suite, and MSW-based API client tests; previously untested paths now have coverage.
- **Auto-bump `bindery-ping` on release** (#557) — The goreleaser CI job now updates the `LATEST_VERSION` env var on the bindery-ping deployment automatically, eliminating the manual `auto/prod-deploy-X.Y.Z` step after each release. Requires `HETZ1_KUBECONFIG` secret.

### Fixed

- **Stale ABS-sourced author aliases are now cleaned up post-import** — When an audiobook import recorded a co-author (or a different-named primary author) as an alias, the alias would stick around tagged `SourceOLID="abs"` even when it no longer matched the canonical author. The importer now sweeps these at the end of each run and drops aliases that don't fuzzy-match the canonical name. Also prevents pen-name corruption by requiring secondary-author aliases recorded during import to fuzzy-match the canonical author. Extracted from #515.
- **Manual alias deletion** — New `DELETE /api/v1/author/{id}/aliases/{aliasID}` endpoint lets the UI / API clients remove specific aliases without merging the whole author. Scoped to (authorID, aliasID) pair; returns 404 if the alias isn't on that author so cross-author tampering can't happen. Extracted from #515.
- **Download client error messages now suggest the root cause** (#621) — Test-connection failures are classified by errno: DNS resolution failure suggests checking container networking; connection refused suggests the service isn't running on that port; timeouts suggest a firewall or proxy.
- **WantedPage optimistic updates now roll back on failure** (#551) — If the API call fails after a monitored/wanted toggle, the UI reverts to the previous state and shows a toast ("Couldn't update — reverted. Retry?") instead of silently diverging from server state.
- **Graceful SIGTERM/SIGINT shutdown** (#559) — The server now drains in-flight requests before exiting. Grace period defaults to 30 s and is configurable via `BINDERY_SHUTDOWN_GRACE`. `kubectl rollout restart` no longer drops in-flight requests.

## [v1.9.3] — 2026-05-12

### Fixed

- **DNB add-by-ISBN no longer fails for German books with no OpenLibrary coverage** (#608) — DNB results now carry a stable author ForeignID (GND from MARC 100 $0 when present, otherwise a synthetic `dnb:author:<name-slug>`). When a canonical author (OpenLibrary / Hardcover) later arrives for the same SortName, the synthetic DNB row is migrated in place so the user keeps a single author record. Previously the add flow returned 422 "Author metadata unavailable" whenever no canonical provider had the ISBN.

## [v1.9.2] — 2026-05-12

### Fixed

- **Backup creation no longer crashes the Settings page** (#594) — The backup endpoint returns `{name, size, modTime}` but the frontend was typed for `{filename}` and rendered the raw object, triggering React error #31 ("Objects are not valid as a React child"). Frontend types and the backup list rendering now match the API response shape.

- **Hardcover list sync** (#562) — `GetListBooks` now queries the plural `lists(where: {id: {_eq: $id}}, limit: 1)` root field. The singular `list(id:)` query was rejected by Hardcover's GraphQL schema ("field 'list' not found in type: 'query_root'"), breaking every custom list Sync Now since v1.1.0.

- **Proxy auth `/api/v1/auth/status`** (#560) — Proxy identity resolution now runs before the allow-unauth fast-path. Previously `/auth/status` was on the allow-unauth list and bypassed the proxy header lookup entirely, so SSO-authed users were never let past the login screen. Trusted-proxy CIDR gating preserved.

- **Newznab / Prowlarr-proxy NZBGet rejections** (#531) — Download enclosure URLs are now signed with the indexer apikey when the URL host matches the indexer's own host. Prowlarr-proxied Usenet downloads were arriving at NZBGet as empty content ("Document is empty" / `id 0`) because the apikey was stripped at client construction but never re-applied to download URLs. Apikey is only appended for same-host URLs to avoid leaking it to third-party redirect targets.

- **Author matching false positives** (#563) — Indexer release filter and library scanner now require all significant author tokens (>=3 chars) to match at word boundaries, not just surname substring. Releases like `Adam.Reid.Book.epub` will no longer be auto-imported under monitored author "Rachel Reid". Initials (1-2 char tokens) are treated as optional, so "George R. R. Martin" still matches "George Martin". ABS path was already safe (Jaro-Winkler whole-string match).

- **Enhanced Hardcover series controls no longer hidden by default** (#596) — The deployment-wide `BINDERY_ENHANCED_HARDCOVER_API` flag now defaults to enabled. The saved Hardcover token and **Settings → General** admin toggle remain as the normal feature gates. Operators can still set the flag to `false` to disable the feature for an entire deployment.

- **ABS review search results are scrollable and keep book-author links intact** (#599) — No-match review author/book searches now show up to 10 scrollable matches instead of truncating after three, and selecting a book result auto-links its author before resolving the book when the review item does not already have a resolved author.

### Added

- **Download queue surfaces timestamps** (#543, #592) — Each queue entry now shows the most recent meaningful event (imported / completed / grabbed / added) as a relative time, with the absolute UTC timestamp on hover.

## [v1.9.1] — 2026-05-11

### Fixed

- **Author list no longer hides authors after user re-creation** — The "author already exists" duplicate check was global-scoped while the author list filtered by `owner_user_id`. Authors whose `owner_user_id` pointed to a deleted or re-created user were permanently invisible in the list but blocked re-addition. The check is now user-scoped so it agrees with what the list shows. A new migration (039) resets orphaned `owner_user_id` values to NULL so those authors become visible to all users immediately on upgrade.

- **Canonical author name search now scoped to current user** — The name-deduplication path during author creation previously searched the global author pool, which could conflict with authors belonging to other users in multi-user setups.

### Chores

- **Frontend regression coverage expanded** (#427) — Added MSW-backed tests for login, CSRF handling, auth state/guards, Book Detail search/grab flows, and Wanted page search/grab/bulk actions.

## [v1.9.0] — 2026-05-11

### Added

- **Book metadata can be remapped from the Book Detail page** (#590) — Books with ABS or stale metadata now show an **Improve metadata** action that searches upstream providers or accepts a direct provider ID. New `POST /api/v1/book/{id}/map` applies the upstream title, cover, language, ratings, genres, and provider ID while preserving local status, files, media type, ASIN, narrator, selected edition, and exclusion state.

- **Calibre-Web-Automated (CWA) ingest** (#417) — A new
  **Settings → General → Calibre-Web-Automated (CWA)** field configures a
  shared ingest folder. When set, every successful ebook import is also
  copied into that folder so a sibling
  [CWA](https://github.com/crocodilestick/Calibre-Web-Automated) container
  can auto-ingest it. Bindery keeps its own copy and never moves the file,
  so a misconfigured CWA can't take bindery's library with it. No Calibre
  runtime dependency is added to the bindery container — only the file
  drop is in scope. Audiobook imports are unaffected since CWA is built
  around ebook libraries.

- **Prowlarr search timeout is now configurable** (#576) — The Prowlarr indexer
  search timeout has been raised from 15 s to 60 s and can be adjusted in
  **Settings → Indexers → Prowlarr → Search timeout**. Slow usenet indexers
  no longer time out on the first query.

### Fixed

**Importer / download clients**

- **qBittorrent SavePath fallback caused incorrect imports** (#574) — When
  qBittorrent's `content_path` field was absent or empty, the importer
  fell back to `SavePath` (the shared download root) and could match
  unrelated files or walk directories it should not touch. The importer
  now uses `content_path` exclusively and aborts cleanly when it is missing.
- **Default import mode changed from `move` to `hardlink`/`copy`** (#577) —
  The out-of-box default was `move`, which silently broke torrent/usenet
  seeding immediately after import. Bindery now defaults to `hardlink` when
  source and destination are on the same filesystem (free, preserves seeding)
  or `copy` when they are cross-device. **Upgrade note**: migration 038
  clears the implicit `move` default written at install time; users who
  explicitly set an import mode in Settings are not affected.
- **Downloads stuck in `importFailed` are now retried automatically** (#578)
  — Previously, a download that failed during import was permanently orphaned.
  Bindery now retries up to three times before leaving it for manual
  intervention. Retry count is persisted via migration 037.
- **CheckDownloads now polls all enabled download clients** (#572) — Only
  the highest-priority client was polled for status updates. Secondary
  clients (e.g. a second qBittorrent instance or a fallover) were silently
  ignored. All enabled clients are now iterated in priority order.
- **Bulk-grab torrent dedup race condition fixed** (#573) — Grabbing multiple
  releases simultaneously could assign the same `torrent_id` to two
  downloads, breaking per-download tracking. `AddTorrent` is now serialised.

**Auth**

- **API key authentication now grants admin role** (#582) — Requests
  authenticated via API key successfully verified the key but did not set
  the admin role in the request context, causing `RequireAdmin`-protected
  endpoints to return 403. The role is now correctly propagated.
- **Auth endpoints no longer require `X-Requested-With` header** (#575) —
  The login endpoint enforced `X-Requested-With: bindery-ui`, blocking
  non-browser clients (curl, mobile apps, integrations). Auth endpoints are
  now exempt; programmatic clients should use API key auth instead of cookie
  sessions.

**AudioBookShelf (ABS)**

- **ABS library is rescanned after audiobook import** (#581) — Bindery now
  triggers `POST /api/v2/libraries/:id/scan` after a successful audiobook
  import so the file appears in ABS immediately rather than on its next
  scheduled scan.
- **Move-mode audiobook imports no longer appear MISSING in ABS** (#583) —
  The ABS rescan after import updates ABS's path knowledge, resolving the
  MISSING status that appeared when the import moved the file.
- **History events include format for dual-format books** (#584) — `bookImported`
  events for books with `media_type='both'` now record which format (ebook
  or audiobook) was imported, making the History page unambiguous.

**Metadata**

- **ISBN lookups now canonicalise provider-native matches** (#590) — ISBN
  searches normalise ISBN input, consult configured metadata enrichers, and
  conservatively relink provider-native results back to canonical OpenLibrary
  works when the author/title evidence is unambiguous. This improves
  translated and edition-specific matches while avoiding plausible
  wrong-title fallbacks.
- **Audiobook ASIN enrichment can relink to upstream metadata** (#590) —
  Enriching an audiobook now uses Audnex ASIN metadata to find a safe
  canonical upstream match, so ABS/imported audiobook rows can gain better
  titles, covers, language, search metadata, and OpenLibrary IDs while
  keeping audiobook-specific fields intact.
- **ABS imports no longer trust stale secondary-author aliases or provenance**
  (#590) — Existing ABS author provenance and aliases are reused only when
  they still match the local author, preventing secondary-author names from
  corrupting future imports.
- **Direct book adds preserve series links** (#590) — Adding a book directly
  no longer drops existing series associations during metadata
  canonicalization.
- **Google Books provider settings are respected at startup** (#590) —
  Bindery now prefers the UI-managed Google Books API key, keeps legacy
  setting fallback for existing installs, and treats a deliberately cleared
  UI setting as disabled.

## [v1.8.1] — 2026-05-09

### Fixed

- **DNB search results couldn't be added to the wanted list** (#545, #561)
  — DNB bib records expose author *names* but not author IDs, so every
  result had the Add button greyed out with a misleading "try a more
  specific search" hint. The fix extracts ISBN(s) from MARC 020 in DNB
  records and adds a cross-provider author resolver: when a search result
  lacks a foreign author ID, the backend looks up the ISBN in OpenLibrary
  and rewrites the request to use OL's canonical author/book identity.
  Books that resolve end up under their OpenLibrary record (with OL's
  title and metadata); books with no OL match return a clear "add the
  author manually first" error instead of silently failing.
- **Telemetry chart hides the freshly cut release** (#546) — `/stats`
  truncated the version chart to top-8 by count, so a brand-new release
  with one or two installs disappeared into `(other)` until it
  organically out-ranked older versions (sometimes weeks). The chart now
  pins the configured `LATEST_VERSION` into the visible region and
  annotates it `(latest)` so newly cut releases are immediately visible.
- **Transmission retry path silently used corrupted bodies** (#558) — On
  retry against the Transmission RPC endpoint, `io.ReadAll` errors were
  dropped and an empty / partial slice was used as the response body.
  Errors now propagate via `fmt.Errorf("transmission: read retry body:
  %w", err)` so a torn body fails loudly instead of producing
  garbage-decoded torrent state.
- **`refreshBookStatus` could zero a user's file paths on transient DB
  errors** (#558) — `Scan` errors on the `book_files` lookup were dropped
  via `_ = QueryRowContext(...).Scan(&path)`, so any error other than
  `sql.ErrNoRows` (lock timeout, corruption, connection drop) wrote `""`
  back to `book.EbookFilePath` / `book.AudiobookFilePath`. Now distinguishes
  `sql.ErrNoRows` (legitimate empty path) from real failures via
  `errors.Is`, returning the wrapped error in the latter case.
- **Non-ASCII filenames mangled in Content-Disposition** (#558) — Library
  file downloads now emit RFC 5987 `filename*=UTF-8''<percent-encoded>`
  alongside the legacy `filename="..."` parameter, so titles with German /
  Cyrillic / CJK characters land on disk with the correct name instead of
  being rewritten to a quoted-printable mojibake form.
- **Frontend timer leaks on unmount** (#556) — `AuthSettings`'s
  copy-to-clipboard "copied" badge and `DiscoverPage`'s toast clear-out
  used bare `setTimeout` calls that fired against unmounted components,
  producing React's "can't update state on unmounted component" warning.
  Both are now `useEffect`-driven with `clearTimeout` cleanup.

### Changed

- **Log persistence shutdown is now graceful** (#558) — `LogHandler.Stop()`
  closes the in-memory channel and waits (bounded by a 5s context) for
  the drain goroutine to flush any in-flight log entries before the
  process exits. Wired into `cmd/bindery/main.go` as a deferred call.
  Previously the goroutine leaked for the lifetime of the process and
  any buffered entries were lost on shutdown. Note: the `defer` only
  fires on clean main-return paths, not on signal-driven termination —
  full benefit requires #559 (signal-based graceful shutdown), tracked
  separately.
- **Several previously-dropped errors now surface in the log stream**
  (#558) — `slog.Warn` calls were added to `internal/api/imageproxy.go`
  (response write), `internal/api/auth_oidc.go` (provider parse),
  `internal/api/authors.go` (batch dedup updates),
  `internal/db/recommendations.go` (genres marshal), and
  `internal/prowlarr/syncer.go` (`SetLastSyncAt` after a successful
  sync). Behaviour is unchanged; visibility is not.

### Security

- **API key compared with `subtle.ConstantTimeCompare` instead of `==`**
  (#555) — Both the main HTTP middleware (`internal/auth/middleware.go`)
  and the OPDS auth path (`internal/api/opds_auth.go`) used variable-time
  string equality on the API key, leaking enough timing information to
  enable a remote byte-by-byte recovery attack against a sufficiently
  determined attacker. Both sites now use `subtle.ConstantTimeCompare`,
  with the existing empty-key short-circuit preserved so empty submissions
  don't telegraph the real key's length.
- **GitHub Actions in `ping-server.yml` pinned to commit SHAs** (#555) —
  Five actions (`actions/checkout`, `docker/login-action`,
  `docker/setup-qemu-action`, `docker/setup-buildx-action`,
  `docker/build-push-action`) were pinned by tag, leaving the workflow
  vulnerable to a tag-rotation supply-chain attack. All are now pinned to
  the same commit SHAs already in use in `ci.yml`.
- **`cmd/telemetry-server/Dockerfile` base images pinned by digest**
  (#555) — `golang:1.25-alpine` and `alpine:3.21` are now pinned to their
  content-addressable digests so a tag rotation can't silently swap the
  base image during the next ping-server build.
- **Dedicated `*http.Client` for telemetry pings** (#555) — Replaces
  `http.DefaultClient` for the once-per-day telemetry ping path with a
  package-local client carrying its own timeout. The 10s context deadline
  was already in place, but the dedicated client guards against unrelated
  code mutating `DefaultClient` and reaching into the ping path.

## [v1.8.0] — 2026-05-09

### Added

- **Manual Hardcover list sync** (#536) — A "Sync now" button on each Hardcover list row in **Settings → Import** triggers an immediate sync without waiting for the 24-hour scheduler tick. New `POST /api/v1/importlist/{id}/sync` endpoint backs the button and is scriptable from the CLI.
- **Top-level React ErrorBoundary** (#530, #539) — Render-time errors no longer blank the entire page. A friendly fallback card with **Reload** / **Show details** buttons sits outside the router, so even router-level throws are caught.

### Fixed

- **Prowlarr add-form silently swallowed errors** (#536) — Failed adds now surface a red error message under the form instead of failing silently. Sync errors (separate from the add itself) are non-fatal so a successful Prowlarr connection is not rolled back by a transient sync failure.
- **Telemetry only pings for semver release versions** (#527) — Dev / branch builds no longer ping the telemetry endpoint, keeping ingestion data clean.

### Security

- **Go 1.26.3 stdlib security release** (#540) — Bumps the runtime image from `golang:1.26.2-alpine` to `1.26.3-alpine`, picking up patches for CVE-2026-42499, CVE-2026-39820, CVE-2026-39823, CVE-2026-39825, CVE-2026-39826, CVE-2026-33811, CVE-2026-33814, CVE-2026-39836. Container Scan (Trivy CRITICAL+HIGH) returns to green on this release.
- **`security-events: write` scoped to SARIF-uploading jobs only** (#538) — Removed the over-broad workflow-level write permission from `security.yml`; only the four jobs that actually call `github/codeql-action/upload-sarif` (`sast-go`, `sast-frontend`, `secrets-scan`, `iac-scan`) hold the scope. OpenSSF Scorecard `Token-Permissions` improvement.
- **Dependabot security updates enabled** at the repo level — weekly version updates already shipped via `dependabot.yml`; this turns on the security advisory channel for transitive vulns.

### Docs

- **Indexer / Prowlarr URL guidance** (#536) — New section in `docs/DEPLOYMENT.md` explaining why loopback URLs (`127.0.0.1`, `localhost`) are rejected by the SSRF policy and what alternatives to use (docker service name, LAN IP, or container IP).
- **README pruned to ~280 lines** — Hero, Why Bindery, Features (compressed), Quick Start, signposts. Implementation detail moved to new `docs/ARCHITECTURE.md` and `docs/API.md`. SECURITY.md supported-versions table bumped to 1.8.x.
- **Unraid Community Apps template** (#526) — Template added to repo; selfhosters marketplace listing pending review.

### Chores

- **Series Codecov follow-up coverage** (#475) — Targeted tests for series API edge cases, repository hydration and linking behavior, metadata aggregator series catalog fallback/cache behavior, and series matching helpers after gaps were noticed in the Codecov report for PR #459.
- **Hero screenshots refreshed** (#528, #529).

## [v1.7.0] — 2026-05-08

### Added

- **Subpath / reverse-proxy hosting** (`BINDERY_URL_BASE`) (#516) — New env var strips incoming URLs to their path, validates the prefix, injects `<base href>` and `window.__BINDERY_BASE__` into the served `index.html` at runtime, and mounts all chi routes under the prefix. Vite is built with `base: './'` for relative asset URLs; the React router and API client read `window.__BINDERY_BASE__` for the basename and prefix.
- **Re-bind book to a different metadata record** (#519) — New `POST /api/v1/books/{id}/rebind` endpoint accepts a provider (`openlibrary` | `hardcover`) and a foreign ID, validates the upstream record, warns on author mismatch (with `force_required:true`) unless `force:true` is sent, clears and re-links series membership, and writes a `bookRebound` audit entry to History. A Re-bind dialog is accessible from the Book Detail page.
- **DNB as primary metadata provider** (#521) — DNB's SRU endpoint now supports `GetAuthorWorks()` and can be selected as the primary provider in **Settings → General → Metadata Provider**. OpenLibrary remains the default. When DNB is primary, roles are swapped at startup.

### Fixed

- **Library scan: sort-suffix folders now reconciled** (#517) — Files stored in librarian sort-suffix form (`Title, The` / `Title, A`) are correctly matched during library scan. A new `normalizeTitle()` helper handles `, the` / `, an` / `, a` comma-suffix inversion and is applied in both `titleMatch()` and the JW-similarity comparison in `ScanLibrary()`.
- **Hardcover lists fetch repaired** (#518) — Three response structs (`GetUserWishlist`, `GetUserLists`, `getShelfBooks`) incorrectly expected a single `Me` object; they now unmarshal `Me` as an array with a `len==0` guard, matching the actual API response shape.

### Changed / Refactored

- **Post-create wanted-book logic centralised** (#520) — `handleNewWantedBook()` extracted in `internal/api/authors.go` and called from `FetchAuthorBooks`, `RecommendationHandler.Add`, and `SeriesHandler.ensureHardcoverCatalogBook`, eliminating three copies of the same file-exists / auto-search dance.

## [v1.6.0] — 2026-05-07

### Fixed

- **Authenticated users no longer see the login page** (#493) — Visiting `/login` (or `/setup`) with a valid session now redirects to `/` instead of rendering a stale auth screen. New `PublicOnlyRoute` guard wraps both routes; mirrors the existing `AuthGuard` loading behaviour so there is no flash on refresh, and routes back to `/setup` if setup is still required.
- **Discover page now uses the wrapping-grid layout** (#347) — Recommendation rows now match Books and Authors with `grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4` so all cards are visible at a glance, instead of horizontally scrolling. Section headings, grouping logic, the cold-start note, and empty/disabled states are unchanged.

## [v1.5.0] — 2026-05-07

### Added

- **Audiobookshelf (ABS) integration is now always enabled** — The `BINDERY_ABS_ENABLED` feature flag has been removed. ABS configuration, import, review, and conflict endpoints are unconditionally available; the ABS tab always appears in Settings for admins. Existing installs that relied on the flag being `false` to hide the UI will now see the tab; those that set it to `true` can simply remove the env var.
- **Grimmory integration** — New **Settings → Grimmory** tab for configuring a [Grimmory](https://grimmory.org/) self-hosted digital library. Stores server URL and API key; a Test Connection button pings `GET /api/status`. New `GET/PUT /api/v1/grimmory/config` and `POST /api/v1/grimmory/test` endpoints back the UI. API paths are based on current Grimmory OpenAPI docs and will be updated as the API stabilises.
- **Separate audiobook download watch folder** — New `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` env var (also exposed in **Settings → General**). When set, the scanner uses this directory for audiobook downloads and falls back to `BINDERY_DOWNLOAD_DIR` for ebooks. Unset by default — fully backwards-compatible. Mirrors the existing `BINDERY_AUDIOBOOK_DIR` split on the library side.

### Fixed

- **`.opus` added to recognised book and audio-tag extensions** — Opus-encoded audiobook files are now detected and tagged correctly.
- **Hardcover built-in shelves surface in the list picker** — "Want to Read", "Currently Reading", and "Read" now appear alongside user-created lists when adding a book to a Hardcover shelf.
- **Telemetry security hardening** (#482) — Three fixes to the optional telemetry server: redirect target now uses `CANONICAL_HOST` instead of the user-controlled `Host` header (open redirect); rate-limiter key now strips the port from `RemoteAddr` so connections from the same IP share one bucket; `BINDERY_TELEMETRY_DISABLED=true` is now checked before the settings table on first boot so the opt-out takes effect immediately.

## [v1.4.5] — 2026-05-06

### Added

- **Book Detail page now exposes the media-type selector** — Imported and downloaded books can now be flipped between ebook / audiobook / both directly from the Book Detail page. Previously this was only available on the Wanted page, so once a book progressed past wanted there was no UI path to add the second format short of deleting and re-adding the author.
- **Author Detail page now has an Edit modal** — Quality profile, metadata profile, and root folder are now editable from the Author Detail page; previously the only way to change them was to delete the author and re-add. Triggered from a new Edit button next to the existing actions.

### Fixed

- **Mobile session cookie no longer evicted on app switch** — Login without "Remember me" now sets `Max-Age` on the session cookie (was previously a browser-session cookie with no expiry hint), so iOS Safari and Android Chrome don't drop it when the tab is backgrounded or the OS suspends the browser process. The 12-hour / 30-day durations were already encoded in `auth.SessionDurationShort` / `auth.SessionDuration` but never reached the wire on the short branch.

## [v1.4.4] — 2026-05-06

### Fixed

- **Manual series mutations require admin role** (#468) — Authenticated non-admin users can no longer create, update, monitor, delete, fill, or link series to Hardcover. Read-only series endpoints remain available to authenticated users.
- **Library file matching now respects media type** (#488, #454) — `FindExisting` now picks the right library root based on the book's `media_type` instead of always walking `BINDERY_LIBRARY_DIR` first. Audiobook book rows are matched against `BINDERY_AUDIOBOOK_DIR` (with fallback to `BINDERY_LIBRARY_DIR` when the audiobook root is unset), ebook rows are matched against `BINDERY_LIBRARY_DIR`, and dual-format / unspecified rows preserve the prior behaviour of walking both roots with the ebook library first. Previously a same-titled ebook in `libraryDir` could be mis-attributed to an audiobook entry on rescan, and authors filtered to "audiobooks only" still had file lookups walk the ebook root.
- **Edition dedup now strips subtitles** (#458) — Author sync no longer creates duplicate rows when OpenLibrary returns the same work twice with different subtitle handling — typically the audiobook drops the post-colon subtitle while the ebook keeps it (e.g. *Carl's Doomsday Scenario* vs. *Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2*). `NormalizeTitleForDedup` now drops a `: subtitle` tail when the colon is followed by whitespace, so both editions collapse to the same key and the existing v1.3.1 dual-format upgrade path is taken instead of inserting a duplicate.
- **Series title inputs now have an API length limit** (#469) — Manual series creation and title updates now reject titles longer than 500 bytes before writing to SQLite, preventing oversized titles from being stored through the HTTP API.
- **Hardcover GraphQL success responses are bounded** (#470) — Successful Hardcover responses are now read through an 8 MiB cap so a misbehaving upstream cannot force unbounded memory growth before JSON parsing.
- **Add-author search no longer hides valid author results when the query matches a book title** — Results whose name exactly matches a known book title and whose disambiguation points to that book's real author are now placed behind a reveal button rather than silently dropped.

## [v1.4.3] — 2026-05-06

### Fixed

- **Discover now shows recommendations for all libraries** — The `ratingsCount < 50` hard filter was silently dropping every candidate from the most useful recommendation sources: monitored-author books, series continuations, and genre-popular picks from OpenLibrary. These sources already carry an implicit quality signal (the user chose to monitor the author; the book is part of a series they're reading; OL's subject curators selected it), so gating them on OL's sparse ratings data was wrong. Only serendipity and list-cross candidates — which come from broader, uncurated pools — now require a ratings signal. The filter is unchanged for those types.

## [v1.4.2] — 2026-05-06

### Fixed

- **Discover works for libraries migrated from pre-v1.4.1** — Author refresh now updates `ratings_count` and `average_rating` on books that already exist in the database. Previously, `FetchAuthorBooks` skipped all processing for existing books (by foreign ID or deduplicated title), so libraries that had synced authors before v1.4.1 kept `ratings_count=0` on every book even after an upgrade and refresh. The recommender's hard filter then dropped all candidates, leaving Discover empty. A refresh-metadata run now back-fills ratings from OpenLibrary for any book where we have better data.

## [v1.4.1] — 2026-05-06

### Fixed

- **Discover page no longer shows "not enough data" for populated libraries** — `GetAuthorWorks` was not requesting `ratings_count`/`ratings_average` from the OpenLibrary search API, so every book stored via a monitored-author sync had `RatingsCount=0`. The recommender's hard filter drops candidates with fewer than 50 ratings, silently eliminating all author-new and series candidates regardless of library size. The search query now fetches those fields and the enrichment merge propagates them to the stored book rows.

## [v1.4.0] — 2026-05-05

### Added

- **Enhanced series data via Hardcover** — Series can now be managed manually, linked to Hardcover series, and compared against the Hardcover catalog. The Series page shows present, missing, local-only, and uncertain books; missing catalog entries can be filled all at once or one row at a time, creating wanted/monitored book rows and queuing searches. The enhanced controls are gated behind `BINDERY_ENHANCED_HARDCOVER_API`, a saved Hardcover API token, and the admin setting in **Settings -> General**.
- **Prometheus `/metrics` endpoint** (#429) — Bindery now exposes `bindery_http_*` (request rate / latency by route template), `bindery_scheduler_*` (job-run counts and durations), and `bindery_build_info` alongside the standard `go_*` runtime and `process_*` collectors. Mounted at `/metrics` outside the `/api` auth chain so Prometheus scrapes work without session cookies; restrict access via NetworkPolicy / firewall / reverse-proxy ACL. Background jobs now also recover from panics so a single buggy job no longer tears down the scheduler goroutine.
- **OIDC settings UI gains a "Test discovery" button** (#460) — Next to the Issuer URL field on the **Add provider** form, a Test button hits the IdP's `/.well-known/openid-configuration` server-side and renders the result inline: discovered authorize/token endpoints + supported scopes on success, the raw error (DNS, TLS, 404, JSON parse) on failure. Critically, surfaces **issuer mismatch** when the discovered issuer differs from the entered URL — the silent killer for Authentik per-provider mode and Keycloak realm paths. New `POST /api/v1/auth/oidc/test-discovery` endpoint backs the button.
- **OIDC settings UI shows a live callback URL preview** (#460) — As you type the provider id in **Settings → Security → OIDC Providers → Add provider**, the form renders the exact redirect URI Bindery will register with the IdP, with a copy-to-clipboard button. New `GET /api/v1/auth/oidc/redirect-base` endpoint returns `{ base, callback_path }` for the current request — eliminates the most common setup mistake (registering a URL that doesn't match what Bindery actually sends).

### Changed

- **OIDC redirect base URL is now optional behind a trusted proxy** (#460) — `BINDERY_OIDC_REDIRECT_BASE_URL` is no longer strictly required when Bindery sits behind a reverse proxy. If the env var is unset and `BINDERY_TRUSTED_PROXY` is configured, Bindery derives the public-facing base URL from `X-Forwarded-Proto` + `X-Forwarded-Host` on each request. Explicit env-var values still win when set (needed for path-prefix deploys). Previously a missing env var produced a relative `redirect_uri`, which IdPs reject with `redirect_uri_mismatch`. The redirect base resolved at `/login` is round-tripped through the flow cookie so `/callback` uses the same value during the token exchange.

### Fixed

- **OIDC providers no longer silently dropped after failed startup discovery** (#461) — Providers whose discovery fails during `Reload()` are now tracked in a separate failed-providers map instead of being silently logged-and-forgotten. `GET /api/v1/auth/oidc/providers` returns a per-provider `status` block (`"ok"` / `"failed"` with the last error and timestamp) so admins can diagnose without grepping logs. The first login attempt for a failed provider triggers an on-demand re-discovery (rate-limited to once per 30s), so transient startup failures (e.g. pod recreated before IdP is reachable) recover automatically without an admin restart.
- **ABS imports require saved source configuration** — import and dry-run starts now use only the stored ABS configuration, and the UI blocks runs while ABS settings contain unsaved changes so previews and imports cannot run against one-off request overrides.
- **Hardcover auto-linking requires local evidence** — automatic series linking now requires local book overlap or author agreement before accepting a high-confidence Hardcover candidate, and missing-book fill skips books that already exist as excluded titles.

### Docs

- Added user-facing Hardcover series wiki documentation and documented the enhanced Hardcover series migration, feature flag, token requirement, admin toggle, and production network expectations in the deployment guide.

## [v1.3.1] — 2026-05-05

### Fixed

- **Possessive author prefix stripped before release matching** (#446) — Search results for titles like *Tom Clancy's Rainbow Six* no longer require the release to carry "Clancy's"; the possessive prefix is stripped before keyword extraction. Handles both ASCII apostrophe and Unicode right-single-quotation-mark (U+2019).
- **Readarr import returns structured error on failure** (#447) — The Readarr DB import handler now returns a JSON `{"error": "…"}` body with an appropriate HTTP status on failure instead of an empty 500.
- **Edition deduplication upgrades existing row to dual-format** (#448) — When OpenLibrary returns both an ebook Work and an audiobook Work for the same title during an author sync, Bindery now upgrades the existing book row to `media_type: both` instead of inserting a duplicate entry.
- **Library scanner searches both library and audiobook roots** (#456) — `FindExisting` now walks `BINDERY_AUDIOBOOK_DIR` alongside `BINDERY_LIBRARY_DIR` when checking for pre-existing files, and pre-filters by author folder to prevent cross-author mismatches. Previously only the ebook library was checked, leaving audiobook files undetected on rescan.
- **Download client edge-case coverage** (#431) — Added hermetic matrix tests for RemoteID normalization, live status error mapping, poll failures, unreachable clients, context deadlines, and qBittorrent unfiltered hash polling. Transmission queue overlays now surface non-empty `errorString` values as error statuses.

## [v1.3.0] — 2026-05-05

### Added

- **Audiobookshelf (ABS) import workflow** (#371) — Bindery can now connect to one ABS source, validate an API key, discover visible book libraries, and import ABS catalog metadata into shared authors, books, series, and ebook/audiobook editions. Imports support dry runs, persisted run history, rollback preview/rollback, low-confidence review queues, metadata conflict resolution, and path remaps when ABS and Bindery see the same files under different mount prefixes. Import quality is best when the ABS library already has strong metadata, especially ASIN coverage.

### Changed

- **ABS configuration saves no longer probe the live server** — saving ABS settings now normalizes and stores the base URL, label, enabled flag, selected library ID, path remaps, and write-only API key without contacting ABS. Use **Test connection** or **List libraries** for live validation; library discovery returns book libraries only.
- **ABS importer internals split by domain** — the importer orchestration remains in `internal/abs/importer.go`, with author matching, upserts, file reconciliation, metadata conflicts, rollback, snapshots, shared types, and utilities split into focused helper files.

### Fixed

- **ABS imports reject non-book libraries before scanning** — import enumeration now validates that the selected ABS library page is `mediaType=book` and that each returned item is a book item before mapping catalog data.
- **ABS API calls use a Bindery user agent** — config probes and import enumeration now send `User-Agent: bindery/<version>` (`bindery/dev` when no build version is available) instead of the Go default user agent.
- **Docker image now published for linux/arm64** (#445) — the `image` CI job previously only built `linux/amd64`; both `linux/amd64` and `linux/arm64` are now built and pushed in a single multi-platform manifest, fixing image-pull failures on Apple Silicon and Raspberry Pi hosts.

### Chores

- **Go toolchain pinned for CI** — `go.mod` now targets Go `1.25.9`, while GitHub Actions setup uses the Go `1.25` minor family across CI, security, and ABS contract jobs.

### Docs

- Added an ABS import guide and user-facing wiki documentation covering setup, required API-key access, path remaps, review flow, conflicts, rollback, and import-quality expectations.

## [v1.2.7] — 2026-05-04

### Added

- **Arr-compatible queue endpoint for Harpoon integrations** (#370) — `GET /api/queue` returns a Sonarr/Radarr-style queue payload with `totalRecords`, queue records, live `size`/`sizeleft`, downloader status, client name, remote download ID, protocol, optional pagination, and sorting. The existing `GET /api/v1/queue` UI response remains unchanged.
- **Author detail search-all-wanted action** (#410) — the Author Detail page now has a **Search all wanted** button that queues searches for that author's monitored wanted books, disables itself when there is nothing searchable, and surfaces bulk-search errors inline. Author bulk search now also skips unmonitored wanted books so explicit per-book unmonitor decisions are respected.
- **Startup configuration validation** (#430) — Bindery now validates its configuration on startup and logs actionable warnings for known conflict patterns (conflicting audiobook dir, invalid URLs, non-existent paths). Does not block startup; surfaces problems early before they cause silent failures at runtime.
- **Configurable login rate-limit thresholds** (#428) — `BINDERY_RATE_LIMIT_MAX_FAILURES` (default 5) and `BINDERY_RATE_LIMIT_WINDOW_MINUTES` (default 15) let operators tune the per-IP brute-force lockout without recompiling.

### Fixed

- **OpenLibrary search results restored** (#408) — the deprecated `/search.json` endpoint (which began returning HTTP 500) is replaced by `/authors/{id}/works.json` as the primary works source with `/search` demoted to enrichment. Series data now comes from the primary call so fewer round-trips are needed.
- **Audiobook routing now respects `BINDERY_AUDIOBOOK_DIR`** (#421) — per-author ebook root folders were incorrectly applied to audiobook destinations; audiobooks now always route to the dedicated audiobook directory and ignore the ebook root.
- **Audiobook directory visible in Settings UI** (#420) — the audiobook storage path is now displayed in Settings → General alongside the library directory.
- **API-key requests exempt from CSRF middleware** (#424) — external tools such as Harpoon that authenticate via X-Api-Key header were receiving 403 on `POST /api/queue`; API-key-authenticated requests now bypass `RequireXRequestedWith` and `RequireCSRFToken` checks while browser-session requests remain protected.
- **Torrent hash case sensitivity** (#425) — torrent hashes are now lowercased on assignment, preventing hash-not-found mismatches when clients return mixed-case identifiers.
- **Transmission error states now surface in queue** (#426) — integer status codes 16 and 32 (error / isolated-error) are now recognised and translated to `TrackedDownloadStatus: Warning`, so stuck Transmission downloads appear in the queue instead of silently stalling.
- **CSV author import skips header row** (#419) — CSV imports with a header row no longer create a spurious author entry from column names.
- **qBittorrent hash detection no longer filtered by category** (#418) — the category filter on the hash detection poll was a spurious race condition that prevented hashes from being recorded on redirect URLs; the filter is removed.
- **Credential normalization silent clear fixed** (#422) — `normalizeClientCredentialStorage` now applies the same `legacyCredentialURLBase` guard as the read path, preventing a bare `url_base` with no `api_key` from being silently migrated into `username` on write.
- **Library scanner series matching now runs in production** — the scanner is wired to the series repository at startup; filename-based series/position matching now runs during normal library scans.
- **qBittorrent and Transmission URL Base preserved on read** — legacy credential hydration no longer clears real reverse-proxy URL Base values (e.g. `/qbit`) — only old credential-as-url_base rows are migrated.

## [v1.2.6] — 2026-04-25

### Fixed

- **NZBGet grabs broken** (#396) — `GetFirstEnabledByProtocol` and `GetEnabledByProtocol` only queried `sabnzbd` for the usenet protocol; NZBGet was never returned, causing "no enabled download clients" on every grab attempt for users with only NZBGet configured.
- **NZBGet credentials zeroed on read** (#396) — `hydrateClientCredentials` blanked `username`/`password` for all non-qBit/Transmission clients, silently wiping NZBGet HTTP Basic auth credentials before they reached the adapter.
- **Deluge missing from torrent protocol selector** (#396) — both `GetFirstEnabledByProtocol` and `GetEnabledByProtocol` excluded Deluge from the torrent client `IN` list, causing "no enabled download clients" for Deluge-only setups.
- **Imageproxy concurrent-write race** (#396) — concurrent requests for the same image URL all wrote to the shared `imgFile+".tmp"` path; a racing `O_TRUNC` open could zero the file while another goroutine renamed it into the cache, resulting in empty image responses. Each goroutine now uses `os.CreateTemp()` for an isolated temp file.

## [v1.2.5] — 2026-04-24

### Added

- **`{Series}` and `{SeriesNumber}` naming tokens** (#389) — file renaming templates now support `{Series}` (primary series name) and `{SeriesNumber}` (position in series, e.g. `3` or `3.5`). Both are looked up at import time from the `series_books` join table; books with no series silently omit the path segment so existing templates are unaffected. The audiobook destination template exposes the same tokens. Default template is unchanged.
- **Scanner series-position matching** (#390) — the library scanner now attempts a fourth matching tier: if a filename contains a series name and position number (e.g. `[Dune Chronicles, Book 2]` or `(Mistborn #1)`) and no title/author match was found, Bindery looks up the series in the database and reconciles the book if the match is unambiguous. Supports bracket and parenthesis notation, `book/vol/part` prefixes, and integer or decimal position numbers. ISBN-shaped numbers are excluded via a letter-start requirement on the series name.

### Fixed

- **Discover: unrated and low-popularity books suppressed** (#391, closes #360) — `hardFilter` now drops candidates with fewer than 50 ratings (obscure long-tail editions that have never been rated) and candidates with 50+ ratings but an average below 3.0 (objectively poor books). Candidates with no ratings data at all are not penalised so missing metadata doesn't hide results.
- **Discover: box sets, omnibuses, and anthology contributions excluded** (#392, closes #361) — a new keyword scan in `hardFilter` drops titles matching "omnibus", "box set", "complete works", "complete collection", "anthology", "collected works", "the best of", and similar multi-volume markers. Users see individual titles on the Discover page rather than compilation volumes they may already own in parts.
- **Download client forms: Use SSL toggle and URL Base field added** (#393, closes #364) — both "Add client" and "Edit client" forms now expose a **Use SSL** checkbox and a **URL Base** text field. Previously these settings existed in the Go model and DB schema but were invisible in the UI, so operators behind a reverse-proxy subpath or needing TLS had no way to configure them without raw DB edits. `urlBase` is also added to the TypeScript `DownloadClient` interface which was missing it.
- **Download client URLs now respect `url_base`** (#375, closes #369) — all five downloader clients (qBittorrent, Transmission, Deluge, NZBGet, SABnzbd) built their connection URL from `host:port` only, ignoring the stored `url_base`. Operators running a client behind a reverse-proxy subpath (e.g. `/qbit`) would see Bindery connect to the wrong endpoint. A new `internal/downloader/urlbase.Normalize()` helper canonicalises the stored value — handles missing leading slash, trailing slashes, and pasted full URLs — and the result is threaded through every `New()` constructor.

## [v1.2.4] — 2026-04-24

### Fixed

- **Non-latin author names now match usenet releases** (#380) — authors whose names are written in a non-latin script (e.g. Japanese, Chinese, Arabic) have an ASCII surname of `""` after tokenisation, so every release was filtered out as irrelevant. Bindery now fetches the author's OpenLibrary `alternate_names` on first add, saves any ASCII-script aliases to `author_aliases`, and includes those aliases when building the surname candidate list for release matching.
- **Import no longer stalls indefinitely on NFS timeouts** (#381) — the file-copy path used bare `io.Copy` with no cancellation. On an NFS stall the goroutine blocked forever and the download record stayed locked in `importing` state. Each import now runs under a 30-minute context timeout; copies run in a goroutine and respect cancellation, closing both file descriptors to help the kernel unblock the stalled call.
- **Wanted filter no longer shows unmonitored books** (#382) — the Books page Wanted status filter matched `status === 'wanted'` without checking `monitored`, so books explicitly set to "don't monitor" appeared alongside genuinely wanted titles. The filter now requires `monitored === true` when the Wanted status is active.
- **Recommender language filter applied to candidates** (#359) — `hardFilter` removed owned, dismissed, and excluded-author candidates but never checked language. Users with a preferred language set would receive foreign-language recommendations. Candidates whose `Language` field differs from `PreferredLanguage` are now filtered out; candidates with an empty language tag pass through so missing metadata doesn't silently hide results.
- **Recommender recency score anchored to library median year** (#357) — `recencyScore` used `time.Now().Year()` as its reference point, penalising any book published before ~2005 regardless of the user's actual reading taste. The score is now relative to the median publication year of the user's library (computed in `BuildProfile`). Window widened from 20 → 30 years, floor lowered from 0.3 → 0.1, and `weightRecency` bumped from 0.10 → 0.15 to give the era-relative signal more influence.

### Changed

- **CI: parallel validate jobs and reduced friction** — the `validate` job split into `validate-go` (race-detector tests) and `validate-frontend` (npm build) running in parallel, cutting PR critical-path time from ~253 s to ~180 s. `golangci-lint` and `govulncheck` removed from the security workflow's `sast-go` job (both already run in `lint`). BuildKit GHA layer-cache added to container scans. Security workflow now skips doc-only changes via `paths-ignore`. Kubesec and Dockle removed (output was silently discarded). Discord release announcements now posted automatically on tag push.

## [v1.2.3] — 2026-04-23

### Fixed

- **Logs tab now displays entries** — `db.LogEntry` was missing `json:""` tags, causing Go to serialise field names as PascalCase (`ID`, `TS`, `Level`, `Component`, `Message`, `Fields`). The TypeScript interface expected camelCase, so every row rendered blank. (#376)

## [v1.2.2] — 2026-04-23

### Fixed

- **Calibre "Push all to Calibre" button state now matches Test-connection result** (#342) — the button was enabled even when the last connectivity test failed, allowing pushes to silently no-op against an unreachable bridge. It now stays disabled until a successful test in the current session.
- **Download client host field no longer double-schemes the URL** (#353) — if a user typed `https://` or `http://` in the Host field, the downloader prepended the scheme a second time, producing `https://https://…` and causing every connection attempt to fail. The host is now stripped of any leading scheme before the URL is assembled.
- **CSP nonce now injected server-side** (#353) — the inline `<script>` tag in `index.html` used a static placeholder nonce that never matched the server-generated nonce, causing the theme-initialisation script to be blocked by Content-Security-Policy in strict environments. The nonce is now written by the Go server at request time.
- **Docker image tags now include `v`-prefixed semver variants** (#353) — the CI metadata action was missing `type=semver,pattern=v{{version}}`, so only bare `1.2.x` tags were pushed to ghcr.io. Both `1.2.x` and `v1.2.x` are now available.
- **Version string in footer links to the GitHub releases page** (#356) — clicking the version badge now opens the corresponding release regardless of whether the string is a semver, `v`-prefixed semver, or `dev-<sha>`.

## [v1.2.1] — 2026-04-23

### Fixed

- **Prowlarr-synced indexers no longer send broad parent category 7000** (#344) — indexers synced from Prowlarr were always requesting category 7000 (Books parent), which caused many indexers to return results for every book-adjacent category including comics. Bindery now sends the appropriate child category (7020 Ebooks, 3030 Audiobooks) and drops the parent when children are present.
- **qBittorrent "hash could not be determined" on category mismatch** (#363) — after adding a torrent, Bindery polled only the configured download category, so if qBittorrent placed the torrent in a different category the hash was never found and the download was marked as failed. Bindery now polls the full torrent list (unfiltered) and logs a detailed error with hash diagnostics if the 30-second window expires.
- **Dual-format delete leaves orphan sibling files** (#343) — deleting one format of a dual-format book failed to remove sibling format files from disk. Sibling cleanup now runs regardless of whether the file being deleted still exists.
- **Rescan misbinds books with similar titles** (#290) — the Jaro-Winkler similarity threshold for matching filenames to book records was too permissive. Threshold raised from 0.80 to 0.88.
- **Interactive search mixes ebook and audiobook results** (#333) — results from all indexers were shown in a single unsorted list for dual-format books. Results are now split into labelled **Ebook results** and **Audiobook results** sections.

## [v1.2.0] — 2026-04-22

### Added

- **Default library location can now be set from Settings → General** (#332). A new "Default root folder" dropdown lets you pick any configured root folder as the library path used when an author has no per-author root folder. Existing `BINDERY_LIBRARY_DIR` continues to work as a fallback when the setting is unset. An inline "Add root folder" affordance lets you create a new root folder without leaving the page. Startup logs a warning (but does not fail) if the configured default root folder no longer exists on disk.
- **Search results grouped by media type** — For dual-format books (ebook + audiobook), the Book Detail page now displays results in two titled sections (Ebooks / Audiobooks) each with its own 20-result cap, so audiobook results can no longer fall past the UI cap. Single-format books retain the existing flat list. Each result row in the split view shows a colour-coded media-type badge (#333).
- **Persistent log store** — Settings → Logs now persists entries across restarts and supports filtering by date range, level, and component. Retention defaults to 14 days and is configurable via `BINDERY_LOG_RETENTION_DAYS` or Settings → General → Log retention. ([#241](https://github.com/vavallee/bindery/issues/241))

### Fixed

- **Multi-file ebook downloads are now fully tracked** — Delete + files removes every file (mobi, epub, pdf, etc.) and rescan cannot re-claim orphan files. Library rescan now requires a matched file to live under the candidate book's configured root folder, preventing cross-author mismapping (#343).
- **Ebook searches no longer include the parent Books category (7000)**, which could return comics and magazines. Affects Prowlarr-synced indexers: `filterCategoriesForMedia` now matches only the 702x ebook subcategory range (7020–7029) and 303x audiobook range (3030–3039), and the syncer drops parent categories (7000, 3000) at sync time and propagates category changes on re-sync. (#344)
- **Author sync no longer creates duplicate book rows that differ only in edition suffix, whitespace, or Unicode normalization.** Existing duplicates are merged on upgrade. Search result filtering no longer drops valid releases when the book title contains a parenthesised edition qualifier (#283).

## [v1.1.7] — 2026-04-22

### Fixed

- **Discover page blank after first refresh** — `models.Recommendation.Genres` was typed as `string`, so the API serialised genres as a JSON-encoded string (`"[\"Fantasy\",...]"`) instead of a JSON array. The frontend called `.map()` on the string, threw a `TypeError`, and React unmounted the whole page. `Genres` is now `[]string`; the DB scan layer deserialises the stored JSON before the struct is marshalled to the API response.

## [v1.1.6] — 2026-04-22

### Fixed

- **Discover page always empty** — `BuildProfile` added every book to `OwnedForeignIDs` regardless of status, so all candidate generators (series, author_new, genre_similar, serendipity) immediately skipped every known book and returned zero results. `OwnedForeignIDs` now only includes books with `downloaded` or `imported` status, allowing `wanted`/`skipped` books to surface as recommendations.

## [v1.1.5] — 2026-04-22

### Fixed

- **Authors tab empty after adding author** (#339) — authors added via the UI or the AddBook implicit-create path were stored with `owner_user_id = NULL` because `Create()` always called `CreateForUser` with a hardcoded zero. The Authors list query filters by the authenticated user's ID, so every freshly-added author was invisible in the tab even though it existed in the database. Both creation sites now pass the user ID from the request context, so authors are correctly owned and appear immediately.

## [v1.1.4] — 2026-04-21

### Fixed

- **Calibre import: Language unknown on all books** (#314) — `books_languages_link` was never queried, so every imported book showed "Language unknown" and editions were hardcoded to `"eng"`. The reader now reads the primary ISO 639-2 language code from Calibre's languages table (falls back gracefully for pre-0.7 Calibre libraries that predate the table). Language is applied to both the book row and the edition.
- **Calibre import: Author ratings missing after first Refresh Metadata** (#314) — `relinkCalibreAuthor` fetched the full OL author but only copied image/description/sort name. `ratings_count` and `average_rating` are now copied as well.
- **Calibre import: Duplicate book rows after Refresh Metadata** (#314) — `FetchAuthorBooks` skipped title-matched books with a bare `continue`, leaving calibre-imported stubs (synthetic `calibre:book:N` ForeignID, no language) un-upgraded when OpenLibrary returned the same title. Calibre stubs are now updated in-place with the real OL ForeignID and language instead of being silently skipped, preventing a second OL row from being created alongside them.

## [v1.1.3] — 2026-04-21

### Fixed

- **Authors missing from list view** (#330) — authors with a NULL `owner_user_id` (created before the multi-user migration backfill ran, or imported without a user context) were silently excluded from `GET /api/v1/author`. The list query now includes `OR owner_user_id IS NULL` so all owned authors appear regardless of when they were added.
- **Delete file leaves zombie on disk** (#290) — `DELETE /book/{id}/file` on legacy books (only `file_path` set, no `ebook_file_path`) cleared the DB column but never called `os.Remove` on the actual file. The legacy path is now handled explicitly in the deletion block.
- **Download link missing for newer books** (#331) — `GET /api/v1/book/{id}/file` returned 404 for books added after the dual-format schema (migration 026+) because it only checked the legacy `file_path` column. It now falls back to `ebook_file_path` then `audiobook_file_path`. The book detail page also hid the "Download file" button for these books; it now appears whenever either path is present.

## [v1.1.2] — 2026-04-21

### Changed

- Prowlarr package test coverage expanded from ~0% to ~98% — adds `client_test.go` covering all HTTP paths and `syncer_extra_test.go` with error-path and edge-case tests for the syncer.
- Pinned all GitHub Actions in `scorecard.yml` to commit SHAs (OpenSSF Scorecard `Pinned-Dependencies` compliance).

## [v1.1.1] — 2026-04-21

### Security

- **API key exposed to non-admin users** — `GET /api/v1/auth/config` returned the global API key to every authenticated account. Since the key is also accepted via the `?apikey=` query string, any regular user could authenticate with full API access. The key is now redacted unless the caller has `role=admin`.
- **Cross-user author visibility** — `GET /api/v1/author` returned all authors regardless of `owner_user_id`, letting one user see (and enumerate) another user's library. The list is now scoped to the authenticated user.
- **Non-admin auth-mode escalation** — `PUT /api/v1/auth/mode` lacked a `RequireAdmin` guard. A regular user could switch the instance to `local-only`, granting unauthenticated access to every client on the local network. The endpoint now requires admin role.
- **Untrusted `X-Forwarded-*` header injection** — `X-Forwarded-Proto` and `X-Forwarded-Host` were accepted from any client when `BINDERY_TRUSTED_PROXY` was not set, enabling OPDS base-URL injection and spurious HSTS headers. All forwarded headers are now stripped from requests that do not originate from a configured trusted proxy.

## [v1.1.0] — 2026-04-20

### Added

- **Audible-direct author lookup** (#302) — audiobook-heavy users no longer lose most of a prolific author's Audible catalogue to OpenLibrary/Hardcover ASIN gaps. When the effective media type is `audiobook` or `both`, `FetchAuthorBooks` supplements OpenLibrary's works list with results from Audible's public catalogue endpoint (`api.audible.com/1.0/catalog/products`). Supplemental books flow through the same dedup + metadata-profile `allowed_languages` filter as OpenLibrary, so foreign-language ASINs are filtered out before persisting.

### Fixed

- **Author ingestion drops books + catalogue noise** (#313) — `GetAuthorWorks` now uses OpenLibrary's search index as the primary source (one call returns title + language + subjects + cover + year) and keeps the `/authors/{id}/works` endpoint as a backfill that hydrates series membership and picks up works the search index has missed (e.g. recent releases). A new subject/title noise filter at the OL client layer drops study guides, summaries, film/TV adaptations, screenplays, and audio-CD pseudo-works before they reach the ingestion pipeline, stopping duplicates like the five "Dutch House" entries previously pulled for Ann Patchett.
- **Audiobook library scan misses tagged files** (#303) — the library scan now reads embedded ID3/iTunes tags (title, author, ASIN) from MP3/M4B/M4A/FLAC/OGG files during reconciliation. Match priority is ASIN → tag title+author → fuzzy filename fallback, so well-tagged Audible/organised libraries match without manual correction. Files whose tags can't be read surface as scan warnings and a new `tag_read_failed` counter in `library.lastScan`.

### Chores

- **golangci-lint cleanup** — resolve errorlint (`%v` → `%w` for double-wrapped errors), staticcheck (apply De Morgan on ASIN charset check), and gofmt formatting noise introduced alongside the three fixes above.

## [v1.0.5] — 2026-04-20

### Fixed

- **Admin role required on fresh install** (#321) — new users created via first-run setup were stored without the admin role, so Settings → Config showed only the General section and any config mutation (Calibre plugin, users, indexers) 403'd with "admin role required" regardless of the security mode. First-run user is now explicitly promoted to admin before the session is issued; existing single-user installs are unaffected. Unblocks #314 reporters from retesting the Calibre metadata fix.
- **NZB grabs misrouted to qBittorrent** (#320) — Prowlarr-synced indexers were hardcoded as `torznab` regardless of the upstream indexer's actual protocol, so NZB search results were tagged `protocol=torrent` and dispatched to qBittorrent, which then failed with `add torrent accepted but hash could not be determined`. The syncer now uses Prowlarr's `protocol` field to choose `newznab` vs `torznab`, and corrects mis-typed rows on the next sync. The scheduler no longer falls back across protocols when the protocol-matched client list is empty — an NZB release can never be pushed to a torrent client.

### Added

- **Bulk media-type update across monitored authors** (#247) — select multiple authors on the Authors page and switch their media type in one action (or flip all authors from a Settings one-shot). `PATCH /api/v1/authors/bulk` re-evaluates wanted/missing status for affected books so a flip from ebook → audiobook (or reverse) doesn't leave the catalogue in an inconsistent state. Companion to the existing global default media-type setting.

### Docs

- **Discord invite added to README and CONTRIBUTING** (#319) — new Community section links the BINDERY Discord server as a real-time channel for support and contributor onboarding, alongside GitHub Issues and Discussions. Security reports continue to go through `SECURITY.md`, not Discord.

### Chores

- **vitest 3.2.4 → 4.1.4** (#312) — dev-dependency bump for the web test runner.

## [v1.0.4] — 2026-04-20

### Reverted

- **eslint 9→10 bump** (#311) — reverted because `eslint-plugin-react-hooks@7.0.1` still peers eslint at `^9.0.0`, breaking `npm ci` in the Docker build. v1.0.3 tag was cut but never produced an image; v1.0.4 ships the same fixes without the eslint upgrade. Will retry once react-hooks catches up.

## [v1.0.3] — 2026-04-20 *(tagged but not released — CI build failure, see v1.0.4)*

### Fixed

- **CSRF token lost on page reload** (#315) — sessions survived reloads cookie-wise, but `initCSRF()` only ran on login, so subsequent mutations hit 403 until next login. `AuthContext.refresh()` now re-hydrates the token whenever the session is authenticated.
- **Calibre-imported authors stuck with no metadata** (#316) — authors with a `calibre:` foreign ID were hard-skipped by the metadata refresh. They are now re-linked to the metadata provider on first refresh via exact name match (case/whitespace-insensitive), pulling real image/description/sort name in place.
- **Misleading author search book count** (#317) — the "books" number on author search results is OpenLibrary's raw work count before dedup/language-filter; relabelled to "up to N works" with a tooltip explaining the post-filter catalogue will be smaller.

### Docs

- **Auth, multi-user, and v1 upgrade guides** (#318) — added `docs/auth-multiuser.md`, `docs/auth-oidc.md`, `docs/auth-proxy.md`, `docs/multi-user.md`, `docs/troubleshooting-auth.md`, `docs/upgrade-v2.md` covering the v1.0 role model, OIDC/proxy setup, and migration path.

### Chores

- Dependency bumps: golang base image (#307), node base image (#308), `modernc.org/sqlite` (#309).

## [v1.0.2] — 2026-04-20

### Added

- **Admin password reset** (#292/#305) — admins can reset any local user's password from the Users page without requiring the user to log out. New endpoint `PUT /auth/users/{id}/reset-password`.

### Fixed

- **Books sort with missing release dates** (#304) — books without a release date were bubbling to the top of "oldest first" date sorts. They now sort to the end in both directions.
- **Empty folders left behind after deleting books** (#290/#306) — when the last file in a book folder was removed, the now-empty parent directory stayed behind. `removeBookPath` now cleans up the parent on a best-effort basis. Multi-format folders shared between ebook + audiobook are only removed when both formats are gone.

## [v1.0.1] — 2026-04-19

### Fixed

- **Admin UI missing from v1.0.0** — the Users management page (`/users`), admin nav icon, role-gated Settings tabs, and `isAdmin` context were omitted from the v1.0.0 build. All admin UI components now ship correctly (#301).

## [v1.0.0] — 2026-04-19

### Added

- **Reverse-proxy SSO** (#238/#239) — new `proxy` auth mode trusts an upstream identity header (`X-Forwarded-User` by default) when the request arrives from a configured trusted proxy IP. Startup refuses proxy mode without `BINDERY_TRUSTED_PROXY` set.
- **Native OIDC client** (#237) — Authorization Code + PKCE with multi-provider support (Google, GitHub/Dex, Authelia, Keycloak). Providers configured via Settings → Authentication; users identified by stable `(issuer, sub)` pair.
- **Multi-user scoping** (#236) — every user-owned entity (authors, books, downloads, profiles, root folders) scoped to its owner. First user auto-promoted to admin; admin users manage all settings.
- **Admin user management** — Users page (admin only): invite users, set roles, delete with last-admin guard.
- **Settings split** — per-user tabs (API key, password, notifications) and admin-only tabs (indexers, clients, profiles, system).
- **CSRF double-submit tokens** (#240) — `GET /auth/csrf` issues a session-bound token; all authenticated mutations require matching `X-CSRF-Token` header. API-key requests exempt.

### Fixed

- **CSRF middleware login bypass** — `POST /auth/login` was incorrectly blocked by CSRF check before a session cookie existed; fixed to skip CSRF when no session is present.

### Breaking

- **Database migration 025**: `owner_user_id` added to all user-owned tables; existing data migrated to user ID 1. Back up before upgrading. See [upgrade guide](https://github.com/vavallee/bindery/wiki/Howto-Migrate-to-multi-user).
- **CSRF tokens required**: browser-based API consumers must call `GET /auth/csrf` and include `X-CSRF-Token` on mutations. API-key clients unaffected.

## [v0.22.0] — 2026-04-19

### Fixed

- **Grab FK constraint crash** — clicking Grab on a search result no longer fails with a foreign-key violation; `bookId`/`indexerId` are now treated as optional nullable fields so free-text search grabs always succeed (#285)
- **Audiobook search details blank page** — `SearchDebug.Filters` is now initialised to an empty slice instead of `null`, preventing a JS crash in `SearchDebugPanel` that was more likely to trigger on audiobook searches (#282)
- **ISBN lookup cryptic error** — unknown ISBNs now surface a friendly "No book found for ISBN X. Check the number, or try searching by title instead." message instead of a misleading upstream-unavailable error (#284)

### Improved

- **Calibre Test Connection feedback** — the Test Connection button now shows ✓/✗ prefixes and the exact plugin reachability message; stale results are cleared whenever Calibre settings are saved (#262)

## [v0.21.0] — 2026-04-19

### Added

- **Spanish, Filipino, and Indonesian UI translations** — language switcher now offers English, Français, Deutsch, Español, Filipino, Nederlands, and Bahasa Indonesia. Browser language is auto-detected on first visit; manual override persists to localStorage.
- **Search hourglass icon** — the Search nav item moves off the main navigation bar and becomes an hourglass icon next to the settings gear, keeping the header cleaner. On mobile it remains accessible as a text item in the hamburger dropdown.

## [v0.20.3] — 2026-04-19

### Security

- **Trusted proxy configuration** — `BINDERY_TRUSTED_PROXY` gates `X-Forwarded-For` rewriting to a configured proxy IP/CIDR. Without it, forwarded headers are ignored and the direct peer IP is used, preventing XFF spoofing in local-only auth mode (mirrors Sonarr CVE-2026-30975).
- **File download path validation** — the file download endpoint now verifies `book.FilePath` falls within a configured library root before serving. Paths outside `BINDERY_LIBRARY_DIR` / `BINDERY_AUDIOBOOK_DIR` return 403.
- **CSRF header exemption for API key requests** — the `X-Requested-With` CSRF check now correctly exempts API-key-authenticated requests; only cookie-session requests are required to supply the header.
- All fixes from v0.20.1: SSRF validation on Prowlarr URLs, path traversal protection in file renamer, strict backup filename regex, image proxy redirect re-validation, Hardcover token moved to Authorization header, OPDS rate limiting, CI hardening.

## [v0.20.0] — 2026-04-18

### Added

- **Deluge download client** ([#263](https://github.com/vavallee/bindery/pull/263)) — adds Deluge alongside qBittorrent and Transmission as a supported torrent client. Configure under Settings → Download Clients with host, port (default 8112), password, and optional label (requires the Label plugin). Deluge authenticates with a single password and no username, which the UI reflects.
- **Direct indexer search page** ([#266](https://github.com/vavallee/bindery/pull/266)) — a new **Search** nav item runs freeform queries across every configured indexer without needing a tracked book. Each result row has a **Grab** button that sends the release straight to the download client, bypassing the per-book decision pipeline. Useful for grabbing one-off titles or testing indexer responses.
- **{ASIN} naming token** ([#269](https://github.com/vavallee/bindery/pull/269)) — `{ASIN}` can now be used in rename templates (e.g. `{Author}/{ASIN}/{Title}.{ext}`). ASINs are also extracted from filenames during library scans and stripped from the title, so Amazon-origin files no longer pollute title matching. Empty string when the book has no ASIN in its metadata.

### Fixed

- **Indexer Test probes with a real search** ([#265](https://github.com/vavallee/bindery/pull/265)) — after the caps probe, **Test** now runs a `t=search&q=book` request across the indexer's book categories. The UI surfaces an amber warning when zero results are returned, catching misconfigured API keys and category mappings that previously reported success on caps alone.

## [v0.19.2] — 2026-04-18

### Fixed

- **Create destination directory before audiobook import** ([#255](https://github.com/vavallee/bindery/pull/255)) — import no longer fails when the target library directory does not yet exist; Bindery now creates it before attempting to move files. Resolves a silent failure that left audiobooks stranded in the download folder.
- **Search consistency for "both" media type** ([#256](https://github.com/vavallee/bindery/pull/256)) — books monitored as `both` now run separate ebook (7xxx) and audiobook (3xxx) category searches and union the results, instead of falling through to the ebook branch only. Also normalises subtitle-heavy query strings to improve match rates on all indexers.

### Added

- **Unknown-language badge and pass/fail setting** ([#257](https://github.com/vavallee/bindery/pull/257)) — books whose language metadata is absent or unrecognised are now surfaced with an "unknown language" badge in the UI. A new setting controls whether unknown-language books pass or fail the language filter, giving users explicit control instead of silent rejection.
- **Search debug panel** ([#258](https://github.com/vavallee/bindery/pull/258)) — a collapsible debug panel on the Book Search page shows every indexer that was queried, how many results each returned, and which pipeline stage (dedupe, junk filter, relevance, language, decision) dropped each candidate. The last debug payload is cached server-side so the panel survives page reloads.
- **Push all to Calibre sync button** ([#259](https://github.com/vavallee/bindery/pull/259)) — a single button on the Calibre settings page triggers a full library sync, pushing every imported book to Calibre in one shot. Useful after initial setup or after a Calibre database restore.
- **Global default media type and bulk author update** ([#260](https://github.com/vavallee/bindery/pull/260)) — a new global setting establishes the default media type for newly added authors. A bulk-update action on the Authors page lets you apply any media-type change across all (or selected) authors at once, eliminating tedious one-by-one edits.

## [v0.19.1] — 2026-04-18

Re-release of the v0.19.0 feature set plus the external-import and newznab-coverage PRs. The previously-tagged `v0.19.0` artifact predated these merges; `v0.19.1` is the authoritative release for this feature batch.

### Added

- **NZBGet download client** ([#233](https://github.com/vavallee/bindery/pull/233)) — adds NZBGet alongside SABnzbd as a Usenet download target. Configure under Settings → Download Clients; Bindery tracks grabs, monitors status, and imports completed downloads the same way it does for SABnzbd.
- **External import mode for Calibre / Grimmory workflows** ([#235](https://github.com/vavallee/bindery/pull/235)) — new import mode that lets Bindery hand completed downloads off to an external importer (Calibre `calibredb add` in a sidecar, or Grimmory's ingest pipeline) instead of moving files directly into the library. Useful when another tool owns the final file layout.
- **Series gap detection and Fill gaps** ([#234](https://github.com/vavallee/bindery/pull/234)) — the Series page now shows how many books are missing from each series ("N missing" badge) and a **Fill gaps** button that marks all non-imported entries as Wanted and kicks off indexer searches immediately. No more manually hunting for which entries you're missing.
- **Series monitoring toggle** — mark a series as monitored so it's easy to identify which series you're actively tracking. Foundation for future automation (auto-adding new entries when they appear).
- **Indexer Test button reports HTTP status, categories, and latency** ([#243](https://github.com/vavallee/bindery/pull/243)) — clicking Test on an indexer now returns a structured probe result (status code, category count, `bookSearch` availability, round-trip latency) instead of a bare "OK / failed" string. Makes misconfigured endpoints and slow indexers much easier to diagnose.
- **Import failure reason surfaced in Queue and History** ([#244](https://github.com/vavallee/bindery/pull/244)) — failed imports now record and display the underlying error (permission denied, path missing, Calibre rejected the file, etc.) instead of silently disappearing. Addresses the top recurring pain point from user feedback: "silent failures make it impossible to know what went wrong."
- **Storage paths visible in Settings UI** ([#245](https://github.com/vavallee/bindery/pull/245)) — download, incoming, and library paths are now surfaced in Settings → Storage so users can confirm where Bindery is reading from and writing to without digging through env vars or ConfigMaps.
- **Auto-grab checkbox persists in Add Author dialog** ([#242](https://github.com/vavallee/bindery/pull/242)) — the "auto-grab on add" toggle now remembers its last value across dialog opens, so users who always (or never) want auto-grab don't have to reset it every time.

### Fixed

- **Indexers tab crash** ([#227](https://github.com/vavallee/bindery/pull/227)) — clicking Settings → Indexers caused a white screen for users without Prowlarr configured.
- **Language filter now rejects books with unknown language** ([#228](https://github.com/vavallee/bindery/pull/228)) — non-English editions no longer slip through English-only metadata profiles when OpenLibrary omits language data.

### Internal

- **Newznab indexer client test coverage** ([#251](https://github.com/vavallee/bindery/pull/251)) — lifted `internal/indexer/newznab` coverage from 56.6% to 89.5% with focused tests for BookSearch tier fallbacks, Probe result shape, URL normalization, and error paths.

## [v0.19.0] — 2026-04-18

Initial tag for the above feature batch; artifact was published before the PRs it was meant to include were merged. Use `v0.19.1` or newer — this tag is retained only for historical reference.

## [v0.18.3] — 2026-04-17

### Fixed

- **Language filter now rejects books with unknown language** ([#228](https://github.com/vavallee/bindery/pull/228)) — when a metadata profile restricts to specific languages (e.g. English-only), books with no language data are now rejected instead of passing through. OpenLibrary doesn't include language at the work level, so translated editions (Turkish, Spanish, Dutch, etc.) were silently ingested for English-only profiles. The OL client already does a best-effort search-index lookup; this closes the gap for works the index doesn't cover. Reported in [#224](https://github.com/vavallee/bindery/issues/224).

## [v0.18.2] — 2026-04-17

### Fixed

- **Indexers tab crash** ([#227](https://github.com/vavallee/bindery/pull/227)) — clicking Settings → Indexers caused a white screen for users without a Prowlarr instance configured. The `/api/v1/prowlarr` endpoint returned JSON `null` (Go nil slice) instead of `[]`, which crashed React on render. Reported in [#225](https://github.com/vavallee/bindery/issues/225).

## [v0.18.1] — 2026-04-17

### Changed

- **Plugin API key field UX** ([#221](https://github.com/vavallee/bindery/pull/221)) — the API key field in Settings → Calibre (plugin mode) now has a show/hide toggle (eye icon) and a **Generate** button that fills the field with a cryptographically random 32-byte hex key (`crypto.getRandomValues`). `autoComplete="off"` prevents browsers from injecting saved passwords. Paste is unrestricted.

## [v0.18.0] — 2026-04-17

Calibre plugin integration mode, decision engine, pending releases, and state machine for downloads.

### Added

- **Calibre plugin integration mode** ([#212](https://github.com/vavallee/bindery/pull/212)) — new `plugin` mode alongside `calibredb`. When selected, Bindery POSTs imported file paths to the Bindery Bridge Calibre plugin over HTTP instead of shelling out to `calibredb`. Allows Calibre to run in a separate pod/container from Bindery without requiring a shared binary or library volume. Configure via Settings → Calibre → Mode: Plugin, with Plugin URL and API Key fields.
- **Release decision engine** ([#218](https://github.com/vavallee/bindery/pull/218)) — specification-pattern engine evaluates candidate releases against per-profile rules before grabbing, replacing ad-hoc inline checks.
- **Pending releases table** ([#219](https://github.com/vavallee/bindery/pull/219)) — delay-held results are tracked in a dedicated pending table with UI, replacing the previous silent-drop behaviour.
- **Prowlarr native indexer sync** ([#215](https://github.com/vavallee/bindery/pull/215)) — Bindery can now sync indexer configurations directly from a Prowlarr instance.
- **Add book by title or ISBN** ([#216](https://github.com/vavallee/bindery/pull/216)) — search bar accepts ISBNs and free-text titles in addition to author queries.
- **qBittorrent hash retry** ([#209](https://github.com/vavallee/bindery/pull/209)) — retries hash lookup for 10 s after torrent URL add, fixing race where hash was not yet visible after `.torrent` fetch.

### Changed

- **Download state machine** ([#217](https://github.com/vavallee/bindery/pull/217)) — formalises download lifecycle states; replaces ad-hoc string constants with typed enum.
- **Calibre settings tab** ([#220](https://github.com/vavallee/bindery/pull/220)) — shared `library_path` field hoisted to top of Calibre tab for clarity.

### Upgrade notes

- **No breaking schema migrations** — additive only. Safe drop-in replacement.
- **Calibre plugin mode** requires the [Bindery Bridge Calibre plugin](https://github.com/vavallee/bindery-plugins) installed in your Calibre instance.

## [v0.17.0] — 2026-04-17

Drop-folder Calibre mode removed, OpenLibrary series schema fixed, and a batch of UX and deployment polish.

### Removed

- **Calibre drop-folder mode** ([#207](https://github.com/vavallee/bindery/pull/207)) — the drop-folder integration has been removed entirely. It depended on the Calibre GUI application's auto-add watcher, which never fires in containerised / headless deployments. Books silently timed out with no feedback. The `calibredb` mode achieves the same result — mirroring every successful import into Calibre — without any of these constraints: it only requires that Bindery and Calibre share the library directory via a volume mount, which is already required for the library import/sync feature. Existing `calibre.mode = drop_folder` settings are treated as `off`; operators should switch to `calibredb` mode. The `calibre.drop_folder_path` setting and the `/api/v1/calibre/test-paths` endpoint are gone.

### Fixed

- **OpenLibrary object-typed series entries** ([#206](https://github.com/vavallee/bindery/pull/206), closes [#201](https://github.com/vavallee/bindery/issues/201)) — some OpenLibrary work records encode `series` as `[{"key": "...", "title": "..."}]` (object array) rather than `["..."]` (string array). Bindery previously crashed with an unmarshal error on these records, silently skipping all books for authors like Pierce Brown, J.K. Rowling, and Cornelia Funke. A new `flexStringSlice` decoder accepts both forms transparently.
- **Calibre settings save errors** ([#202](https://github.com/vavallee/bindery/pull/202), closes [#175](https://github.com/vavallee/bindery/issues/175)) — validation errors on `PUT /api/v1/setting/calibre.*` were returned as 400 but the UI silently discarded the response body; the error message now surfaces in the Settings page. Also fixes a case-sensitivity bug where NFS paths with uppercase letters were rejected.
- **Search "no indexers" message** ([#203](https://github.com/vavallee/bindery/pull/203)) — when a search returns no results *and* no indexers are configured, the UI now shows "No indexers configured — add one in Settings" instead of the generic "No results" empty state.
- **Login form method** ([#195](https://github.com/vavallee/bindery/pull/195)) — login form missing `method="POST"` caused browsers to silently submit via GET, leaking credentials in the URL bar and query-string logs.
- **Auth visibility refresh** ([#199](https://github.com/vavallee/bindery/pull/199)) — auth status was not rechecked when a browser tab regained focus after a session expiry, leaving users on a page that appeared authenticated but returned 401 on the next action.
- **Books empty state** ([#197](https://github.com/vavallee/bindery/pull/197)) — Books page showed a bare spinner when the library was empty; now shows instructional copy pointing to the "Add author" flow.
- **Version badge and footer links** ([#196](https://github.com/vavallee/bindery/pull/196)) — version badge in the header now links to the corresponding GitHub release; footer links to the repo.
- **Calendar aria-labels** ([#198](https://github.com/vavallee/bindery/pull/198)) — previous/next month buttons on the calendar lacked `aria-label` attributes, failing screen-reader and accessibility audits.

### Changed

- **Per-page document titles** ([#200](https://github.com/vavallee/bindery/pull/200)) — each page sets `document.title` to reflect the current view (e.g. "Authors — Bindery", "Settings — Bindery") for browser tab identification and history navigation.
- **Helm chart: corrected `BINDERY_DOWNLOAD_PATH_REMAP`** ([#204](https://github.com/vavallee/bindery/pull/204)) — default remap was `/downloads:/downloads`; corrected to `/downloads:/media` to match the NFS-mount convention documented in the reference deployment.
- **ArgoCD reference application** ([#205](https://github.com/vavallee/bindery/pull/205)) — updated NFS volume configuration and container entrypoints in the reference ArgoCD application manifest.

### Upgrade notes

- **No schema migrations** — this is a pure-logic and UI release. Drop-in binary or image replacement is safe.
- **Drop-folder users:** if `calibre.mode` is set to `drop_folder`, Bindery will treat it as `off` on startup. Switch to `calibredb` mode in Settings → Calibre to restore automatic mirroring. The `calibre.library_path` and `calibre.binary_path` settings are unchanged.

## [v0.16.0] — 2026-04-17

Calibre library import, Settings reorganisation, stalled download detection, and image proxy hardening.

### Added

- **Calibre library import** ([#63](https://github.com/vavallee/bindery/issues/63)) — import books, authors, and editions from an existing Calibre `metadata.db` via Settings → Calibre → Library import. Incremental and idempotent; progress bar with live stats.
- **Calibre Settings tab** — Calibre settings extracted from the General tab into a dedicated tab. Eliminates the duplicate `library_path` field; single shared path at the top used by both write integration and library import. Adds "Test paths" button for drop-folder mode (validates `metadata.db` readable, drop folder writable).
- **Stalled download detection** ([#142](https://github.com/vavallee/bindery/issues/142)) — background job detects qBittorrent `stalledDL` torrents and Transmission stopped-with-error states. Automatically marks failed, blocklists the release, and triggers a re-search.

### Fixed

- **calibredb error messages** ([#160](https://github.com/vavallee/bindery/issues/160)) — "no such file or directory" now includes the path and explains the binary must be accessible inside the Bindery container, not just on the Docker host.
- **Image proxy cache** ([#138](https://github.com/vavallee/bindery/issues/138)) — sharded cache directories, LRU eviction, and atomic writes prevent corruption on large libraries.
- **Calibre import when write mode is off** — library import no longer incorrectly gated behind the write-mode toggle.
- **Calibre crash recovery** — pod restarts between `Create` and `SetCalibreID` no longer produce duplicate book rows.
- **Author sync duplicate constraint** — UNIQUE constraint on `books.foreign_id` during concurrent author syncs treated as benign skip.
- **TOCTOU-safe file copy** — importer uses `os.Root` for directory copy/hardlink to prevent symlink traversal.

### Upgrade notes

- **Schema:** migration `018_calibre_sync.sql` adds tables for Calibre library import. Drop-in binary or image replacement is safe.

## [v0.8.0] — 2026-04-14

Major feature release. Calibre users can finally automate the last mile — finished Bindery imports land in Calibre with no manual "Add books" step. Library curation gets a sharper tool: the author list stops fragmenting into "RR Haywood" / "R.R. Haywood" / "R R Haywood" duplicates, and the new **Merge authors** flow reunites them under one canonical row. Backend test coverage continues its climb, with `internal/api` and `internal/importer` both breaking 60%.

### Added

- **Calibre library integration via `calibredb`** ([#32](https://github.com/vavallee/bindery/issues/32)) — after a successful import, Bindery mirrors the book into a configured Calibre library by shelling out to `calibredb add --with-library <path>` and stores the returned Calibre book id on the Bindery book row for future OPDS and cross-library lookups. Opt-in under Settings → General → Calibre with three fields (enabled / library path / binary path) and a **Test connection** button that probes `calibredb --version`. Failures during the Calibre call are logged and swallowed so a missing binary or unreachable library never rolls back an otherwise-good Bindery import. Matches the Path A approach on the roadmap — the looser-coupled drop-folder / OPDS paths remain planned.
- **Author aliases — merge duplicate authors** ([#45](https://github.com/vavallee/bindery/issues/45)) — new `author_aliases` table plus a **Merge authors** modal on the Authors page (and a Merge button on each author's detail page). Picking a source and target reparents the source's books onto the target, deletes the source row, and preserves the source's name + OpenLibrary id as aliases pointing at the target. The add-author flow now consults the alias table: if the requested name already resolves to an existing author, the POST returns `409 Conflict` with `canonicalAuthorId` so the UI can prompt for merge instead of silently ingesting a duplicate. Two new endpoints: `GET /api/v1/author/{id}/aliases` and `POST /api/v1/author/{id}/merge`. The merge is transactional — if any child update fails, nothing changes.

### Changed

- **Backend test coverage raised to 60%+ on the two laggards** — `internal/api` now 62.7% (was 40.5%), `internal/importer` 62.2% (was 40.7%). New `_test.go` files cover the settings / custom-formats / delay-profiles / files / import-lists / migrate / notifications / quality-profiles / search / series handlers and the importer's `titleMatch` / tokenize / path-remap helpers.

### Upgrade notes

- **Schema:** two additive migrations land (`008_calibre.sql` adds a `calibre_id INTEGER` column on `books` plus three `calibre.*` settings rows; `009_author_aliases.sql` adds the `author_aliases` table). Drop-in binary or image replacement is safe.
- **Calibre is off by default.** Existing installs are unaffected until you flip the toggle in Settings → General → Calibre. The `calibredb` binary must be reachable from the Bindery process — in Docker this means either bind-mounting a calibre install or picking an image that ships `calibredb` on the PATH. A future release may publish a `bindery-calibre` image variant; track progress on [#32](https://github.com/vavallee/bindery/issues/32).
- **No duplicate-author migration is run automatically.** Existing fragmented author rows stay as-is until you merge them manually via the new modal — this is intentional, since "are these two rows the same person?" needs a human in the loop.

## [v0.7.2] — 2026-04-14

Quality release. Bulk actions land for users curating large libraries (the painful-after-CSV-import flow), the silent library-scan bug is fixed, and backend coverage jumps from 34% to 53% to quiet codecov and harden the regression safety net.

### Added

- **Multi-select / bulk actions on Authors, Books, and Wanted** ([#12](https://github.com/vavallee/bindery/issues/12)) — row checkboxes with "Select all on this page" in table headers (and overlay checkboxes on grid cards), plus a sticky `BulkActionBar` footer that appears whenever any items are selected. Authors support Monitor / Unmonitor / Search / Delete; Books additionally support Set Ebook / Set Audiobook; Wanted supports Search / Unmonitor / Blocklist (marks book as skipped and unmonitored). Three new endpoints: `POST /api/v1/author/bulk`, `POST /api/v1/book/bulk`, `POST /api/v1/wanted/bulk`. All return a per-ID result map at HTTP 200 so partial failures (e.g. a stale ID) report inline without aborting the rest of the batch.

### Changed

- **Backend test coverage raised to ≥50% (52.8% total)** — new `_test.go` files added for `internal/db`, `internal/downloader/qbittorrent`, `internal/metadata` (aggregator), `internal/metadata/googlebooks`, `internal/metadata/hardcover`, `internal/metadata/openlibrary`, `internal/notifier`, and `internal/scheduler`. No production code was modified.

### Fixed

- **Manual library scan silently aborted** ([#55](https://github.com/vavallee/bindery/issues/55)) — `POST /api/v1/library/scan` spawned the scan goroutine with the HTTP request context, which Go cancels the moment the 202 response is sent; the scan now uses `context.WithoutCancel` so it always runs to completion.

## [v0.7.1] — 2026-04-14

Build-pipeline patch. No code changes — re-cuts the v0.7.0 binary archives against a fixed GoReleaser config so the Windows / macOS / Linux downloads actually contain the frontend.

### Fixed

- **Standalone binary UI served only `.gitkeep`** — every GoReleaser-built archive since GoReleaser was introduced shipped with the `.gitkeep` placeholder as the embedded frontend instead of the built React app. Root cause: `.goreleaser.yaml` ran `npm run build --prefix web` (output lands in `web/dist/`) but never copied the artefacts into `internal/webui/dist/` where the `go:embed` directive reads from. Both the `Makefile` build and the `Dockerfile` image build had the copy step; the GoReleaser path was the only one that missed it. Fixed by adding an equivalent copy hook. The v0.7.0 **Docker image** (`ghcr.io/vavallee/bindery:v0.7.0`) was **not** affected — this only applies to users who downloaded a binary archive from the v0.7.0 Release page.

### Upgrade notes

- If you downloaded a v0.7.0 binary and saw only `.gitkeep` in the browser, re-download the v0.7.1 archive for your platform. No database migration, no config change.
- Docker / Helm deployments on `ghcr.io/vavallee/bindery:v0.7.0` do not need to move — they were built from a different pipeline and work correctly. Moving to `:v0.7.1` is fine (identical behaviour) but not required.

## [v0.7.0] — 2026-04-14

Polish & onboarding release. Fixes the "added an author, nothing happened for 12 hours" gap that new Sonarr/Radarr migrants hit on day one, fills in the long-broken Series view, and tightens the list-page experience with filters, sorting, and cross-page navigation.

### Fixed

- **Series view always empty** ([#46](https://github.com/vavallee/bindery/issues/46)) — the `/series` endpoint returned nothing because `series` and `series_books` rows were never populated during author ingestion. OpenLibrary's `series` field is now parsed from author-works responses and single-work lookups; after a successful book insert the corresponding `series` row is upserted (by a stable title-derived slug) and a `series_books` link is created with the book's position and primary-series flag.
- **Books page "Sort by newest/oldest" broken** — sort now uses the book's release date rather than its DB insertion timestamp, so chronological ordering matches the year shown in the UI.
- **Books page missing author** — the table now shows an Author column (linked to the author page) and the grid view shows the author name under each cover.

### Added

- **Auto-search on author add** ([#11](https://github.com/vavallee/bindery/issues/11)) — when an author is added with "Start search for books on add" enabled (default on), Bindery immediately fires an indexer search for each wanted book after fetching the author's catalogue from OpenLibrary. Previously users had to wait up to 12 hours for the first automatic grab. The search is gated on the author being monitored; unmonitored authors are unaffected.
- **Auto-search on book status transition to wanted** — updating a book's status to `wanted` (e.g. via "Delete file" → flips imported → wanted, or a manual status edit via the API) now triggers an immediate indexer search. Same logic as the 12-hour scheduler job. Always-on for v0.7.0; a `search_on_status_change` setting can be added later if opt-out is requested.
- **"Start search for books on add" checkbox** in the Add Author modal (default checked), matching Sonarr's phrasing. Uncheck to add an author without an immediate search.
- **`bindery reconcile-series` CLI subcommand** — re-fetches OpenLibrary series data for every already-ingested author and backfills the series/series_books tables. Run once after upgrading from any version that did not populate series during ingestion. Idempotent; prints `{"linked":<n>,"skipped":<n>}` on completion. See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md#from-v06x-to-v070) for usage.
- **Authors page filter by monitored status** — filter chips for `Monitored` / `Unmonitored` / `All`, persisted per-page to localStorage.
- **Author Detail page — publication dates, filters, sort** — books now show a Published column (sortable asc/desc by release date), with filter chips for Type (Ebook/Audiobook), Status (Wanted/Downloaded/Imported/Missing), and Publication state (Released/Upcoming).
- **Book Detail page shows full metadata** — author (linked), series (linked), description, publication date, language.
- **Wanted page navigation** — the book title and author on each Wanted row are now clickable links into the Book Detail and Author Detail pages.

### Changed

- **`NewAuthorHandler` signature** — now takes `*db.SeriesRepo` and `BookSearcher` arguments (added for series upsert and auto-search). Internal API; only callers in `cmd/bindery/main.go` are affected. No REST API change.
- **`NewBookHandler` signature** — now takes a `BookSearcher` argument for the wanted-transition hook. Same scope note.

## [v0.6.4] — 2026-04-14

### Fixed
- **Search returns zero results on many indexers** — two root causes addressed ([#48](https://github.com/vavallee/bindery/pull/48)):
  - *Hardcoded categories:* indexer categories were permanently set to `[7000, 7020]` with no UI to change them. Indexers using non-standard category IDs (e.g. SceneNZBs: **7120** for German books, **3130** for German audio) returned zero results because the `cat=7000,7020` query matched nothing. Both the Add and Edit indexer forms now expose a comma-separated categories input. `filterCategoriesForMedia` already routes 7xxx IDs to ebook searches and 3xxx IDs to audiobook searches — no backend changes needed.
  - *`filterRelevant` phrase-match trap:* a batch-level `anyPhraseMatch` gate disabled keyword fallback for the **entire** result set if any single result happened to have the significant title keywords adjacent. For titles like *"The Name of the Wind"* (`sigWords: ["name","wind"]`), the phrase pattern `\bname\W+wind\b` fails on the correct release because stop words (`"of the"`) sit between the keywords. An abbreviated result (`"Name.Wind.epub"`) could trigger the gate, causing all correctly-titled releases to be dropped. The gate is removed; each result is now evaluated independently — phrase match first, keyword fallback always allowed.

## [v0.6.3] — 2026-04-14

### Fixed
- **Standalone binaries (Windows, macOS, Linux) shipped with no UI** — visiting port 8787 showed only `.gitkeep`. GoReleaser's `before.hooks` only ran `go mod download`; the `npm run build` step ran in the Docker job but not before the cross-compile. Added `npm ci` + `npm run build` to `.goreleaser.yaml` so `internal/webui/dist/` is populated for all release targets ([#44](https://github.com/vavallee/bindery/pull/44)).
- **Protocol-aware download routing** — torznab indexers now route grabs to qBittorrent and newznab indexers route to SABnzbd. Previously the scheduler and manual grab both hardcoded `protocol: "usenet"`, so torrent results were sent to SABnzbd and failed silently ([#41](https://github.com/vavallee/bindery/pull/41)).
- **qBittorrent client form** — the Settings form now shows Username/Password fields for qBittorrent clients (instead of API Key), resets credentials on client-type change, and the Test button dispatches to the correct client type ([#40](https://github.com/vavallee/bindery/pull/40)).
- **Media-type client selection** — when multiple download clients are configured, Bindery now prefers a client whose category contains "audio" for audiobook grabs ([#41](https://github.com/vavallee/bindery/pull/41)).
- **Scan Library button had no feedback** — clicking the button returned silently because the endpoint returns 202 immediately (scan is async). Now shows a green "Scan started" confirmation for 5 seconds.

### Added
- **Per-page size persists across tabs** — the page-size selector in paginated views is stored in `localStorage` per page so the choice survives navigation ([#37](https://github.com/vavallee/bindery/pull/37)).
- **Grab feedback on Wanted page** — the Grab button shows a spinner while the request is in flight and a ✓ check on success before closing the results list ([#38](https://github.com/vavallee/bindery/pull/38)).
- **Manual library scan button** — Settings → General now has a Scan Library button that triggers an immediate background reconciliation ([#39](https://github.com/vavallee/bindery/pull/39)).

### Changed
- Test coverage improved from ~26% to ~33%: new tests for `DownloadRepo`, `BlocklistRepo`, `HistoryRepo`, `UserRepo`, `PickClientForMediaType`, virtual credential round-trips, API handlers (download clients, indexers, tags, library scan), `titleMatch`, `protocolForType`, `dedupe`, `IsArticle`, `ParseMode`, and more.

## [v0.6.2] — 2026-04-14

Bug-fix release on top of v0.6.1.

### Fixed
- **Windows binary exits immediately** ([#7](https://github.com/vavallee/bindery/issues/7)): the default `BINDERY_DB_PATH` was hardcoded to the Linux-container path `/config/bindery.db`. On Windows, `os.MkdirAll("/config", …)` failed, the preflight write probe returned an error, and because the process was spawned from an Explorer double-click the cmd window closed before the user could read the log line. Defaults are now platform-aware via `os.UserConfigDir`: `%APPDATA%\Bindery\bindery.db` on Windows, `~/Library/Application Support/Bindery/bindery.db` on macOS, unchanged `/config/bindery.db` on Linux (existing Docker / Helm / bare-metal deployments are untouched). The resolved paths are emitted in the `"starting bindery"` startup log line so `bindery.exe` runs from `cmd` will surface them even if db.Open later fails.
- **Header nav overflowed into a horizontal scrollbar** at mid viewport widths (≈768–1024px). Desktop nav + version label + sign-out now collapse into the hamburger menu at `lg` (1024px) instead of `md` (768px), and the right-hand cluster is `flex-shrink-0` so it stops being squeezed by the nav tabs.

### Changed
- CI now uploads Go coverage to Codecov (`codecov/codecov-action@v5`) on both the build and validate jobs, with a `.codecov.yml` that marks project/patch checks as informational so coverage dips don't block PRs.

## [v0.6.1] — 2026-04-14

v0.6.1 is the first installable build of the v0.6.0 feature set. The `v0.6.0` tag itself failed GoReleaser cross-compile: `describeDir` referenced `syscall.Stat_t` (POSIX-only) so `GOOS=windows` builds aborted and no binaries or `ghcr.io/vavallee/bindery:0.6.0` image were ever published. See v0.6.0 below for the full feature list.

### Fixed
- Split `describeDir` (the Linux ownership hint in the SQLite "can't open" error path) into `describe_unix.go` (POSIX uid/gid via `syscall.Stat_t`) and `describe_windows.go` (path + mode only) via `//go:build` tags. No runtime behaviour change on Linux; unblocks `windows/amd64` and `windows/arm64` release binaries.

## [v0.6.0] — 2026-04-14

### Authentication overhaul

Replaces the single-env-var API key gate with a full Sonarr-parity auth model. Upgrading from v0.5.x: the first launch after upgrade detects no user, redirects to `/setup`, and you create an admin account. `BINDERY_API_KEY` is still honoured as a seed for the new DB-stored key so existing integrations keep working on restart; after that it is inert (the key can be regenerated in-app).

#### Added
- **Password-based login** — first-run `/setup` flow creates a single administrator account. Passwords hashed with argon2id (OWASP 2024 parameters). Minimum 8 characters enforced client-side.
- **Signed session cookies** — self-contained HMAC-SHA256 cookies (no server-side session table). `bindery_session` is `HttpOnly` + `SameSite=Lax`. 30-day "remember me" or 12-hour default. `Secure` is intentionally left unset because TLS is usually terminated upstream (Traefik); front with a proxy that adds `Strict-Transport-Security` if you need strict HTTPS-only cookies.
- **Three auth modes** — `enabled` (always require login), `local-only` (bypass auth for RFC1918 / loopback / link-local / IPv6 ULA), `disabled` (no auth — only for trusted reverse-proxy deployments). Toggle in **Settings → General → Security**. Sonarr v4 parity.
- **Per-account API key** — auto-generated on first boot, visible/regenerable in the Settings Security panel. Accepts `X-Api-Key` header or `?apikey=` query param. Independent of the session cookie so scripts, `curl`, Tautulli, etc. work without cookies.
- **Login rate limiting** — per-IP sliding window, 5 failures / 15 minutes, returns `429`. Blocks credential-stuffing on internet-exposed deployments.
- **New endpoints** — `GET /auth/status`, `POST /auth/login`, `POST /auth/logout`, `POST /auth/setup`, `GET /auth/config`, `POST /auth/password`, `POST /auth/apikey/regenerate`, `PUT /auth/mode`.

#### Changed
- `/api/v1/*` is now authenticated by default (previously optional). Health, auth status/login/logout/setup, and the setup flow itself are exempt.
- `BINDERY_API_KEY` is now a **seed-only** bootstrap variable. If set on first boot, the generated key matches it; on subsequent boots the stored DB value wins. Setting the env var on an already-initialised instance has no effect.
- `auth.api_key`, `auth.session_secret`, and `auth.mode` settings are filtered out of the generic `GET /setting` and `GET /setting/{key}` endpoints — they are readable only via `/auth/config` for the authenticated admin.
- Frontend: added `/login` and `/setup` routes, an `AuthProvider` + `AuthGuard` that redirect unauthenticated users, a "Sign out" action in the header, and a Security section in Settings → General.

#### Fixed
- Middleware was treating `/auth/status` as an unauth-allowed path *before* verifying the session cookie, so the endpoint always reported `authenticated: false`. Valid logins still set the cookie correctly but the UI bounced right back to `/login`. Cookie verification now runs for every request; the unauth-allow list only controls the 401 rejection.
- Login and setup forms now read values via `FormData` on submit instead of relying on React-controlled state. Browser password-manager autofill populates `input.value` without firing `onChange`, which left React state empty and silently disabled the submit button.

### PUID/PGID startup sanity check ([#13](https://github.com/vavallee/bindery/issues/13))

Bindery ships on distroless/static-debian12:nonroot — no shell, no `gosu`, so the container cannot switch user at runtime the way LinuxServer.io images do. The common failure mode is: operator sets `PUID=1000` / `PGID=1000` in their `.env` expecting LSIO semantics, but forgets the matching `--user` / `runAsUser`; Bindery silently runs as UID `65532`, and the first write to `/config` or the library mount fails with an opaque `permission denied`.

This release turns that into a loud, actionable startup error. When `BINDERY_PUID` or `BINDERY_PGID` is set but does not match `os.Getuid()` / `os.Getgid()`, Bindery logs the mismatch along with the exact `docker run --user`, `docker-compose user:`, and `securityContext.runAsUser` snippets that would fix it, then exits non-zero. Leaving both variables unset preserves the previous behaviour (no check, runs as the distroless default UID). Non-Linux builds skip the check entirely (`Getuid` / `Getgid` return `-1` on Windows).

The README's **Configuration → Running as a specific UID/GID** section documents the Docker / compose / k8s patterns end-to-end.

A follow-up ticket (to be opened after v0.6.0) tracks the larger LSIO-style variant image with a gosu entrypoint that actually switches user at runtime — the Bindery team didn't want to ship a second image this cycle.

### Author delete can sweep files ([#15](https://github.com/vavallee/bindery/issues/15))

`DELETE /api/v1/author/{id}?deleteFiles=true` now walks every book's `file_path` and removes it from disk before the DB cascade takes the rows out. Paths are collected *before* the delete (the cascade wipes the book rows that hold them, so a post-delete walk would find nothing). Per-path errors are logged but don't abort the response — the author is already gone and a partial sweep is better than rolling the whole thing back.

The UI confirm dialogs on the Author list and detail pages peek at each author's books, name the file/folder count in the confirmation message, and pass `deleteFiles=true` when the user OKs. Authors with no files on disk get the old plain confirm.

Closes the orphan-files gap reported against the Jared Diamond delete after #9 landed.

### Metadata language filter ([#14](https://github.com/vavallee/bindery/issues/14))

Foreign-language works from OpenLibrary/Hardcover/Google Books were landing in the library regardless of the user's preferred language. The `metadata_profiles` table (seeded in migration 003) already carried `allowed_languages='eng'` by default, but nothing consulted it — author-book ingestion filtered against a separate global `search.preferredLanguage` setting, and authors were never linked to a profile.

#### Added
- Author record now carries `metadata_profile_id`; `POST /author` and `PUT /author/{id}` accept `metadataProfileId`. New authors default to the seeded "Standard" profile (id=1) so the language filter applies out of the box.
- Metadata profile editor in **Settings → Metadata Profiles** — create/edit profiles with a language multi-select (English, French, German, Dutch, Spanish, Italian, Portuguese, Japanese, Chinese, Russian). Empty selection = accept any language.
- Metadata profile picker in the Add Author modal (shown when more than one profile exists).

#### Changed
- `FetchAuthorBooks` now filters against the author's metadata profile's `allowed_languages` CSV instead of the global `search.preferredLanguage` setting. Books with an unknown language are always kept (data-availability varies by provider).

#### Security notes
- Sessions use `SameSite=Lax`, which mitigates cross-site form-submission CSRF. An explicit CSRF token pass is on the roadmap.
- OIDC / SSO and reverse-proxy header trust are explicitly out of scope for this release; see the Roadmap in the README for the planned path.

## [v0.5.2] — 2026-04-13

### Security & hardening

Followed up v0.5.1 with a gosec audit pass. One HIGH-severity finding was real; the rest were false positives (taint analysis couldn't see input validation). Fixed the real issue and tightened two adjacent MEDIUM items.

#### Fixed
- **Remote filesystem deletion via book update (HIGH).** `PUT /api/v1/book/{id}` previously accepted a `filePath` field and wrote it to the book record unchecked. A caller could then trigger `DELETE /api/v1/book/{id}?deleteFiles=true` (or `DELETE /api/v1/book/{id}/file`) to run `os.RemoveAll` on that path — unbounded by the library dir. When `BINDERY_API_KEY` is unset (a warn-only configuration) this is unauthenticated. `filePath` is now omitted from the update schema; it remains internally-set by the importer after a successful grab.
- **Multipart upload error response.** `/api/v1/migrate/csv` and `/api/v1/migrate/readarr` already capped body size via `http.MaxBytesReader`, but passed `nil` as the ResponseWriter, so oversize requests surfaced as a generic 400. They now pass `w`, so oversize uploads receive a proper `413 Request Entity Too Large`.

#### Changed
- Backup directory (`<data>/backups`) is now created with mode `0700` instead of `0755`. SQLite snapshots there may contain indexer/client credentials in plaintext rows; only the bindery process should read them.
- Library and audiobook directories created by the importer are now `0750` (was `0755`). Host users needing read access should run bindery with a matching UID/GID (standard `PUID`/`PGID` pattern used by Readarr/Sonarr containers).

## [v0.5.1] — 2026-04-13

### Packaging & cross-platform

#### Fixed
- **Multi-arch Docker image.** `ghcr.io/vavallee/bindery` is now published as a multi-arch manifest covering `linux/amd64` and `linux/arm64`. Previously only `amd64` was built, so `docker compose pull` on Apple Silicon (and Raspberry Pi 4 / 5 running 64-bit Docker) failed with `no matching manifest for linux/arm64/v8 in the manifest list entries`. The Dockerfile now cross-compiles Go natively via buildx `TARGETOS` / `TARGETARCH` build args, so the arm64 variant builds on the amd64 runner without QEMU emulation overhead. Fixes #4.

#### Added
- **Pre-built release binaries** attached to every GitHub Release via GoReleaser. Targets: linux (amd64/arm64/armv7/armv6), macOS (amd64/arm64), windows (amd64/arm64). A `bindery_vX.Y.Z_checksums.txt` file is published alongside the archives for integrity verification. Raspberry Pi 4 / 5 use the `linux_arm64` archive; Pi 2 / 3 running 32-bit use `linux_armv7`; Pi Zero / Pi 1 use `linux_armv6`.

## [v0.5.0] — 2026-04-13

### Audiobook support + Readarr-parity UX + migration paths

### Import cleanup
- Ebook import no longer leaves the SABnzbd job folder behind. After every book file matches bindery's extension set and moves cleanly, the importer removes the source directory — PAR2, NFO, SFV, NZB, and sample leftovers go with it. Partial-failure runs are untouched so the files remain for investigation.
- Audiobook import handles destination collisions. `UniqueDir` resolves `{Author}/{Title} ({Year})` against the filesystem and appends ` (2)`, ` (3)`, … when a prior import or manual copy already occupies the slot. Previously `MoveDir` hard-failed on any collision and the download stuck at `Completed` forever.
- SABnzbd history is pruned once bindery owns the files. New `DeleteHistory(nzoID, deleteFiles=false)` on the SAB client is called after each successful import so completed rows stop accumulating in SAB's UI with stale storage paths.
- **Remote path mapping** (`BINDERY_DOWNLOAD_PATH_REMAP`). When SABnzbd and bindery run in separate containers with the shared storage mounted at different paths, SAB would report a completed job at `/downloads/complete/X` and bindery would fail to find it under its own mount point — logging `no book files found in download` and leaving files in SAB's completed dir forever. The new env var accepts comma-separated `from:to` pairs (e.g. `/downloads:/media`), applied longest-prefix-first to each path before the importer walks it. Same-filesystem installs leave it unset and see no behaviour change.

### Audiobook support
- Books now carry a `media_type` (`ebook` | `audiobook`) that drives indexer categories, ranking, library destination, and UI badges. Flip per-book inline on the Wanted page or via the Book detail page.
- Search pipeline: `filterCategoriesForMedia` narrows indexer queries to the Newznab audio tree (3030) for audiobook books and the books tree (7000 range) for ebooks, with a fallback to the standard category when the indexer's configured set has nothing matching.
- Ranking applies a −500 media-type-mismatch penalty and +250 for ASIN exact matches parsed from release titles. `isAudiobookFormat` recognises `m4b` / `m4a` / `mp3` / `flac` / `ogg`.
- Import pipeline: audiobook grabs move the entire download directory as one unit via `MoveDir` (multi-part `m4b` / `mp3` + cover art + cue sheet stay together) into `BINDERY_AUDIOBOOK_DIR` (falls back to `BINDERY_LIBRARY_DIR` if unset). Naming template defaults to `{Author}/{Title} ({Year})` — preserves original filenames inside.
- Audnex metadata provider (`api.audnex.us`, no auth) fetches narrator, duration, cover, and description by ASIN. Endpoint: `POST /api/v1/book/{id}/enrich-audiobook`.
- Release parser extracts Audible-shaped ASINs (`B[0-9A-Z]{9}`) from NZB titles; `UNABRIDGED` / `ABRIDGED` / `RETAIL` edition flags now factor into ranking.
- Raw per-article Usenet postings (`.part09.rar`, `.vol003+004.par2`, `.sfv`, `yEnc`, `[12/22]` brackets) filtered out of search results before ranking so multi-part noise no longer buries clean `[M4B]` releases.

### Readarr-parity UX
- **Book and author detail pages** at `/book/:id` and `/author/:id` — routed, deep-linkable, replace the previous click-opens-modal flow. Book detail hosts cover, metadata, format toggle, ASIN field, audnex enrich button, inline search-and-grab, and per-book history. Author detail shows portrait + stats + description + Monitored/Refresh/Delete actions + their books as a mini grid.
- **Grid / Table view toggle** on Books and Authors pages (persists per-page in localStorage). Books table: thumbnail + title, author, year, type, status with responsive column hiding on phones. Authors table: avatar + name, book count, rating, Monitored toggle, inline Refresh/Delete.
- History page adds **Size** and **Type** columns (desktop table + mobile card) — type auto-detected from the release title's format tokens.
- Books tab: audiobook corner badge on cards; inline `<select>` per row on Wanted persists media type via `PUT /api/v1/book/{id}`.

### Migration paths
- `POST /api/v1/migrate/csv` — upload a newline-separated list of author names or a `name,monitored,searchOnAdd` CSV. Each name resolved via OpenLibrary.
- `POST /api/v1/migrate/readarr` — upload `readarr.db`. Authors re-resolved via OpenLibrary (Goodreads IDs aren't portable since bookinfo.club is dead); Indexers / Download clients / Blocklist entries port structurally.
- `bindery migrate csv <path>` and `bindery migrate readarr <path>` CLI subcommands — exit with JSON summary.
- **Settings → Import** tab with file uploads and per-section result cards showing requested / added / skipped / failures.

### Infrastructure
- `development` branch joins `main` in CI — builds push `:development` + `:dev-<sha>` images and auto-bump `charts/bindery/values.yaml`. Point ArgoCD `targetRevision` at `development` to follow dev builds.
- Version badge shows `dev-<sha>` on development builds, `sha-<sha>` on main builds, or `v0.4.x` on tagged releases.
- File download endpoint (`/api/v1/book/{id}/file`) now streams a zip when the book's `file_path` is a directory (audiobook folders come down as a single archive).
- Background download-status poll tightened from 60s to 15s so imported status lands in near-real-time after SABnzbd finishes.
- Fixed a `rankResults` bug where precomputed scores were read from stale indices during the in-place sort — composite ranking effectively fell back to indexer-return order. Now zips score with result and sorts pairs. Regression test added.

### Added (smaller)
- `/book/{id}/enrich-audiobook` endpoint (audnex).
- Foreign-language tag filter now word-boundary-anchored (the tag `RUSSE` no longer substring-matches inside `RUSSELL`).
- Book PUT handler accepts `mediaType` / `asin` / `narrator` fields (was silently dropping them).
- **Delete downloaded files from the UI.** Book detail page gains a red "Delete file" action that wipes the on-disk file (ebook) or folder (audiobook) and flips the book back to `wanted`, plus a "Delete book + files" action that removes the record and its files in one go. New endpoints: `DELETE /api/v1/book/{id}/file` and `DELETE /api/v1/book/{id}?deleteFiles=true`. A `bookFileDeleted` history event is recorded so the deletion is auditable.
- **Skip OpenLibrary "works" whose title equals the author's name.** An upstream OL data-quality bug occasionally emits works (e.g. `OL29342228W` for Jared Diamond) where the Work record was never given a title and the API falls back to the author's name. These polluted the Wanted page and produced nonsense destination folders like `Jared M. Diamond/Jared M. Diamond ()`. `FetchAuthorBooks` now filters them out at ingest time and counts the skips in its summary log.

## [v0.4.2] — 2026-04-12

### Light mode

#### Added
- Light theme using a slate palette, with an iOS-style toggle in **Settings → General → Appearance**. First-load default respects the browser's `prefers-color-scheme`; saved preference lives in `localStorage` under `bindery.theme` and syncs instantly across tabs via the `dark` class on `<html>`.
- Pre-paint bootstrap script in `index.html` applies the saved theme before React hydrates, eliminating the dark-to-light flash on page load.
- New `useTheme` hook (`web/src/theme.ts`) and `ThemeToggle` component (`web/src/components/ThemeToggle.tsx`) that both modules outside Settings can reuse later.

#### Changed
- Every hardcoded `zinc-*` utility class across the UI (App shell, all 10 pages, Pagination, AddAuthorModal) now has a paired `dark:` variant. Light mode is the default, dark mode activates when `<html>` has the `dark` class. No semantic-color token refactor — just the standard Tailwind class-based strategy.
- `tailwind.config.js` was already set to `darkMode: 'class'` — no config change needed.

## [v0.4.1] — 2026-04-12

### Security & quality patch

#### Fixed
- Rebuilt against go1.25.9, clearing 17 stdlib CVEs reachable via the API, TLS, and URL-parsing paths (most notably GO-2026-4870 TLS KeyUpdate DoS, GO-2025-4012 cookie memory exhaustion, GO-2025-4009 PEM quadratic complexity, GO-2025-4007 x509 name-constraint quadratic).
- Repaired `.golangci.yml` — removed `gosimple` (absorbed into `staticcheck` in lint v2) and dropped `continue-on-error` on the lint job. The lint CI gate had been silently failing since the v2 upgrade.
- qBittorrent client no longer panics on session-expiry retry when `http.NewRequestWithContext` fails — the error is now propagated instead of calling `Do` on a nil request.
- API handlers that take `{id}` in the URL path now return HTTP 400 for non-numeric IDs instead of silently acting on ID 0. New `parseID` helper in `internal/api/helpers.go` consolidates the pattern.
- Library-scan importer no longer dereferences nil pointers when a book or author lookup fails; lookup errors are logged and the file falls through to the unmatched-import path.
- History-blocklist handler logs corrupt JSON `data` columns instead of silently returning a zero-value event to the client.
- SQL UPDATE in `downloads.UpdateStatus` no longer interpolates a column name via `fmt.Sprintf`. Three explicit statements, one per known status, with the column name as a fixed literal.
- Primary HTTP server now sets `ReadHeaderTimeout` / `ReadTimeout` / `WriteTimeout` / `IdleTimeout` instead of running with the defaults (which are effectively unlimited). Mitigates slow-loris and resource-exhaustion attacks on the public API surface.

#### Added
- Startup warning logged when `BINDERY_API_KEY` is unset, making it obvious that `/api/v1/*` is unauthenticated.
- Helm chart `deployment.yaml` now sets a hardened pod+container `securityContext`: `runAsNonRoot: true`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile.RuntimeDefault`, plus a writable `emptyDir` mounted at `/tmp`.
- CI workflow now auto-creates a GitHub Release with notes extracted from the matching CHANGELOG section on every `v*` tag push. Title is derived from the first `###` subheading. Idempotent — updates notes if a release already exists.

#### Changed
- Dockerfile base image pinned to `golang:1.25.9-alpine` (was `golang:1.25-alpine`) and runtime switched to `gcr.io/distroless/static-debian12:nonroot` with `USER nonroot`.
- `go.mod` bumped to `go 1.25.9`.
- Internal DB queries using `sql.ErrNoRows` comparison now use `errors.Is` for wrap-safety.

#### Frontend
- Fixed four missing `reset` dependencies in `useEffect` hooks on Authors / Books / History / Wanted pages (react-hooks/exhaustive-deps).
- Extracted `usePagination` hook into its own file (`web/src/components/usePagination.ts`) so `Pagination.tsx` is a pure component module (fixes react-refresh/only-export-components).

## [v0.4.0] — 2026-04-12

### Search overhaul

Inspired by the matching patterns in Readarr, Sonarr, and LazyLibrarian.
Fixes the long-standing "short titles get junk results" problem (e.g.
searching "The Sparrow" by Mary Doria Russell no longer returns unrelated
sparrow-themed books, comics, and music releases).

#### Added
- **Four-tier query fallback** in `BookSearch`: `t=book` → `surname+title`
  → `author+title` → title-only. The new surname+title tier disambiguates
  short titles without the noise of full-name queries that some indexers
  fail to match.
- **Word-boundary keyword matching** (`\b...\b`) everywhere in the filter
  and language checks. `sparrow` no longer leaks into `sparrowhawk` or
  `sparrows`.
- **Contiguous-phrase matching** for multi-word titles. A release must
  contain the title words together; scattered occurrences no longer pass.
- **Subtitle handling** for `Title: Subtitle` books. "Dune: Messiah"
  accepts releases tagged as either "Dune" or "Dune Messiah".
- **Composite ranking score**: quality × 100 + edition tag (RETAIL +50,
  UNABRIDGED +30, ABRIDGED −50) + year-match (±20/10/5) +
  log₁₀(grabs) × 10 + size tiebreaker + ISBN exact-match +200.
- **Release parser** (`internal/indexer/release.go`): extracts year,
  format, RETAIL/UNABRIDGED/ABRIDGED flags, release group, and ISBN from
  NZB titles.
- **Blocklist consulted during search** (both manual and auto-grab). The
  infrastructure existed but was never wired into the search flow.
- **Download quality populated on grab** via the new release parser, in
  both the manual grab handler and the scheduler auto-grab path.
- 23 new unit tests covering the matching and ranking pipeline.

#### Fixed
- Scheduler now resolves and passes the book's author name to `SearchBook`
  (previously always empty, which silently disabled the `t=book` tier,
  the `author+title` tier, and the filter's surname anchor for every
  automated search).
- Foreign-language tag filter now word-boundary-anchored. The tag `RUSSE`
  (French for "Russian") was substring-matching inside `RUSSELL`, causing
  books by authors named Russell, Russ, Russo, etc. to be rejected as
  Russian-language releases.

#### Changed
- `Searcher.SearchBook` signature: now takes `MatchCriteria{Title, Author,
  Year, ISBN}` instead of `(title, author)` so ranking can use year and
  ISBN signals.

#### Deliberately out of scope
- qBittorrent grab path and `Download.Protocol` handling (bigger refactor
  planned separately).
- Readarr-style user-facing Quality Profiles (overkill for a single-user
  tool; hardcoded weights serve 95% of cases).

## [v0.3.0] — 2026-04-12

### Added
- Mobile browsing support: responsive layout, hamburger nav, card views
  for History / Blocklist, agenda view for Calendar.
- Blocklist-from-history action for grabbed/failed events (one-click add).
- Preferred language filter for download search results (English default).
- Quick search filter on the Wanted page.
- Inline edit + enable/disable toggles for indexers, clients, and
  notifications in Settings.
- GitHub profile link in the footer.
- "No results" message when indexer search returns empty (previously
  silent).

### Fixed
- Scanner false matches; tightened title matching in library scan.
- Non-English books incorrectly ingested from OpenLibrary author works.
- `imported` books now display as "In Library" in Books page; removed the
  transient `downloaded` filter.
- Version badge only shown for tagged releases; short SHA for branch
  builds.

### Changed
- CI pushes `:latest` image tag on version-tag builds.
- Image SHA tags shortened to 7 chars.

## [v0.2.0] — 2026-04-12

### Added
- Full Readarr feature parity: tag system, metadata profiles, import
  lists, quality profiles with cutoffs, custom formats, delay profiles,
  notifications, backup/restore, and API key authentication.
- Authors / Books / Wanted / History / Blocklist list pagination.
- History page shows error details; grab events are recorded.
- Download error messages surfaced in queue UI.
- `downloaded` status filter + badge on Books page.
- App logo and favicon.

### Fixed
- OpenLibrary author works endpoint now used for accurate book fetching.
- Author search results show top work, book count, and ratings.
- Version / commit / build-date injected into Docker image via ldflags.

## [v0.1.0] — 2026-04-11

Initial public release.

### Added
- Author monitoring with OpenLibrary metadata.
- Per-book status workflow (wanted → downloading → downloaded → imported).
- Series tracking with dedicated page.
- Edition tracking (format, ISBN, publisher, page count).
- Library scan for pre-existing files.
- Newznab / Torznab indexer support with parallel querying.
- SABnzbd download client integration.
- qBittorrent client (scaffolded).
- Automatic import with naming template tokens (`{Author}`, `{Title}`,
  `{Year}`, `{ext}`).
- Cross-filesystem move support (atomic rename → copy+verify+delete).
- Webhook notifications for grab / import / failure events.
- Google Books and Hardcover.app as enricher metadata sources.
- Single-binary distribution with embedded React frontend.
- Distroless Docker image and Helm chart.

[v1.4.5]: https://github.com/vavallee/bindery/releases/tag/v1.4.5
[v1.4.4]: https://github.com/vavallee/bindery/releases/tag/v1.4.4
[v1.4.3]: https://github.com/vavallee/bindery/releases/tag/v1.4.3
[v1.4.2]: https://github.com/vavallee/bindery/releases/tag/v1.4.2
[v1.4.1]: https://github.com/vavallee/bindery/releases/tag/v1.4.1
[v1.4.0]: https://github.com/vavallee/bindery/releases/tag/v1.4.0
[v1.3.1]: https://github.com/vavallee/bindery/releases/tag/v1.3.1
[v1.3.0]: https://github.com/vavallee/bindery/releases/tag/v1.3.0
[v1.2.7]: https://github.com/vavallee/bindery/releases/tag/v1.2.7
[v1.2.6]: https://github.com/vavallee/bindery/releases/tag/v1.2.6
[v1.2.5]: https://github.com/vavallee/bindery/releases/tag/v1.2.5
[v1.2.4]: https://github.com/vavallee/bindery/releases/tag/v1.2.4
[v1.2.3]: https://github.com/vavallee/bindery/releases/tag/v1.2.3
[v1.2.2]: https://github.com/vavallee/bindery/releases/tag/v1.2.2
[v1.2.1]: https://github.com/vavallee/bindery/releases/tag/v1.2.1
[v1.2.0]: https://github.com/vavallee/bindery/releases/tag/v1.2.0
[v0.19.0]: https://github.com/vavallee/bindery/releases/tag/v0.19.0
[v0.18.3]: https://github.com/vavallee/bindery/releases/tag/v0.18.3
[v0.18.2]: https://github.com/vavallee/bindery/releases/tag/v0.18.2
[v0.18.1]: https://github.com/vavallee/bindery/releases/tag/v0.18.1
[v0.18.0]: https://github.com/vavallee/bindery/releases/tag/v0.18.0
[v0.17.0]: https://github.com/vavallee/bindery/releases/tag/v0.17.0
[v0.16.0]: https://github.com/vavallee/bindery/releases/tag/v0.16.0
[v0.8.0]: https://github.com/vavallee/bindery/releases/tag/v0.8.0
[v0.7.2]: https://github.com/vavallee/bindery/releases/tag/v0.7.2
[v0.7.1]: https://github.com/vavallee/bindery/releases/tag/v0.7.1
[v0.7.0]: https://github.com/vavallee/bindery/releases/tag/v0.7.0
[v0.6.4]: https://github.com/vavallee/bindery/releases/tag/v0.6.4
[v0.6.3]: https://github.com/vavallee/bindery/releases/tag/v0.6.3
[v0.6.2]: https://github.com/vavallee/bindery/releases/tag/v0.6.2
[v0.6.1]: https://github.com/vavallee/bindery/releases/tag/v0.6.1
[v0.6.0]: https://github.com/vavallee/bindery/releases/tag/v0.6.0
[v0.5.2]: https://github.com/vavallee/bindery/releases/tag/v0.5.2
[v0.5.1]: https://github.com/vavallee/bindery/releases/tag/v0.5.1
[v0.5.0]: https://github.com/vavallee/bindery/releases/tag/v0.5.0
[v0.4.2]: https://github.com/vavallee/bindery/releases/tag/v0.4.2
[v0.4.1]: https://github.com/vavallee/bindery/releases/tag/v0.4.1
[v0.4.0]: https://github.com/vavallee/bindery/releases/tag/v0.4.0
[v0.3.0]: https://github.com/vavallee/bindery/releases/tag/v0.3.0
[v0.2.0]: https://github.com/vavallee/bindery/releases/tag/v0.2.0
[v0.1.0]: https://github.com/vavallee/bindery/releases/tag/v0.1.0
