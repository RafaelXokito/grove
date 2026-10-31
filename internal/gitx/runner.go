// Package gitx wraps the git CLI: a Runner abstraction for executing git
// commands and parsers for git's porcelain output formats.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// OutputRunner is an optional Runner extension that also returns stderr on
// success, for commands that report on stderr (e.g. `worktree prune -v`).
type OutputRunner interface {
	RunOutput(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error)
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
	out, _, err := r.RunOutput(ctx, dir, args...)
	return out, err
}

// RunOutput implements OutputRunner.
func (r *ExecRunner) RunOutput(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	stderr, err := r.run(ctx, dir, &stdout, args...)
	return stdout.Bytes(), stderr, err
}

// RunStream implements StreamRunner.
func (r *ExecRunner) RunStream(ctx context.Context, dir string, w io.Writer, args ...string) error {
	_, err := r.run(ctx, dir, w, args...)
	return err
}

func (r *ExecRunner) run(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error) {
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
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &capWriter{buf: &stderr, max: maxStderr}
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return stderr.Bytes(), &Error{
			Dir:    dir,
			Args:   append([]string(nil), args...),
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stderr.Bytes(), nil
}

// maxStderr bounds the stderr kept per git command.
const maxStderr = 64 << 10

// StreamRunner is an optional Runner extension that streams stdout to w
// instead of buffering it, for output of unbounded size.
type StreamRunner interface {
	RunStream(ctx context.Context, dir string, w io.Writer, args ...string) error
}

// capWriter keeps the first max bytes in buf and discards the rest (never
// failing, so the command runs to completion).
type capWriter struct {
	buf       *bytes.Buffer
	max       int
	truncated bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	keep := p
	if room := max(c.max-c.buf.Len(), 0); len(keep) > room {
		keep, c.truncated = keep[:room], true
	}
	c.buf.Write(keep)
	return len(p), nil
}

// stream runs args with stdout going to w, streaming when r supports it;
// otherwise the (buffered) Run output is copied to w.
func stream(ctx context.Context, r Runner, dir string, w io.Writer, args ...string) error {
	if sr, ok := r.(StreamRunner); ok {
		return sr.RunStream(ctx, dir, w, args...)
	}
	out, err := r.Run(ctx, dir, args...)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// RunCapped runs git keeping at most max bytes of stdout in memory;
// truncated reports that more was produced.
func RunCapped(ctx context.Context, r Runner, dir string, max int, args ...string) (out []byte, truncated bool, err error) {
	var buf bytes.Buffer
	cw := &capWriter{buf: &buf, max: max}
	err = stream(ctx, r, dir, cw, args...)
	return buf.Bytes(), cw.truncated, err
}

// nulCounter counts NUL-terminated records up to max, discarding the data.
type nulCounter struct{ n, max int }

func (c *nulCounter) Write(p []byte) (int, error) {
	if c.n < c.max {
		c.n = min(c.n+bytes.Count(p, []byte{0}), c.max)
	}
	return len(p), nil
}

// CountRecords runs a `-z` git command and counts its NUL-terminated
// records without keeping them in memory; at most max are counted
// (capped = true when the count reached max).
func CountRecords(ctx context.Context, r Runner, dir string, max int, args ...string) (n int, capped bool, err error) {
	c := &nulCounter{max: max}
	err = stream(ctx, r, dir, c, args...)
	return c.n, c.n >= max, err
}
