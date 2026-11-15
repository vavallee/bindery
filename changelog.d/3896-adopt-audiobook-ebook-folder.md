### Fixed
- **Adopting an audiobook no longer registers the ebook beside it** (#3896). When one folder held both an epub and an m4b, adopting the audiobook row could pick the epub as the audiobook file, which then blocked the ebook row from being adopted. The lookup for an existing file now only considers files of the format being added. Thanks to chrisnlarsen for the report.
