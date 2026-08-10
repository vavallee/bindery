### Fixed
- **Helm chart `appVersion` no longer drifts from the deployed tag** — it had been frozen at 1.22.3 while `values.yaml` advanced to 1.30.0, because the release pipeline only bumped the latter. The prod-deploy step now updates both, and `appVersion` is corrected to 1.30.0.
