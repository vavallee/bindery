### Fixed
- **Auto approve leaves excluded books alone** (#3954). When an account that auto approves asks for a book already in the catalogue, the request goes through on its own, except for a book an admin turned away. A book you Exclude, or one marked Skipped, keeps the request waiting in the queue and the book is not touched, so an admin's call to leave a book out still holds.
