# Regression rate

Generated 2026-11-14 at main `95a7b1f34245` by `go run ./tools/regressionrate`. Do not edit the tables by hand; rerun the tool.

2463 non merge commits, 1048 of them `fix` commits, 176 release tags from v0.1.0 to v1.50.0.

## Method

Every non merge commit whose subject starts with `fix` (any scope) is a fix. For each one the tool takes the lines it deleted or rewrote in non test Go and TS/TSX source (test files, docs, changelog fragments, vendored and generated code, e2e and test helpers are skipped) and runs `git blame -w -M -C` on the parent to find the commits that wrote those lines. Blank lines, lone braces, bare import paths and similar boilerplate are dropped first, because they vote for whoever last reformatted a file rather than whoever wrote the bug.

Both the fix and each blamed commit are mapped to the first release tag that contains them, by walking the tags in version order once. Hotfix releases are cut by cherry pick onto release branches, so a commit whose subject carries the same PR number as a commit in an earlier tag is credited to that earlier tag.

The distance in releases between the fix and the blamed code gives the class:

* **unreleased**: the code was written and fixed inside one release cycle. No user ever ran the bug.
* **regression_1**: the code first shipped in the release right before the fix.
* **regression_2**: two releases before.
* **latent**: older than that. An old bug someone finally hit.

**majority** classifies a fix by the class holding most of its blamed lines (ties go to the younger class) and is the headline. **youngest** classifies it by the newest code it touched at all; the hand check below found that rule far too eager, because one recently reformatted line in a large fix is enough to call it a regression. The regression share is regression_1 plus regression_2 over all fixes with blameable deleted lines. Patch releases count as releases, so v1.43.0 to v1.43.1 is one step.

Fixes that only add lines (a missing check, a new branch) are **additive**. They are kept out of the headline numbers and classified separately on the two lines either side of each insertion, which is weaker evidence. Fixes that touch no blameable source at all (tests, docs, CI, SQL migrations, new files only) are **no source**, and fixes whose subject says they repair lint, formatting, tests or CI are **tooling**: real rework, but nothing a user runs.

Releases here come every 1.2 days on average (176 tags over 212 days), so two releases is often under a week and "latent" includes code that shipped last week. The calendar view classifies the same fixes by the age in days of the code they touched when the fix was committed, counting only code that had shipped.

What it cannot see: a fix that deletes old code to repair a bug a recent change caused elsewhere reads as latent, a fix that rewrites a recently edited line to repair an old bug reads as a regression, and a follow up that extends a recent partial fix reads as a regression of that fix. The hand check below measures how often that happens.

## Headline

| rule | fixes | unreleased | regression_1 | regression_2 | latent | regression share | shipped regression share |
|---|---:|---:|---:|---:|---:|---:|---:|
| majority | 853 | 66 | 32 | 23 | 732 | **6%** | 7% |
| youngest | 853 | 117 | 73 | 52 | 611 | 15% | 17% |
| additive, youngest context | 82 | 7 | 6 | 1 | 68 | 9% | 9% |

Shipped regression share leaves out unreleased fixes: of the bugs that reached a release, how many came from one of the last two. Of 1048 fix commits, 853 had blameable deleted lines, 82 were additive, 70 were tooling and 43 touched no blameable source.

### Calendar age of shipped code

By the youngest code touched, among fixes with deleted lines whose youngest code had shipped:

| up to 7 days | 8 to 30 days | 31 to 90 days | over 90 days |
|---:|---:|---:|---:|
| 240 | 215 | 183 | 98 |

Fixes where at least half the blamed lines are shipped code 30 days old or less: **294 of 853 (34%)**. By the youngest line instead: 455 (53%). This is the widest reading of "a recent change caused it" and an upper bound; at this release cadence it covers code from the last few weeks of releases, and it carries the same misreadings as the release based rules.

## Trend

Class counts use the majority rule on fixes with deleted lines. Untagged fixes on main count with the newest line.

