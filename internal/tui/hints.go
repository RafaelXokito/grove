package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The key hint bar is the context-sensitive line just above the status line
// listing the keys that apply to the selected row (like lazygit/k9s
// footers). It is hidden while a dialog, the filter input or a pane is open:
// those already show their own keys.

var (
	styleHintKey   = lipgloss.NewStyle().Bold(true)
	styleHintLabel = lipgloss.NewStyle().Faint(true)
)

const hintSep = " · "

type hint struct{ key, label string }

func (h hint) String() string { return h.key + " " + h.label }

var (
	hintHelp    = hint{"?", "help"}
	hintQuit    = hint{"q", "quit"}
	hintFilter  = hint{"/", "filter"}
	hintRefresh = hint{"r", "refresh"}
)

// hintsVisible reports whether the hint bar is shown: not in a modal mode,
// and only when the tree keeps at least one row.
func (m *Model) hintsVisible() bool {
	return m.dialog == nil && !m.filtering && m.overlay == nil && m.rawBodyHeight() >= 2
}

// hints lists, in priority order, the keys that apply to the selected row.
// The conditions are the key handlers' own (targetRefusal, detachRefusal,
// lockRefusal, removeRefusal, repoBusy).
func (m *Model) hints() []hint {
	var hs []hint
	r, ok := m.current()
	switch {
	case !ok:
		hs = append(hs, hintRefresh)
	case r.kind == rowRepo:
		if r.expandable && !r.forced {
			hs = append(hs, hint{"←/→", "fold"})
		}
		if m.repoBusy(r.repo) == "" {
			hs = append(hs, hint{"n", "new"}, hint{"p", "prune"})
		}
		hs = append(hs, hintFilter, hintRefresh)
	case r.kind == rowSession:
		hs = append(hs, hint{"enter", "details"})
	case r.kind == rowMore:
		hs = append(hs, hint{"enter", "show all"})
	case r.wt != nil: // worktree row (or its error row)
		hs = append(hs, hint{"enter", "details"})
		wt := r.wt
		if m.targetRefusal(r.repo, wt) == "" {
			hs = append(hs, hint{"o", "open"})
			if detachRefusal(wt) == "" {
				hs = append(hs, hint{"d", "detach"})
			}
			hs = append(hs, hint{"c", "checkout"})
		}
		if m.repoBusy(r.repo) == "" {
			hs = append(hs, hint{"n", "new"})
		}
		if m.targetRefusal(r.repo, wt) == "" {
			if lockRefusal(wt) == "" {
				if wt.Locked {
					hs = append(hs, hint{"l", "unlock"})
				} else {
					hs = append(hs, hint{"l", "lock"})
				}
			}
			if removeRefusal(wt) == "" {
				hs = append(hs, hint{"x", "remove"})
			}
		} else if wt.Prunable && m.repoBusy(r.repo) == "" {
			hs = append(hs, hint{"p", "prune"})
		}
		hs = append(hs, hintFilter)
	}
	if m.filter != "" {
		hs = append(hs, hint{"esc", "clear filter"})
	}
	if m.deps.Scope != "" { // low priority: dropped first when narrow
		if m.scoped {
			hs = append(hs, hint{"a", "all repos"})
		} else {
			hs = append(hs, hint{"a", "this repo"})
		}
	}
	return append(hs, hintHelp, hintQuit)
}

// hintLine renders hs within w cells. Entries are dropped from the end
// (lowest priority first) until the line fits, keeping "? help" last;
// nothing is cut mid-word unless "? help" alone is too wide.
func hintLine(hs []hint, w int) string {
	var rest []hint
	hasHelp := false
	for _, h := range hs {
		if h == hintHelp {
			hasHelp = true
			continue
		}
		rest = append(rest, h)
	}
	build := func(n int) []hint {
		out := append([]hint(nil), rest[:n]...)
		if hasHelp {
			out = append(out, hintHelp)
		}
		return out
	}
	shown := hs
	if plainWidth(hs) > w {
		n := len(rest)
		for n > 0 && plainWidth(build(n)) > w {
			n--
		}
		shown = build(n)
	}
	parts := make([]string, 0, len(shown))
	for _, h := range shown {
		parts = append(parts, styleHintKey.Render(sanitize(h.key))+" "+styleHintLabel.Render(sanitize(h.label)))
	}
	return fit(strings.Join(parts, styleHintLabel.Render(hintSep)), w)
}

func plainWidth(hs []hint) int {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = sanitize(h.String())
	}
	return ansi.StringWidth(strings.Join(parts, hintSep))
}
