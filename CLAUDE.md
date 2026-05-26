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

| Command | Status | Notes |
|---|---|---|
| `doctor` | ✅ shipped | Pre-flight checks |
| `photos list` | ✅ shipped | List assets |
| `photos download` | ✅ shipped | Export assets via osascript; flags: `--output`, `--sensitive`, `--type`, `--limit`, `--confirm` |
| `photos search` | 🚧 coming | Not yet implemented |

Source: `internal/cli/` — one file per command group.

## Deploying the website

```bash
cd /Users/matysanchez/personal/icloudcli/web
npx wrangler pages deploy . --project-name icloudcli
```

## Photos library path (default)

`~/Pictures/Photos Library.photoslibrary/database/Photos.sqlite`

Sentinel coordinates for "no GPS data": `-180.0, -180.0` — always filter with
`ZLATITUDE BETWEEN -89 AND 89 AND ZLONGITUDE BETWEEN -179 AND 179`.