| lines | fix commits | tooling | no source | additive | unreleased | regression_1 | regression_2 | latent | regression share | youngest rule | recent 30 days |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| v0.x | 112 | 19 | 16 | 9 | 13 | 5 | 4 | 46 | **13%** | 21% | 82% |
| v1.0 to v1.9 | 150 | 28 | 9 | 11 | 22 | 3 | 6 | 71 | **9%** | 10% | 77% |
| v1.10 to v1.19 | 170 | 8 | 8 | 11 | 3 | 8 | 3 | 129 | **8%** | 19% | 38% |
| v1.20 to v1.29 | 140 | 3 | 0 | 19 | 2 | 2 | 6 | 108 | **7%** | 8% | 22% |
| v1.30 to v1.39 | 254 | 4 | 8 | 25 | 15 | 5 | 2 | 195 | **3%** | 10% | 18% |
| v1.40 to v1.49 | 207 | 8 | 2 | 7 | 10 | 9 | 2 | 169 | **6%** | 21% | 20% |
| v1.50 to v1.59 | 15 | 0 | 0 | 0 | 1 | 0 | 0 | 14 | **0%** | 13% | 13% |

## By minor version line

Class counts use the majority rule on fixes with deleted lines.

| line | fix commits | tooling | no source | additive | unreleased | regression_1 | regression_2 | latent | regression share | youngest rule | recent 30 days |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| v0.x | 112 | 19 | 16 | 9 | 13 | 5 | 4 | 46 | **13%** | 21% | 82% |
| v1.0.x | 11 | 0 | 1 | 2 | 1 | 1 | 0 | 6 | **12%** | 12% | 88% |
| v1.1.x | 12 | 3 | 1 | 0 | 0 | 0 | 0 | 8 | **0%** | 0% | 100% |
| v1.2.x | 42 | 11 | 4 | 5 | 2 | 0 | 2 | 18 | **9%** | 14% | 95% |
| v1.3.x | 26 | 10 | 1 | 1 | 12 | 0 | 0 | 2 | **0%** | 0% | 21% |
| v1.4.x | 32 | 3 | 2 | 2 | 7 | 1 | 4 | 13 | **20%** | 12% | 72% |
| v1.5.x | 4 | 1 | 0 | 0 | 0 | 0 | 0 | 3 | **0%** | 33% | 100% |
| v1.6.x | 2 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | **0%** | 0% | 100% |
| v1.7.x | 2 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | **0%** | 50% | 100% |
| v1.8.x | 3 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | **0%** | 0% | 100% |
| v1.9.x | 16 | 0 | 0 | 1 | 0 | 1 | 0 | 14 | **7%** | 7% | 80% |
| v1.10.x | 4 | 1 | 0 | 0 | 0 | 0 | 0 | 3 | **0%** | 33% | 100% |
| v1.11.x | 15 | 4 | 2 | 1 | 0 | 0 | 0 | 8 | **0%** | 0% | 62% |
| v1.12.x | 17 | 1 | 0 | 1 | 0 | 1 | 0 | 14 | **7%** | 7% | 20% |
| v1.13.x | 15 | 1 | 0 | 0 | 1 | 0 | 1 | 12 | **7%** | 14% | 21% |
| v1.14.x | 16 | 0 | 0 | 2 | 0 | 0 | 1 | 13 | **7%** | 14% | 57% |
| v1.15.x | 23 | 1 | 1 | 1 | 0 | 2 | 0 | 18 | **10%** | 10% | 50% |
| v1.16.x | 30 | 0 | 1 | 4 | 1 | 0 | 0 | 24 | **0%** | 12% | 40% |
| v1.17.x | 30 | 0 | 2 | 1 | 1 | 3 | 0 | 23 | **11%** | 30% | 33% |
| v1.18.x | 13 | 0 | 2 | 1 | 0 | 1 | 1 | 8 | **20%** | 70% | 20% |
| v1.19.x | 7 | 0 | 0 | 0 | 0 | 1 | 0 | 6 | **14%** | 14% | 14% |
| v1.21.x | 7 | 0 | 0 | 2 | 0 | 0 | 0 | 5 | **0%** | 0% | 60% |
| v1.22.x | 34 | 0 | 0 | 6 | 0 | 0 | 3 | 25 | **11%** | 11% | 43% |
| v1.23.x | 17 | 0 | 0 | 2 | 0 | 1 | 3 | 11 | **27%** | 27% | 47% |
| v1.24.x | 20 | 1 | 0 | 2 | 0 | 1 | 0 | 16 | **6%** | 6% | 12% |
| v1.25.x | 7 | 0 | 0 | 0 | 0 | 0 | 0 | 7 | **0%** | 0% | 0% |
| v1.26.x | 13 | 1 | 0 | 1 | 0 | 0 | 0 | 11 | **0%** | 0% | 9% |
| v1.27.x | 4 | 0 | 0 | 1 | 1 | 0 | 0 | 2 | **0%** | 0% | 0% |
| v1.28.x | 19 | 1 | 0 | 2 | 1 | 0 | 0 | 15 | **0%** | 6% | 6% |
| v1.29.x | 19 | 0 | 0 | 3 | 0 | 0 | 0 | 16 | **0%** | 6% | 0% |
| v1.30.x | 45 | 1 | 3 | 5 | 5 | 1 | 0 | 30 | **3%** | 3% | 25% |
| v1.31.x | 9 | 0 | 0 | 0 | 0 | 0 | 0 | 9 | **0%** | 0% | 0% |
| v1.32.x | 51 | 0 | 0 | 9 | 3 | 1 | 0 | 38 | **2%** | 12% | 17% |
| v1.33.x | 36 | 0 | 4 | 3 | 0 | 0 | 0 | 29 | **0%** | 3% | 10% |
| v1.34.x | 10 | 2 | 1 | 2 | 0 | 0 | 0 | 5 | **0%** | 0% | 0% |
| v1.35.x | 25 | 0 | 0 | 1 | 2 | 0 | 0 | 22 | **0%** | 12% | 29% |
| v1.36.x | 30 | 0 | 0 | 1 | 3 | 1 | 0 | 25 | **3%** | 17% | 10% |
| v1.37.x | 10 | 0 | 0 | 2 | 1 | 0 | 1 | 6 | **12%** | 12% | 25% |
| v1.38.x | 21 | 0 | 0 | 1 | 0 | 0 | 0 | 20 | **0%** | 10% | 15% |
| v1.39.x | 17 | 1 | 0 | 1 | 1 | 2 | 1 | 11 | **20%** | 27% | 33% |
| v1.40.x | 36 | 3 | 1 | 0 | 3 | 1 | 1 | 27 | **6%** | 28% | 25% |
| v1.41.x | 20 | 0 | 0 | 0 | 0 | 0 | 0 | 20 | **0%** | 25% | 5% |
| v1.42.x | 34 | 1 | 1 | 1 | 2 | 3 | 0 | 26 | **10%** | 32% | 16% |
| v1.43.x | 26 | 0 | 0 | 1 | 1 | 2 | 1 | 21 | **12%** | 20% | 24% |
| v1.44.x | 3 | 0 | 0 | 0 | 1 | 0 | 0 | 2 | **0%** | 0% | 0% |
| v1.45.x | 13 | 1 | 0 | 1 | 0 | 1 | 0 | 10 | **9%** | 36% | 36% |
| v1.46.x | 44 | 2 | 0 | 2 | 2 | 1 | 0 | 37 | **2%** | 15% | 30% |
| v1.48.x | 7 | 0 | 0 | 0 | 0 | 0 | 0 | 7 | **0%** | 0% | 0% |
| v1.49.x | 24 | 1 | 0 | 2 | 1 | 1 | 0 | 19 | **5%** | 5% | 10% |
| v1.50.x | 8 | 0 | 0 | 0 | 0 | 0 | 0 | 8 | **0%** | 12% | 12% |
| next (untagged) | 7 | 0 | 0 | 0 | 1 | 0 | 0 | 6 | **0%** | 14% | 14% |

