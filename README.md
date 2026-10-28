# TeamSesh

Share an AI coding session with a teammate, so they can continue it with the full context.
Works with Claude Code, Codex and Grok Build. Runs over your Tailscale network. No server, no
accounts.

```
you$       teamsesh share --last --wait
           Shared as k3x7q2mzpa. A teammate continues it with:  teamsesh pick k3x7q2mzpa
           Delivered to alice@example.com/alice-mbp. You can close this machine now.

alice$     teamsesh pick k3x7q2mzpa
           Continuing your session "Fix the checkout bug" in Claude Code as a new session.
```

## How it works

- Every session is **private** until you share it. Sharing makes one frozen snapshot. Later
  messages are not added unless you share again.
- Only the conversation goes out: your messages and the assistant's answers (and tool results
  for Claude Code). Your CLAUDE.md, AGENTS.md, memory, settings and system prompt stay home.
- Before anything leaves, TeamSesh looks for secrets (API keys, tokens, private keys,
  passwords in config) and stops if it finds one. `--redact` replaces them, `--force` shares
  anyway. It also shows a preview and asks first. Pattern matching misses things, so read the
  preview.
- Shares travel with a private copy of [Syncthing](https://syncthing.net) that only talks over
  Tailscale. Your own Syncthing, if you have one, is not touched. Every teammate keeps a copy,
  so a teammate can pick up your share after you close your laptop. `--wait` tells you when
  that is safe.
- Pickup creates a **new** session on the teammate's machine with their own settings and
  permissions. For Claude Code and Codex it is the real session. For other cases it starts
  the assistant with a handoff summary.

## Install

Requires [Tailscale](https://tailscale.com/download), logged in.

From source for now (one-line installers come next):

```
go install github.com/Simplero/teamsesh/cmd/teamsesh@latest
teamsesh service install          # runs in the background, starts at login (macOS, Linux)
```

## Set up your team

Your team is a list of Tailscale logins. Both people must add each other:

```
teamsesh team add alice@example.com
teamsesh status
```

Your own other devices are always on your team. The team members' devices need TCP ports
22100 and 22101 open to each other in your tailnet policy.

## Commands

```
teamsesh ls                      your recent sessions
teamsesh share <id> | --last     share a session (--wait, --redact, --force, --dry-run)
teamsesh ls --team               sessions your teammates shared
teamsesh pick <share-id>         continue one here (--with claude|codex|grok, --handoff, --dir)
teamsesh show <share-id>         print a share's handoff
teamsesh ls --mine               your shares, and who has them
teamsesh unshare <share-id>      stop sharing
teamsesh status
```

Unshare is best effort: a teammate who already picked up a session keeps it.

## Files

- `~/TeamSesh/team/` holds the shares, one folder per device.
- Settings and the Syncthing copy live in `~/Library/Application Support/teamsesh` (macOS) or
  `~/.config/teamsesh` (Linux).
