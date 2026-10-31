package live

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// ProcFSScanner is the Linux /proc process scanner. Root is normally "/proc";
// tests point it at a fake tree.
type ProcFSScanner struct {
	Root string
}

// Scan implements ProcScanner by reading <Root>/<pid>/cmdline and cwd.
func (s *ProcFSScanner) Scan(ctx context.Context) ([]sessions.LiveProc, error) {
	ents, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, &UnavailableError{Err: fmt.Errorf("proc: %w", err)}
	}
	var procs []sessions.LiveProc
	for _, de := range ents {
		if ctx.Err() != nil {
			return procs, ctx.Err()
		}
		pid, err := strconv.Atoi(de.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(s.Root, de.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue // kernel thread, gone, or not ours
		}
		ag, ok := Classify(splitCmdline(raw))
		if !ok {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(dir, "cwd"))
		if err != nil {
			continue
		}
		procs = append(procs, sessions.LiveProc{PID: pid, Agent: ag, Cwd: cwd})
	}
	return procs, nil
}

// splitCmdline splits a NUL-separated /proc cmdline. Processes that rewrote
// their title into one space-joined string are split on whitespace instead.
func splitCmdline(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	parts := strings.Split(string(raw), "\x00")
	if len(parts) == 1 {
		return strings.Fields(parts[0])
	}
	return parts
}

// StartTimes implements ProcScanner using field 22 (starttime, clock ticks
// since boot) of <Root>/<pid>/stat.
func (s *ProcFSScanner) StartTimes(_ context.Context, pids []int) (map[int]string, error) {
	m := map[int]string{}
	for _, pid := range pids {
		raw, err := os.ReadFile(filepath.Join(s.Root, strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		if st, ok := parseStatStart(string(raw)); ok {
			m[pid] = st
		}
	}
	return m, nil
}

// parseStatStart extracts starttime from /proc/<pid>/stat; comm (field 2) may
// contain spaces and parentheses, so fields are counted after the last ')'.
func parseStatStart(stat string) (string, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(stat[i+1:])
	// f[0] is field 3 (state); starttime is field 22 -> f[19].
	if len(f) < 20 {
		return "", false
	}
	return f[19], true
}
