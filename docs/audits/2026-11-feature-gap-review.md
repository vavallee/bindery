# Feature gap review, November 2026

This page checks an outside analysis of Bindery's feature set against the code, then sets out what to build next. The analysis compared Bindery with Readarr and its forks, the other book managers, and the library and audiobook servers people run next to it. Every claim below was checked against `main` at `09fc44d4` (2026-11-04, the v1.46.2 era). Claims about other projects come from their own repositories and documentation.

The goal behind the ranking is the one Bindery has always had: reliable acquisition, correct identity, safe imports, and failures you can recover from. Reading, listening, progress sync and audio conversion stay with the servers built for them.

## 1. What the analysis got right, and what it got wrong

| Claim | Verdict | Evidence |
|-|-|-|
| Owned books are never upgraded to a better copy | Correct | `QualityProfile.UpgradeAllowed` and `Cutoff` are stored and read by nothing (`internal/models/settings.go`). The search sweep only reads wanted books (`internal/scheduler/scheduler.go`, `searchWanted`). Tracked in #2420 |
| Several formats of one book are only partly supported | Corrected | Different ebook formats are already kept side by side: an EPUB imported for a book that has an AZW3 keeps both (`internal/importer/scanner.go`, the per file `book_files` loop), and the EPUB is served first in OPDS and downloads (#3467, v1.46.0). What is missing is a policy: a second file in the same format is skipped without a word, and nothing lets you choose to keep or replace. Tracked in #1357 |
| Narrator is a single text field, with no edition type | Correct | `Book.Narrator` is one string; editions carry no narrator; abridged and unabridged exist only as flags parsed from release titles (`internal/indexer/release.go`) |
| Monitoring a series does not look for new books | Correct, and the gap is wider | `series.monitored` is written by the toggle and read by nothing else. Fill gaps is a manual action. Scheduled author discovery exists but is off by default (`internal/scheduler/discovery.go`). Tracked in #2523, after #2524 |
| Requests, approvals, auto approval, Prowlarr sync, author discovery, OPDS search, Hardcover list sync and Calibre integration all exist | Correct | `internal/api/requests.go`, `internal/prowlarr/syncer.go`, `internal/scheduler/discovery.go`, `internal/opds/builder.go`, `internal/hardcoverlistsyncer/syncer.go`, `internal/calibre/` |
| There is no watched inbox, and the Calibre watched folder was removed | Partly right | No job ingests files dropped into a folder. The Calibre GUI drop folder was removed in v0.17.0 because a headless container never runs the Calibre watcher. Handing files out to watched folders (Calibre-Web Automated ingest, the post import drop folder, Storyteller) works today |
| Multi book packs are not handled | Correct, with nuance | Automatic search refuses packs and the importer refuses to import one as a single book. Manual import splits a pack across books. Some pack shapes are still not recognised (#3247) |
| Audiobookshelf integration is catalogue import and a scan trigger | Correct | `internal/abs/client.go` only authorises, reads, and asks for a library scan |
| No M4B merging, chapter editing or tag writing | Correct, by design | Bindery reads audio tags and can write a `metadata.opf` next to a book (off by default). Merging tracks belongs to Audiobookshelf's M4B encoder; see the roadmap's out of scope section |
| The latest release is v1.46.0 | Wrong | v1.46.2 was published on 2026-11-03 |

## 2. How Bindery compares

| Area | Bindery today | Where others are ahead |
|-|-|-|
| Ebook and audiobook acquisition in one app | Strong. Split roots and pipelines, both formats on one book, format aware search and import | Chaptarr covers both too and adds MP3 to M4B conversion |
| Author release discovery | Present, opt in | Sonarr style monitors are on by default |
| Series discovery | Missing (#2523) | Sonarr and Radarr follow a collection by itself |
| Quality upgrades | Missing (#2420) | Sonarr, Radarr and the Readarr forks re-search until a cutoff is met |
| Work, edition and performance identity | Partial (#1358) | Chaptarr is narrator aware |
| Existing library adoption | Strong. Adoption with Undo, manual and bulk import, series hints from paths | |
| Download recovery | Strong. Bounded retries, re-grab, path remap diagnosis | |
| Household requests | Present. Requester role, approvals, auto approval, quotas | Seerr has the richer request interface |
| Reading, listening and progress sync | Out of scope; OPDS and file downloads only | Audiobookshelf, Kavita, Komga and Calibre-Web own this |
| Calibre and Audiobookshelf integration | Strong for Calibre, catalogue only for Audiobookshelf (#2173) | |

## 3. What to build next

Ranked by how often users hit the gap, how much a wrong result costs them, and how much work it is.

**Now: correctness of what already ships.**

1. Files landing in another book's folder: #3556 (import) and #3599 (Reorganize). A wrong destination is worse than no import.
2. Search that grabs the wrong thing or gives up: #3615 (wrong author for books not out yet), #3564 (gives up when the best release is held for another book), #3563 (a blocklist entry for one book blocks the release for every book).
3. Pack shapes that slip through: #3247.
4. A one step Reject import (#3250), once the import provenance in #3248 lands.

**Next minor: the upgrade path.**

5. Upgrades for owned ebooks (#2420), designed as the issue describes: a second sweep over imported books whose best file ranks below the cutoff, rate limited like the wanted sweep, with the content check run on the new file before the old one is touched. The old file is kept unless the profile says otherwise (#1357). This does not need the edition model first.
6. A per book file list with format, size, source and delete, so a skipped duplicate is visible (#1357).

**After that: identity and series.**

7. Telling a new work from a new edition, starting with fractional positions under a held book (#2524), then series discovery with an Upcoming mode that lists future books without searching for them until their date (#2523). Make the off by default state of author discovery obvious in the interface.
8. Writing title, author and series into imported files, opt in and behind the content check (#2692).

**Foundations, design first.**

9. Grouping editions and translations under one work, with structured contributors (narrator, translator) and an edition type (abridged, unabridged, dramatised, full cast) (#1358). It unlocks narrator aware audiobook choice and matters most to readers outside English.
10. Audiobook quality scoring by codec and size per minute (#2740), incremental Audiobookshelf import (#2173), importing from paths that are not library roots (#2168), and a paged export for companion tools (#3328).

**Not planned.** A built in reader or player, progress and annotation sync, Kobo or Kindle sync, and audio conversion. Audiobookshelf, Calibre-Web Automated, Kavita and Komga do these well and Bindery already hands files to them.

## 4. Limits of this review

- How often each gap comes up was judged from issue comments and reactions and from the maintainer's notes on those issues. Discord and Reddit threads were not counted.
- Statements about other projects reflect their documentation in November 2026 and may change.
