# Calibre on a Windows desktop

This runbook connects Bindery to the Calibre desktop app on a Windows PC through the [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins). It is written from a real setup: Bindery in a container with its library on a NAS share, and Calibre 9 on Windows 11. For how the plugin fits alongside the other ways of reaching Calibre, see [Calibre and Calibre-Web-Automated](Calibre-Integration-Wiki.md).

**What you end up with.** Every ebook Bindery imports is added to your Calibre library while Calibre is open, and your existing Bindery library can be pushed across in one run. From Calibre you can then send books to an e-reader.

**What you need.**

| Item | Why |
|---|---|
| Bindery with its library on a network share the PC can open, for example `\\nas\media\books` | Bindery sends Calibre a file path, not the file. Calibre has to open that path itself |
| Calibre desktop running on the PC | The plugin runs inside Calibre and only listens while Calibre is open |
| Bindery Bridge plugin **0.6.2 or later** | Older versions fail on long share paths and can leave empty records behind (see [Troubleshooting](#troubleshooting)) |
| Admin rights on the PC | For the firewall rule |
| Access to your router's DHCP settings | For a fixed address |

## 1. Find the share address Calibre can open

Do not use a mapped drive letter. A mapped drive such as `Z:` belongs to the Windows logon session that created it, so Explorer can show `Z:\books` while the running Calibre cannot see it at all. In the real setup `Z:\books` probed as not existing and the share address worked first time.

Find the share address behind the drive letter from a Command Prompt:

```
net use
```

The **Remote** column shows it, for example `Z:  \\nas\media`. Your books folder is then `\\nas\media\books`. Paste that into the Explorer address bar to confirm it opens. If the NAS asks for a login, tick **Remember my credentials** so Calibre, which runs as you, can use the same login.

You will also need the path of the same folder as Bindery sees it inside its container, which is `BINDERY_LIBRARY_DIR` (for example `/books`). The two together make the push path remap in step 6.

## 2. Install the Bindery Bridge plugin

1. Download `calibre-bridge-vX.Y.Z.zip` and `calibre-bridge-vX.Y.Z.zip.sha256` from [the plugin releases](https://github.com/vavallee/bindery-plugins/releases). Take 0.6.2 or later.
2. Check the download in a Command Prompt, in the folder that holds both files:

   ```
   certutil -hashfile calibre-bridge-vX.Y.Z.zip SHA256
   ```

   Open the `.sha256` file in Notepad and compare. The two hashes must match; upper or lower case does not matter.
3. Install it, either way:
   - in Calibre: **Preferences, Plugins, Load plugin from file**, then pick the zip
   - or close Calibre and run `"C:\Program Files\Calibre2\calibre-customize.exe" -a calibre-bridge-vX.Y.Z.zip`
4. Restart Calibre. Calibre only loads a new plugin, or a new version of one, after a restart. Upgrading later is the same steps.

## 3. Configure the plugin

Open **Preferences, Plugins, User plugins, Bindery Bridge, Customize**.

| Setting | Value | Notes |
|---|---|---|
| Listen port | `8099` (the default) | Any free port works; use the same one in the firewall rule and in Bindery |
| Bind host | `0.0.0.0` (the default) | Listens on every interface, which is what lets the Bindery host reach it. `127.0.0.1` would only accept connections from the PC itself |
| API key | Click **Generate** | Copy it now; Bindery needs the same key |

The settings are stored in `%APPDATA%\calibre\plugins\bindery_bridge.json`. The plugin refuses to serve its API on a non loopback bind host with an empty key, so leaving the key blank on `0.0.0.0` does not open anything; it just stops the plugin working.

## 4. Let the Bindery host through the firewall

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

## 5. Give the PC a fixed address

The plugin URL in Bindery names the PC by its address, and a PC normally gets its address from DHCP, which can change it. In your router, add a DHCP reservation for the PC so it always gets the same one, for example `192.168.1.50`. Find the current address with `ipconfig` (the **IPv4 Address** line for the adapter you use).

## 6. Point Bindery at the plugin

In Bindery open **Settings, Calibre tab, Write integration** and choose **Calibre Bridge plugin**. Then fill in:

| Field | Value |
|---|---|
| **Plugin URL** (`calibre.plugin_url`) | `http://192.168.1.50:8099`, with the PC's reserved address and the plugin port |
| **API key** (`calibre.plugin_api_key`) | The key you generated in the plugin |
| **Push path remap** (`calibre.push_path_remap`) | Bindery's path on the left, the share on the right: `/books:\\nas\media\books` |

Type the backslashes once, exactly as you would in Explorer. The left side is the library path inside the Bindery container, not a path on the PC. If Bindery has more than one library root on the share, add a pair for each, separated by commas.

Changes take effect on the next import; Bindery does not need a restart.

## 7. Run Test connection

Click **Test connection** under the Calibre settings. It checks three things in turn: that the plugin answers, that Calibre can see the library root through your remap, and that Calibre can read one real book from your library through the same remap. The last check matters because a remap can reach the root and still produce broken paths for the books under it.

| Message | What it means | Fix |
|---|---|---|
| `plugin client: health: ...` followed by `connection refused`, `i/o timeout` or `Client.Timeout exceeded` | Bindery could not reach the plugin | Check that Calibre is open, the firewall rule from step 4 covers the active profile, the Plugin URL has the right address and port, and that neither the PC nor Bindery sits behind a VPN that drops LAN traffic ([Running Bindery behind a VPN](DEPLOYMENT.md#running-bindery-behind-a-vpn-network_mode-service)) |
| `plugin client: authentication failed, check api_key in Settings then Calibre` | The plugin answered but rejected the key | Copy the key from the plugin's Customize dialog into Bindery again |
| `the plugin answered but is not serving the API: ...` | The plugin refused to start its API, usually because the key is empty on a `0.0.0.0` bind | Generate a key in the plugin and restart Calibre |
| `plugin reachable, but the Calibre container cannot see "..."` | Calibre cannot open the library root your remap produces | Check the right side of the remap is the share address from step 1, not a drive letter, and that it opens in Explorer on the PC |
| `... but not the book at "..."` | The root works but a real book path does not | The remap covers the root but not where your books are. Check the remap pair, and add a pair for any other root folder your books are stored under |
| `... can see the book at "..." but cannot read it` | The file is there but Calibre cannot open it | Check the share permissions for the Windows account Calibre runs as |
| Any failure ending `S: is a drive letter. A mapped drive belongs to one Windows logon session...` | The remap points at a mapped drive | Use the share address, like `\\nas\share\books`, as described in step 1 |
| `plugin reachable, and it can read ... and the book at "..."` | It works | Carry on |
| `... No imported book was found to test, so only the library root was checked.` | It works as far as it can tell | Test again after the first import |

If the result also carries a warning that the Bindery Bridge version is older than 0.6.2, update the plugin (step 2) before pushing: 0.6.1 fixes long network share paths and 0.6.2 fixes the empty books a failed add can leave behind.

The message says "container" whatever Calibre runs in. On Windows read it as "the PC".

## 8. Push your existing library

**Push all to Calibre** appears under the test button in plugin mode once the test has reached the plugin. It sends every imported, monitored book that has an ebook file. The progress window has four tiles:

| Tile | Meaning |
|---|---|
| **Pushed** | Added to Calibre in this run |
| **Already in Calibre** | Matched a book Calibre already had, and was left alone |
| **Failed** | Calibre refused it; the table under the tiles gives the reason for each (see [Troubleshooting](#troubleshooting)) |
| **Skipped** | Imported books the run left out, each with a reason: `not monitored`, `no file on disk`, or `audiobook only with no ebook`. Books not yet imported are not listed, since they have no file to send |

Running it again is safe and is the recovery step for almost everything on this page: books already in Calibre are skipped, and only the rest are tried. The first run on the real setup pushed 1,540 books, found 337 already in Calibre and failed 17. All 17 were fixed by plugin 0.6.1 and 0.6.2, and a second run picked them up.

## 9. Get books onto a Kobo

Bindery stops at Calibre; Calibre does the device side.

1. Connect the Kobo to the PC by USB and let Calibre detect it.
2. Select the books and use **Send to device**.
3. Kobo renders KEPUB better than plain EPUB (faster page turns, reading stats). Recent Calibre versions can convert to KEPUB on their own; if yours does not offer it, the KoboTouchExtended plugin adds it.

Reading Bindery's library on an e-reader directly over OPDS is not covered here yet. It is being checked in [#2834](https://github.com/vavallee/bindery/issues/2834).

## What to expect day to day

- **Calibre must be open for pushes to land.** A book imported while Calibre is closed is not retried automatically today; run **Push all to Calibre** again after you open Calibre and it picks up whatever was missed. A retry queue is tracked in [#2832](https://github.com/vavallee/bindery/issues/2832), and this step goes away when it lands.
- **Ebooks only.** Audiobooks are never sent to Calibre. Use Audiobookshelf for those.
- **Every book exists twice on disk.** Calibre copies each file into its own library folder, so the Bindery copy on the share and the Calibre copy both stay. That is what keeps Calibre's own edits and conversions away from Bindery's files.
- **Never point Calibre's Auto-add folder at the Bindery library.** Auto-add removes the files it adds, so it would empty Bindery's library into Calibre's.

## Troubleshooting

**`[Errno 22] Invalid argument` on a path that starts with `\\?\\\`.** The share path is long, over about 200 characters, and Calibre builds an invalid long path form for a network path. Fixed in plugin 0.6.1. Upgrade the plugin, restart Calibre and run **Push all to Calibre** again.

**Push all says "Already in Calibre" but the Calibre record has no file.** An earlier failed add left an empty record behind, and the next push matched it. Plugin 0.6.2 removes the record when an add fails, and attaches the file to an existing empty record on the next push. Upgrade, restart Calibre and push again.

**`Cannot determine book format from extension` with a folder path.** Bindery recorded a folder as the book's ebook file, and the plugin cannot add a folder. In the real case the folder held an audiobook of a different book, so the fix is in Bindery, not the plugin: open the book, look at its **Files**, use **Forget this file** on the wrong entry, and import the right ebook.

**Test connection passes but pushes still fail.** Read the reason in the Failed table. A path error there usually means the remap covers the library root but the book sits under a different root folder; add a pair for that root.

**Books imported while Calibre was closed are missing.** Expected today; run **Push all to Calibre** after opening Calibre (see [What to expect day to day](#what-to-expect-day-to-day)).

**The plugin stopped answering after a Windows update or a network change.** Windows may have moved the network to a different profile. Rerun the checks in [step 4](#4-let-the-bindery-host-through-the-firewall).

## See also

- [Calibre and Calibre-Web-Automated](Calibre-Integration-Wiki.md) for the three ways Bindery hands books to Calibre and the plugin's capabilities by version
- [User guide, Quick answers](User-Guide-Wiki.md#quick-answers) for short answers to the symptoms above
- [Plugin installation](https://github.com/vavallee/bindery-plugins/blob/main/docs/installation.md) for containerised Calibre
- [Troubleshooting](Troubleshooting-Wiki.md)
