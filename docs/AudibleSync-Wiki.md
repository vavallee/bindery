# Audible Sync integration

[Audible Sync](https://github.com/AlexStansfield/audible-sync) is a separate,
self-hosted service that signs in to your Audible account, downloads and decrypts
your library, and serves your wishlist and library over an HTTP API. When you run
it, Bindery can sync your **Audible wishlist** into your library as wanted
audiobooks.

The integration is **off by default** and is a preview. Nothing changes until an
admin turns it on.

## Set it up

1. Run Audible Sync and sign in to at least one Audible account there.
2. Find its API token. Audible Sync logs one on its first start, or uses
   `AUDIBLE_SYNC_API_TOKEN` when you set that. Its own *Settings, API token* shows
   the last four characters.
3. In Bindery open **Settings, Audible Sync** (in the Integrations group).
4. Enter the **Server URL** (for example `http://audible-sync:8080`, with the base
   path if you serve it under one) and the **API token**, tick **Enable**, and
   **Save**.
5. Press **Test Connection**. It reports the Audible Sync version and how many
   Audible accounts it holds, and fills the account picker.
6. Optionally pick the default **Audible account** wishlist lists read, and
   **Save** again.
7. Open **Settings, Import** and press **Add Audible wishlist**.

The URL is checked like every other service URL: LAN and loopback addresses are
fine, link-local and cloud metadata addresses are refused. **Test Connection**
only ever probes the saved URL, never one typed into the form.

## The Audible wishlist as an import list

Each sync of a wishlist list:

- adds every wishlist book as a **wanted audiobook** and searches for it, unless
  *Download books from this list* is off, in which case it is added unmonitored;
- links the book to the work your primary metadata provider knows when the ASIN
  matches one, the same match **Add Book** by ASIN makes, so it sits beside the
  ebook of the same work. An audiobook the provider does not have (an Audible
  Original, say) is added as an Audible book under its author;
- **skips** a book whose author the metadata provider cannot find, rather than
  inventing an author no refresh could ever fill. It is counted as *unknown
  author* in the sync result;
- turns a tracked ebook of the same work into *ebook and audiobook*, so the
  audiobook is searched for too;
- leaves books on the import list exclusions alone.

Per list options:

| Option | Default | What it does |
|---|---|---|
| Audible account | Default account | Whose wishlist to read. The default is the account chosen in Settings, Audible Sync, or Audible Sync's oldest signed in account |
| Skip owned books | On | A book the Audible account already owns is left alone: Audible Sync downloads it itself |
| Remove from wishlist once imported | Off | Once Bindery has imported a wishlist book, the next sync takes it off the Audible wishlist. This writes to your Audible account |
| Owner | Global | Who sees the books it adds, on a multi user install |

Lists sync every 6 hours, or every `audiblesync.sync_interval` (1h to 168h,
read at startup). Reading the wishlist does not contact Audible: Audible Sync
serves it as its own sync last read it. **Sync now** runs a list at once.

Series are not linked from the wishlist yet: Audible's series do not match the
series the metadata provider files books under, and two rows for one series are
worse than none.

### Troubleshooting

| Message | Meaning |
|---|---|
| `could not reach Audible Sync` | The URL is wrong, or Bindery cannot route to it (a VPN container's killswitch is the usual cause; the message says so when the host looks LAN-local). |
| `rejected the API token` | The token is wrong or was regenerated in Audible Sync. |
| `needs to be signed in again in Audible Sync` | Audible refused the account. Sign it in again in Audible Sync; Bindery cannot. |
| `it has no Audible accounts yet` | The connection works, but there is nothing to read yet. |
| A sync adds nothing and logs that the wishlist is *as last read* | Audible Sync's latest read of the wishlist failed; it serves the previous one until its next sync works. |

## API

The connection routes are admin only.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/audiblesync/config` | The stored settings; the token is reported as `apiTokenConfigured`, never returned |
| `PUT` | `/api/v1/audiblesync/config` | Save any of `enabled`, `baseUrl`, `apiToken`, `accountId`. An empty token keeps the stored one. Every value is validated before anything is written |
| `POST` | `/api/v1/audiblesync/test` | Probe the saved URL; an `apiToken` in the body is tried in place of the stored one |
| `POST` | `/api/v1/audiblesync/accounts` | The Audible accounts Audible Sync holds |

Wishlist lists are ordinary import lists of type `audiblesync`
(`POST /api/v1/importlist` with `account`, `skipOwned` and `removeOnImport`).
`POST /api/v1/importlist/{id}/sync` starts one and answers 202, and
`GET /api/v1/importlist/sync/status?type=audiblesync` reports the run, with the
counts `owned`, `unresolved` and `removed` besides the usual ones.

The settings are `audiblesync.enabled`, `audiblesync.base_url`,
`audiblesync.api_token` (secret, never returned by the settings API),
`audiblesync.account_id` and `audiblesync.sync_interval`. Connection changes
apply to the next sync without a restart.

## What Audible Sync is

Audible Sync is not part of Bindery and Bindery does not install it. It is its
own project ([source and install guide](https://github.com/AlexStansfield/audible-sync)),
run by the operator, which talks to Audible on the operator's behalf with the
operator's own credentials.
