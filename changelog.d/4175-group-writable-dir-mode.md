### Added

- `BINDERY_DIR_MODE` now accepts `0770` and `0775`, so a library shared with another service such as Audiobookshelf through a common group can be group-writable. Bindery now sets the mode explicitly on the directories it creates, so the value takes effect under the usual `022` umask. The default stays `0750`, and world-writable modes are still rejected. Thanks to koinupapi for the report (#4175).
