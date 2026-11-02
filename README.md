# grove

`grove` is a terminal UI that manages **git worktrees across many projects** and
shows, read-only, which **AI agent sessions** (Claude Code and Codex) are bound
to each worktree, and whether they are live right now.

It does **not** start, stop, resume or otherwise manage agent sessions; it only
reads their transcripts and process state so you can see at a glance which
worktree an agent is working in before you detach, check out or remove it.

```
grove  ~/Documents/repos
▾ api  3 worktrees
  ▾ api  ★  main  clean  ●
      claude 4f1c2a9e  Fix pagination in /orders            2m ago  ● busy
      codex  0199a7b2  Add rate limiting middleware         3h ago
  ▾ api-auth-refactor  auth-refactor  dirty (4)  ↑2 ↓0
      claude 8be03d11  Refactor token refresh flow          1d ago
  ▸ api-spike  detached @ 3e9a1f0  clean  [locked]
▾ web  2 worktrees
  ▸ web  ★  main  clean
  ▸ web-old  missing  [prunable]
▸ infra  2 worktrees
←/→ fold · n new · p prune · / filter · r refresh · ? help · q quit
3 repos · 7 worktrees · 3 sessions · 1 live
```

(`★` marks the primary worktree, `●` a live agent. The line above the status
line lists the keys that apply to the selected row.)

## Install

There is no published remote yet; build from a checkout:

```sh
go install ./cmd/grove          # needs Go 1.26+
grove --version
```

Set a version at build time with
`go install -ldflags "-X main.version=v0.1.0" ./cmd/grove`.

## Usage

```sh
grove                                   # scan ~/Documents/repos
grove --root ~/src --root ~/work        # several roots
GROVE_ROOT=~/src,~/work grove
grove --history-days 0                  # show all sessions, not just the last 30 days
```

grove scans each root (to depth 3 by default, skipping hidden dirs,
`node_modules` and `vendor`) for git repositories, lists their worktrees with
branch, dirty/clean state, ahead/behind, lock and prunable flags, and nests
the agent sessions bound to each worktree underneath.

### Keys

| Key | Action |
| --- | --- |
| `↑`/`↓`, `k`/`j` | move (also `pgup`/`pgdn`, `home`/`end`) |
| `←` / `→` | collapse (or go to parent) / expand; `space` toggles |
| `enter` | details pane (diff stat, sessions with full prompt) |
| `d` | detach HEAD (`checkout --detach`); asks only if an agent is live there |
| `c` | check out / reattach a local branch (prefilled with the last one) |
| `n` | new worktree (new or existing branch) |
| `x` | remove worktree — always confirms, using a fresh git status; risky removals (dirty / live agent / locked) list each risk and use `--force`; aborts if the state worsened since the dialog opened; `b` toggles `git branch -d` |
| `p` | `git worktree prune` |
| `l` | lock / unlock |
| `o` | open the worktree: `$GROVE_OPEN`, else `$EDITOR <path>`, else `$SHELL` in it |
| `/` | filter by path/branch (`esc` clears) |
| `?` | help |
| `r` | full refresh (git + sessions) |
| `q` | quit |

Branches are never deleted unless you ask for it in the remove dialog.

## Configuration

Precedence: **flags > environment > config file > defaults**.

| Flag | Env | Config key | Default |
| --- | --- | --- | --- |
| `--root` (repeatable) | `GROVE_ROOT` (comma-separated) | `roots` | `["~/Documents/repos"]` |
| `--depth` | | `max_depth` | `3` (`0` = roots only) |
| `--history-days` | | `history_days` | `30` (`0` = unlimited) |
| | | `live_interval` | `"5s"` (min `500ms`) |
| | | `git_timeout` | `"10s"` per git call |
| `--claude-dir` | | | `~/.claude` |
| `--codex-dir` | `CODEX_HOME` | | `~/.codex` |

The config file is optional and lives at `$XDG_CONFIG_HOME/grove/config.toml`
(default `~/.config/grove/config.toml`):

```toml
roots = ["~/Documents/repos", "~/work"]
max_depth = 3
history_days = 30
live_interval = "5s"
git_timeout = "10s"
```

Unknown keys produce a warning; malformed TOML or invalid values (negative
days, unparsable durations, empty roots) exit with status 2 and a message.
`grove --help` lists all flags, environment variables and the config path.

## How session binding works

- **Transcripts.** Claude Code transcripts (`~/.claude/projects/**/*.jsonl`)
  and Codex rollouts (`$CODEX_HOME/sessions`, `archived_sessions`) are read with
  bounded head/tail reads (never whole files). A session binds to the worktree
  containing the **last `cwd`** recorded in it, so a session that `cd`-ed into
  another worktree follows it. Only files modified within `history_days` are
  opened. Subagent transcripts are excluded.
- **Live status.** Polled every `live_interval` without rescanning git: the
  Claude Code live registry (`~/.claude/sessions/<pid>.json`, checked against
  the PID and its start time) gives live + busy/idle per session; a process
  scan (`ps` + `lsof` on macOS, `/proc` on Linux) covers Codex and acts as a
  fallback. Process cwd is only used for live status, never for binding.

## Platform notes

- macOS is the primary platform; Linux is supported.
- git ≥ 2.36 is recommended; older versions work through a fallback path.
- If `ps`/`lsof` are unavailable, process-based live detection is disabled
  (noted in the status line); the Claude registry still works.

## Limitations

- Compressed Codex rollouts (`.jsonl.zst`) are skipped and counted in the
  status line.
- Sessions whose last cwd is outside every scanned worktree are not shown.
- grove is read-only with respect to agents: it never starts, resumes or kills
  sessions.

## Development

```sh
go build ./... && go vet ./... && go test -race ./...
go test ./internal/scan -run x -bench Scan     # FR-18 scan benchmark
```

See `SPEC.md` for the full specification.

## License

[MIT](LICENSE)
