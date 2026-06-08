# icloud-pp-cli

Query your iCloud data from the command line. Reads your Mac's local databases
directly — no Photos.app launch, no API token, no network calls.

**[icloudcli.com](https://icloudcli.com)** · macOS · Apache-2.0

---

## Install

```bash
go install github.com/matysanchez/icloudcli/cmd/icloud-pp-cli@latest
```

Or via [Printing Press](https://github.com/mvanhorn/printing-press-library):

```bash
npx -y @mvanhorn/printing-press install icloud
```

**Requires:** macOS (Sonoma / Sequoia), Go 1.23+

---

## Quick start

```bash
icloud-pp-cli doctor              # verify access to every data source
icloud-pp-cli photos top          # top 25 heaviest files
icloud-pp-cli messages audit      # conversation stats + longest threads
icloud-pp-cli contacts sync       # cache Contacts, then list/search/merge
icloud-pp-cli notes search wifi   # full-text search your Apple Notes
icloud-pp-cli reminders list --overdue
icloud-pp-cli calendar agenda     # the week ahead, grouped by day
icloud-pp-cli safari top-sites    # most-visited domains
```

Pipe any command for automatic JSON:

```bash
icloud-pp-cli photos top | jq '.[0:5]'
icloud-pp-cli messages audit --agent | jq .conversations
```

---

## Commands

Seven data sources, all read locally. The `sync`-based groups (contacts, notes,
reminders, calendar) cache into SQLite once, then every read is instant and
offline. Photos, messages, and safari read their databases directly.

```
icloud-pp-cli
  photos                                          [no extra permission]
    top        Top N heaviest files (--limit, --type all|photo|video)
    videos     Largest videos (--limit, --year, --month)
    storage    Breakdown by media type and year
    stats      Total items and library size
    search     Filter by --person, --year/--month, --type, --favorites,
               --has-gps, --near LAT,LON --radius, --keyword
    delete     Move items to Recently Deleted (dry run until --confirm)
    download   Export originals via Photos.app (--output, --sensitive)

  messages                                         [Full Disk Access]
    list-chats Conversations by recent activity (--limit, --since)
    search     Search bodies, attributedBody-aware (--chat, --handle, --since)
    stats      Totals, by-year, top handles
    audit      Unique conversations, longest threads, activity, direction
    export     Export a chat (or --chat all) to JSON

  contacts                                         [no extra permission]
    sync       Cache Contacts.app into local SQLite (JXA)
    list / get / search    Browse + FTS5 search (8-char UUID prefix lookup)
    create / update / delete   Write back to Contacts.app (syncs to iCloud)
    merge / duplicates     Find + merge dupes (dry run until --confirm)
    analytics  By country (E.164), email domain, missing fields

  notes                                            [Automation]
    sync       Cache Notes.app into local SQLite (JXA)
    list / get / search    Browse + FTS5 search of titles and bodies
    analytics  Word counts, folders, shared/locked/empty, longest

  reminders                                        [Automation]
    sync       Cache Reminders.app into local SQLite (JXA)
    list       Open by default (--list, --completed, --all, --overdue, --upcoming N)
    get / search    Show one, or FTS5 search titles/notes/lists
    analytics  Open/done/overdue, due today/this week, per-list

  calendar (alias: cal)                            [Automation]
    sync       Cache events in a window (--back, --forward) via JXA
    agenda     Upcoming events grouped by day (--days, --calendar)
    list       Explicit range (--from, --to, --calendar)
    search     FTS5 search titles, locations, notes
    analytics  Per-calendar hours, busiest weekday

  safari                                           [Full Disk Access]
    history    Recent pages (--limit, --domain, --since)
    search     Search history by URL and title
    top-sites  Most-visited domains by visit count
    bookmarks  All bookmarks, flattened with folder path
    analytics  History overview + top domains

  doctor       Pre-flight check for every data source + permissions
```

All commands accept: `--json` `--compact` `--no-color` `--agent` `--library PATH`
(plus `--messages-db PATH` for messages/doctor).

`--agent` sets `--json --compact --no-color` in one flag — use it in AI workflows.

---

## Permissions

icloudcli never sends data anywhere — but macOS gates some on-device databases:

| Group | Permission | How |
|---|---|---|
| photos, contacts | none | works out of the box |
| messages, safari | **Full Disk Access** | System Settings → Privacy & Security → Full Disk Access → add your terminal → quit & reopen |
| notes, reminders, calendar | **Automation** | macOS prompts on first `sync` — click OK (or pre-grant under Privacy & Security → Automation) |

Run `icloud-pp-cli doctor` anytime to see exactly what's granted and what's missing.

---

## Repository layout

```
icloudcli/
  cmd/icloud-pp-cli/   Go binary entry point
  internal/cli/        Command implementations — one <group>.go + <group>_db.go
                       per data source, plus jxa.go (shared JXA runner),
                       helpers.go, doctor.go, root.go
  web/                 Landing page (deployed to icloudcli.com via Cloudflare Pages)
  go.mod               module: github.com/matysanchez/icloudcli
```

Each `sync`-based group keeps its own SQLite cache under
`~/Library/Application Support/icloud-pp-cli/` (`contacts.db`, `notes.db`,
`reminders.db`, `calendar.db`). Photos reads `Photos.sqlite`, messages reads
`chat.db`, and safari reads `History.db` — all opened read-only.

### Submitting to Printing Press

To submit a snapshot to [printing-press-library](https://github.com/mvanhorn/printing-press-library):

1. Fork the library repo
2. Copy `cmd/`, `internal/`, `go.mod`, `go.sum`, `LICENSE`, `SKILL.md`, `.printing-press.json` into `library/media/icloud/`
3. Update `go.mod` module to `github.com/mvanhorn/printing-press-library/library/media/icloud`
4. Update the import in `cmd/icloud-pp-cli/main.go` to match
5. Open a PR with commit message: `feat(icloud): add icloud-pp-cli`

---

## Contributing

Issues and PRs welcome. This repo is the source of truth — the Printing Press
submission is a periodic snapshot with its module path updated.

## License

Apache-2.0
