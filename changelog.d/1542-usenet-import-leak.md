### Fixed
- **Usenet imports no longer leak completed job folders or library receipts**
  (#1542) — `import.mode` was applied with no protocol distinction, so usenet
  downloads inherited hardlink/copy behaviour whose only purpose is preserving
  torrent seeding. The completed job folder was left behind forever — and
  invisibly, since post-import cleanup removes the client's history entry but
  not its files (one report: 2.4 GB orphaned from three audiobook grabs).
  SABnzbd/NZBGet downloads now resolve `auto` and `hardlink` to `move`
  (explicit `copy` stays honoured for operators who want the client's
  retention to see finished files). Separately, directory placements copied
  the whole job tree verbatim, so `.nzb` receipts and `.par2` repair files
  landed in the library next to the media: hardlink, copy, move, and
  multi-disc-flatten placements now all skip download artifacts
  (`.nzb`/`.par2`/`.sfv`/`.srr`/`.srs`/`.diz` — `.nfo`, covers, and cue
  sheets are deliberately kept). Multi-disc flattening also works under move
  mode now (flatten via copy, then remove the source), so usenet downloads
  resolving to move don't lose it. Reported by cleb on Discord with both root
  causes correctly identified.
