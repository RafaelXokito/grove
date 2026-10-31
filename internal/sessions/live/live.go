// Package live detects running agent processes (FR-6): the Claude Code live
// registry (<claude-dir>/sessions/<pid>.json) plus an OS process scan for
// Codex and registry-less Claude processes. Everything is read-only.
//
// Process/registry cwd is the launch directory; callers use it only for live
// status, never for binding sessions (see sessions.ApplyLive).
package live

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// ErrUnavailable marks process-based detection as disabled (ps/lsof/proc
// missing or failing). Registry-based Claude detection keeps working.
var ErrUnavailable = errors.New("process detection unavailable")

// UnavailableError wraps the underlying cause of ErrUnavailable.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return fmt.Sprintf("%v: %v", ErrUnavailable, e.Err) }

// Unwrap lets errors.Is match both ErrUnavailable and the cause.
func (e *UnavailableError) Unwrap() []error { return []error{ErrUnavailable, e.Err} }

// Exec runs a command and returns its stdout. Injected so tests use fakes.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// OSExec is the real Exec.
func OSExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ProcScanner enumerates agent processes from the OS.
type ProcScanner interface {
	// Scan returns native claude/codex processes (helpers excluded) with cwd.
	Scan(ctx context.Context) ([]sessions.LiveProc, error)
	// StartTimes returns an OS start-time string per pid, in the same format
	// Claude writes as procStart on this OS. Missing pids are omitted.
	StartTimes(ctx context.Context, pids []int) (map[int]string, error)
}

// Lister implements sessions.ProcLister by combining the registry and a
// process scan. Either may be nil.
type Lister struct {
	Registry *Registry
	Scanner  ProcScanner
}

// New returns a Lister for the given Claude dir using the OS process scanner
// (darwin: ps+lsof, linux: /proc, elsewhere: unavailable).
func New(claudeDir string) *Lister {
	sc := DefaultScanner()
	return &Lister{
		Registry: &Registry{Dir: claudeDir + "/sessions", Alive: PIDAlive, StartTimes: sc.StartTimes},
		Scanner:  sc,
	}
}

var _ sessions.ProcLister = (*Lister)(nil)

// List returns registry entries (alive PIDs only) plus scanned processes not
// already covered by the registry, sorted by PID. If the process scan fails,
// the registry result is still returned together with an *UnavailableError.
func (l *Lister) List(ctx context.Context) ([]sessions.LiveProc, error) {
	var out []sessions.LiveProc
	var errs []error
	seen := map[int]bool{}
	if l.Registry != nil {
		ps, err := l.Registry.List(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		for _, p := range ps {
			seen[p.PID] = true
			out = append(out, p)
		}
	}
	if l.Scanner != nil {
		ps, err := l.Scanner.Scan(ctx)
		if err != nil {
			var ue *UnavailableError
			if !errors.As(err, &ue) {
				err = &UnavailableError{Err: err}
			}
			errs = append(errs, err)
		}
		for _, p := range ps {
			if !seen[p.PID] {
				seen[p.PID] = true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, errors.Join(errs...)
}
