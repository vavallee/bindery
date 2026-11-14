# Metrics

Bindery serves Prometheus metrics at `GET /metrics` in the text exposition format. Under `BINDERY_URL_BASE` the path moves under the prefix, like every other route (`/bindery/metrics` for a base of `/bindery`).

## Access

`/metrics` is mounted at the router root, ahead of authentication, CSRF and the `X-Requested-With` check. It needs no session and no API key, and Bindery's API key is not accepted there either, because a Prometheus scrape config would otherwise have to carry it. It is served on the same port as the UI and API.

Nothing on the endpoint names a user, a book, a path or a URL, but it does show what Bindery is doing and which download client types you run. If Bindery is reachable from outside your network, keep `/metrics` off the public side: a NetworkPolicy, a firewall rule, or a reverse proxy rule that refuses the path.

## Label bounds

Every label comes from a fixed set, and a value outside the set is recorded as `other`. Titles, authors, file paths, user ids, URLs, and the names you give your indexers and download clients are never labels. The indexer and download client labels carry the configured *type* (`torznab`, `qbittorrent`), not the name.

| Label | Values |
| --- | --- |
| `media_type` | `ebook`, `audiobook`, `other` |
| `client_type` | `sabnzbd`, `nzbget`, `transmission`, `qbittorrent`, `deluge`, `rtorrent`, `other` |
| `indexer_type` | `newznab`, `torznab`, `other` |
| `state` | every download state (`grabbed`, `downloading`, `completed`, `importPending`, `importing`, `imported`, `failed`, `importFailed`, `importBlocked`, `importExternal`, `importHeld`), plus `other` |
| `job` | the scheduler's job names, fixed in code (`check-downloads`, `search-wanted`, `scan-library` and so on) |
| `route` | the matched route template (`/api/v1/book/{id}`), or `other` |

The scheduler metrics' `job` label collides with the `job` label Prometheus puts on every scraped series (the scrape job, usually the Service name). Unless the scrape config sets `honor_labels: true`, Prometheus keeps its own value and renames Bindery's to `exported_job`, so a query for the background job name reads `exported_job="check-downloads"`.

## Reference

### HTTP

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_http_requests_total` | counter | `method`, `route`, `status` | Requests served. `method` is the standard HTTP method or `OTHER`. |
| `bindery_http_request_duration_seconds` | histogram | `method`, `route` | Time to handle a request. |

### Scheduler

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_scheduler_runs_total` | counter | `job`, `result` | Background job runs. `result` is `ok`, or `panic` when the job panicked and was recovered. |
| `bindery_scheduler_run_duration_seconds` | histogram | `job` | Job run time. |
| `bindery_scheduler_last_success_timestamp_seconds` | gauge | `job` | Unix time the job last finished without panicking. Absent until the job has run once since startup. `time() - bindery_scheduler_last_success_timestamp_seconds` is how long ago that was. |

A job that logs an error and returns is still `ok` here: jobs report problems through the log and the UI, and only a panic counts as a failed run.

### Grabs and download clients

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_grabs_total` | counter | `media_type`, `result` | Releases sent to a download client, from a manual grab or the automatic search. `result` is `ok` when the client accepted the release and returned an id Bindery can track, otherwise `error`. |
| `bindery_download_client_requests_total` | counter | `client_type`, `result` | Every HTTP request Bindery makes to a download client: adds, the import poll, removals, connection tests. `result` is `ok` for a 1xx to 3xx answer, `rejected` for 4xx, `error` for 5xx or a request that never got an answer (refused, timed out, TLS failure). |
| `bindery_download_client_request_duration_seconds` | histogram | `client_type` | Download client request latency. |

`rejected` includes the session handshakes some clients use on purpose: Transmission answers the first request of a session with 409, and qBittorrent answers 403 when its cookie expires and Bindery logs in again. A steady trickle of `rejected` is normal. Alert on `error`.

Fetching a `.torrent` file from an indexer on a client's behalf is not a download client request and is not counted here.

### Imports

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_imports_total` | counter | `media_type`, `result`, `reason` | Import outcomes. `result` is `imported`, `failed` (left in `importFailed`, retried automatically) or `blocked` (left in `importBlocked`, needs you). `reason` is `none` for an import. |

Each failure is counted once per attempt, so a download that fails three times before it imports counts three failures and one import.

`reason` is derived from the failure message shown on the Queue page:

| Reason | Covers |
| --- | --- |
| `path_missing` | The download's files are not where the client says, on this host. Usually a missing PathRemap or a file moved after the download. |
| `no_files` | No book files in the download, only files too small to be a book, or an archive nothing has extracted. |
| `unmatched` | The download could not be matched to a book in the library. |
| `rejected_format` | Refused by format: the quality profile, a file of the other media type, video content, or a pack linked to one book. |
| `rejected_language` | The file's declared language is not allowed. |
| `rejected_content` | The content check found a different book in the file. |
| `conflict` | Another book already owns the destination, or two files would land on the same name. |
| `placement` | Copying, moving, linking or recording the files failed, the destination is invalid, no library folder is set, or a drop folder hand off failed. |
| `source_gone` | The download disappeared from the client before it could be imported. |
| `retry_limit` | The automatic retries ran out. |
| `other` | A failure message none of the above recognises. |

### Indexers

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_indexer_requests_total` | counter | `indexer_type`, `result` | Searches against one indexer. One book search across three indexers counts three. `result` is `ok`, `error`, `timeout`, or `quota` when the indexer's daily query cap ran out part way through. |
| `bindery_indexer_request_duration_seconds` | histogram | `indexer_type` | Time for one indexer to answer one search, including every query variant Bindery tried against it. |

An indexer skipped because it is in a rate limit cooldown or already at its daily cap is not searched and not counted.

### Queue

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_queue_items` | gauge | `state` | Download records in each state, across every user. |

The gauge is counted from the database when Prometheus scrapes, with one `COUNT ... GROUP BY status` over an indexed column, so it is never stale. Every known state is reported, at 0 when empty. If the count does not finish within 2 seconds, or fails, that scrape leaves `bindery_queue_items` out rather than failing the whole scrape.

`imported` and `failed` are terminal states and only grow until history is cleared, so for a dashboard of what is in flight use the other states.

### Build and runtime

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bindery_build_info` | gauge | `version`, `commit`, `date` | Always 1. The labels identify the running build. |
| `go_*`, `process_*` | various | | The Go runtime and process collectors: goroutines, memory, CPU, open files. |

## Example queries

```promql
# HTTP 5xx ratio over 5 minutes
sum(rate(bindery_http_requests_total{status=~"5.."}[5m]))
  / sum(rate(bindery_http_requests_total[5m]))

# Import failure ratio over an hour, failed and blocked together
sum(increase(bindery_imports_total{result!="imported"}[1h]))
  / sum(increase(bindery_imports_total[1h]))

# Hours since the download poller last finished cleanly
(time() - bindery_scheduler_last_success_timestamp_seconds{exported_job="check-downloads"}) / 3600

# Download client errors per second by type
sum by (client_type) (rate(bindery_download_client_requests_total{result="error"}[5m]))

# What is in the queue right now
sum by (state) (bindery_queue_items{state!~"imported|failed"})
```
