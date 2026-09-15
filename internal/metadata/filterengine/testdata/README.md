# works_sample.csv

An 80-row stratified sample of `data/works.csv` from
[gchahcg/fiction-author-dataset](https://github.com/gchahcg/fiction-author-dataset),
commit `1b1751b97fd56e0ae11bbc4a23bd481a160191e3`.

**License:** the source dataset's `data/` directory is CC-BY-4.0. This
subset carries the same license; attribution is this file.

**Why a subset, not the full 1,422 rows:** the full dataset is a citable,
independently-versioned research artifact in its own right — vendoring all of
it here would drift out of sync with corrections upstream and bloat this
repo for no benefit `go test` needs. This sample exists only to pin a handful
of concrete title-shape regressions as real Go tests, not to reproduce the
dataset's own precision/recall analysis (that lives in the dataset repo
itself, via `scripts/eval-filter.js`).

**Sampling method:** first N rows per `work_kind` category, in file order
(deterministic, not random), quota: 35 core, 15 compilation, 8
posthumous-compilation, 12 misattrib, 5 edition-of, 3 non-book, 2
companion-reference.

**Schema:** `work_id, author_slug, author_name, title, year, work_kind,
verification, note`. `author_name` is joined in from the source dataset's
`data/authors.csv` (not a column in the original `works.csv`) purely for
test-fixture convenience — see the upstream repo's `schema/` directory for
the authoritative schema of both source files.

## What this dataset can and can't validate here

The source dataset carries ground-truth **bibliographic identity and
classification** (title, `work_kind`, verification) for testing whether a
pipeline's output contains the right titles and excludes the wrong ones. It
does **not** carry the raw provider-response fields most of this package's
signals actually consume — no `language`, no `subjects`, no `edition_count`,
no ISBN, no page count, no release date. Those were never captured anywhere
checked into the dataset repo; the 60.9%-language-drop figure originally
cited in issue #2235 came from one-time `AuthorSyncSummary` counters read off
a live Bindery instance, not from a stored raw pull.

So this sample can only exercise the **title-only** signals — `JunkTitleSignal`
and `PartBookSignal` — against real, hand-verified titles. It cannot exercise
`LanguageSignal`, `MissingDateSignal`, `MissingISBNSignal`, or `MinPagesSignal`
at all; those are tested with synthetic fixtures elsewhere in this package's
test files instead.
