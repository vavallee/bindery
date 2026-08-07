### Fixed
- **Book detail File card lost its two-column layout** — the label/value grid used a comma in its arbitrary track list, which compiles to invalid CSS that browsers drop, collapsing the card to a single column.
- **Author detail page was rendering unconstrained** — its max-width class sat directly against a `${…}` interpolation, so Tailwind's scanner never extracted it and the rule was absent from the compiled CSS entirely.
