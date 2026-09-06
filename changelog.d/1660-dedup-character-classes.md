### Fixed
- **Books written with `&` and with `and` are no longer treated as two different books** (#1660) — the key that decides whether two records are the same edition treated an ampersand as punctuation and dropped it, so *Foundation & Empire* keyed as "foundation empire" and *Foundation and Empire* as "foundation and empire". Providers disagree about which form to send, so the same book could be added twice and a release named with one spelling would not match a book stored with the other. An ampersand is now read as the word it stands for.
- **Authors whose names arrive in full-width or ligature form no longer duplicate** (#1660) — some catalogues send *Ｈａｒｕｋｉ　Ｍｕｒａｋａｍｉ* or a name containing a typographic ligature. Those forms did not compare equal to the ordinary spelling, so the same author could be created twice depending on which source a record came from.

### Changed
- **Existing libraries repair their book comparison keys once on the first start after upgrading** (#1660) — the ampersand change alters the stored key for any title containing one, so the keys are recomputed on the next boot. This is a single pass and is not repeated.
