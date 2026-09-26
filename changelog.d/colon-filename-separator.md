### Changed
- **Colon-separated subtitles read naturally in library filenames.** A book
  titled "System Design Interview: Volume 2" used to be organized to
  `System Design Interview- Volume 2.epub`; the colon that separates a title
  from its subtitle now renders as " - ", so the same book lands at
  `System Design Interview - Volume 2.epub`. The OpenLibrary-style
  "Title : Subtitle" spelling normalizes the same way. A colon without any
  adjacent space ("A:B") is unchanged.
