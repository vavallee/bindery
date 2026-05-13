# Deploy manifests

Auxiliary Kubernetes manifests applied outside the main Helm chart. The
primary bindery app and telemetry/ping server are deployed through their
own charts (`charts/bindery`) and the `bindery-ping` Deployment; manifests
in this directory cover small companion services.

## `discord-stats.yaml` — Discord stats voice channels

A CronJob that runs every 10 minutes on the `hetz1` cluster in the
`bindery-ping` namespace. It updates three Discord voice channels with live
numbers:

- `📊 Active: <30d-active install count>`
- `📦 Latest: <latest released version>`
- `⭐ Stars: <GitHub star count>`

Telemetry data comes from `https://api.getbindery.dev/stats.json` (served by
`cmd/telemetry-server`). Stars come from the public GitHub repo API
(unauthenticated, well under the 60 req/hour limit at one call every 10 min).

### Setup

1. Create a Discord application at <https://discord.com/developers>, add a
   Bot, and copy its token.
2. Bot permissions: `Manage Channels` only (least privilege — the bot does
   not need anything else).
3. Add the bot to the bindery server using an OAuth URL with `bot` scope
   and the `Manage Channels` permission.
4. Create three voice channels in the server (e.g. `📊 Active`,
   `📦 Latest`, `⭐ Stars` — the bot overwrites the names on first run).
5. Get each channel's ID: Discord Settings → Advanced → enable Developer
   Mode, then right-click each voice channel → Copy ID.
6. Create the k8s secret holding the bot token and the three channel IDs:

   ```bash
   kubectl --context hetz1 -n bindery-ping create secret generic discord-stats \
     --from-literal=bot_token=<TOKEN> \
     --from-literal=active_channel_id=<ID> \
     --from-literal=latest_channel_id=<ID> \
     --from-literal=stars_channel_id=<ID>
   ```

7. Apply the manifest:

   ```bash
   kubectl --context hetz1 apply -f deploy/discord-stats.yaml
   ```

8. Force the first run (don't wait 10 min):

   ```bash
   kubectl --context hetz1 -n bindery-ping create job \
     --from=cronjob/discord-stats discord-stats-manual
   ```

### Notes on rate limits

Discord allows 2 channel renames per 10 minutes per channel. The cron is
scheduled `*/10 * * * *` so we are never close to the cap; in addition, the
bot reads the current name first and only PATCHes when it differs, so
quiescent ticks cost zero renames.
