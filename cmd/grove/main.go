// Command grove is a terminal UI for managing git worktrees across many
// projects and showing which AI agent sessions are bound to each worktree.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// runTUI starts the interactive UI; replaced in tests.
var runTUI = func(d tui.Deps) error {
	m := tui.New(d)
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// getenv and home are seams for tests.
var (
	getenv = os.Getenv
	home   = pathx.UserHome
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run(args []string, stdout, stderr io.Writer) int {
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
		fmt.Fprintf(stdout, "grove %s\n", version)
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

	d := deps(cfg, h)
	d.Warnings = warns
	if err := runTUI(d); err != nil {
		fmt.Fprintf(stderr, "grove: %v\n", err)
		return 1
	}
	return 0
}

func usage(fs *flag.FlagSet, cfgPath string) {
	w := fs.Output()
	fmt.Fprintf(w, "grove %s: git worktree manager that shows bound Claude Code / Codex sessions\n\n", version)
	fmt.Fprintf(w, "Usage: grove [flags]\n\nFlags:\n")
	fs.PrintDefaults()
	fmt.Fprintf(w, `
Environment:
  GROVE_ROOT    comma-separated roots (overridden by --root)
  CODEX_HOME    Codex data directory (overridden by --codex-dir)
  GROVE_OPEN    command run by "o" (else $EDITOR, else $SHELL)
  XDG_CONFIG_HOME  config base directory

Config file (optional): %s
  roots = ["~/Documents/repos"]   max_depth = 3   history_days = 30
  live_interval = "5s"            git_timeout = "10s"

Precedence: flags > environment > config file > defaults.
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
