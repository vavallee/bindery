### Fixed
- **Add Book and Add Author find authors another account added** (#3557). With tenancy off, or for an admin, Add Book now files the book under the author that is already in the library instead of answering 409 "author already exists", and Add Author offers the existing author as the match instead of creating a second one or returning a conflict with no author attached.
