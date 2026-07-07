### Fixed
- **Import Mode UI now shows Auto as the default** (#1444) — the selector used to pre-select Move when no mode had ever been saved, while Bindery actually defaults to auto (hardlink same-filesystem, copy otherwise). Auto is now a real, selectable option, so the UI matches what's actually happening and you can switch back to it after picking something else.
