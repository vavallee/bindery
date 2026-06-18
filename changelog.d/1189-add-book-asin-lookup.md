### Added
- **Add Book searches by Audible ASIN** (#1189) — entering a 10-character ASIN such as `B0DBJBFHGT` in **Authors → Add Book** now resolves the Audible edition directly (via the existing ASIN resolver) and returns one addable audiobook result with the ASIN preserved, instead of falling through to an unreliable title search. ISBN and title searches are unchanged.
