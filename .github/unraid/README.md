# Unraid Community Applications template

`bindery.xml` is the template Unraid's Community Applications (CA) plugin
reads to render the install page for Bindery.

## Where Bindery is listed

- **Live today: the selfhosters feed.** CA lists Bindery only through
  [`selfhosters/unRAID-CA-templates`](https://github.com/selfhosters/unRAID-CA-templates)
  (`templates/bindery.xml`, added in their PR #667 on 2026-05-08). CA shows it
  under "Selfhosters Unraid Discord Repository". Treat that copy as secondary:
  this file is the source of truth, and the selfhosters copy is brought in line
  with it by PR.
- **First-party listing: not live yet.** A first-party repository (CA shows
  Bindery as "by vavallee" and reads this file's raw URL directly) is requested
  through the Unraid forum, not by a pull request to `Squidly271/AppFeed`. The
  AppFeed issue opened in May 2026 (#30) never got a response for that reason.

## Install counts: HOST_OS detection

Unraid's Docker manager (dockerMan, the Docker tab and Community Applications)
sets `HOST_OS=Unraid` in every container it creates. Bindery's telemetry ping
(only when telemetry is on) reads it and reports the deployment method as
`unraid` instead of `docker`, so Unraid installs are counted without anything
in this template. Containers started outside dockerMan, such as Compose Manager
or a plain `docker run`, do not get it and still count as `docker`. An explicit `BINDERY_DEPLOY_METHOD`
still wins. Do not add `BINDERY_DEPLOY_METHOD` here; `HOST_OS` already covers
it.

## Updating on release

The `<Repository>` tag is `:latest`, so most releases need no template change:
Unraid users get the new image by clicking "Force Update". Edit the template
only when:

- The container surface changes: a new env var, a new port, a new required
  volume, or a removed env var.
- A default needs to change, for example switching a `BINDERY_*` flag from
  `false` to `true`.
- A major release changes what the overview should say.

## Keeping both copies in step

Every time this file changes:

1. Edit `bindery.xml` here first and validate it (below).
2. Make the same change to `templates/bindery.xml` in a fork of
   `selfhosters/unRAID-CA-templates` and open a PR there. The two files match
   apart from the fields that belong to the feed: `<TemplateURL>` (each feed
   points at its own raw URL), and on their side `<Support>` and `<Category>`
   as they chose them. Known harmless differences: their Log Level and Cookie
   Secure defaults are pipe separated lists (`info|debug|warn|error`,
   `auto|always|never`), which CA renders as dropdowns, and this file carries
   empty `PostArgs`, `CPUset`, `DateInstalled`, `DonateText`, `DonateLink` and
   `Requires` tags that theirs omits.
3. Check these always hold in both copies:
   - The overview names Readarr (CA search matches overview text).
   - The licence reads MIT. The image is distroless, so saying so is accurate.
   - Books (`/mnt/user/Books`) and Downloads (`/mnt/user/downloads/bindery`)
     have those defaults and `Required="true"`.
   - `<Icon>` and `<TemplateURL>` are raw GitHub URLs that answer 200.
4. After their PR merges, CA picks it up within about a day. Search CA for
   "bindery" and check the overview.

`patches/selfhosters-bindery.diff` at the repository root is the last patch
made for their copy. It applies with `patch -p1` from the root of their repo.
Regenerate it from their live file rather than reusing an old one.

## Validating

```bash
python3 -c "import xml.etree.ElementTree as E; E.parse('.github/unraid/bindery.xml')"
curl -sI "https://raw.githubusercontent.com/vavallee/bindery/main/.github/assets/logo.png" | head -1
```

If you have an Unraid box (or a VM): Apps, "Click here to add a missing
application", paste the raw URL of `bindery.xml`. The form should render
1 port, 4 paths, 3 always visible env vars, and a handful of advanced ones.
Click Apply, watch the container come up, and open `http://<unraid-ip>:8787/`.
