### Fixed
- **Download status races** (#1462): two things updating the same download at once could slip an illegal state change through (re-completing an already imported download or double stamping timestamps). The status update is now applied atomically so only one writer wins and the row can't land in a bad state.
