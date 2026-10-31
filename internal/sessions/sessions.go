// Package sessions defines the agent-session model shared by the Claude Code
// and Codex sources, plus the binder that maps session cwds to worktrees.
// Everything here is read-only with respect to agent state.
package sessions

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Agent identifies the tool that produced a session.
type Agent string

// Known agents.
const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

// TitleMaxRunes is the display limit for Session.Title (FR-4).
const TitleMaxRunes = 80

// Session is one top-level agent conversation (subagents excluded).
type Session struct {
	Agent        Agent
	ID           string
	Cwd          string // last known cwd recorded in the transcript (as recorded, not canonicalised)
	Title        string // ai-title, else first real prompt; whitespace-collapsed, ≤ TitleMaxRunes
	LastActivity time.Time
	Path         string // source file
	Live         bool
	Busy         bool // Claude only, from registry status
}

// Source lists sessions of one agent. Implementations are read-only and use
// bounded reads. A zero since disables the history window. skipped counts
// files deliberately not parsed (e.g. compressed Codex rollouts). A non-nil
// err may accompany a partial result (per-file problems are joined).
type Source interface {
	Agent() Agent
	List(ctx context.Context, since time.Time) (ss []Session, skipped int, err error)
}

// LiveProc is a running agent process. SessionID/Busy come from the Claude
// registry when available. Cwd is the launch directory: used only for live
// status, never for binding sessions (FR-6).
type LiveProc struct {
	PID       int
	Agent     Agent
	Cwd       string
	SessionID string
	Busy      bool
}

// ProcLister enumerates live agent processes (implemented in T6).
type ProcLister interface {
	List(ctx context.Context) ([]LiveProc, error)
}

// CleanTitle collapses runs of whitespace to single spaces, trims, and
// truncates to max runes, ending with "…" when shortened.
func CleanTitle(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// Detail is per-session information re-read on demand for the details pane
// (FR-15); List keeps only what the tree needs.
type Detail struct {
	Prompt    string // first real prompt, untruncated (from a bounded head read)
	GitBranch string // Codex payload.git.branch, when recorded
}

// Detailer is implemented by sources that can re-read a session's
// transcript (s.Path) with a bounded read.
type Detailer interface {
	Detail(ctx context.Context, s Session) (Detail, error)
}