## By area

Class counts use the majority rule on fixes with deleted lines. A fix belongs to the area holding most of its blamed lines.

| area | fix commits | tooling | no source | additive | unreleased | regression_1 | regression_2 | latent | regression share | youngest rule | recent 30 days |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| internal/importer | 119 | 0 | 0 | 11 | 8 | 2 | 6 | 92 | **7%** | 19% | 37% |
| internal/indexer | 66 | 0 | 0 | 9 | 4 | 2 | 1 | 50 | **5%** | 7% | 40% |
| internal/metadata | 77 | 0 | 0 | 9 | 6 | 2 | 2 | 58 | **6%** | 12% | 31% |
| internal/api | 178 | 0 | 0 | 18 | 12 | 6 | 4 | 138 | **6%** | 16% | 38% |
| internal/abs | 20 | 0 | 0 | 0 | 6 | 0 | 2 | 12 | **10%** | 0% | 25% |
| internal/calibre | 18 | 0 | 0 | 2 | 1 | 2 | 0 | 13 | **12%** | 19% | 31% |
| internal/downloader | 59 | 0 | 0 | 7 | 2 | 1 | 0 | 49 | **2%** | 13% | 33% |
| web/src | 177 | 0 | 0 | 11 | 9 | 8 | 3 | 146 | **7%** | 20% | 34% |
| other | 221 | 0 | 0 | 15 | 18 | 9 | 5 | 174 | **7%** | 12% | 32% |
| (no source) | 113 | 70 | 43 | 0 | 0 | 0 | 0 | 0 | **n/a** | n/a | n/a |

