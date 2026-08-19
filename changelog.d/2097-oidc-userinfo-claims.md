### Fixed

- **OIDC group mapping works with Authelia, Okta and Auth0 again, and a missing
  group claim no longer takes your admin rights away** (#2097) — Bindery read
  group membership only from the ID token and never called the IdP's userinfo
  endpoint. Those providers do not put `groups` in the ID token by default and
  serve it only from userinfo, so Bindery saw no groups at all. With
  `BINDERY_OIDC_ADMIN_GROUP` set, that empty result was then read as "not an
  admin" and the user was demoted on every login — including the operator who
  configured it, and with local auth disabled that was a lockout, since the
  last-admin demotion guard is deliberately bypassed on this path. It also
  explains OIDC users whose Bindery username was their `sub` UUID:
  `preferred_username` is userinfo-only under the same defaults.

  Bindery now reads the userinfo document as well and merges it underneath the
  ID token, so a claim carried by both keeps the signed ID token's value, and a
  userinfo document whose subject does not match the ID token's is discarded
  (OIDC Core 1.0 §5.3.2). A group claim that is missing from both leaves the
  existing role untouched and logs why; a claim that is present and does not
  list the admin group still demotes, because that is the IdP actually saying
  so. `allowed_groups` now says which of the two it rejected a login for
  instead of giving the same message either way.
