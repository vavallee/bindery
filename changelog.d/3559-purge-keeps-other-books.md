### Fixed
- **Purging a user no longer deletes other accounts' books** (#3559). If someone else owned a book under one of the purged user's authors, the purge used to take it along. Now those books stay, and the author goes to the account that owns them (or becomes shared when several accounts do). The Delete user dialog says how many books are kept.
