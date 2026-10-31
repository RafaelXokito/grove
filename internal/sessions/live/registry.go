package live

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// maxRegistryFile bounds each registry read.
const maxRegistryFile = 64 << 10

// Registry reads Claude Code's live registry <Dir>/<pid>.json.
type Registry struct {
	Dir string
	// Alive reports whether pid is running; nil means PIDAlive.
	Alive func(pid int) bool
	// StartTimes, if non-nil, is used to reject stale entries whose procStart
	// differs from the running process (pid reuse after a crash). Errors or
	// missing pids skip the check.
	StartTimes func(ctx context.Context, pids []int) (map[int]string, error)
}

type registryEntry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	ProcStart any    `json:"procStart"` // string on darwin; tolerate numbers
	Status    string `json:"status"`
}

// List returns the alive registry entries. A missing dir yields nothing;
// malformed or unreadable files are skipped silently (they are transient).
func (r *Registry) List(ctx context.Context) ([]sessions.LiveProc, error) {
	ents, err := os.ReadDir(r.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	alive := r.Alive
	if alive == nil {
		alive = PIDAlive
	}
	type cand struct {
		e     registryEntry
		start string
	}
	var cands []cand
	for _, de := range ents {
		name := de.Name()
		if !strings.HasSuffix(name, ".json") || de.IsDir() {
			continue
		}
		e, ok := readEntry(filepath.Join(r.Dir, name))
		if !ok {
			continue
		}
		if e.PID <= 0 {
			if n, err := strconv.Atoi(strings.TrimSuffix(name, ".json")); err == nil {
				e.PID = n
			}
		}
		if e.PID <= 0 || !alive(e.PID) {
			continue
		}
		cands = append(cands, cand{e: e, start: procStartString(e.ProcStart)})
	}
	var starts map[int]string
	if r.StartTimes != nil && len(cands) > 0 {
		pids := make([]int, 0, len(cands))
		for _, c := range cands {
			if c.start != "" {
				pids = append(pids, c.e.PID)
			}
		}
		if len(pids) > 0 {
			starts, _ = r.StartTimes(ctx, pids) // best-effort
		}
	}
	out := make([]sessions.LiveProc, 0, len(cands))
	for _, c := range cands {
		if got, ok := starts[c.e.PID]; ok && c.start != "" && startMismatch(c.start, got) {
			continue // pid reused by another process
		}
		out = append(out, sessions.LiveProc{
			PID:       c.e.PID,
			Agent:     sessions.AgentClaude,
			Cwd:       c.e.Cwd,
			SessionID: c.e.SessionID,
			Busy:      c.e.Status == "busy",
		})
	}
	return out, nil
}

func readEntry(path string) (registryEntry, bool) {
	var e registryEntry
	f, err := os.Open(path)
	if err != nil {
		return e, false
	}
	defer f.Close()
	buf := make([]byte, maxRegistryFile+1)
	n, _ := readFull(f, buf)
	if n == 0 || n > maxRegistryFile {
		return e, false
	}
	if err := json.Unmarshal(buf[:n], &e); err != nil {
		return e, false
	}
	return e, true
}

func readFull(f *os.File, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func procStartString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// startMismatch reports whether the registry procStart and the OS start
// time provably differ. Both must be comparable: macOS lstart strings (parsed
// in the C locale, compared within 1s) or all-digit Linux clock ticks.
// Anything else (unparsable, locale-dependent, mixed) skips the check.
func startMismatch(reg, got string) bool {
	reg, got = normSpace(reg), normSpace(got)
	if a, err := time.Parse(lstartLayout, reg); err == nil {
		b, err := time.Parse(lstartLayout, got)
		if err != nil {
			return false
		}
		d := a.Sub(b)
		return d > time.Second || d < -time.Second
	}
	if allDigits(reg) && allDigits(got) {
		return reg != got
	}
	return false
}

// lstartLayout is `ps -o lstart=` in the C locale, after normSpace.
const lstartLayout = "Mon Jan 2 15:04:05 2006"

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