## Changes that caused the most rework

Ranked by the number of later fixes the majority rule holds this commit responsible for: within the fix's majority class, it wrote the most blamed lines. "any line" counts every fix that rewrote at least one of its lines; "shipped" counts the fixes classified regression_1 or regression_2. The big early commits lead because they wrote most of the code that still exists; the next table is the one about regressions.

| commit | PR | tag | fixes | any line | shipped | subject |
|---|---|---|---:|---:|---:|---|
| `550d67ec7` |  | v0.2.0 | 34 | 101 | 1 | feat: v0.2.0 - major feature release with full Readarr feature parity |
| `6af1595dc` |  | v1.3.0 | 26 | 54 | 3 | feat(abs): add import source, runtime, and review workflows |
| `ce7a0155b` | #218 | v0.18.2 | 18 | 58 | 0 | feat(decision): specification-pattern release evaluation engine (#218) |
| `193d195f4` | #18 | v0.6.0 | 17 | 60 | 1 | feat: PUID/PGID sanity check, doc split, api test coverage (#18) |
| `81fb58449` |  | v0.14.0 | 13 | 56 | 0 | feat: Transmission client, downloader adapter, Prowlarr Torznab fix |
| `689b34d3a` |  | v0.1.0 | 12 | 22 | 0 | feat: add metadata layer with OpenLibrary, Google Books, and aggregator |
| `434020c05` | #234 | v0.19.0 | 10 | 33 | 1 | feat(series): monitor flag + Fill gaps action (#234) |
| `879bbde8e` |  | v0.5.0 | 9 | 24 | 1 | Add migration paths: CSV author import + Readarr DB importer |
| `b545d61d1` |  | v1.4.0 | 8 | 31 | 0 | feat(series): add hardcover series matching |
| `6fde2d35d` | #641 | v1.12.0 | 8 | 27 | 0 | fix(qbittorrent): recover imports after path remaps (#641) |
| `73ebfd6da` |  | v0.4.0 | 8 | 22 | 0 | Overhaul indexer search matching and ranking (v0.4.0) |
| `3b7ed23ec` |  | v0.14.0 | 8 | 19 | 0 | feat(recommender): add recommendation engine backend (Phase 1) |
| `ef0f5aa5c` |  | v0.1.0 | 7 | 38 | 1 | feat: add full React frontend with all pages |
| `3cac5a4e3` |  | v0.1.0 | 7 | 37 | 1 | feat: add author/book CRUD API and Newznab indexer search |
| `1b3f0b09d` | #772 | v1.14.0 | 7 | 34 | 1 | refactor(web): decompose SettingsPage monolith into per-tab components (#547) (#772) |

### Most shipped regressions

Commits ranked by fixes the majority rule classified regression_1 or regression_2 against them. Commits with one such fix are left out.

| commit | PR | tag | shipped regressions | fixed by | subject |
|---|---|---|---:|---|---|
| `6af1595dc` |  | v1.3.0 | 3 | `ae614abc0` `9c30366d6` `da1ba9a0a` | feat(abs): add import source, runtime, and review workflows |
| `a153e6a16` | #3065 | v1.41.0 | 2 | `5c4ccd648` `76f9ba03d` | fix(web): fix page layouts that break on phones (#3052) (#3065) |
| `dadead4d5` | #2743 | v1.38.0 | 2 | `47930e522` `a4bcfb77f` | fix(calibre): resolve the hand off per push and speak Bridge 0.6.0 (#2743) |

48 other commits have exactly one shipped regression against them.

