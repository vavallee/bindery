# Calibre on a Windows desktop

This runbook connects Bindery to the Calibre desktop app on a Windows PC through the [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins). It is written from real setups: Bindery in a container with its library on a NAS share, Bindery in WSL on the same PC, and Calibre 9 on Windows 11. For how the plugin fits alongside the other ways of reaching Calibre, see [Calibre and Calibre-Web-Automated](Calibre-Integration-Wiki.md).

**What you end up with.** Every ebook Bindery imports is added to your Calibre library, and your existing Bindery library can be sent across in one run. Calibre does not have to be open when a book is imported: Bindery keeps it in a delivery queue until Calibre can take it. From Calibre you can then send books to an e-reader.

## Choose a transport

The plugin can be reached two ways. You pick one on the Calibre tab under **Transport** (`calibre.plugin_transport`).

| Transport | How it works | Use it when |
|---|---|---|
| **Pull** | Calibre fetches from Bindery. The plugin connects out to Bindery, downloads each waiting book over HTTP(S) and adds it | Recommended for a desktop Calibre. It needs no shared drive, no path remap, no inbound firewall rule and no fixed address for the PC. Needs plugin **0.8.0 or later** |
| **Push** | Bindery sends paths to Calibre. Bindery connects to the plugin and hands it each book's file path, and Calibre opens that path itself | Calibre runs in a container or on a server that already sees the Bindery library |

Both go through the same delivery queue, so retries, **Push all to Calibre**, books with several formats and the Calibre tab's **Delivery queue** panel work the same either way. Switching from one to the other later never makes a second copy of a book: a book Calibre already has is recognised as already there.

**What you need.**

