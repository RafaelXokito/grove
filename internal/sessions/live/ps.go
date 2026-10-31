package live

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// PSScanner is the macOS (BSD ps + lsof) process scanner.
type PSScanner struct {
	Exec Exec // nil means OSExec
}

func (s *PSScanner) exec() Exec {
	if s.Exec == nil {
		return OSExec
	}
	return s.Exec
}

// Scan implements ProcScanner: one ps call plus one batched lsof call.
func (s *PSScanner) Scan(ctx context.Context) ([]sessions.LiveProc, error) {
	out, err := s.exec()(ctx, "ps", "-axo", "pid=,ppid=,args=")
	if err != nil {
		return nil, &UnavailableError{Err: fmt.Errorf("ps: %w", err)}
	}
	var procs []sessions.LiveProc
	for _, e := range ParsePS(out) {
		if ag, ok := Classify(e.Args); ok {
			procs = append(procs, sessions.LiveProc{PID: e.PID, Agent: ag})
		}
	}
	if len(procs) == 0 {
		return nil, nil
	}
	pids := make([]string, len(procs))
	for i, p := range procs {
		pids[i] = strconv.Itoa(p.PID)
	}
	out, err = s.exec()(ctx, "lsof", "-a", "-p", strings.Join(pids, ","), "-d", "cwd", "-Fn")
	// lsof exits 1 when any listed pid vanished; partial output is still valid.
	if err != nil && len(out) == 0 {
		return nil, &UnavailableError{Err: fmt.Errorf("lsof: %w", err)}
	}
	cwds := ParseLsofCwd(out)
	res := procs[:0]
	for _, p := range procs {
		if c, ok := cwds[p.PID]; ok {
			p.Cwd = c
			res = append(res, p)
		}
	}
	return res, nil
}

// StartTimes implements ProcScanner via `ps -o pid=,lstart=`, the format
// Claude writes as procStart on macOS (C locale for stable month/day names).
func (s *PSScanner) StartTimes(ctx context.Context, pids []int) (map[int]string, error) {
	if len(pids) == 0 {
		return nil, nil
	}
	ps := make([]string, len(pids))
	for i, p := range pids {
		ps[i] = strconv.Itoa(p)
	}
	out, err := s.exec()(ctx, "env", "LC_ALL=C", "ps", "-o", "pid=,lstart=", "-p", strings.Join(ps, ","))
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return ParsePSStart(out), nil
}
