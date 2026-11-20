### Fixed
- **Requests treat shared books like the book routes do.** With multi-user enforcement on, a requester's auto approval no longer adds a missing format to a shared book (one with no owner) or starts a search for it. The request stays pending for an admin, the same as a request for a book another account holds. An admin approving by hand and installs with enforcement off are unchanged.
