// Command grove is a terminal UI for managing git worktrees across many
// projects and showing which AI agent sessions are bound to each worktree.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/config"
	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
	"github.com/RafaelXokito/grove/internal/sessions/claude"
	"github.com/RafaelXokito/grove/internal/sessions/codex"
	"github.com/RafaelXokito/grove/internal/sessions/live"
	_ "github.com/RafaelXokito/grove/internal/termbg" // before bubbletea's init; see package doc
	"github.com/RafaelXokito/grove/internal/tui"
	"github.com/RafaelXokito/grove/internal/update"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// readBuildInfo is a seam for tests.
var readBuildInfo = debug.ReadBuildInfo

// currentVersion is main.version, or, for the default "dev", the module
// version stamped by `go install …@vX.Y.Z`. "(devel)" reads as "dev".
func currentVersion() string {
	if version != "dev" && version != "" {
		return version
	}
	if bi, ok := readBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// updateClient builds the GitHub client; replaced in tests.
var updateClient = func(ver string) *update.Client { return &update.Client{Version: ver} }

// runTUI starts the interactive UI; replaced in tests.
var runTUI = func(d tui.Deps) error {
	m := tui.New(d)
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// resolvedHook observes the resolved configuration (compat tests).
var resolvedHook = func(config.Config) {}

// getenv, home, getwd and resolveScope are seams for tests.
var (
	getenv       = os.Getenv
	home         = pathx.UserHome
	getwd        = os.Getwd
	resolveScope = func(dir string, timeout time.Duration) (string, bool, error) {
		return scan.ResolveScope(context.Background(), gitx.NewExecRunner(), dir, timeout)
	}
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run(args []string, stdout, stderr io.Writer) int {
	// FR-24: "update" is a subcommand only as the first argument; before it
	// existed any positional argument was a usage error.
	if len(args) > 0 && args[0] == "update" {
		return runUpdate(args[1:], stdout, stderr)
	}
	h := home()
	cfgPath := config.Path(getenv, h)

	fs := flag.NewFlagSet("grove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var roots stringList
	fs.Var(&roots, "root", "directory to scan for git repos (repeatable; default ~/Documents/repos)")
	depth := fs.Int("depth", config.DefaultMaxDepth, "directory depth scanned below each root (0 = roots only)")
	historyDays := fs.Int("history-days", config.DefaultHistoryDays, "session history window in days (0 = unlimited)")
	claudeDir := fs.String("claude-dir", "~/.claude", "Claude Code data directory")
	codexDir := fs.String("codex-dir", "$CODEX_HOME or ~/.codex", "Codex data directory")
	var all bool
	fs.BoolVar(&all, "all", false, "show all repos under the roots even when started inside a git repo")
	fs.BoolVar(&all, "a", false, "shorthand for --all")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() { usage(fs, cfgPath) }

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "grove: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "grove %s\n", currentVersion())
		return 0
	}

	fl := config.Flags{Roots: roots}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "depth":
			fl.MaxDepth = depth
		case "history-days":
			fl.HistoryDays = historyDays
		case "claude-dir":
			fl.ClaudeDir = claudeDir
		case "codex-dir":
			fl.CodexDir = codexDir
		}
	})

	file, warns, err := config.LoadFile(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "grove: %v\n", err)
		return 2
	}
	for _, w := range warns {
		fmt.Fprintf(stderr, "grove: warning: %s\n", w)
	}
	cfg, err := config.Resolve(fl, getenv, file, h)
	if err != nil {
		fmt.Fprintf(stderr, "grove: %v\n", err)
		return 2
	}

	resolvedHook(cfg)
	d := deps(cfg, h)
	d.Warnings = warns
	if ver := currentVersion(); cfg.UpdateCheck && !update.IsDev(ver) {
		checker := &update.Checker{Client: updateClient(ver), CacheDir: update.CacheDir(getenv, h)}
		d.UpdateCheck = func(ctx context.Context) string { return checker.Available(ctx, ver) }
	}
	// FR-22: started inside a repo, show just that repo unless --root or
	// --all asked for the roots.
	if len(roots) == 0 && !all {
		d.Scope, d.Warnings = scope(cfg.GitTimeout, d.Warnings)
	}
	if err := runTUI(d); err != nil {
		fmt.Fprintf(stderr, "grove: %v\n", err)
		return 1
	}
	return 0
}

// scope resolves the repository containing the cwd (FR-22); "" means the
// all-repos view. Errors fall back to that view with a status-line warning.
func scope(timeout time.Duration, warns []string) (string, []string) {
	wd, err := getwd()
	if err != nil {
		return "", append(warns, fmt.Sprintf("cwd: %v (showing all repos)", err))
	}
	repo, ok, err := resolveScope(wd, timeout)
	if err != nil {
		return "", append(warns, fmt.Sprintf("cwd repo: %v (showing all repos)", err))
	}
	if !ok {
		return "", warns
	}
	return repo, warns
}

func usage(fs *flag.FlagSet, cfgPath string) {
	w := fs.Output()
	fmt.Fprintf(w, "grove %s: git worktree manager that shows bound Claude Code / Codex sessions\n\n", currentVersion())
	fmt.Fprintf(w, "Usage: grove [flags]\n       grove update [--check] [--force]   update to the latest release\n\nFlags:\n")
	fs.PrintDefaults()
	fmt.Fprintf(w, `
Environment:
  GROVE_ROOT    comma-separated roots (overridden by --root)
  CODEX_HOME    Codex data directory (overridden by --codex-dir)
  GROVE_OPEN    command run by "o" (else $EDITOR, else $SHELL)
  GROVE_NO_UPDATE_CHECK=1  disable the new-release check
  XDG_CONFIG_HOME  config base directory
  XDG_CACHE_HOME   cache base directory (update check)

Config file (optional): %s
  roots = ["~/Documents/repos"]   max_depth = 3   history_days = 30
  live_interval = "5s"            git_timeout = "10s"
  update_check = true

Precedence: flags > environment > config file > defaults.

Started inside a git repo, grove shows only that repo (press "a" to toggle
all repos); --root or --all start in the all-repos view.
`, cfgPath)
}

func deps(c config.Config, home string) tui.Deps {
	depth := c.MaxDepth
	if depth == 0 {
		depth = -1 // scan.Options: < 0 scans only the roots
	}
	return tui.Deps{
		Roots:        c.Roots,
		Home:         home,
		MaxDepth:     depth,
		Runner:       gitx.NewExecRunner(),
		Limiter:      scan.NewLimiter(0),
		GitTimeout:   c.GitTimeout,
		Sources:      []sessions.Source{claude.New(c.ClaudeDir), codex.New(c.CodexDir)},
		Procs:        live.New(c.ClaudeDir),
		HistoryDays:  c.HistoryDays,
		LiveInterval: c.LiveInterval,
	}
}
