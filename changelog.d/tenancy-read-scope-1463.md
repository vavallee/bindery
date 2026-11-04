### Security
- **Shared authors no longer show other users' books** (#1463). With `BINDERY_ENFORCE_TENANCY` on, a shared author can hold books from several users' libraries. For a user who is not an admin, the books list with excluded books included, the duplicate review for an author and for the whole library, and an author's series list now show only that user's own books and books nobody owns. The author level actions that apply to every book under an author (monitor settings applied to existing books, the genres action, and the bulk set media type, monitor, set monitor mode and search actions) now change or search only the books that user may edit. Admins and installs with tenancy off are unchanged.

### Fixed
- **Setting a series genre no longer clears other book details** (#1463). Applying genres to a series used to blank the description, file paths, narrator, ASIN, rating and other locked fields of every book in that series. Now only the genres change.
- **The Hardcover series comparison finds books from the whole library for admins** (#1463). It used to match missing books only against books the signed in account owned, even for admins and with tenancy off.
