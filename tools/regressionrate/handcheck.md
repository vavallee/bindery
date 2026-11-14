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
