# Spike results, 2026-10-28

Machines: Calvin's MacBook Pro M5 (macOS, Claude Code 2.1.312, Codex 0.165.0, Syncthing 2.1.6)
and sdev2 (Ubuntu x86_64, Claude Code 2.1.63, Codex 0.107.0), over Tailscale.
Test sessions were throwaway one-liners with a codeword. All spike data was deleted afterwards.

## 1. Claude Code: native pickup works

| Test | Result |
|---|---|
| Raw transcript copied to sdev2, different user path and an older Claude version, `claude -p --resume <id> --fork-session` | Pass. Recalled the codeword. New session ID. |
| **Stripped** transcript (only `user` and `assistant` records, `parentUuid` relinked, new session ID, new `cwd`) on sdev2 | Pass |
| Stripped transcript on the Mac with the current Claude version, in another folder | Pass |

Import rule: write to `<config>/projects/<cwd with every non-alphanumeric char replaced by ->/<id>.jsonl`,
then `claude --resume <id> --fork-session` from that folder.

**Finding: the raw transcript leaks a lot.** A one-line session was 377 KB. Most of it is
injected context: the full system prompt (`api-request-blob`), CLAUDE.md and rules
(`prompt_snapshot`, `instructions`), memory, the skill list, MCP server instructions, hook output.
The stripped file was 2 KB and resumed fine. **Bundles must use the stripped form.** This is
the most important privacy result of the spike.

Script: `claude_strip.py`.

## 2. Codex: native pickup works on the same version

| Test | Result |
|---|---|
| Stripped rollout (`session_meta` without `base_instructions` and account IDs, plus real user and assistant messages), new ID, new `cwd`, `codex exec resume <id>` on the Mac | Pass. Recalled the codeword. |
| Same file on sdev2 (Codex 0.107) | Not tested: Codex is not logged in on sdev2 (`invalid_refresh_token`). |

`codex exec` has no fork option, so "import as a new session" means: write a copy with a new
UUID in `sessions/YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl`. The raw rollout also leaks:
developer messages (permissions, tools) and AGENTS.md text in the first user message.

Follow-up the same day on sdev4 (Codex 0.116, logged in): the old Codex parsed the stripped
rollout and got as far as calling the model, then the server refused the old client
("requires a newer version of Codex"). So old Codex clients stop working anyway; TeamSesh can
require a current Codex instead of supporting old rollout formats. sdev1 and sdev3 have no
Codex; sdev2's Codex login is expired.

Script: `codex_strip.py`.

## 3. Sidecar Syncthing: works next to Calvin's own

- A second instance with its own home folder ran next to Calvin's normal Syncthing (`*:22000`,
  `127.0.0.1:8384`). The sidecar bound to `100.118.101.92:22100` (Tailscale only) and
  `127.0.0.1:28384` (API). No clash.
- Discovery, relays, NAT and auto-upgrade off. Static peer address on the Tailscale IP.
  Tailnet policy allowed TCP 22100 between the two machines.
- A file in the owner's send-only folder arrived on sdev2's receive-only folder in about 6 s.

Script: `syncthing_configure.py`.

### Receive-only behavior

| Test | Result |
|---|---|
| Teammate edits a received file, and adds a new file | Stays on the teammate's machine. Owner's copy unchanged. Folder status shows `receiveOnlyChangedFiles: 2`. |
| Owner deletes the share (unshare) | Deleted on the teammate's machine, **but** the teammate's edited version stayed as a `.sync-conflict-…` file, and the added file stayed. |
| `POST /rest/db/revert` on the teammate's folder | Removed the conflict copy and the added file. Folder matched the owner again. |

So: TeamSesh must call `revert` on receive-only folders after each sync, and list only bundles
whose name and hashes check out (never conflict copies).

### Delivery state

`GET /rest/db/completion?folder=…&device=…` on the owner returned `completion: 100`, but with
`globalItems: 0` right after a new file. It needs more work before `--wait` can trust it
(probably wait for the owner's own scan to finish first).

## Plan changes from the spike

1. Bundles carry **stripped** transcripts, never raw files. The stripping is per tool and
   versioned. The secret check runs on the stripped form.
2. Claude Code native pickup is confirmed. Codex native pickup is confirmed on the same
   version, with a cross-version test still to do.
3. The sidecar design holds. Add: revert receive-only folders, ignore conflict copies, and
   build `--wait` on scan state plus completion.
