package live

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// PSEntry is one row of `ps -axo pid=,ppid=,args=`.
type PSEntry struct {
	PID, PPID int
	Args      []string // whitespace-split command line
}

// ParsePS parses `ps -axo pid=,ppid=,args=` output. Malformed rows are
// skipped; arbitrarily long lines are handled (no scanner token limit). (comm
// is not requested: macOS truncates it to 16 chars and helper process titles
// contain spaces, making it unparseable as a column.) Because ps joins argv
// with spaces, an executable path containing spaces is recovered by trying
// progressively longer prefixes until one looks like an agent binary.
func ParsePS(out []byte) []PSEntry {
	var es []PSEntry
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pid <= 0 {
			continue
		}
		es = append(es, PSEntry{PID: pid, PPID: ppid, Args: joinArgv0(f[2:])})
	}
	return es
}

// joinArgv0 merges leading fields into argv0 when the joined prefix (but not
// the bare first field) names an agent binary, e.g. "/Applications/Claude
// Code.app/Contents/MacOS/claude".
func joinArgv0(f []string) []string {
	for k := 1; k <= len(f); k++ {
		cand := strings.Join(f[:k], " ")
		if _, ok := agentArgv0(cand); ok {
			if k == 1 {
				return f
			}
			return append([]string{cand}, f[k:]...)
		}
	}
	return f
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+([-+.][0-9A-Za-z.-]+)?$`)

// Classify identifies a native agent process from its argv. Matches native
// claude (argv0 basename "claude" or .../versions/<semver> for resumed
// sessions) and native codex (basename "codex" or "codex-<triple>"). Not
// agents: the node codex.js wrapper, Claude helpers (args[1] == "daemon", or
// any arg containing bg-pty-host / bg-spare) and Codex IDE/MCP helpers
// (args[1] in app-server, mcp-server, mcp).
func Classify(args []string) (sessions.Agent, bool) {
	if len(args) == 0 {
		return "", false
	}
	ag, ok := agentArgv0(args[0])
	if !ok {
		return "", false
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	switch ag {
	case sessions.AgentClaude:
		if sub == "daemon" {
			return "", false
		}
		for _, a := range args {
			if strings.Contains(a, "bg-pty-host") || strings.Contains(a, "bg-spare") {
				return "", false
			}
		}
	case sessions.AgentCodex:
		switch sub {
		case "app-server", "mcp-server", "mcp":
			return "", false
		}
	}
	return ag, true
}

func agentArgv0(argv0 string) (sessions.Agent, bool) {
	base := filepath.Base(argv0)
	switch {
	case base == "claude":
		return sessions.AgentClaude, true
	case versionRe.MatchString(base) && filepath.Base(filepath.Dir(argv0)) == "versions" &&
		strings.Contains(argv0, "claude"):
		return sessions.AgentClaude, true
	case base == "codex" || (strings.HasPrefix(base, "codex-") && !strings.Contains(base, ".")):
		return sessions.AgentCodex, true
	}
	return "", false
}

// ParseLsofCwd parses `lsof -a -p <pids> -d cwd -Fn` output (p<pid> / n<path>
// lines; other field lines such as f/cwd are ignored) into pid -> cwd.
func ParseLsofCwd(out []byte) map[int]string {
	m := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(line[1:])
			if err != nil {
				pid = 0
				continue
			}
			pid = n
		case 'n':
			if pid > 0 {
				if _, dup := m[pid]; !dup {
					m[pid] = line[1:]
				}
			}
		}
	}
	return m
}

// ParsePSStart parses `ps -o pid=,lstart= -p ...` output into pid -> lstart.
func ParsePSStart(out []byte) map[int]string {
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		m[pid] = strings.Join(f[1:], " ")
	}
	return m
}
