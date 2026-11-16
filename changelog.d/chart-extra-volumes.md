### Added
- **Helm chart: `extraVolumes`, `extraVolumeMounts` and `affinity`.** The chart can now mount an existing claim that another app also mounts, for example one tree holding downloads, a drop folder and the library, and can pin the pod next to that app with pod affinity. All three are empty by default and the rendered manifests are unchanged unless you set them.
