package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/RafaelXokito/grove/internal/update"
)

// Seams for `grove update` tests.
var (
	executable = os.Executable
	goos       = runtime.GOOS
	goarch     = runtime.GOARCH
	runVersion = func(ctx context.Context, path string) ([]byte, error) {
		return exec.CommandContext(ctx, path, "--version").Output()
	}
)

const (
	updateTimeout = 5 * time.Minute
	latestTimeout = 15 * time.Second
)

// runUpdate implements `grove update [--check] [--force]` (FR-24).
func runUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grove update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "print the current and latest versions and exit")
	force := fs.Bool("force", false, "allow replacing a development build")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: grove update [--check] [--force]\n\nReplaces this grove binary with the latest GitHub release (checksum-verified).\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "grove update: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	ver := currentVersion()
	dev := update.IsDev(ver)
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	client := updateClient(ver)
	lctx, lcancel := context.WithTimeout(ctx, latestTimeout)
	rel, err := client.Latest(lctx)
	lcancel()
	if err != nil {
		fmt.Fprintf(stderr, "grove: update: checking the latest release: %v\n", err)
		return 1
	}

	if *check {
		fmt.Fprintf(stdout, "grove %s\nlatest: %s\n", ver, rel.Tag)
		if update.Newer(rel.Tag, ver) {
			fmt.Fprintf(stdout, "update available: run \"grove update\"\n")
		}
		return 0
	}
	if dev && !*force {
		fmt.Fprintf(stderr, "grove: update: this is a development build (%s); rerun with --force to replace it with %s\n", ver, rel.Tag)
		return 1
	}
	if !dev && !update.Newer(rel.Tag, ver) {
		fmt.Fprintf(stdout, "grove %s is up to date\n", ver)
		return 0
	}

	exe, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "grove: update: locating the grove binary: %v\n", err)
		return 1
	}
	in := &update.Installer{Client: client, Exe: exe, GOOS: goos, GOARCH: goarch, RunVersion: runVersion}
	fmt.Fprintf(stdout, "updating grove %s -> %s\n", ver, rel.Tag)
	if err := in.Install(ctx, rel); err != nil {
		fmt.Fprintf(stderr, "grove: update: %v\n", err)
		if errors.Is(err, update.ErrPermission) {
			fmt.Fprintf(stderr, "  rerun with elevated rights (sudo grove update), or reinstall with:\n  go install github.com/RafaelXokito/grove/cmd/grove@latest\n")
		}
		return 1
	}
	fmt.Fprintf(stdout, "grove updated to %s\n", rel.Tag)
	return 0
}
