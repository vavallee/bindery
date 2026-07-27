### Fixed
- **German and Scandinavian authors no longer end up duplicated or stuck in the review queue** (#1647) — author matching stripped accents ("Müller" → "muller") while every title matcher expands them ("Müller" → "mueller"), so a name spelled with accents in one place and ASCII-ised in another ("Jörg Müller" vs "Joerg Mueller") scored just under the auto-accept threshold. The same gap affected "Nesbø"/"Nesbo" and "Łukasz"/"Lukasz". Both spellings now resolve to one author.

### Changed
- **Normalization rules are defined in one place** (#1648) — the character folding used to compare titles and release names was copy-pasted into three packages and had started to drift, which is what caused #1643 and made #1642 possible. It now lives in a single shared helper, with the four legitimately-different comparison alphabets (title matching, author identity, sort keys, slugs) documented alongside it. No change to how releases match.
