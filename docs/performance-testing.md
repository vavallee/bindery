# Performance testing

Bindery has two ways to measure itself. Go benchmarks time one function in
isolation and are compared with `benchstat`. The harness in `tools/perf` boots
the real binary on a seeded library and drives it over HTTP, the way a browser,
an OPDS reader and the scheduler do.

Neither runs in CI. Run them by hand before and after a change that claims to
make something faster, on a machine that is otherwise idle.

## Benchmarks

```sh
make bench                      # every benchmark, 10 runs each, about an hour
make bench BENCH_COUNT=3        # a faster look
make bench BENCH='BookPage|FanOut'  # only the benchmarks whose name matches
benchstat before.txt after.txt  # go install golang.org/x/perf/cmd/benchstat@latest
```

`make bench` finds every package that declares a `Benchmark` function and runs
them with `-benchmem -count=10 -run '^$'`, one package at a time. The output
goes to `$PERF_DATA/bench/<commit>.txt` in the format `benchstat` reads. Run it
on the base commit and on your branch, then hand both files to `benchstat`.

## The harness

```sh
make perf-quick   # about 4 minutes, 1,000 books
make perf-full    # about an hour, 1,000, 10,000 and 40,000 books
```

Both write under `PERF_DATA` (default `$TMPDIR/bindery-perf`) and never inside
the repository. Extra flags go through `PERF_ARGS`:

```sh
make perf-full PERF_DATA=/var/tmp/perf PERF_ARGS='-only "read|poll" -indexer-latency 200ms'
```

| Flag | Default | Meaning |
|---|---|---|
| `-only` | all | Regular expression over the group names: `startup`, `read`, `poll`, `search`, `scan`, `import`, `profile` |
| `-indexer-latency` | `50ms` | Delay before every fake indexer response |
| `-client-latency` | `5ms` | Delay before every fake download client response |
| `-reps` | `3` | Repetitions per scenario. One repetition gives no spread, so use it only to try something out |
| `-seed` | `1` | Seed for the data and the request order |
| `-binary` | built | A Bindery binary to drive instead of building one. Profiles need a binary built by the harness |
| `-no-profiles` | off | Skip the profile pass |
| `-log-level` | `info` | Log level of the binary under test. `debug` is for diagnosing the harness and distorts the numbers |

### What a run does

1. Builds `cmd/bindery` from the checkout (see "Profiles" for the one thing it adds).
2. Seeds a library per size through the `internal/db` package, so the schema is
   whatever the migrations in the checkout produce. Seeds are cached under
   `PERF_DATA` and reused when the size, the seed and the seeder version match.
3. For every scenario, copies the seed database, starts the binary on it with a
   fixed API key, measures, and stops it. Each scenario runs three times.
4. Writes one results directory.

The binary gets `BINDERY_OUTBOUND_PROXY` pointed at a local proxy that refuses
everything, so no run reaches a metadata provider. The summary reports how many
requests were refused. The fake indexers and the fake qBittorrent listen on
loopback, which bypasses that proxy.

### The seeded library

For N books: N/20 authors with a skewed number of books each, a second edition
for every third book, a third of the books in a series of up to five, about
3.5 history rows per book spread over a year, a blocklist, and downloads. Three
quarters of the books have a file on disk (one in eight of those is an
audiobook), a tenth are wanted, a twentieth are not yet released.

The files are real enough for the scanner: each EPUB is a valid archive of
about 5 KB with a package file that carries the title and author, and each
`.m4b` starts with an M4B file type box. They are not readable books.

Two things are assumptions rather than facts about anyone's library, and both
move the numbers:

- **Finished downloads kept.** `/api/v1/queue` returns every row of the
  `downloads` table, finished ones included. The seed keeps one finished
  download for a quarter of the books on disk. An install that never prunes has
  more and a slower queue poll.
- **Everything belongs to one user.** The harness authenticates with the API
  key, which is an admin, and seeds no second user.

### Scenarios

