# icloudcli — Agent Context

## What this repo is

`icloud-pp-cli` — a macOS command-line tool that reads the user's Photos library (Photos.sqlite) without touching Photos.app. It is published at **icloudcli.com**.

## Critical: site identity

| Property | Value |
|---|---|
| **Public domain** | `icloudcli.com` |
| **Cloudflare Pages project** | `icloudcli` (branch `main` → production) |
| **Repo owner** | matysanchez |

**Do NOT confuse this with `artificialpoets.com`.**
- `artificialpoets.com` is a completely separate site and repo (`/Users/matysanchez/artificial-poets/artificialpoets.com/`).
- Never edit files under `artificial-poets/artificialpoets.com/` when working on icloudcli.

## Directory layout

```
/Users/matysanchez/personal/icloudcli/
  cmd/                        — CLI entry point
  internal/                   — CLI source (cobra commands)
  web/                        — Static site → icloudcli.com
    index.html                — The entire landing page (single file)
    apple2e.avif / .webp      — Optimised hero/newsletter image assets
    install.sh                — curl-pipe installer
  printing-press-library/     — Published catalog (separate AGENTS.md inside)
  Makefile / go.mod / go.sum
```

## Leads / newsletter

The newsletter signup form on icloudcli.com uses the **`leads` Worker** at `api.poets.sh` (a separate platform, not the same as artificialpoets.com content).

| Setting | Value |
|---|---|
| Worker URL | `https://api.poets.sh` |
| Embed script | `https://api.poets.sh/embed.js` |
| Leads project path | `/Users/matysanchez/artificial-poets/leads/` |
| D1 database | `leads-prod` (ID `32fc39a2-f976-4194-b0d0-8f183c67a31e`) |
| List name (slug) | `icloudcli-newsletter` |
| Opt-in mode | `single` (no confirmation email sent) |
| Turnstile sitekey | `0x4AAAAAADS18u_g2-im7Ml-` |

The leads Worker is deployed independently via `wrangler` from `/Users/matysanchez/artificial-poets/leads/`. Changes to the newsletter form HTML live in `web/index.html`.

## CLI commands shipped

| Group | Status | Permission | Notes |
|---|---|---|---|
| `doctor` | ✅ | — | Pre-flight checks for every data source + permissions |
| `photos` | ✅ | none | top, videos, storage, stats, search (person/date/type/favorites/gps/near/keyword), delete, download. Only `ask` (NLP) still 🚧 |
| `messages` | ✅ | Full Disk Access | list-chats, search, stats, audit, export. Reads `chat.db` read-only; attributedBody decoder |
| `contacts` | ✅ | none | sync (JXA→SQLite), list, get, search (FTS5), create/update/delete, merge, duplicates, analytics |
| `notes` | ✅ | Automation | sync (JXA→SQLite), list, get, search (FTS5), analytics |
| `reminders` | ✅ | Automation | sync, list (--overdue/--upcoming/--list/--completed/--all), get, search, analytics |
| `calendar` (alias `cal`) | ✅ | Automation | sync (windowed), agenda, list, search, analytics |
| `safari` | ✅ | Full Disk Access | history, search, top-sites, bookmarks (via plutil), analytics |

Source: `internal/cli/` — one `<group>.go` + `<group>_db.go` per command group.
Shared: `jxa.go` (JXA runner + Automation-denial detection), `helpers.go`
(shortID/shortDate/joinArgs, color), `doctor.go`, `root.go`.

**Architecture conventions** (mirror these when adding a group):
- `sync`-based groups (contacts, notes, reminders, calendar) read a scriptable
  app via JXA and cache into SQLite under `~/Library/Application Support/icloud-pp-cli/`.
  Pattern: typed columns + an FTS5 virtual table + a `*_sync_state` row.
- Direct-read groups (photos, messages, safari) open the OS database read-only
  via the `file:` URI with `mode=ro` (load-bearing on modernc.org/sqlite).
- Every command supports `--json`/`--agent`. Resolve-by-prefix via `GetByAny`
  (full id, exact UUID, or escaped-LIKE prefix). Never interpolate user input
  into AppleScript/JXA — pass as argv after `--`.

## Deploying the website

```bash
cd /Users/matysanchez/personal/icloudcli/web
npx wrangler pages deploy . --project-name icloudcli
```

## Photos library path (default)

`~/Pictures/Photos Library.photoslibrary/database/Photos.sqlite`

Sentinel coordinates for "no GPS data": `-180.0, -180.0` — always filter with
`ZLATITUDE BETWEEN -89 AND 89 AND ZLONGITUDE BETWEEN -179 AND 179`.