| Item | Pull | Push | Why |
|---|---|---|---|
| Calibre desktop on the PC | Yes | Yes | The plugin runs inside Calibre. Books wait in Bindery's queue while Calibre is closed |
| Bindery Bridge plugin | 0.8.0 or later | 0.7.0 or later | 0.8.0 adds pull. 0.7.0 puts every format of a book on the same Calibre record. Before 0.6.2 the plugin fails on long share paths and can leave empty records behind (see [Troubleshooting](#troubleshooting)) |
| A Bindery URL the PC can open | Yes | No | The plugin connects to it. Use HTTPS when Bindery is not on the same machine (see [Keep the key safe in pull mode](#keep-the-key-safe-in-pull-mode)) |
| Bindery's library on a network share the PC can open, for example `\\nas\media\books` | No | Yes | In push Bindery sends a file path, not the file, so Calibre has to open that path itself |
| Admin rights on the PC | No | Yes | For the inbound firewall rule |
| Access to your router's DHCP settings | No | Yes | For a fixed address |

## Install the Bindery Bridge plugin

Both transports start here.

1. Download `calibre-bridge-vX.Y.Z.zip` and `calibre-bridge-vX.Y.Z.zip.sha256` from [the plugin releases](https://github.com/vavallee/bindery-plugins/releases). Take 0.8.0 or later for pull, 0.7.0 or later for push.
2. Check the download in a Command Prompt, in the folder that holds both files:

   ```
   certutil -hashfile calibre-bridge-vX.Y.Z.zip SHA256
   ```

   Open the `.sha256` file in Notepad and compare. The two hashes must match; upper or lower case does not matter.
3. Install it, either way:
   - in Calibre: **Preferences, Plugins, Load plugin from file**, then pick the zip
   - or close Calibre and run `"C:\Program Files\Calibre2\calibre-customize.exe" -a calibre-bridge-vX.Y.Z.zip`
4. Restart Calibre. Calibre only loads a new plugin, or a new version of one, after a restart. Upgrading later is the same steps.

The plugin's settings are under **Preferences, Plugins, User plugins, Bindery Bridge, Customize**, and are stored in `%APPDATA%\calibre\plugins\bindery_bridge.json`.

## Set up pull

1. **Open the right library in Calibre.** Pull delivers only to the library that is open when you turn it on, and pauses while any other library is open, so books never land in the wrong one.
2. **Make a key in the plugin.** Open the plugin's **Customize** dialog, click **Generate** next to **API key**, then **Show**, and copy the key. Leave the dialog open.
3. **Set Bindery to pull.** In Bindery open **Settings, Calibre tab**:

   | Field | Value |
   |---|---|
   | **Write integration** (`calibre.mode`) | **Calibre Bridge plugin** |
   | **Transport** (`calibre.plugin_transport`) | **Pull: Calibre fetches books from Bindery** |
   | **API key** (`calibre.plugin_api_key`) | The key you copied. Save it with the button next to the field |

   **Plugin URL** and **Push path remap** disappear in pull; they are not used. The key must be at least 16 characters, and Bindery refuses every pull request while it is shorter or empty; a generated key is 64. The same key goes into the plugin, and it authenticates both directions.
4. **Turn pull on in the plugin.** Back in the **Customize** dialog, under **Pull from Bindery**:

   | Setting | Value |
   |---|---|
   | **Pull mode** | Tick **Fetch books from Bindery (pull mode)** |
   | **Bindery URL** | The address you open Bindery at, including any URL base (`BINDERY_URL_BASE`), for example `https://bindery.example.net` or `http://192.168.1.20:8787/bindery`. When Bindery runs on this same PC in WSL or Docker Desktop, use `http://127.0.0.1:8787`, not `localhost` (see [Troubleshooting](#pull-troubleshooting)) |
   | **CA file** | Leave empty unless Bindery's certificate comes from your own certificate authority or is self signed. Then point it at that certificate as a PEM file. There is no setting that turns certificate checks off |

   **Listen port** and **Bind host** belong to push. The push server keeps running in pull mode; if you only use pull you can set **Bind host** to `127.0.0.1` so it only answers the PC itself.
5. **Click OK.** Saving starts a pull at once, with no restart. After that the plugin checks Bindery every 60 seconds by default.
6. **Confirm it works.** Reopen **Customize** and read **Pull status**. It should say `Connected to Bindery; nothing waiting`, or `Connected to Bindery; N of M delivered in the last pass`. In Bindery the **Delivery queue** panel on the Calibre tab should say **Calibre last checked in** with a time, and **Test connection** reports the same with the plugin version. "Calibre has not checked in since Bindery started" means the plugin has not reached Bindery yet; see [Pull troubleshooting](#pull-troubleshooting). The check in time is held in memory, so it is blank after a Bindery restart until the plugin's next check.

Then send your existing library across with [Push all to Calibre](#send-your-existing-library).

## Keep the key safe in pull mode

- **The key opens your queue.** Anyone who holds it can list and download every book waiting for Calibre. The plugin sends it to whatever Bindery URL you configure, so a plugin pointed at a wrong or hostile URL hands that server your key. Only enter a Bindery URL you trust, and change the key in both places if it ever went somewhere else.
- **Use HTTPS when Bindery is not on the same machine.** Over plain `http` the key and every book cross the network unencrypted. The plugin warns about this in its status when the URL is plain `http` to anything but the PC itself. It also refuses redirects, so the key is never forwarded to a second address.
- **Certificate checks cannot be turned off.** For a private certificate authority or a self signed certificate, give the plugin the certificate in **CA file**.
- **The plugin key and the Bindery API key are separate.** The Bindery API key and a browser session are refused on the bridge routes, and the plugin key is refused everywhere under `/api/v1`. The plugin key is required in every auth mode, including Disabled and Local only.
- **Repeated wrong keys are rate limited.** Too many bad keys from one address get `429` with a `Retry-After` header, on a counter separate from the login form, so a plugin with an old key cannot lock you out of the web UI. A plugin that hits the limit waits as long as Bindery asks.

The routes are documented in [API.md](API.md#calibre-bridge-pull).

## Set up push

Use push when Calibre can open the Bindery library by path. On a desktop PC that means a network share, an inbound firewall rule and a fixed address, which is what the steps below cover.

### 1. Find the share address Calibre can open

Do not use a mapped drive letter. A mapped drive such as `Z:` belongs to the Windows logon session that created it, so Explorer can show `Z:\books` while the running Calibre cannot see it at all. In the real setup `Z:\books` probed as not existing and the share address worked first time.

Find the share address behind the drive letter from a Command Prompt:

```
net use
```

The **Remote** column shows it, for example `Z:  \\nas\media`. Your books folder is then `\\nas\media\books`. Paste that into the Explorer address bar to confirm it opens. If the NAS asks for a login, tick **Remember my credentials** so Calibre, which runs as you, can use the same login.

You will also need the path of the same folder as Bindery sees it inside its container, which is `BINDERY_LIBRARY_DIR` (for example `/books`). The two together make the push path remap in step 5.

### 2. Configure the plugin

In the plugin's **Customize** dialog:

| Setting | Value | Notes |
|---|---|---|
| Listen port | `8099` (the default) | Any free port works; use the same one in the firewall rule and in Bindery |
| Bind host | `0.0.0.0` (the default) | Listens on every interface, which is what lets the Bindery host reach it. `127.0.0.1` would only accept connections from the PC itself |
| API key | Click **Generate** | Copy it now; Bindery needs the same key |

Leave **Pull mode** unticked. The plugin refuses to serve its API on a non loopback bind host with an empty key, so leaving the key blank on `0.0.0.0` does not open anything; it just stops the plugin working.

### 3. Let the Bindery host through the firewall

Do not assume a rule already exists. On the test machine the rules named "The main calibre program" were not made by the Calibre installer: Windows created them from its **Allow access** prompt the first time `calibre.exe` listened on a port, and they applied only to the Public profile. A home network is usually Private, so those rules did nothing for it.

Check which profile your network uses, then which Calibre rules exist, in PowerShell:

```powershell
Get-NetConnectionProfile
Get-NetFirewallRule -DisplayName "The main calibre program*" | Format-Table DisplayName,Enabled,Action,Profile
```

`NetworkCategory` in the first output is the active profile. If no enabled Allow rule covers it, add one from an elevated PowerShell (Run as administrator), scoped to the plugin port and your local subnet:

```powershell
New-NetFirewallRule -DisplayName "Bindery Bridge" -Direction Inbound -Protocol TCP -LocalPort 8099 -RemoteAddress 192.168.1.0/24 -Action Allow
```

Change `192.168.1.0/24` to your own subnet. The rule Windows makes from its prompt allows any remote address on any port Calibre opens, which is broader than the plugin needs. A Block rule for Calibre on the active profile wins over any Allow rule, so if the check above shows one, disable it.

### 4. Give the PC a fixed address

The plugin URL in Bindery names the PC by its address, and a PC normally gets its address from DHCP, which can change it. In your router, add a DHCP reservation for the PC so it always gets the same one, for example `192.168.1.50`. Find the current address with `ipconfig` (the **IPv4 Address** line for the adapter you use).

### 5. Point Bindery at the plugin

In Bindery open **Settings, Calibre tab**, set **Write integration** to **Calibre Bridge plugin** and **Transport** to **Push: Bindery sends books to Calibre**. Then fill in:

| Field | Value |
|---|---|
| **Plugin URL** (`calibre.plugin_url`) | `http://192.168.1.50:8099`, with the PC's reserved address and the plugin port |
| **API key** (`calibre.plugin_api_key`) | The key you generated in the plugin |
| **Push path remap** (`calibre.push_path_remap`) | Bindery's path on the left, the share on the right: `/books:\\nas\media\books` |

Type the backslashes once, exactly as you would in Explorer. The left side is the library path inside the Bindery container, not a path on the PC. If Bindery has more than one library root on the share, add a pair for each, separated by commas.

Changes take effect on the next delivery run; Bindery does not need a restart.

### 6. Run Test connection

Click **Test connection** under the Calibre settings. It checks three things in turn: that the plugin answers, that Calibre can see the library root through your remap, and that Calibre can read one real book from your library through the same remap. The last check matters because a remap can reach the root and still produce broken paths for the books under it.

| Message | What it means | Fix |
|---|---|---|
| `plugin client: health: ...` followed by `connection refused`, `i/o timeout` or `Client.Timeout exceeded` | Bindery could not reach the plugin | Check that Calibre is open, the firewall rule from step 3 covers the active profile, the Plugin URL has the right address and port, and that neither the PC nor Bindery sits behind a VPN that drops LAN traffic ([Running Bindery behind a VPN](DEPLOYMENT.md#running-bindery-behind-a-vpn-network_mode-service)) |
| `plugin client: authentication failed, check api_key in Settings then Calibre` | The plugin answered but rejected the key | Copy the key from the plugin's Customize dialog into Bindery again |
| `the plugin answered but is not serving the API: ...` | The plugin refused to start its API, usually because the key is empty on a `0.0.0.0` bind | Generate a key in the plugin and restart Calibre |
| `plugin reachable, but the Calibre container cannot see "..."` | Calibre cannot open the library root your remap produces | Check the right side of the remap is the share address from step 1, not a drive letter, and that it opens in Explorer on the PC |
| `... but not the book at "..."` | The root works but a real book path does not | The remap covers the root but not where your books are. Check the remap pair, and add a pair for any other root folder your books are stored under |
| `... can see the book at "..." but cannot read it` | The file is there but Calibre cannot open it | Check the share permissions for the Windows account Calibre runs as |
| Any failure ending `S: is a drive letter. A mapped drive belongs to one Windows logon session...` | The remap points at a mapped drive | Use the share address, like `\\nas\share\books`, as described in step 1 |
| `plugin reachable, and it can read ... and the book at "..."` | It works | Carry on |
| `... No imported book was found to test, so only the library root was checked.` | It works as far as it can tell | Test again after the first import |

If the result also carries a warning that the Bindery Bridge version is older than 0.6.2, update the plugin ([Install the Bindery Bridge plugin](#install-the-bindery-bridge-plugin)) before sending anything: 0.6.1 fixes long network share paths and 0.6.2 fixes the empty books a failed add can leave behind.

The message says "container" whatever Calibre runs in. On Windows read it as "the PC".

## Send your existing library

**Push all to Calibre** appears on the Calibre tab in plugin mode, with either transport. Despite the name it sends nothing itself: it puts every ebook file of every imported, monitored book that the delivery queue does not hold yet into the queue, and the queue delivers them. In push Bindery sends them once the plugin answers; in pull the plugin takes them at its next check. Calibre does not have to be open when you click it.

Running it again is safe. A book the queue already holds, delivered or not, is left alone, so a book delivered earlier is never sent twice. For the same reason a rerun does not retry failures: use **Retry failed** for those.

The progress window reads the queue and has four tiles, counted per file:

| Tile | Meaning |
|---|---|
| **Pushed** | Added to Calibre in this run, as a new record or as a format added to one |
| **Already in Calibre** | Matched a book Calibre already had, or was delivered by Bindery before this run, and was left alone |
| **Failed** | Calibre refused it; the table under the tiles gives the reason for each (see [Troubleshooting](#troubleshooting)) |
| **Skipped** | Imported books the run left out, each with a reason: `not monitored`, `no file on disk`, `audiobook only with no ebook` or `ebook file not tracked`. Books not yet imported are not listed, since they have no file to send |

While books are still waiting the window says how many, and you can close it; the queue keeps going. The first run on the real push setup sent 1,540 books, found 337 already in Calibre and failed 17. All 17 were fixed by plugin 0.6.1 and 0.6.2, and retrying them picked them up.

## Watch the delivery queue

The **Delivery queue** panel on the Calibre tab shows how many books are **Waiting**, **Delivered** and **Failed**, and when the last one was delivered. In push it also says whether Calibre was reachable the last time Bindery had something to send, with the error when it was not; in pull it says when Calibre last checked in. Below it the **Failed deliveries** table lists every failed book with its code, the error, the attempts and the last try. A book's own page shows **Waiting for Calibre**, **In Calibre** or **Calibre failed**.

| Button | What it does |
|---|---|
| **Retry failed** | Puts every failed book back in the queue. Fix the cause first; this is the recovery step for almost everything on this page |
| **Clear waiting** | Drops every book still waiting. The record of what was already delivered is kept |
| **Reset delivery state** | Forgets every delivery, without touching either library. Only for pointing Bindery at a different Calibre library: afterwards run **Push all to Calibre** to fill the new one |

## Get books onto a Kobo

Bindery stops at Calibre; Calibre does the device side.

1. Connect the Kobo to the PC by USB and let Calibre detect it.
2. Select the books and use **Send to device**.
3. Kobo renders KEPUB better than plain EPUB (faster page turns, reading stats). Recent Calibre versions can convert to KEPUB on their own; if yours does not offer it, the KoboTouchExtended plugin adds it.

Reading Bindery's library directly on an e-reader, without Calibre, is covered in [Reading on an e-reader over OPDS](#reading-on-an-e-reader-over-opds).

## Reading on an e-reader over OPDS

Besides the Calibre route, Bindery serves its own OPDS catalogue at `/opds` (under `BINDERY_URL_BASE` if you set one). Readers such as KOReader and Moon+ Reader browse it and download books straight from Bindery, with no Calibre involved. A requester account gets `403` here; only admin and user accounts can read the feed.

**What works.** Add the catalogue to the reader as `https://your-bindery-address/opds`, with your Bindery username and password saved in the reader. The reader sends them as HTTP Basic credentials on every request, so browsing and downloading both work. KOReader was measured doing exactly this: browse and download with a saved login.

**What fails: the Android download manager.** Moon+ Reader on Android was measured behind a reverse proxy, away from home. Browsing the catalogue with the saved login worked, but the book download was handed to Android's download manager (user agent `Dalvik/2.1.0`), which sends no login, so `/opds/book/<id>/file` answered `401`. At home it worked only because of the local network exemption described below. Bindery does not change this: a download link that carries its own credential was proposed in [#3464](https://github.com/vavallee/bindery/issues/3464) and declined, because a link that downloads without a login ends up in proxy logs and reader histories. The reader hands the download to something else, so the fix has to be on one of the sides below.

### How OPDS decides who may in

Checked in this order (`internal/api/opds_auth.go`). The first match wins.

1. **Auth mode Disabled** admits everyone, as long as the address used to reach Bindery is an accepted host name.
2. **The API key**, sent as an `X-Api-Key` header or as `?apikey=` on the URL. Unlike the main API, OPDS takes the query form on every route it serves, because a reader cannot always set a header. The key is in **Settings, General, Security**.
3. **Auth mode Local only** admits a reader on a private network address (and again only under an accepted host name). The address is the one Bindery sees, so behind a reverse proxy this depends on `BINDERY_TRUSTED_PROXY`.
4. **A signed in session cookie.** Readers do not have one.
5. **Basic credentials**, a Bindery username and password.
6. Otherwise `401` with a Basic challenge, which is what makes a reader prompt for a login.

Two limits matter for readers.

- **The feed does not carry the API key.** No link Bindery writes into a feed carries the API key or any other credential, including the download link (`internal/opds/builder.go`). Some links do have a query string, such as the search template and proxied cover images, but none holds a key. A `?apikey=` on the catalogue address authenticates that one request only. The links the reader follows next do not have it, so the key is no way to make a reader that does not send a login work.
- **Basic needs a password on the account.** An account created automatically on someone's first proxy or OIDC sign in has an empty password (`GetOrCreateByUsername` and the OIDC provisioning in `internal/db/auth.go`), so Basic can never match it. An account an admin created with a password does work, which is why `docs/auth-oidc.md` has you create accounts before turning local sign in off. Give the reader an account that has a password, or use the API key. Turning off local sign in with `BINDERY_LOCAL_AUTH_ENABLED=false` stops the login form but does not itself stop OPDS Basic for accounts that do have a password.

### Workarounds that exist today

- **Use a reader that sends its login on downloads.** KOReader does, per the measurement above. Whether a given reader can be told to download itself rather than through the system downloader is a reader setting, not something Bindery controls.
- **Local network exemption.** With the auth mode set to **Local only**, readers on your home network need no login at all. This is why the failure shows up only away from home. Do not use it as a fix for remote reading: it also opens the whole web UI and API to the private network as the administrator.
- **Reverse proxy.** Because Bindery accepts `X-Api-Key` on any OPDS route, a proxy you control can add that header itself to the requests the download manager makes, or rewrite the acquisition links, which is what the reporter of [#3464](https://github.com/vavallee/bindery/issues/3464) does. The trade-off is yours: a proxy rule that adds the key for `/opds/book/<id>/file` makes that path download for anyone who can reach the proxy, so restrict it, for example to your own source addresses or behind the proxy's own login or an expiring signed link. Bindery has no setting for this and does not check what the proxy does.
- **Via Calibre.** If the e-reader can be plugged in, the [Kobo route above](#get-books-onto-a-kobo) avoids OPDS completely.

If your reader works some other way, say which reader and which step fails in a new issue.

## What to expect day to day

- **Books imported while Calibre is closed wait for it.** Bindery queues every imported ebook and delivers it when Calibre is reachable: in push within about a minute of Calibre running again, in pull at the plugin's next check, 60 seconds by default. Nothing to do on your side.
- **Failed deliveries are retried with backoff.** A book Calibre answers with an error is tried again after 1 minute, 5 minutes, 15 minutes, 1 hour, 6 hours and then daily, and marked failed after 8 attempts. Two errors are marked failed at once because waiting cannot fix them: `bad_format` and `path_forbidden`. See [Deliveries are queued and retried](Calibre-Integration-Wiki.md#deliveries-are-queued-and-retried).
- **Every ebook format of a book lands on the same Calibre record** with plugin 0.7.0 or later, EPUB first and the others added to it. With an older plugin the first format arrives and the others are held with the reason `bridge cannot add a second format; update the Calibre plugin to 0.7.0`, then delivered on their own once the plugin is updated.
- **Ebooks only.** Audiobooks are never sent to Calibre. Use Audiobookshelf for those.
- **Every book exists twice on disk.** Calibre copies each file into its own library folder, so the Bindery copy and the Calibre copy both stay. That is what keeps Calibre's own edits and conversions away from Bindery's files.
- **Never point Calibre's Auto-add folder at the Bindery library.** Auto-add removes the files it adds, so it would empty Bindery's library into Calibre's.

## Troubleshooting

### Pull troubleshooting

Read the plugin's **Pull status** first (reopen **Customize** to refresh it), then the **Delivery queue** panel in Bindery.

**Pull status says `Bindery is set to push; switch Settings, Calibre, Transport to Pull`.** Bindery answered but is not in pull mode. On the Calibre tab set **Write integration** to **Calibre Bridge plugin** and **Transport** to **Pull**. The plugin checks again every five minutes while Bindery says push; click OK in its **Customize** dialog to try at once.

**Pull status says `Bindery rejected the API key; set the same key here and in Bindery Settings, Calibre`.** Bindery answered `401`: the key in the plugin and the key in Bindery differ, or Bindery's key is shorter than 16 characters. Copy the key into both again. After a rejected key the plugin waits an hour before trying again; click OK in its **Customize** dialog to retry at once.

**Pull status says `Bindery asked the plugin to slow down`.** Bindery answered `429`: too many requests with a wrong key came from that address. The plugin waits as long as Bindery's `Retry-After` says, at most an hour, then carries on. Fix the key if it is wrong; the first request with the right key after the wait clears the count.

**Pull status says `Paused: a different library is open`.** Pull only delivers to the library that was open when it was turned on. Switch back to that library, or untick pull, click OK, then tick it again with the right library open.

**Pull status says `Cannot reach Bindery; retrying with backoff`.** The plugin got no answer, or a server error. Check that the **Bindery URL** opens in a browser on the PC. While Bindery stays unreachable the plugin waits longer between tries, up to 15 minutes. `This Bindery has no pull routes; update Bindery or check the URL` means the URL reached a server without the pull routes: a Bindery older than the pull release, or a URL missing its URL base.

**Pull status says `Bindery redirected the request`.** Something in front of Bindery answered with a redirect, for example from `http` to `https`, and the plugin never follows one. Set the **Bindery URL** to the address the redirect pointed at.

**Pull status warns `Bindery URL is plain http`.** The key and the books cross the network unencrypted. Fine when Bindery is on the same trusted LAN; use HTTPS otherwise (see [Keep the key safe in pull mode](#keep-the-key-safe-in-pull-mode)).

**Every request from the plugin takes about 20 seconds.** The **Bindery URL** says `localhost` and Bindery runs in WSL or Docker Desktop on the same PC. Windows tries IPv6 `::1` first, the forwarding into WSL or Docker drops that connection silently, and Windows falls back to IPv4 only after about 21 seconds. Use `http://127.0.0.1:8787`, or the machine's address, instead of `localhost`.

**Books fail with `path_forbidden`.** The book's file is outside Bindery's library folders (`BINDERY_LIBRARY_DIR`, `BINDERY_AUDIOBOOK_DIR` or a root folder), and Bindery will not serve a file from anywhere else. Move or import the book into the library, then click **Retry failed**. It is marked failed at once rather than retried, because waiting cannot fix it.

### Push troubleshooting

**`[Errno 22] Invalid argument` on a path that starts with `\\?\\\`.** The share path is long, over about 200 characters, and Calibre builds an invalid long path form for a network path. Fixed in plugin 0.6.1. Upgrade the plugin, restart Calibre and click **Retry failed** on the Calibre tab.

**Test connection passes but deliveries still fail.** Read the reason in the **Failed deliveries** table on the Calibre tab (or the Failed table in the Push all window). A path error there usually means the remap covers the library root but the book sits under a different root folder; add a pair for that root, then click **Retry failed**.

**Books sit in Waiting and nothing arrives.** The **Delivery queue** panel says `Calibre not reachable` with the error Bindery got. Calibre is closed, the plugin URL or key is wrong, or the firewall is in the way. Once that is fixed the books go out within about a minute.

**The plugin stopped answering after a Windows update or a network change.** Windows may have moved the network to a different profile. Rerun the checks in [step 3](#3-let-the-bindery-host-through-the-firewall), or switch to pull, which needs no inbound rule.

### Either transport

**Push all says "Already in Calibre" but the Calibre record has no file.** An earlier failed add left an empty record behind, and the next delivery matched it. Plugin 0.6.2 removes the record when an add fails, and attaches the file to an existing empty record on the next delivery. Upgrade and restart Calibre. Bindery has recorded those books as delivered, so Push all on its own will not send them again: click **Reset delivery state** on the Calibre tab, confirm, then run **Push all to Calibre**. With plugin 0.6.0 or later that is safe, because every book Calibre already has comes back as already in Calibre instead of being added twice.

**`Cannot determine book format from extension` with a folder path.** Bindery recorded a folder as the book's ebook file, and the plugin cannot add a folder. In the real case the folder held an audiobook of a different book, so the fix is in Bindery, not the plugin: open the book, look at its **Files**, use **Forget this file** on the wrong entry, and import the right ebook.

**Books imported while Calibre was closed are missing.** They are waiting in the queue. Leave Calibre open: in push they arrive within about a minute, in pull at the plugin's next check. The **Delivery queue** panel shows how many are still waiting. If a book shows as failed, its reason is in the **Failed deliveries** table; fix the cause and click **Retry failed**. Running Push all again does not retry it.

**Only one format of a book reached Calibre.** The plugin is older than 0.7.0, so the other formats are held with the reason `bridge cannot add a second format; update the Calibre plugin to 0.7.0`. Update the plugin and restart Calibre; the held formats are delivered to the same record on their own.

## See also

- [Calibre and Calibre-Web-Automated](Calibre-Integration-Wiki.md) for the three ways Bindery hands books to Calibre, the delivery queue and the plugin's capabilities by version
- [User guide, Quick answers](User-Guide-Wiki.md#quick-answers) for short answers to the symptoms above
- [Plugin installation](https://github.com/vavallee/bindery-plugins/blob/main/docs/installation.md) for pull mode on the plugin side and for containerised Calibre
- [API.md, Calibre bridge (pull)](API.md#calibre-bridge-pull) for the routes the plugin calls
- [Troubleshooting](Troubleshooting-Wiki.md)
