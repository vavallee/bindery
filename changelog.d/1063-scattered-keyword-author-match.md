### Fixed
- **Releases whose title words match out of order are no longer grabbed** (#1063) — the weakest release-matching fallback accepted any release containing all the title's keywords in any order, so e.g. searching "Secrets of the Human Body" could grab "Body of Secrets". That fallback now requires either the author's name in the release or the title words appearing in order.
