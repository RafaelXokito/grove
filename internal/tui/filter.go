package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
)

// startFilter implements `/` (FR-17): focus the filter input. Typing
// live-filters the tree; enter keeps the filter, esc clears it.
func (m *Model) startFilter() tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.CharLimit = 128
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.SetValue(m.filter)
	ti.CursorEnd()
	ti.Focus()
	m.filterInput = ti
	m.filtering = true
	return nil
}

// filterKey handles a key while the filter input is focused; every key goes
// to the input (no global key fires) except ctrl+c.
func (m *Model) filterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.Close()
		return tea.Quit
	case tea.KeyEsc:
		m.filtering = false
		m.setFilter("")
		return nil
	case tea.KeyEnter:
		m.filtering = false
		return nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.setFilter(m.filterInput.Value())
	return cmd
}

func (m *Model) setFilter(q string) {
	if q == m.filter {
		return
	}
	m.filter = q
	m.cursorMoved = true // keep the cursor on its row when it survives
	m.rebuild()
}

// treeFilter matches rows against the active filter (case-insensitive
// substring on repo name/path, worktree path, branch, session title/id).
type treeFilter struct {
	m *Model
	q string // lower-cased; "" matches everything
}

func (m *Model) treeFilter() treeFilter {
	return treeFilter{m: m, q: strings.ToLower(strings.TrimSpace(m.filter))}
}

func (f treeFilter) active() bool { return f.q != "" }

func (f treeFilter) any(fields ...string) bool {
	if f.q == "" {
		return true
	}
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), f.q) {
			return true
		}
	}
	return false
}

func (f treeFilter) repo(r *scan.Repo) bool { return f.any(r.Name, r.Path) }

func (f treeFilter) worktree(wt *gitx.Worktree) bool {
	return f.any(wt.Path, f.m.display(wt.Path), wt.Branch)
}

func (f treeFilter) session(s *sessions.Session) bool { return f.any(s.Title, s.ID) }

// helpLines renders the help overlay from keyMap.all, so it lists exactly
// the bound keys.
func (m *Model) helpLines(w int) []string {
	out := []string{styleHeader.Render("Keys")}
	for _, b := range m.keys.all() {
		h := b.Help()
		out = append(out, fit(fmt.Sprintf("  %-8s %s", h.Key, h.Desc), w))
	}
	out = append(out, "",
		styleDim.Render(fit("  esc closes panes and dialogs and clears an active filter", w)),
		styleDim.Render(fit("  actions on a session row apply to its worktree", w)))
	return out
}
