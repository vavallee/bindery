### Fixed
- **Helm chart: the PostSync smoke hook works with the NetworkPolicy enabled** (#3848). The smoke Job's pod no longer carries the Bindery Service selector labels, so it is not swept into the NetworkPolicy's egress lockdown or listed as a Service endpoint. The NetworkPolicy now also admits the smoke and log alert pods when `networkPolicy.ingressSelector` is set.
