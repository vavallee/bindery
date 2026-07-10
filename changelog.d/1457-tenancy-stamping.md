### Security
- **Multi-user tenancy: owners are now stamped and series views scoped**
  (#1457, #1416) — new books inherit their author's owner on every create
  path (add-book, author sync, series fill, ABS/Calibre imports), and new
  downloads carry the grabbing user (background grabs inherit the book's
  owner), so per-user scoping finally has data to scope on: a non-admin's
  queue is no longer empty under tenancy, and the series list/detail no
  longer let one user enumerate another user's titles, covers, and statuses.
  The author page's embedded book list now applies the same owner predicate
  as the book list, so the two views agree on co-authored books. Existing
  unowned rows keep their legacy world-visible behaviour; only affects
  deployments with `BINDERY_ENFORCE_TENANCY` enabled.
