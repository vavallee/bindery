- **Directory moves and hardlinks refuse a destination inside the source**
  ([#1809](https://github.com/vavallee/bindery/issues/1809)) — the copy-based
  move and `HardlinkDir` both walked into the directory they were creating and
  recursed until the disk filled. Containment is now checked case-insensitively
  as well, so a case-only difference cannot slip past on APFS or a Windows
  mount.
