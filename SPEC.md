# grove — Specification

Status: draft v0.2 · Date: 2026-10-31 · Target: Go 1.26, macOS (primary), Linux (secondary)

## 1. Overview

`grove` is a terminal UI that **manages git worktrees across many projects** and **shows, read-only, which AI agent sessions** (Claude Code and Codex) are bound to each worktree.

The main use case: an agent session is working in a worktree that holds a branch, and the user wants to free that branch (detach HEAD) without hunting for the right directory. grove finds every repo under a root, lists worktrees with their state, shows which agent sessions used/are using each one, and offers one-key worktree actions with explicit confirmation for risky ones.

### 1.1 Non-goals

- **No session management.** grove never launches, stops, kills, resumes, or sends input to Claude Code or Codex sessions. Session data is display-only.
- No writes to `~/.claude`, `~/.codex`, or any agent state.
- No go-git or libgit2; git operations shell out to the `git` CLI.
- No remote operations (fetch/push/pull). Ahead/behind uses whatever upstream refs already exist locally.
- No Windows support.
- No diff viewer beyond a `git diff --stat` summary.

### 1.2 Glossary

- **Root**: the directory scanned for repos (default `~/Documents/repos`).
- **Repo**: a git repository found under the root, identified by its main worktree path.
- **Worktree**: one entry of `git worktree list --porcelain` (main worktree included).
- **Session**: one top-level agent conversation (Claude Code transcript or Codex rollout; subagents excluded) whose cwd is equal to or inside a worktree path.
- **Live**: a session/worktree with a running `claude`/`codex` process. Claude live sessions are additionally **busy** or **idle**.

Source-format details and evidence: `docs/research.md`.

## 2. Functional requirements

Acceptance criteria use Given/When/Then. "Fixture repo" means a repo created in `t.TempDir()` by the test.

### FR-1 Repo discovery
grove scans the root for git repositories, concurrently, to a bounded depth.
- Given a root containing `a/.git` (dir), `b/.git` (dir), and `c/` (no git), When scanning, Then repos `a` and `b` are found and `c` is not.
- Given a repo whose linked worktree lives under the root (its `.git` is a *file*), When scanning, Then it is not listed as a separate repo; it appears only as a worktree of its main repo.
- Given nested dirs deeper than `max_depth` (default 3), Then they are not scanned. Directories named `node_modules`, `vendor`, `.git`, and hidden dirs are skipped.
- Given an unreadable directory, Then scanning continues and the error is recorded (not fatal).

### FR-2 Worktree listing
For each repo, grove lists worktrees via `git -C <repo> worktree list --porcelain`.
- Given a fixture repo with one linked worktree on branch `feat`, Then two worktrees are listed: main (marked `main`) and the linked one with branch `feat`.
- Given a worktree with detached HEAD, Then it shows `detached @ <short sha>` (7 chars).
- Given a worktree with `locked [reason]` or `prunable [reason]` lines, Then the Locked/Prunable flags and reasons are set.
- Given a bare repo, Then it is listed with its worktrees and the bare entry is not actionable.

### FR-3 Worktree status
For each worktree grove shows dirty/clean with changed-file count and ahead/behind vs upstream.
- Uses `git -C <wt> status --porcelain=v2 --branch` (one call yields both).
- Given a worktree with 2 modified and 1 untracked file, Then it shows `dirty (3)`.
- Given a branch 2 ahead and 1 behind its upstream, Then it shows `↑2 ↓1`. Given no upstream, Then no ahead/behind is shown (not `0`).
- Given a prunable worktree (path missing), Then status is not queried and the row shows `missing`.

