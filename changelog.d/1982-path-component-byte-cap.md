### Fixed

- **Books with long non-ASCII titles no longer fail to import** (#1982) — a
  Japanese, Chinese, Korean, Russian, Greek or heavily accented title of roughly
  83 characters or more could kill the import with "file name too long". The
  importer was limiting each name to 200 *characters*, but filesystems count
  *bytes*, and one CJK character is three or four of them. The limit is now 200
  bytes, cut on a character boundary so a name never ends in half a character.
  Titles in plain ASCII are unaffected — nothing already in your library gets
  renamed.
