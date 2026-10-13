### Fixed
- **Editing a metadata profile that allows any language no longer shows English ticked.** The form defaulted an empty language list to English, so it showed a filter that was not applied, and saving it unchanged switched the profile to English-only. An existing profile now opens with exactly the languages it stores; only a new profile starts with English (#3212).
