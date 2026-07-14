### Fixed
- **Storage health now says *why*, not just *that*** (#1427) — the
  "downloads and library can't hardlink" banner and the "not writable"
  warnings were generic, sending operators hunting through mount tables and
  permission bits blind. The hardlink probe now names the actual cause
  (different filesystems; same device ID but cross-mount EXDEV, typical of
  mergerfs pools, separate bind mounts, and Unraid `/mnt/user` shares; or a
  filesystem that refuses hardlinks, common on exFAT/NTFS/network shares) in
  Settings → General. And a failed writability check now reports the uid/gid
  the process actually runs as versus who owns the directory, with the
  `user: "UID:GID"` hint when they differ — the classic case being folders
  prepared for the stack's usual user while the container runs as the
  distroless default `65532` because `user:` was never set in Compose.
