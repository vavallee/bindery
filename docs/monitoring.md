# Monitoring with the Helm chart

The chart can wire Bindery into a Prometheus Operator and Grafana setup. Everything here is off by default.

| Value | Renders | Needs |
| --- | --- | --- |
| `metrics.serviceMonitor.enabled` | a `ServiceMonitor` that scrapes `/metrics` | the `monitoring.coreos.com/v1` API |
| `metrics.prometheusRule.enabled` | a `PrometheusRule` with the alerts below | the `monitoring.coreos.com/v1` API |
| `metrics.dashboard.enabled` | a `ConfigMap` holding a Grafana dashboard, labelled `grafana_dashboard: "1"` | a Grafana sidecar that loads dashboards from ConfigMaps |

The `ServiceMonitor` and `PrometheusRule` are only rendered when the cluster serves `monitoring.coreos.com/v1`, so turning them on in a cluster without the Prometheus Operator renders nothing rather than failing the install. Argo CD and `helm install` see the cluster's APIs. With `helm template`, pass `--api-versions monitoring.coreos.com/v1`.

## What an operator has to set

`/metrics` is served on the same port as the UI with no authentication, and it ignores Bindery's API key, so the `ServiceMonitor` carries no credentials and there is no secret to wire up. What you may need:

- **Prometheus selector labels.** Most Prometheus installs only pick up a `ServiceMonitor` or `PrometheusRule` carrying a particular label. For kube-prometheus-stack that is usually `release: <its release name>`. Put it in `metrics.serviceMonitor.labels` and `metrics.prometheusRule.labels`.
- **URL base.** When `env.BINDERY_URL_BASE` is set, the scrape path follows it (`/bindery/metrics`). Set `metrics.serviceMonitor.path` only to override that.
- **NetworkPolicy.** With `networkPolicy.enabled`, only `networkPolicy.ingressSelector` can reach the pod, and that selector is namespace local. A Prometheus in another namespace needs `networkPolicy.metricsFrom`, for example:

  ```yaml
  networkPolicy:
    enabled: true
    metricsFrom:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
  ```

  A NetworkPolicy works per port, so those peers can reach the whole port, not only `/metrics`.
- **Dashboard namespace.** If your Grafana sidecar only watches its own namespace, set `metrics.dashboard.namespace` to it. Folder annotations such as `grafana_folder` go in `metrics.dashboard.annotations`.

Because `/metrics` needs no login, keep it off the public side of any reverse proxy or ingress that exposes Bindery outside your network.

## The `job` label

Prometheus labels every scraped series with the target's `job`, which for this `ServiceMonitor` is the Service name (`bindery`). The scheduler metrics carry their own `job` label, the name of the background job, and Prometheus keeps the target's value and renames the scraped one to `exported_job`. So in PromQL:

```promql
time() - bindery_scheduler_last_success_timestamp_seconds{job="bindery", exported_job="check-downloads"}
```

The bundled rules and dashboard already use `exported_job`. If you set `honorLabels: true` through your own scrape config the scraped value wins instead and the rules will not match.

## Suggested alert thresholds

Every Bindery instance is self hosted with its own library, indexers and clients, so there are no SLOs here. These are starting points, all set under `metrics.prometheusRule.rules`; raise them if they are noisy for your setup, and turn any rule off with `enabled: false`.

| Alert | Fires when | Suggested threshold | `for` | Severity |
| --- | --- | --- | --- | --- |
| `BinderyDown` | the scrape target is down | `up == 0` | 5m | critical |
| `BinderyMetricsAbsent` | Prometheus has no Bindery target at all | `absent(up)` | 5m | critical |
| `BinderyHighHTTP5xxRatio` | the share of 5xx responses over 5 minutes is above `ratio` | 0.05 | 15m | warning |
| `BinderySchedulerJobStale` | a listed job has not finished cleanly for `hours` | `check-downloads` 1h, `search-wanted` 36h | 15m | warning |
| `BinderySchedulerJobPanicking` | a background job panicked in the last hour | any | 0m | warning |
| `BinderyImportFailureRatio` | over the last hour, the share of import attempts that failed or were blocked is above `ratio`, with at least `minImports` attempts | 0.5, at least 3 | 30m | warning |
| `BinderyDownloadClientErrors` | over 10 minutes, the share of requests to one client type that errored (5xx or no answer) is above `ratio` | 0.5 | 15m | warning |

Notes on choosing values:

- **Scheduler staleness.** Pick `hours` comfortably above the job's interval. `check-downloads` runs every 15 seconds, so an hour without a clean run means the import poller is wedged. `search-wanted` follows the search interval setting (12 hours by default); if you change that, change the threshold. Add other jobs (`refresh-metadata`, `calibre-sync`, `download-client-health`) to `schedulerStale.jobs` if you rely on them. The timestamp is only reported after a job's first clean run since startup, so a job that never succeeds after a restart shows up through `BinderySchedulerJobPanicking` or the logs rather than this rule.
- **Import failures.** A failure is counted per attempt, and a download that is still being extracted or moved by another tool can fail a few times before it imports, so a small library sees spikes. `minImports` stops a single bad release from paging you.
- **Download client errors.** The import poll talks to every configured client every 15 seconds, so a client that is down drives its ratio to 1 within minutes. A 4xx answer is counted as `rejected`, not `error`, because Transmission and qBittorrent use 409 and 403 as normal session handshakes.

Rules can be extended with `metrics.prometheusRule.additionalRules`, which is appended to the group as is.

The import, grab, download client, indexer, queue and scheduler freshness metrics come from #3851. Against a Bindery build without them, the rules and panels that use them have no data and never fire; the `up`, HTTP and scheduler panic rules work on any version.
