# grove

`grove` is a terminal UI that manages **git worktrees across many projects** and
shows, read-only, which **AI agent sessions** (Claude Code and Codex) are bound
to each worktree, and whether they are live right now.

It does **not** start, stop, resume or otherwise manage agent sessions; it only
reads their transcripts and process state so you can see at a glance which
worktree an agent is working in before you detach, check out or remove it.

```
grove  all repos: ~/Documents/repos
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

Download a release archive (`grove_<version>_<os>_<arch>.tar.gz`, macOS and
Linux, amd64/arm64) from
[Releases](https://github.com/RafaelXokito/grove/releases), verify it against
`checksums.txt`, and put `grove` on your `PATH`. Or, with Go 1.26+:

```sh
go install github.com/RafaelXokito/grove/cmd/grove@latest
grove --version
```

Set a version at build time with
`go install -ldflags "-X main.version=v0.1.0" ./cmd/grove` from a checkout.

## Updating

When a newer release exists, grove shows `↑ vX.Y.Z available · grove update`
at the bottom right, next to the key hints. The check runs in the background
at most once a day (cached in `$XDG_CACHE_HOME/grove/update.json`, else
`~/Library/Caches/grove` on macOS / `~/.cache/grove` on Linux), never delays
startup, fails silently, and is skipped for development builds. Turn it off
with `update_check = false` in the config file or `GROVE_NO_UPDATE_CHECK=1`.

```sh
grove update           # download, verify (sha256) and replace this binary
grove update --check   # print the current and latest versions
```

`grove update` only downloads from GitHub over https, checks the archive
against the release's `checksums.txt`, and checks that the new binary runs
before swapping it in atomically. If the binary's directory isn't writable,
rerun with `sudo` or reinstall with `go install …@latest`. Development
builds refuse to self-update unless given `--force`.

## Usage

```sh
grove                                   # inside a repo: just that repo; elsewhere: scan ~/Documents/repos
grove --all                             # all repos even when started inside one (also -a)
grove --root ~/src --root ~/work        # several roots
GROVE_ROOT=~/src,~/work grove
grove --history-days 0                  # show all sessions, not just the last 30 days
```

grove scans each root (to depth 3 by default, skipping hidden dirs,
`node_modules` and `vendor`) for git repositories, lists their worktrees with
branch, dirty/clean state, ahead/behind, lock and prunable flags, and nests
the agent sessions bound to each worktree underneath.

Started inside a git repository (its main worktree, a linked worktree, any
subdirectory of one, or a bare repo), grove shows only that repository, with
all its worktrees, even if it lives outside the roots; the header reads
`grove  repo: <name>`. Inside a submodule it shows the outermost
superproject. Press `a` to switch between this repo and all repos. `--root`
or `--all` start in the all-repos view; `GROVE_ROOT` and the config `roots`
only define what "all repos" scans.

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
| `r` | full refresh (git + sessions), in the current scope |
| `a` | toggle this repo / all repos (when started inside a repo) |
| `q` | quit |

Branches are never deleted unless you ask for it in the remove dialog.

## Configuration

Precedence: **flags > environment > config file > defaults**.

| Flag | Env | Config key | Default |
| --- | --- | --- | --- |
| `--root` (repeatable) | `GROVE_ROOT` (comma-separated) | `roots` | `["~/Documents/repos"]` |
| `-a`, `--all` | | | off: started inside a repo, show only that repo |
| `--depth` | | `max_depth` | `3` (`0` = roots only) |
| `--history-days` | | `history_days` | `30` (`0` = unlimited) |
| | | `live_interval` | `"5s"` (min `500ms`) |
| | | `git_timeout` | `"10s"` per git call |
| `--claude-dir` | | | `~/.claude` |
| `--codex-dir` | `CODEX_HOME` | | `~/.codex` |
| | `GROVE_NO_UPDATE_CHECK=1` | `update_check` | `true` (new-release check) |

The config file is optional and lives at `$XDG_CONFIG_HOME/grove/config.toml`
(default `~/.config/grove/config.toml`):

```toml
roots = ["~/Documents/repos", "~/work"]
max_depth = 3
history_days = 30
live_interval = "5s"
git_timeout = "10s"
update_check = true
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

See `SPEC.md` for the full specification and `CONTRIBUTING.md` for the
compatibility policy and release process.

## Compatibility

Every release is backwards compatible: flags, subcommands, environment
variables, config keys and key bindings are never removed, renamed or given
a new meaning, and a config file that worked with an earlier release keeps
working the same way (SPEC §7).

## License

[MIT](LICENSE)
