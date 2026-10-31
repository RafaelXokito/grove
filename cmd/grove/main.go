// Command grove is a terminal UI for managing git worktrees across many
// projects and showing which AI agent sessions are bound to each worktree.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

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

// Defaults until config loading lands (SPEC T12 / FR-20).
const (
	defaultRoot        = "~/Documents/repos"
	defaultHistoryDays = 30
)

// runTUI starts the interactive UI; replaced in tests.
var runTUI = func(d tui.Deps) error {
	m := tui.New(d)
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
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
	if err := runTUI(defaultDeps()); err != nil {
		fmt.Fprintf(stderr, "grove: %v\n", err)
		return 1
	}
	return 0
}

func defaultDeps() tui.Deps {
	home := pathx.UserHome()
	claudeDir := filepath.Join(home, ".claude")
	codexDir, err := codex.DefaultDir()
	if err != nil {
		codexDir = filepath.Join(home, ".codex")
	}
	return tui.Deps{
		Roots:       []string{pathx.ExpandHome(defaultRoot, home)},
		Home:        home,
		Runner:      gitx.NewExecRunner(),
		Limiter:     scan.NewLimiter(0),
		Sources:     []sessions.Source{claude.New(claudeDir), codex.New(codexDir)},
		Procs:       live.New(claudeDir),
		HistoryDays: defaultHistoryDays,
	}
}