### FR-4 Claude Code session binding
grove reads Claude Code transcripts from `<claude-dir>/projects/*/*.jsonl` (default `~/.claude`).
- One top-level file = one session; id = file basename (= `sessionId` in lines). The `<enc>` dir name is lossy: **never decode it**; read `cwd` from content.
- **Exclude** `*/subagents/` dirs, `memory/`, and lines with `isSidechain:true`.
- **cwd**: the **last** `cwd` field in the file (tail read, ≤64 KiB backwards); if none in the tail, the last `cwd` seen in a head read (≤256 KiB, scanning past leading `mode`/`bridge-session`/`file-history-snapshot` lines that have no cwd). cwd changes mid-session because Bash `cd` persists into per-line `cwd`.
- **Title**: the last `{"type":"ai-title","aiTitle":…}` value if present, else the first real prompt. First real prompt = first `type:"user"` line with `isMeta`≠true, not sidechain, no `toolUseResult`, `message.content` a string not starting with `<`, or an array whose first `text` block doesn't start with `<` and that has no `tool_result` block (e.g. pasted image + text) (if `origin` exists, require `origin.kind=="human"`). Truncated to 80 runes for display.
- **Ignore `gitBranch`** (unreliable, often `"HEAD"`); branch comes from git (FR-2).
- Last activity = file mtime. Files with mtime older than `history_days` (default 30) are skipped without opening.
- Binding (all sources): session binds to the worktree whose path is the **longest** path equal to or an ancestor of the session cwd, after `filepath.Clean` + `EvalSymlinks` on **both** sides (`/tmp` vs `/private/tmp`).
- Given cwd `/x/repo-wt/sub` and worktrees `/x/repo` and `/x/repo-wt`, Then it binds to `/x/repo-wt` (no prefix false-match).
- Given a session whose first cwd is `/x/repo` and last cwd is `/x/repo-wt`, Then it binds to `/x/repo-wt`.
- Given a large file whose tail has no cwd and whose head has cwd `/first` then `/second`, Then cwd is `/second`.
- Given an `ai-title` line, Then the title is the ai-title, not the prompt.
- Given a malformed line, Then it is skipped; given no cwd at all, Then the session is ignored.
- Given a `subagents/agent-*.jsonl` file, Then it is not listed.
- Unverified: how `EnterWorktree` / `.claude/worktrees` sessions are recorded (expected as a cwd change, covered by last-cwd binding); long-path encoding of `<enc>` (irrelevant since never decoded).

### FR-5 Codex session binding
grove reads Codex rollouts from `<codex-dir>/sessions/YYYY/MM/DD/rollout-*.jsonl` and `<codex-dir>/archived_sessions/` (`<codex-dir>` = `$CODEX_HOME` else `~/.codex`).
- Line format: `{"timestamp":…,"type":"<snake_case>","payload":{…}}`.
- Line 1 `session_meta`: id = `payload.id` (the thread id `codex resume` takes) else `payload.session_id` else the thread id from the filename (`rollout-<ts>-<threadId>[_<rolloutId>].jsonl`); initial cwd = `payload.cwd`. Line 1 is read with its own 4 MiB cap (real metas carry large `base_instructions`); if unreadable, fall back to filename id + `turn_context` cwd, and with no cwd at all count it in `skipped`.
- Several rollouts can share a thread id (revert writes a new rollout; `sessions/` vs `archived_sessions/`): keep the one with the newest mtime.
- **Exclude** subagent/internal sessions: `payload.source` is an object with key `subagent` or `internal`, or `payload.thread_source` ∈ {`subagent`, `guardian_review`, `memory_consolidation`}. `{"custom":…}` sources are kept.
- **cwd**: last `turn_context` `payload.cwd` (tail read, ≤64 KiB) wins, else `session_meta.cwd`.
- **Title**: first `{"type":"event_msg","payload":{"type":"user_message","message":…}}`. Do not use `response_item` user messages (they contain injected AGENTS.md/env context).
- `payload.git.branch` is reliable but only shown in details (FR-15); the tree uses git's branch.
- `.jsonl.zst` (compressed newer builds): **skipped in v1**; count shown in the status line (`codex: N compressed sessions skipped`).
- Same binding, truncation, history window, and mtime rules as FR-4.
- Given a fixture rollout, Then id, cwd (from last `turn_context`), title, and last activity are extracted.
- Given `source:{"subagent":…}`, Then the session is ignored.
- Given `<codex-dir>` does not exist, Then the source returns zero sessions without error.

