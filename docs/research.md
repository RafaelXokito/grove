# Research: session sources (2026-10-31)

Input for SPEC.md tasks T4 (Claude), T5 (Codex), T6 (live). Claude data verified on macOS with CLI v2.1.312/2.1.314; Codex from openai/codex main source (not installed locally).

## Claude Code

- Transcripts: `~/.claude/projects/<enc>/<sessionId>.jsonl`. `<enc>` = launch cwd with `/` (and likely other non-alnum) → `-`. Lossy — never decode; read `cwd` from content.
- Subagents: `<sessionId>/subagents/agent-<id>.jsonl` (+ `.meta.json`); lines have `isSidechain:true`. **Exclude** (skip `subagents/` dirs and `isSidechain:true` lines). Ignore `memory/`.
- Message lines (`user`/`assistant`/`system`) carry: `cwd`, `sessionId`, `timestamp` (ISO-8601 UTC), `gitBranch` (unreliable — often `"HEAD"`), `isSidechain`, `isMeta`, `uuid`, `version`.
- Metadata lines (no cwd/timestamp): `{"type":"ai-title","aiTitle":"..."}` (use as title when present), `{"type":"last-prompt","lastPrompt":"..."}`. Leading lines are often `mode`/`bridge-session`/`file-history-snapshot` — scan forward to the first line with `cwd`.
- First real prompt: first `type=="user"` with `isMeta`≠true, `isSidechain` false, no `toolUseResult`, `message.content` is a string not starting with `<`. Arrays of `tool_result` → skip. Newer versions add `origin:{"kind":"human"}` — most precise when present.
- **cwd changes mid-session** (Bash `cd` persists into per-line `cwd`). Bind on the **last** cwd (tail read, ~64KB backwards), fall back to first cwd.
- **Live registry:** `~/.claude/sessions/<pid>.json` → `{pid, sessionId, cwd, startedAt(ms), procStart, status:"idle"|"busy", updatedAt, name, kind, entrypoint, version}`. May be stale after a crash: verify PID alive (`syscall.Kill(pid,0)`) and, if possible, `procStart` vs process start time. `cwd` here is the launch cwd.
- Unverified: how EnterWorktree / `.claude/worktrees` sessions appear (expected: cwd change); long-path encoding in `<enc>`.

## Codex CLI

- Home: `$CODEX_HOME` else `~/.codex`.
- Files: `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-YYYY-MM-DDThh-mm-ss-<uuid>.jsonl` (local time), plus `$CODEX_HOME/archived_sessions/`. Newer builds may compress to `.jsonl.zst` — prefer `.jsonl`; `.zst` via `github.com/klauspost/compress/zstd` or skip.
- Line: `{"timestamp":..., "type":"<snake_case>", "payload":{...}}`. Types: `session_meta`, `response_item`, `event_msg`, `turn_context`, `compacted`, …
- Line 1 `session_meta` payload: `id`, `session_id` (may be absent → use `id`), `timestamp`, `cwd`, `originator`, `cli_version`, `source` (`cli`|`vscode`|`exec`|`mcp`|object e.g. subagent), `git:{commit_hash,branch,repository_url}` (branch is reliable), `forked_from_id`, `parent_thread_id`.
  ```json
  {"timestamp":"2026-10-31T07:48:17.472Z","type":"session_meta","payload":{"id":"<uuid>","cwd":"/abs/path","source":"cli","git":{"branch":"feat-x"}}}
  ```
- Exclude subagent sessions (`source` is an object/subagent, or `thread_source:"subagent"`).
- cwd changes: `turn_context` lines carry `payload.cwd`; last one wins.
- First prompt: `{"type":"event_msg","payload":{"type":"user_message","message":"..."}}`. Avoid `response_item` user messages (injected AGENTS.md/env context).

## Live process detection

- Prefer Claude's `~/.claude/sessions/*.json` registry; use process checks only to confirm liveness.
- macOS `ps -axo pid=,ppid=,comm=,args=`: claude is a native binary (`comm=claude`, or `.../versions/<semver>` when resumed). Exclude helpers whose args contain `daemon`, `bg-pty-host`, `bg-spare`.
- Codex: npm wrapper `node .../bin/codex.js` spawns native `codex` (unverified locally) — match the native `codex`.
- cwd on macOS: one batched `lsof -a -p <pid1,pid2,...> -d cwd -Fn` (~20ms); parse `p`/`n` lines. Linux: `os.Readlink("/proc/<pid>/cwd")`, `/proc/<pid>/cmdline`.
- Process cwd = launch dir, does not follow `cd`. Use transcript last-cwd for **binding**; use process/registry only for **live/busy** status.
- Poll on a ticker (2–5s), never per frame.

## Binding algorithm (all sources)

Match session cwd to the worktree whose path equals it or is its longest path-prefix. Resolve symlinks on both sides (`/tmp` vs `/private/tmp`). Limit history to the last N days by file mtime.
