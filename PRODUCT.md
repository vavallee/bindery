# Product

## Register

product

## Users

Bindery is for self-hosted media-library operators who manage ebook and audiobook collections through Usenet, torrent, Calibre, OPDS, and metadata integrations. They use the app in an operational context: monitor authors, resolve metadata, search indexers, watch download/import state, and tune settings without needing a separate database or stack of services.

## Product Purpose

Bindery is a clean-room Readarr replacement for automated book management. It should make the core loop clear and dependable: monitor authors, find releases, download, import, organize, and surface failures early enough for users to act.

## Brand Personality

Direct, dependable, and practical. The product voice should favor specific labels, visible state, and clear operational feedback over promotional language.

## Anti-references

Avoid fragile scraper-driven workflows, dead metadata backends, decorative SaaS landing-page patterns inside the app, and clever custom controls that obscure standard settings or queue actions.

## Design Principles

- Keep task state visible: users should understand what is monitored, wanted, downloading, imported, failed, or blocked.
- Prefer dense but scannable layouts: book, author, queue, and settings screens need comparison and repeated action more than presentation.
- Make failure actionable: errors should identify the integration, record, or setting that needs attention.
- Preserve familiar product UI affordances: standard navigation, tables, forms, dialogs, and route structure should stay predictable.
- Respect self-hosted constraints: the interface should feel fast on modest hardware and resilient under reverse proxies and subpath deployments.

## Accessibility & Inclusion

Use WCAG AA as the baseline for new UI work. Preserve keyboard access, readable contrast in both light and dark themes, responsive layouts for mobile operators, reduced-motion safety, and localized copy through the existing i18n system.
