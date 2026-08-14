### Changed

- **GPL-3.0 dependency removed from the release binaries** (#1988) — series and
  title matching used `github.com/creditx/go-fuzzywuzzy`, which is licensed
  GPL-3.0. Because Go links statically, every published binary and every
  `ghcr.io/vavallee/bindery` image was a combined work with that code while
  `LICENSE` and the README offered MIT, which is not a licence we could grant
  for the combined work. Anyone redistributing Bindery on the stated MIT terms
  was exposed. The four similarity ratios are now a first-party implementation
  with no new dependency, so the binaries are genuinely MIT again.

  Match quality is unaffected: three of the four metrics reproduce the previous
  scores exactly, and across 19,306 pairs of real series and book titles no pair
  crossed any of the score thresholds that decide whether books are linked,
  deduplicated or created.
