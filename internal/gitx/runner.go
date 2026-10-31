// Package gitx wraps the git CLI: a Runner abstraction for executing git
// commands and parsers for git's porcelain output formats.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes git commands. All git side effects go through a Runner so
// tests can substitute a fake.
type Runner interface {
	// Run executes git with args in dir (via "git -C dir" when dir is
	// non-empty) and returns stdout. On failure err is an *Error that
	// carries git's stderr.
	Run(ctx context.Context, dir string, args ...string) (stdout []byte, err error)
}

// Error is returned by ExecRunner when git exits unsuccessfully or cannot be
// started. Stderr holds git's (trimmed) diagnostic output.
type Error struct {
	Dir    string
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := "git "
	if e.Dir != "" {
		msg += "-C " + e.Dir + " "
	}
	msg += strings.Join(e.Args, " ")
	if e.Stderr != "" {
		return msg + ": " + e.Stderr
	}
	return fmt.Sprintf("%s: %v", msg, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// ExecRunner runs the real git binary.
type ExecRunner struct {
	// Git is the git executable; "git" (looked up in PATH) if empty.
	Git string
	// Env is appended to the process environment for every command.
	Env []string
}

// NewExecRunner returns an ExecRunner using git from PATH.
func NewExecRunner() *ExecRunner { return &ExecRunner{} }

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin := r.Git
	if bin == "" {
		bin = "git"
	}
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	// If git is killed on cancellation but a child it spawned keeps the
	// output pipes open, don't let Wait block forever.
	cmd.WaitDelay = 2 * time.Second
	// Never prompt for credentials and avoid taking optional locks (e.g. the
	// index refresh in `git status`) so read-only scans don't contend with
	// the user's or an agent's git commands. LC_ALL=C keeps stderr stable for
	// callers that match git's messages.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, r.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return stdout.Bytes(), &Error{
			Dir:    dir,
			Args:   append([]string(nil), args...),
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.Bytes(), nil
}