## Hand check

Done on 2026-11-13 against main `95a7b1f3`. The tool's `-dump` output was sampled at random with a fixed seed: 10 fixes the youngest rule called regression_1, 5 it called latent, then 10 the majority rule called regression_1, since the first sample showed the youngest rule could not carry the headline. Each fix was read alongside the commit blamed for it. "Right" means the blamed commit really introduced the defect the fix repairs.

### Youngest rule, regression_1: 3 of 10 right, 1 of them user facing

| fix | verdict | why |
|---|---|---|
| `2ffc9c603` enrich authors on Refresh all (#3399) | right | #3288 shipped author enrichment with bulk refresh left out; the next release added it |
| `89c356b6c` defer rows.Close in telemetry | right, not user facing | lint failures the v1.9.0 trend charts introduced |
| `ec3c27241` gofmt calibre syncer | right, not user facing | formatting from #259; now filtered as tooling |
| `043ee162f` fold case in the destination check (#1876) | wrong | extends the #1865 guard to case insensitive filesystems; the runaway copy predates #1865 |
| `df7a5b760` pause bulk work until the Hardcover quota resets | wrong | new behaviour; one recently edited line among 19 drew the youngest vote (majority: latent) |
| `c2273db10` constrain author bio width (#1039) | wrong | layout polish; #1018 had only rewritten the className line |
| `37aba5ed5` readable secondary text in components (#3394) | wrong | 155 line contrast sweep; one line from a new feature drew the vote (majority: latent) |
| `e6f36bd49` readable secondary text on pages (#3393) | wrong | same sweep, 252 lines (majority: latent) |
| `751dbd03c` placeholder colour (#3260) | wrong | #3223 reformatted the className line; the low contrast placeholder colour was older |
| `792057fd9` dangling separator in naming templates (#1128) | wrong | blame landed on the TypeScript preview mirror #1060 added; the Go renderer bug was older |

### Latent: 5 of 5 right

`9776df933` (Fix Match confirmation, defect in the original v1.23 feature), `a813dab59` (pending releases keyed by GUID, an old schema choice), `d8a990d19` (wait for an unpacker, an old limitation), `3c55da65e` (ttlCache janitor leak, old), `5a9f3dc2a` (misleading download client errors, old). Two of these touched code only 12 and 21 days old and were still not regressions, which is why the calendar view is an upper bound and not an estimate.

### Majority rule, regression_1: 5 of 8 right, 4 of them user facing

Two of the 10 drawn (`0c4544f07` lint, `ec3c27241` gofmt) are now classified tooling and left out. Of the other eight, right: `461583f0c` (a gosec G118 finding in the series Fill code #234 added; not user facing), `b44222b9a` (the host match fallback #2039 added never recorded the indexer), `5c4ccd648` (the phone layout pass #3065 left no room for the select arrow on desktop), `0f54c6dca` (the library scan result #962 added was not written on two early returns), `b4b2296de` (the refresh all status endpoint 404'd from the release that added it). Wrong: `e7de4161b` (an enhancement labelled fix), `792057fd9` (above), `4c7dec20e` (#1371 generalises the #1369 ownership check to every delete path; the unguarded paths were older).

Counting every distinct majority regression_1 fix read across both samples, 7 of 13 were right, and two of those seven are lint fixes whose subjects do not say so.

### What the method gets wrong

* **One recent line in a big fix.** Sweeps and features labelled fix rewrite many old lines and one or two recent ones. The youngest rule calls them regressions; the majority rule mostly does not. This is why majority is the headline.
* **Long lines edited for another reason.** A Tailwind className string touched by a recent style pass gets the blame for a colour or width that was there before it.
* **Follow ups to a partial fix.** When a fix extends a guard the previous release added, blame points at the guard, but the bug predates it.
* **Mirrored code.** The TypeScript preview of a Go function inherits the Go bug and takes the blame for it.
* **Fixes that are really enhancements.** The `fix` prefix is used for new behaviour as well as broken behaviour.

None of these can be told apart from the diff alone, so the tool does not try. Read the release based numbers as an upper bound of roughly twice the true rate: with about half of the majority rule's regressions confirmed, the real share of source fixes repairing a bug from one of the two previous releases is around 3 to 4 percent, not the 15 percent the youngest rule gives.
