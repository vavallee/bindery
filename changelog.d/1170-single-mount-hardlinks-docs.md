### Changed
- **Document the single-mount (hardlink) storage layout** (#1170) — `docs/DEPLOYMENT.md` now explains the TRaSH-style single-mount layout required for hardlink imports/seeding, and the Helm `values.yaml` flags that the stock `BINDERY_*_DIR` defaults are placeholders that must point inside a real mounted volume.
