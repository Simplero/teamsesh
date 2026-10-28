# Team session sharing: plan

Status: draft v3, 2026-10-28. Revised after a Codex review (see "Review changes") and the spike (`spike/RESULTS.md`).
Name: **TeamSesh** (CLI `teamsesh`).
Owner: Calvin.

## Problem

Calvin works with Claude Code, Codex, Grok Build and OpenCode on several machines.
Syncthing already copies his own sessions between his machines, and Backtrace (a Mac app)
browses them. He cannot easily hand a conversation to a teammate. Today he makes a secret
GitHub gist. That works, but the teammate gets a page to read, not a session to continue.

Goal: Calvin marks a conversation as shared. A teammate on the same tailnet sees it and types
one command to continue it in their own assistant, with the full context. Calvin's laptop can
be closed after at least one teammate has a full copy (TeamSesh shows when that is true).

## Non-goals (v1)

- No server that Calvin or Simplero must run. No accounts or passwords.
- No live co-editing of one session by two people.
- No sharing of private history. A share is a frozen snapshot. Later turns are not added
  unless the owner shares again.
- No hosted web viewer. Public sharing is a secret gist.
- No protection against a hostile teammate. The team is people you trust with your code.
  TeamSesh prevents accidents and detects changed bundles. It cannot stop a teammate who keeps
  a copy.

## Levels

Each session has one level. The default is **private**.