### FR-6 Live session detection
Polled every 5 s (`live_interval`), independently of the full rescan (FR-19).
- **Claude (primary): registry** `<claude-dir>/sessions/<pid>.json` → `{pid, sessionId, cwd, startedAt, procStart, status:"idle"|"busy", updatedAt, …}`. An entry counts only if the PID is alive (`syscall.Kill(pid,0)`), and, where obtainable, `procStart` matches the process start time (stale entries survive crashes). The matching session (by `sessionId`) is live with its busy/idle status.
- **Process scan (Codex, and fallback):** macOS `ps -axo pid=,ppid=,comm=,args=`; Linux `/proc/<pid>/cmdline`. Match native `claude` (`comm=claude` or `…/versions/<semver>`) excluding args containing `daemon`, `bg-pty-host`, `bg-spare`; match native `codex` (not the `node …/codex.js` wrapper). cwd: macOS one batched `lsof -a -p <pids> -d cwd -Fn`; Linux `os.Readlink("/proc/<pid>/cwd")`.
- Process/registry cwd is the **launch** dir (doesn't follow `cd`); it is used only for live status, never for binding. A worktree is live if a live session binds to it, or (no matching session) if a live process cwd binds to it. For Codex, the live session is the most recently active Codex session bound to that worktree (best-effort; Codex has no busy/idle).
- Given a registry entry `{pid: <alive>, sessionId: S, status: "busy"}`, Then session S shows busy; given the PID is dead, Then S is not live.
- Given a fake process lister returning `{pid 1, codex, cwd /x/repo-wt}`, Then worktree `/x/repo-wt` shows live.
- Given `lsof`/`ps` unavailable or failing, Then process-based detection is disabled with a status-line note; registry-based Claude detection and everything else still work.

### FR-7 Tree view
- Rows: project → worktrees → sessions, collapsible (`←`/`→` or `space`; see FR-8).
- Worktree row: path (relative to root, `~`-abbreviated otherwise), branch or `detached @ sha`, `clean` / `dirty (n)`, `↑a ↓b`, `[locked]`, `[prunable]`, `●` if any bound session is live.
- Session row: agent (`claude`/`codex`), short id (8 chars), title (FR-4/FR-5, truncated to fit width), last activity relative (`3m ago`, `2h ago`, `5d ago`), live marker: `● busy` / `○ idle` (Claude), `●` (Codex).
- Sessions sorted by last activity desc; show at most 5 per worktree with `+N more` row (expand on enter).
- Given a 80-column terminal, Then no row wraps; truncation uses `…`.

### FR-8 Key bindings
| Key | Action |
|---|---|
| `↑`/`↓`, `k`/`j` | move |
| `←` / `→`, `space` | collapse / expand (toggle) |
| `enter` | details pane (FR-15) |
| `d` | detach HEAD immediately; y/n confirm only if a live agent is in the worktree (FR-9) |
| `c` | checkout/reattach branch (FR-10) |
| `n` | new worktree (FR-11) |
| `x` | remove worktree (FR-12) |
| `p` | prune repo (FR-13) |
| `l` | lock/unlock (FR-14) |
| `o` | open shell / `$EDITOR` (FR-16) |
| `r` | refresh |
| `/` | filter (FR-17) |
| `?` | help |
| `q`, `ctrl+c` | quit |
- Given the cursor on a session row, When an action key for a worktree is pressed, Then it applies to the session's parent worktree.
- Given the cursor on a project row, Then `p` and `n` apply to that repo; worktree-only actions show "select a worktree".
- `h` is unbound (no vim-style horizontal navigation); `←` on an expanded node collapses it, on a collapsed/leaf node moves to its parent.

### FR-9 Detach HEAD (`d`)
Runs `git -C <wt> checkout --detach`. Detaches immediately (no dialog) unless a live agent is bound to the worktree; only then a y/n confirmation is shown.
- Given worktree on `feat` and no live agent, When `d`, Then HEAD is detached at the same commit and branch `feat` can now be checked out elsewhere; the row refreshes.
- Given the worktree is live, Then `d` asks for confirmation, saying "An agent is running here; it will now be on a detached HEAD." (plus "Uncommitted changes stay in this worktree." if dirty).
- Given the worktree is dirty, Then the detach still runs and the status message notes the uncommitted changes were kept.
- Given already detached, Then show "already detached" and do nothing.

### FR-10 Checkout / reattach branch (`c`)
Prompt for a branch name, prefilled with the last branch known for this worktree (from reflog `git reflog -1 --grep-reflog`, best-effort) or empty; tab-completes from local branches.
- Runs `git -C <wt> checkout <branch> --` (the `--` keeps a name that matches a file from being treated as a pathspec); names that are not local branches are rejected before running git.
- Given the branch is checked out in another worktree, Then git's error is shown verbatim and nothing changes, and the message names the worktree holding it (from FR-2 data) with hint "press d there to detach".

### FR-11 New worktree (`n`)
Two prompts in turn: branch (tab-completes local branches), then path (default `<repo-parent>/<repo>-<branch-slug>`, shown with `~`; relative paths resolve against the repo's parent).
- If branch exists locally: `git worktree add -- <abs-path> <branch>`; else `git worktree add -b <branch> -- <abs-path>` (from current HEAD of main worktree).
- Given an existing branch checked out in another worktree, Then the in-use message naming the holder ("press d there to detach") is shown before the path prompt.
- Given valid input, Then the new worktree appears in the tree selected.
- Given the path exists and is non-empty (or is a file), Then it is refused before running git (git would create the `-b` branch before failing) and nothing is created.
- Given the path equals or is inside any known repo/worktree path (canonical, case-folded on darwin), Then it is refused with a clear message.

### FR-12 Remove worktree (`x`)
- Never allowed on the main worktree: Given main worktree selected, When `x`, Then show "cannot remove the main worktree" and run nothing.
- On `x` a fresh `git status` of the worktree runs (in a `tea.Cmd`) before the dialog opens; risks come from it plus the current live/lock state. A failed status counts as dirty ("status unknown — uncommitted changes may be LOST").
- Clean, not live, not locked: confirm dialog → `git worktree remove -- <path>`.
- Dirty/unknown and/or live and/or locked: **not blocked**; the dialog spells out each risk ("3 uncommitted changes will be LOST", "a claude agent is running here (busy)", "worktree is locked: <reason>") and confirms with a single `y`.
- Force rules: one `--force` only when there are (or may be) uncommitted changes; `--force --force` when locked (git's rule). A live agent alone is a risk line only and adds no `--force`.
- Re-check: on `y`, if the worktree became live or locked since the dialog opened, or (immediately before removing) a fresh status shows more changes than at open, the remove is aborted with "worktree changed since confirmation — press x again".
- After a remove the live state is re-polled once so live counts update.
- Branches are **never deleted by default**. The dialog has an optional toggle (`b`, default off) "also delete branch `<name>`"; when on, after a successful remove run `git -C <repo> branch -d <name>` (safe delete; if git refuses because unmerged, show its error, no `-D`). Hidden for detached worktrees.
- Given cancel (`esc`/`n`), Then no mutating git command runs (only the read-only status check; verified via the command runner fake).

### FR-13 Prune (`p`)
Runs `git -C <repo> worktree prune --dry-run -v` first; shows the list; on confirm runs `git worktree prune -v`.
- Given nothing to prune, Then show "nothing to prune" without a dialog.

### FR-14 Lock / unlock (`l`)
- Unlocked: prompt for optional reason → `git worktree lock [--reason r] <path>`. Locked: confirm → `git worktree unlock <path>`.
- Main worktree: show "cannot lock the main worktree" (git refuses).

### FR-15 Details pane (`enter`)
Side or bottom pane for the selected worktree: full path, branch/HEAD sha + subject (`git log -1 --format=%h %s`), upstream, `git diff --stat HEAD` (max 20 lines), lock/prune reasons, full session list with full ids, full title/first prompt (wrapped), last cwd, Codex `git.branch` when present.
- `esc` or `enter` closes it. Loads asynchronously with a spinner.

### FR-16 Open (`o`)
Suspend the TUI (`tea.ExecProcess`) and run `$GROVE_OPEN` if set, else `$EDITOR <path>` if set, else `$SHELL` with cwd = worktree. On exit the TUI resumes and refreshes that worktree.

### FR-17 Filter (`/`)
Case-insensitive substring match on project name, worktree path, branch, and session title; non-matching rows hidden, parents of matches kept. `esc` clears.

### FR-18 Responsiveness
- Given any root, When grove starts, Then the first frame renders within 100 ms showing a loading spinner; repos appear incrementally as they are discovered.
- All git and file I/O happens in `tea.Cmd`s, never in `Update`/`View`.
- Concurrency bounded (default `runtime.NumCPU()*2` workers for git calls).
- Each scan git call (`worktree list`, per-worktree `status`) has a timeout (default 10 s, `scan.Options.GitTimeout`); a hung repo yields a per-repo/per-worktree error (`status <path>: timed out after 10s`), frees its worker slot, and never stalls the scan.
- Session parsing reads at most 256 KiB head + 64 KiB tail per file plus `stat`; files are never fully read; files outside the history window are not opened.
- Given 50 repos × 3 worktrees and 2,000 session files on an SSD, Then full scan completes in < 2 s (benchmark, not a CI gate).

### FR-19 Refresh
Full git + session rescan happens only on `r` (everything) and after an action (affected repo + session rebind). Live status (FR-6) is polled every 5 s without rescanning git or transcripts. Cursor stays on the same worktree path if it still exists.

### FR-20 Configuration
Precedence: flag > env > config file > default.
- `--root` (repeatable) / `GROVE_ROOT` (comma-separated) / `roots` TOML array; default `["~/Documents/repos"]`. Multiple roots supported; a repo found under two roots is listed once.
- `--depth` / `max_depth` (default 3).
- `--history-days` / `history_days` (default 30; `0` = unlimited).
- `live_interval` (default `5s`).
- `~/.config/grove/config.toml` (respect `$XDG_CONFIG_HOME`), optional; unknown keys warn, don't fail.
- `--claude-dir` (default `~/.claude`), `--codex-dir` (default `$CODEX_HOME` or `~/.codex`) — primarily for tests/debug.
- `--version`, `--help`.
- Given `GROVE_ROOT=/a` and `--root /b`, Then root is `/b`.

### FR-21 Errors
Per-repo or per-source errors appear inline (dimmed `!` row) and in a status line; they never crash the app. Git command failures show stderr (first 3 lines) in a dismissable message.

## 3. Architecture

### 3.1 Layout

```
cmd/grove/main.go            flags, config load, tea.NewProgram
internal/config/             Config struct, load (flag/env/toml)
internal/gitx/               Runner interface; worktree list/status parsers; action commands
internal/scan/               repo discovery + concurrent worktree/status loading
internal/sessions/           Session type, Source interface, Binder (path → worktree)
internal/sessions/claude/    Claude Code transcript source
internal/sessions/codex/     Codex rollout source
internal/sessions/live/      process lister (darwin: ps+lsof, linux: /proc)
internal/tui/                Bubble Tea model, tree, dialogs, details pane, keymap, styles
internal/pathx/              Clean+EvalSymlinks, IsWithin, ~ abbreviation
testdata/                    fixture JSONL files
```

(`gitx` avoids colliding with the `git` binary name in shell contexts; name is a suggestion.)

### 3.2 Key types

```go
// gitx
type Runner interface {
    Run(ctx context.Context, dir string, args ...string) (stdout []byte, err error) // err wraps stderr
}

type Worktree struct {
    Path       string
    HEAD       string // full sha
    Branch     string // "" if detached; short name ("feat"), not refs/heads/feat
    IsMain     bool
    IsBare     bool
    Locked     bool;   LockReason   string
    Prunable   bool;   PruneReason  string
    Status     *Status // nil until loaded or if missing
}

type Status struct {
    Changed  int
    Upstream string
    Ahead, Behind int
    HasUpstream bool
}

func ParseWorktreeList(porcelain []byte) ([]Worktree, error)
func ParseStatus(porcelainV2 []byte) (Status, error)

// scan
type Repo struct {
    Name      string // dir name relative to root
    Path      string // main worktree path
    Worktrees []gitx.Worktree
    Err       error
}
func Discover(ctx context.Context, roots []string, maxDepth int, out chan<- string) error

// sessions
type Agent string // "claude" | "codex"

type Session struct {
    Agent        Agent
    ID           string
    Cwd          string
    Title        string // ai-title, else first real prompt
    LastActivity time.Time
    Path         string // source file
    Live         bool
    Busy         bool   // Claude only, from registry status
}

type Source interface {
    Agent() Agent
    List(ctx context.Context, since time.Time) (ss []Session, skipped int, err error) // read-only, bounded reads; skipped = e.g. .zst
}

type LiveProc struct { PID int; Agent Agent; Cwd string; SessionID string; Busy bool } // SessionID/Busy from Claude registry
type ProcLister interface { List(ctx context.Context) ([]LiveProc, error) }

// Bind maps each session/proc to the worktree with the longest matching ancestor path.
func Bind(worktrees []string, cwd string) (wt string, ok bool)
```

### 3.3 Data flow

1. `main` loads config, builds `Runner`, `[]Source`, `ProcLister`, starts `tui.Model`.
2. `Init` returns a batch: `discoverCmd` (streams `repoFoundMsg` via a channel-reading cmd), `sessionsCmd` (all sources in parallel → `sessionsLoadedMsg`), `liveCmd` → `liveLoadedMsg`, re-armed by a 5 s `tea.Tick`.
3. Each `repoFoundMsg` triggers `loadWorktreesCmd(repo)` → `worktreesMsg`, which triggers per-worktree `statusCmd` → `statusMsg` (bounded by a shared semaphore).
4. The model holds `repos`, `sessions`, `live` and recomputes bindings (cheap, in-memory) when any arrive; `View` renders a flattened visible-row list.
5. Actions: key → (optional) dialog → `actionCmd` executes via `Runner` → `actionDoneMsg{repo, err}` → rescan that repo.

All side effects go through `Runner`, `Source`, `ProcLister`, and an `fs.FS`/dir parameter so tests inject fakes.

## 4. Testing strategy

- **Git layer**: parsers tested with literal porcelain strings (incl. locked/prunable/bare/detached). Integration tests create real fixture repos in `t.TempDir()` with the real `git` binary (`git init`, commit, `git worktree add`), set `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, and author env vars for hermeticity. Skip if `git` is missing.
- **Actions**: run against fixture repos (detach, checkout, add, remove, prune, lock) and assert resulting `git worktree list` state; plus a fake `Runner` that records commands for "cancel runs nothing" tests.
- **Sessions**: fixture JSONL files under `testdata/` (Claude: normal, leading no-cwd lines, meta/`<`-prefixed first, cwd change mid-file, ai-title, malformed line, no cwd, `subagents/` file, prompt beyond head limit; Codex: normal, `turn_context` cwd change, `session_id` absent, subagent `source` object, `.jsonl.zst` skipped+counted; registry: alive/dead PID, busy/idle). Sources take the base dir as a constructor parameter; tests point at `t.TempDir()` copies. **No test reads the real `~/.claude` or `~/.codex`** — enforce by setting `HOME=t.TempDir()` in `TestMain` of session packages.
- **Binding**: table tests for `Bind` (prefix trap `/x/repo` vs `/x/repo-wt`, trailing slashes, symlink on session side and on worktree side).
- **Live**: parser tests for `lsof -Fn` and `ps` output samples; `ProcLister` faked elsewhere. `/proc` impl tested only on Linux (build tags).
- **TUI**: construct `Model` with fakes, send `tea.Msg`s (`repoFoundMsg`, `tea.KeyMsg{...}`, `tea.WindowSizeMsg`) to `Update`, assert model state and substrings in `View()`. Optionally golden files for `View()` at fixed width with Lip Gloss color disabled (`lipgloss.SetColorProfile(termenv.Ascii)`).
- `go test -race ./...` and `go vet ./...` must pass; CI on macOS and Ubuntu.

## 5. Implementation tasks

Each task: one agent, < 45 min, ends with `go build ./... && go test -race ./...` green.

**T1 — Scaffold + git runner + worktree parser** (FR-2)
`go.mod` (module `github.com/RafaelXokito/grove`, Go 1.26), `cmd/grove` printing version, `gitx.Runner` exec impl, `ParseWorktreeList`, `pathx` helpers.
AC: parser tests cover main/linked/detached/locked/prunable/bare; integration test on fixture repo with one linked worktree returns 2 entries with correct branch.

**T2 — Worktree status** (FR-3)
`ParseStatus` for porcelain v2 `--branch`; `LoadStatus(ctx, runner, wt)`.
AC: fixture repo tests for clean, dirty (3), ahead/behind with a local bare "remote", no upstream, missing path.

**T3 — Repo discovery + concurrent scanner** (FR-1, FR-18 partial)
`scan.Discover` (depth, skip list, `.git` file vs dir), bounded worker pool loading worktrees+status, streaming results.
AC: TempDir root with 2 repos, 1 non-repo, 1 linked worktree inside root, 1 repo beyond depth → exactly 2 repos; `-race` clean; unreadable dir doesn't abort; a hung git call times out (default 10 s) as a per-repo/per-worktree error; `MaxDepth` 0 = default 3, negative = roots only; roots differing only in case (darwin) scanned once.

**T4 — Session model, binder, Claude source** (FR-4)
`sessions.Session`, `Source`, `Bind`; `claude.Source{Dir}` with bounded head/tail reads, last-cwd binding, ai-title, subagent exclusion, history window.
AC: fixture tests for all cases in FR-4; Bind table tests incl. prefix trap and symlinks both sides; head ≤ 256 KiB + tail ≤ 64 KiB (large fixture with prompt past limit → title empty, cwd from tail, no error); file older than window not opened.
Notes: (a) canonicalize worktree paths (`pathx.Canonical`) **once per scan** and cache them; only session/proc cwds are canonicalized per lookup, so `Bind` stays a cheap in-memory operation. (b) On darwin, compare paths **case-insensitively** (default APFS is case-insensitive, and agents may record a cwd with different casing than git); on Linux compare case-sensitively. Choice: fold case via a build-tagged comparison helper in `pathx` rather than querying the filesystem, accepting a rare false match on case-sensitive APFS volumes.

**T5 — Codex source** (FR-5)
`codex.Source{Dir}` per FR-5: `session_meta` (id precedence `id` > `session_id` > filename; large line 1 handled), dedupe by thread id, last `turn_context` cwd, `user_message` title, subagent exclusion, `sessions/` + `archived_sessions/`, `.jsonl.zst` skipped and counted; `$CODEX_HOME`.
AC: fixture rollouts parse to expected Session; subagent excluded; `.zst` file → skipped=1; missing dir → empty, nil error.

**T6 — Live process detection** (FR-6)
Claude registry reader (`<claude-dir>/sessions/*.json`, PID liveness + `procStart` check, busy/idle); `live.ProcLister` darwin (`ps -axo pid=,ppid=,comm=,args=` + batched `lsof`) and linux (`/proc`) with build tags, helper exclusion; parsers separated from exec; merge into per-session/worktree live state.
AC: registry tests (alive busy, alive idle, dead PID ignored, malformed file skipped); `ps`/`lsof -Fn` parser tests with sample outputs incl. helper processes; fake lister → worktree marked live via Bind; failure path returns typed "unavailable" error without disabling the registry.

**T7 — TUI skeleton: tree list + loading** (FR-7, FR-18, FR-19 `r`, FR-21)
Model, messages, flattened rows, collapse/expand, navigation, spinner, status line, `q`, `r`.
AC: model tests: first `View()` before any msgs shows spinner; after `scanEventMsg`+`sessionsLoadedMsg` the view contains branch, `dirty (n)`, session short id, relative time, busy/idle marker; `←`/`→` collapse/expand; 80-col no-wrap check; live tick does not trigger git calls.
Note: `scan.Scan` streams each repo with its worktrees and status already loaded, so a single `scanEventMsg` (one `tea.Cmd` per event, re-armed) replaces the `repoFoundMsg`+`worktreesMsg` pair of §3.3; per-repo reloads use `scan.LoadRepo` → `repoLoadedMsg`.

**T8 — Simple actions: detach, checkout, lock/unlock, prune** (FR-8, FR-9, FR-10, FR-13, FR-14)
Confirm dialog component (single-key y/n), text-input prompt, action cmds, per-repo refresh after action.
AC: model tests with fake runner assert exact git args; integration test detaching a fixture worktree frees the branch for `git checkout` in main; dirty/live notes appear in dialog text; branch-held-elsewhere error names the holding worktree.

**T9 — New + remove worktree with risk confirmation** (FR-11, FR-12)
Two-field form; risk dialog (single `y`) with delete-branch toggle.
AC: main worktree `x` → message, zero runner calls; dirty+live remove dialog lists both risks; cancel → no mutating runner calls; live+clean → plain `worktree remove`; changes growing after the dialog opened → aborted; confirm → `worktree remove --force`, no `branch` call; toggle on → `branch -d <name>` after remove; new worktree with new and existing branch both work on fixture repo.

**T10 — Details pane + open** (FR-15, FR-16)
Async details load, pane layout, `tea.ExecProcess` for open.
AC: model test: `enter` → loading, `detailsMsg` → view contains diff stat lines and full session prompt; `o` produces an exec cmd with `$GROVE_OPEN`/`$EDITOR`/`$SHELL` precedence (tested by inspecting the built `*exec.Cmd`).

**T11 — Filter + help** (FR-17, FR-8 `?`)
AC: filtering by branch substring hides non-matching worktrees but keeps their project row; `esc` restores.

**T12 — Config, flags, polish, README** (FR-20)
TOML via `github.com/BurntSushi/toml`, precedence, multiple roots, `history_days`, `live_interval`, `--version`, styles pass, README with install (`go install ./cmd/grove`; no remote yet) and key table, GitHub Actions CI workflow file (macOS + Ubuntu; runs once a remote exists).
AC: precedence table test; unknown key warns; `go install ./cmd/grove` produces working binary; benchmark from FR-18 exists.

## 6. Decisions (2026-10-31)

1. **Module path**: `github.com/RafaelXokito/grove`; local only for now, no remote push.
2. **Keys**: `←`/`→` collapse/expand (no `h`/`l` navigation); `l` = lock/unlock; `↑`/`↓` and `k`/`j` move.
3. **Risky remove** (dirty / live / locked): single `y` keypress; the dialog spells out each risk.
4. **Branches**: remove never deletes branches by default; optional dialog toggle runs `git branch -d`.
5. **Session history**: last 30 days by file mtime, configurable (`history_days`).
6. **Refresh**: live status polled every 5 s; full git/session rescan only on `r` and after actions.
7. **Roots**: multiple supported; default `["~/Documents/repos"]`.
8. **Codex `.jsonl.zst`**: skipped with a count in v1 (no zstd dependency).
