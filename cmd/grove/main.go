// Command grove is a terminal UI for managing git worktrees across many
// projects and showing which AI agent sessions are bound to each worktree.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

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
	if *showVersion {
		fmt.Fprintf(stdout, "grove %s\n", version)
		return 0
	}
	// The TUI is not implemented yet (see SPEC.md T7); print the version.
	fmt.Fprintf(stdout, "grove %s\n", version)
	return 0
}