| Level | Who can see it | How it gets there |
|---|---|---|
| private | Only the owner's own machines | The owner's own sync setup (not TeamSesh's job) |
| team | The people on the team list | TeamSesh's sync, with Syncthing over Tailscale |
| public | Anyone with the link | Secret GitHub gist (`gh gist create`) of the handoff |

"Unshare" deletes the bundle from the team folders and deletes the gist. This is best effort.
It cannot remove a session that a teammate already picked up, or a copy someone saved.
TeamSesh says this when you share.

## Shape

One Go binary, `teamsesh`, for macOS, Linux and Windows. It has four parts.

### 1. Session readers and adapters

Each tool gets a versioned adapter with three jobs: list sessions, **export** a session to a
bundle, and **import** a bundle as a new local session.

| Tool | What a session is on disk | Import in v1 |
|---|---|---|
| Claude Code | `projects/<encoded-cwd>/<id>.jsonl`, plus companion files (subagent transcripts, tool results) | Native, confirmed by the spike: stripped transcript + `--resume <id> --fork-session` |
| Codex | `sessions/YYYY/MM/DD/rollout-*.jsonl[.zst]`, plus state databases | Native, confirmed on the same version: stripped rollout copied with a new ID. Cross-version still to test. |
| Grok Build | A folder: `summary.json`, `updates.jsonl` (the restore log), and more | Handoff only |
| OpenCode | A SQLite database | `opencode export` / `opencode import` |

Every tool also has the **handoff route**: start the chosen tool with a clean handoff
document as the first prompt (as Backtrace's "Continue in" does). The handoff is clearly
labeled as a summary. It is trimmed for very long sessions, so it is not full context.

Readers respect home-folder overrides (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, and so on).

### 2. Bundles

A share is **one immutable file**, written atomically (write to a temp name, then rename):

```
<share-id>.teamsesh     # a tar.zst archive
  manifest.json      # format version, share ID, owner, source tool and session ID, title,
                     # git remote (credentials stripped), branch, commit SHA, dirty flag,
                     # created time, SHA-256 of every file
  handoff.md
  native/...         # the STRIPPED session for the tool adapter: user and assistant turns only.
                     # Raw files carry the system prompt, CLAUDE.md, memory, skills and MCP
                     # instructions (spike: 377 KB raw vs 2 KB stripped). Never ship raw files.
```

- The share ID is random. It is not a login name or a path.
- Before a bundle leaves the machine, TeamSesh freezes the snapshot in a private temp folder,
  runs the secret check on **everything** in it (manifest and handoff too), and only then
  moves it into the team folder.
- On receive, TeamSesh checks the format version, size limits, file count, hashes, and every
  path (no `..`, no absolute paths, no symlinks) before it lists or imports the bundle.
  A bundle that fails a check is not shown.

### 3. Sync sidecar

TeamSesh runs **its own Syncthing instance**, with its own config folder and ports. It does not
touch a Syncthing that the user already runs.

- The sync listener binds only to the Tailscale IP. Global discovery, local discovery,
  relays, NAT traversal and auto-upgrade are off. Each peer's address is its Tailscale IP.
- The Syncthing GUI and REST API bind to `127.0.0.1` only, on a separate port, with a random
  API key in TeamSesh's config (file mode 0600).
- **One folder per owner device**: `~/TeamSesh/team/<folder-id>/`, where the folder ID is a
  hash of login and Syncthing device ID. (Per device, not per person: two send-only copies
  of one folder on two of your machines would ignore each other.) On the owner's machines it is
  send-only. On everyone else's machines it is receive-only. A local change on a receiving
  machine stays local, and Syncthing does not send it on. Receivers still pass the owner's
  original files to each other, so a teammate who comes online later can get a share from
  any other teammate, not only from the owner.
- After each sync, call `POST /rest/db/revert` on every receive-only folder, and never list
  `.sync-conflict-` files or files not named in a valid bundle (spike finding).
- Lifecycle: one instance per user (a lock file). It waits for Tailscale to be up. It handles
  sleep and wake, a changed Tailscale IP, and a port that is already in use. It stops
  cleanly with the service.

### 4. Team membership

- The team is an **explicit list** of Tailscale login names, kept by each person on their own
  machine. `teamsesh team add alice@example.com` adds a person. Two people pair only when both
  list each other. (Built 2026-10-28: simpler than a synced team file, and nobody can add
  themselves to your team.) Your own other devices always count as team.
- Pairing: TeamSesh only talks to peers whose Tailscale identity is on the list. It checks the
  identity with `tailscale whois` on the **actual connection address**, then links that
  identity to the peer's Syncthing device ID. Unknown identities, tagged devices, devices
  shared in from other tailnets, and a switch to another tailnet all fail closed.
- Removing a person removes their devices from every folder the next time TeamSesh refreshes.

### Secret check

Transcripts often hold tokens, API keys and pasted `.env` values.

- Patterns: GitHub, Anthropic, OpenAI, AWS, Slack, Stripe, private keys, `Authorization:`
  headers, and long high-entropy strings next to `key`, `token`, `secret` or `password`.
  This includes base64- and URL-encoded forms.
- On a hit, TeamSesh shows each hit (masked) and stops. `--redact` replaces the hits in the
  shared copy. `--force` shares as is.
- Patterns miss things: plain passwords, customer data, and text inside images. So every
  share shows a **preview** first: title, size, number of turns, the first and last user
  messages, and whether images are included. The user confirms with `y`. `--yes` skips this
  for scripts.
- The local original is never changed.

### Pick up

`teamsesh pick <share-id>` on the teammate's machine:

1. Check the bundle (see "Bundles").
2. Find the working folder. Use the local checkout whose git remote matches the manifest.
   If there is none, or more than one, ask for it (`--dir`). Never fall back to the current
   folder without asking. Warn if the local commit differs from the manifest commit, or if
   the owner had uncommitted changes.
3. Import as a **new session** with a new ID, using the teammate's own permissions and
   settings. Never overwrite an existing session. Never copy the owner's permission or
   approval settings. Keep a note of where it came from.
4. Start the tool. Pass the handoff as a file, not as a command-line argument.
   Imported content is context, not instructions from the teammate. The handoff says so at
   the top.

`--handoff` forces the handoff route. `--with <tool>` picks the tool.

## CLI

```
teamsesh up                         # start the sidecar and pair (also run by the service)
teamsesh status                     # peers online, sync state, delivery state of my shares
teamsesh team [add | remove] <login>
teamsesh ls [--team|--mine]         # local sessions, or sessions teammates shared
teamsesh share [<id>|--last] [--team] [--public] [--redact|--force] [--yes] [--wait]
teamsesh unshare <share-id>
teamsesh pick <share-id> [--with claude|codex|grok|opencode] [--handoff] [--dir <path>]
teamsesh show <share-id>            # print the handoff
```

- `--last` picks the newest session whose working folder is the current folder. Inside an
  assistant, the user can run `! teamsesh share --last`.
- **Delivery state.** `teamsesh status` and `teamsesh share` report each share as *queued* (no
  teammate has all of it yet) or *delivered to N people*. `--wait` blocks until at least one
  teammate has the full bundle. That is the moment the laptop can close.
- Diagnostics say what TeamSesh knows and nothing more: "cannot reach alice's machine on
  TCP 22100; your tailnet policy must allow this port between team members".

## Install

- macOS and Linux: `curl -fsSL https://<domain>/install.sh | sh`
- Windows: `irm https://<domain>/install.ps1 | iex`

The script downloads the release binary for the platform from GitHub Releases, checks its
SHA-256, puts it in `~/.local/bin` (or `%LOCALAPPDATA%\TeamSesh`), and installs a user service
(launchd, `systemd --user`, or a Windows scheduled task) that runs `teamsesh up`.
TeamSesh downloads a pinned Syncthing release on first run and checks its checksum.
Tailscale must already be installed and logged in. If it is not, TeamSesh says so plainly.

Releases: GoReleaser on GitHub Actions. `teamsesh update` replaces the binary.
`<domain>` starts as a GitHub raw URL. A short domain can come later.

Shares older than 90 days are removed from the team folders (configurable).

## Backtrace integration (last)

- Backtrace shows `~/TeamSesh/team` as a "Team" section in the sidebar.
- The detail view gets "Share with team", which calls `teamsesh share <id> --team`.
- "Continue in" on a team session calls `teamsesh pick`.
- The existing gist Share Link becomes `teamsesh share --public`.

## Milestones

Each milestone ends with a test on two machines: Calvin's Mac and a second user's Linux or
Windows machine.

1. **Spike (1–2 days).** Export and import a Claude Code session as a fork on a second
   machine. Try the same for Codex. Run two Syncthing instances on one Mac. Bind the sidecar
   to the Tailscale IP. Result: a list of what works per tool, which updates the adapter table.
2. **The risky slice.** Team list and identity checks, bundle export with the secret check
   and preview, sync with per-owner folders, delivery state, and `pick` for **one** tool
   (Claude Code, native) plus the handoff route for all tools. Includes `teamsesh up` as a
   service on macOS and Linux.
3. **Recovery.** Sleep and wake, owner offline before delivery, a late teammate getting the
   bundle from another teammate, `unshare` while someone is offline, restart, broken bundles.
4. **Install** on all three platforms, Windows included. Release pipeline.
5. **More tools.** Codex native import, if the spike allows it. OpenCode via export/import.
6. **Public** (`--public` gist) and **Backtrace** integration.

## Risks and open points

- **Secrets and private data in transcripts** are the biggest risk. The check plus the
  preview lower it. They do not remove it.
- **Native resume** may not work across machines for some tools. Each tool falls back to the
  handoff route, so the product still works, with less context.
- **Two Syncthing instances on one Mac** (Calvin's own and TeamSesh's): different ports and
  config folders. Tested in the spike.
- **Tailnet policy** may block the sync port between users. The team owner adds one rule.
  TeamSesh prints the exact port.
- **Large sessions**: some Codex sessions are tens of MB. The bundle is zstd-compressed. The
  gist route uses the trimmed handoff.
- **Repo and license**: `Simplero/teamsesh`, MIT. Private until the install works; it must be public for the curl install.

## Review changes (v1 → v2)

From Codex's review, accepted:

- An explicit team list replaces "everyone on the tailnet". Identity is checked on the real
  connection, and unknown peers fail closed.
- Per-owner folders: send-only for the owner, receive-only for others. This replaces the
  "owner must match the folder name" convention.
- One immutable, hashed bundle per share replaces three loose files. The snapshot is frozen
  and checked before it is published.
- Pickup imports a new forked session with the teammate's own permissions. It needs an
  explicit folder when the git remote does not match.
- Per-tool adapters. Grok is handoff-only and OpenCode uses its own export/import.
- Delivery state and `--wait` make "the laptop can close" something you can check.
- A loopback-only Syncthing API, path and size checks, and the handoff passed as a file.
- Milestones reordered: the risky slice first, public gist and Backtrace last.

Not taken, and why:

- **Signed manifests** (to stop a teammate from changing someone else's share). The team is
  trusted, receive-only folders already stop changes from spreading, and hashes catch
  damage. Signing adds key management for a rare case. Revisit if the team grows past people
  who share a codebase.

## Name

TeamSesh, chosen 2026-10-28. Repo: `Simplero/teamsesh`.

## Build log

- 2026-10-28: milestone 2 built (`cmd/teamsesh`, `internal/*`). Tested end to end between
  Calvin's Mac and sdev2: pairing, share with secret check and preview, delivery in about 6 s,
  `--wait`, `ls --team`, Claude Code pickup as a new session (recalled the codeword),
  handoff route, tamper repair (revert), unshare, and the Linux systemd service. Not yet
  tested: the macOS launchd service, two different people (only Calvin's own devices so far),
  Codex pickup through `teamsesh pick` (the strip and import were tested in the spike).