| Group | What is measured |
|---|---|
| `startup` | Time from process start to the first healthy answer, for a new install (every migration runs) and for each seeded size |
| `read` | 29 read requests one at a time with one client: authors, author detail, the Books list with paging, sorting and filters, series, wanted, calendar, history, queue, library search, OPDS. Then all of them as a weighted mix, and the mix at 1, 4, 16 and 64 clients |
| `poll` | The requests an open tab repeats (`/queue` every 5 s, author refresh status every 2 s, the Books page every 10 s) from N tabs, first on an idle server and then while a library scan is writing. This is the case in issue 2147 |
| `search` | An interactive book search against 1, 5 and 20 fake indexers, once when the first query finds releases and once when every query comes back empty, which sends Bindery through its fallback queries. Counts the requests the indexers received per search. This is the case in issue 3455 |
| `scan` | A library scan over files no book owns yet, then a rescan when every file is tracked |
| `import` | N finished torrents in the fake qBittorrent, from the first time Bindery lists them until every one is imported |
| `profile` | CPU and heap profiles of the read mix, the three slowest read requests, the first scan and the worst search |

Every search uses a book that has not been searched before on that instance.
Bindery caches indexer responses per query, so a repeated search sends nothing
and would measure the cache.

The import scenario waits for the scheduler's own 15 second download check
rather than triggering one, so its wall time is counted from the first torrent
list the fake client served.

### Not measured

- **Author refresh and metadata lookups.** The provider base URLs are constants
  in the code, so there is nowhere to point them at a fake without changing
  production code.
- **Migrations replayed on a populated database.** The seed is created at the
  current schema. `startup` on a seeded library measures opening it, not
  migrating it.
- **Grabbing a release.** The import scenario starts from downloads that are
  already recorded and finished.
- **SABnzbd, NZBGet, Transmission and Deluge.** Only qBittorrent is faked.

## Reading the output

A run leaves `summary.md`, `results.json`, `profiles/` and `logs/` in
`$PERF_DATA/results/<time>-<mode>/`.

`summary.md` starts with the host: CPU model, cores, memory and the load
average at the start and at the end. A load average near the core count at the
start means something else was running and the run is not worth comparing.

Latencies are in milliseconds. Every cell is `median (lowest to highest)` over
the three repetitions, so the spread is in front of you. When the ranges of two
runs overlap, the difference between their medians is noise.

The latency columns are client side and closed loop: each client sends its next
request when the last one answers. Under the 16 and 64 client rows the p50
therefore includes time spent waiting behind other requests. Read "requests per
second" across the sweep for capacity, and the 1 client rows for the cost of a
single request.

The polling section ends with a table that puts each request's idle and writing
numbers side by side with the ratio of their p95. A ratio near 1 means reads
did not wait for the writer. A scan of 1,000 files lasts about a second, which
is less than one poll interval, so the quick mode ratio rests on a handful of
samples. Use the full mode numbers.

`results.json` holds the same data per repetition, plus the seed manifests.

## Profiles

The profile pass needs `net/http/pprof` inside the binary, and the production
binary does not have it. The harness adds it at build time with
`go build -overlay`: the build sees one extra file in `cmd/bindery` that is not
in the repository. That file starts a pprof listener only when
`BINDERY_PERF_PPROF_ADDR` is set and names a loopback address. A release build
does not contain the file, so the variable does nothing there.

The same overlay supplies a placeholder `internal/webui/dist/index.html` when
the web UI has not been built, so the harness does not need Node.

Open a profile with:

```sh
go tool pprof -http=: $PERF_DATA/bin/bindery-perf profiles/read-mix-10000-books-16-clients.cpu.pb.gz
```

Next to each profile is a `.top.txt` with the 40 heaviest functions.

## Keeping it working

`go test ./tools/perf` includes a test that builds the binary and runs every
scenario on 60 books. It takes about half a minute and is skipped under
`-short`. It is what tells you a route or a payload the harness depends on has
changed.
