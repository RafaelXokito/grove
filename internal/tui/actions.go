package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/scan"
)

// actionTimeout bounds one git action.
const actionTimeout = 60 * time.Second

// maxMessageLines caps the wrapped status message (FR-21: first 3 lines of
// git's stderr plus a hint).
const maxMessageLines = 4

// maxPruneListed caps the entries listed in the prune confirmation.
const maxPruneListed = 8

type dialogKind int

const (
	dlgConfirm dialogKind = iota // single-key y / n
	dlgInput                     // one-line text input
)

// dialog is the modal confirm/input prompt shown above the status line.
// While it is open every key goes to it.
type dialog struct {
	kind  dialogKind
	lines []string // title first, then notes (plain text)
	input textinput.Model
	hint  string
	// ok runs on confirmation (y / enter) with the input value; for a
	// confirm dialog with a toggle, the value is toggle.on as "on" or "".
	ok func(value string) tea.Cmd
	// toggle is an optional on/off option of a confirm dialog, flipped by
	// its key (e.g. `b` = also delete branch).
	toggle *dialogToggle
}

type dialogToggle struct {
	key, label string
	on         bool
}

// actionFunc performs a git action and returns the success message.
type actionFunc func(ctx context.Context, r gitx.Runner) (string, error)

// actionDoneMsg reports a finished action; repo is reloaded afterwards.
type actionDoneMsg struct {
	key  string // busy key
	repo string
	ok   string
	err  error
	// selectPath, if set, is the worktree the cursor moves to after the
	// reload (a newly created worktree).
	selectPath string
}

// branchesMsg delivers the checkout prompt's data.
type branchesMsg struct {
	wtPath   string
	branches []string
	last     string
	err      error
}

// pruneListMsg delivers the `worktree prune --dry-run -v` result.
type pruneListMsg struct {
	repo  string
	lines []string
	err   error
}

func (m *Model) confirm(ok func(string) tea.Cmd, lines ...string) {
	m.dialog = &dialog{kind: dlgConfirm, lines: lines, hint: "[y] confirm  [n/esc] cancel", ok: ok}
}

func (m *Model) prompt(value string, suggestions []string, hint string, ok func(string) tea.Cmd, lines ...string) {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.CharLimit = 256
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.SetValue(value)
	ti.CursorEnd()
	if len(suggestions) > 0 {
		ti.ShowSuggestions = true
		ti.SetSuggestions(suggestions)
	}
	ti.Focus()
	m.dialog = &dialog{kind: dlgInput, lines: lines, input: ti, hint: hint, ok: ok}
}

// dialogKey handles a key while a dialog is open.
func (m *Model) dialogKey(msg tea.KeyMsg) tea.Cmd {
	d := m.dialog
	if msg.Type == tea.KeyCtrlC {
		m.Close()
		return tea.Quit
	}
	if msg.Type == tea.KeyEsc {
		m.dialog = nil
		m.setMessage("cancelled")
		return nil
	}
	switch d.kind {
	case dlgConfirm:
		switch msg.String() {
		case "y", "Y":
			m.dialog = nil
			v := ""
			if d.toggle != nil && d.toggle.on {
				v = "on"
			}
			return d.ok(v)
		case "n", "N":
			m.dialog = nil
			m.setMessage("cancelled")
		default:
			if t := d.toggle; t != nil && msg.String() == t.key {
				t.on = !t.on
			}
		}
		return nil
	default:
		if msg.Type == tea.KeyEnter {
			m.dialog = nil
			return d.ok(strings.TrimSpace(d.input.Value()))
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return cmd
	}
}

// setMessage and setError sanitise every status message (control runes from
// git output or paths cannot reach the terminal).
func (m *Model) setMessage(s string) { m.message, m.msgErr = sanitize(s), false }
func (m *Model) setError(s string)   { m.message, m.msgErr = sanitize(s), true }

// target returns the worktree an action applies to, or sets an explanatory
// message and returns ok=false (FR-8: session rows act on their worktree).
func (m *Model) target() (*scan.Repo, *gitx.Worktree, bool) {
	repo, wt := m.selection()
	switch {
	case repo == nil:
		return nil, nil, false
	case wt == nil:
		m.setMessage("select a worktree")
	case wt.IsBare:
		m.setMessage("bare repository: no working tree")
	case m.busy[wt.Path] != "":
		m.setMessage(m.busy[wt.Path] + " in progress")
	case m.busy["r:"+repo.Path] != "":
		m.setMessage(m.busy["r:"+repo.Path] + " in progress for " + repo.Name)
	case wt.Prunable:
		m.setMessage("worktree is missing; press p to prune")
	default:
		return repo, wt, true
	}
	return nil, nil, false
}

// run starts an action in a tea.Cmd, marking key busy until it finishes.
func (m *Model) run(key, label, repo string, f actionFunc) tea.Cmd {
	m.busy[key] = label
	ctx, r := m.ctx, m.deps.Runner
	return tea.Batch(m.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, actionTimeout)
		defer cancel()
		ok, err := f(ctx, r)
		return actionDoneMsg{key: key, repo: repo, ok: ok, err: err}
	})
}

// startBusy marks key busy for a non-action load (branch list, prune list).
func (m *Model) startBusy(key, label string, f func(ctx context.Context, r gitx.Runner) tea.Msg) tea.Cmd {
	m.busy[key] = label
	ctx, r := m.ctx, m.deps.Runner
	return tea.Batch(m.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, actionTimeout)
		defer cancel()
		return f(ctx, r)
	})
}

func (m *Model) handleActionMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case actionDoneMsg:
		delete(m.busy, msg.key)
		if msg.err != nil {
			m.setError(errSummary(msg.err))
		} else {
			m.setMessage(msg.ok)
			m.selectPath = msg.selectPath
		}
		m.ensureVisible()
		cmds := []tea.Cmd{m.reloadRepo(msg.repo)}
		if m.deps.Procs != nil {
			// Refresh live state now (e.g. an agent in a removed worktree);
			// a one-shot poll that does not re-arm the live tick.
			cmds = append(cmds, pollLiveOnce(m.ctx, m.deps.Procs))
		}
		return tea.Batch(cmds...), true
	case branchesMsg:
		delete(m.busy, msg.wtPath)
		if m.dialog != nil {
			m.setMessage("checkout: another prompt is open, press c again")
		} else {
			m.openCheckout(msg)
		}
		m.ensureVisible()
		return nil, true
	case newBranchesMsg:
		delete(m.busy, "r:"+msg.repo)
		if m.dialog != nil {
			m.setMessage("new worktree: another prompt is open, press n again")
		} else {
			m.openNewBranch(msg)
		}
		m.ensureVisible()
		return nil, true
	case removeCheckMsg:
		delete(m.busy, msg.path)
		if m.dialog != nil {
			m.setMessage("remove: another prompt is open, press x again")
		} else {
			m.openRemove(msg)
		}
		m.ensureVisible()
		return nil, true
	case pruneListMsg:
		delete(m.busy, "r:"+msg.repo)
		if m.dialog != nil {
			m.setMessage("prune: another prompt is open, press p again")
		} else {
			m.openPrune(msg)
		}
		m.ensureVisible()
		return nil, true
	}
	return nil, false
}

// errSummary renders a git failure for the status line: the first 3 lines
// of git's stderr, sanitised (FR-21).
func errSummary(err error) string {
	var iu inUseError
	if errors.As(err, &iu) {
		return iu.msg
	}
	var ge *gitx.Error
	text := err.Error()
	if errors.As(err, &ge) {
		switch {
		case errors.Is(ge.Err, context.DeadlineExceeded):
			text = "git " + ge.Args[0] + ": timed out"
		case ge.Stderr != "":
			text = ge.Stderr
		}
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(sanitize(l)); l != "" {
			lines = append(lines, l)
		}
		if len(lines) == 3 {
			break
		}
	}
	return strings.Join(lines, " · ")
}

func (m *Model) branchText(wt *gitx.Worktree) string {
	if wt.Detached() {
		return "detached @ " + wt.ShortHEAD()
	}
	return wt.Branch
}

// detach implements `d` (FR-9): it detaches immediately, asking for
// confirmation only when a live agent is running in the worktree.
func (m *Model) detach() tea.Cmd {
	repo, wt, ok := m.target()
	if !ok {
		return nil
	}
	if wt.Detached() {
		m.setMessage("already detached")
		return nil
	}
	disp, branch, path, sha := m.display(wt.Path), wt.Branch, wt.Path, wt.ShortHEAD()
	dirty := wt.Status != nil && wt.Status.Changed > 0
	do := func(string) tea.Cmd {
		return m.run(path, "detaching", repo.Path, func(ctx context.Context, r gitx.Runner) (string, error) {
			if err := gitx.Detach(ctx, r, path); err != nil {
				return "", err
			}
			s := fmt.Sprintf("detached %s at %s; %s is free", disp, sha, branch)
			if dirty {
				s += " (uncommitted changes kept)"
			}
			return s, nil
		})
	}
	if !m.bound.liveWT[path] {
		return do("")
	}
	lines := []string{
		fmt.Sprintf("Detach HEAD of %s at %s? Branch %s becomes free to check out elsewhere.", disp, sha, branch),
		"An agent is running here; it will now be on a detached HEAD.",
	}
	if dirty {
		lines = append(lines, "Uncommitted changes stay in this worktree.")
	}
	m.confirm(do, lines...)
	return nil
}

// checkout implements `c` (FR-10): load branches, then prompt.
func (m *Model) checkout() tea.Cmd {
	_, wt, ok := m.target()
	if !ok {
		return nil
	}
	path := wt.Path
	return m.startBusy(path, "loading branches", func(ctx context.Context, r gitx.Runner) tea.Msg {
		bs, err := gitx.LocalBranches(ctx, r, path)
		var last string
		if err == nil {
			last = gitx.LastBranch(ctx, r, path)
		}
		return branchesMsg{wtPath: path, branches: bs, last: last, err: err}
	})
}

func (m *Model) findWorktree(path string) (*scan.Repo, *gitx.Worktree) {
	for _, r := range m.repos {
		for i := range r.Worktrees {
			if r.Worktrees[i].Path == path {
				return r, &r.Worktrees[i]
			}
		}
	}
	return nil, nil
}

func (m *Model) openCheckout(msg branchesMsg) {
	if msg.err != nil {
		m.setError(errSummary(msg.err))
		return
	}
	repo, wt := m.findWorktree(msg.wtPath)
	if wt == nil {
		return // vanished meanwhile
	}
	prefill := ""
	for _, b := range msg.branches {
		if b == msg.last && b != wt.Branch {
			prefill = b
		}
	}
	path, disp, cur := wt.Path, m.display(wt.Path), wt.Branch
	known := make(map[string]bool, len(msg.branches))
	for _, b := range msg.branches {
		known[b] = true
	}
	hint := "tab complete · enter checkout · esc cancel"
	if len(msg.branches) > 0 {
		hint = "branches: " + strings.Join(msg.branches, " ") + " · " + hint
	}
	m.prompt(prefill, msg.branches, hint, func(branch string) tea.Cmd {
		switch {
		case branch == "":
			m.setMessage("no branch given")
			return nil
		case branch == cur:
			m.setMessage("already on " + branch)
			return nil
		case !known[branch]:
			m.setError("no local branch " + branch)
			return nil
		}
		holder := ""
		if _, h := m.holder(repo, branch, path); h != nil {
			holder = m.display(h.Path)
		}
		return m.run(path, "checking out", repo.Path, func(ctx context.Context, r gitx.Runner) (string, error) {
			err := gitx.Checkout(ctx, r, path, branch)
			if err == nil {
				return fmt.Sprintf("checked out %s in %s", branch, disp), nil
			}
			if gitx.IsBranchInUse(err) && holder != "" {
				return "", inUseError{err: err, msg: fmt.Sprintf("%s is checked out in %s; press d there to detach", branch, holder)}
			}
			return "", err
		})
	}, fmt.Sprintf("Checkout branch in %s (now %s):", disp, m.branchText(wt)))
}

// holder finds the worktree (other than self) that has branch checked out,
// from the scanned worktree list (FR-2).
func (m *Model) holder(repo *scan.Repo, branch, self string) (*scan.Repo, *gitx.Worktree) {
	if repo == nil {
		return nil, nil
	}
	for i := range repo.Worktrees {
		if w := &repo.Worktrees[i]; w.Branch == branch && w.Path != self {
			return repo, w
		}
	}
	return nil, nil
}

// inUseError replaces git's error (which names the holder by absolute path)
// with a one-line message naming the holding worktree and a hint.
type inUseError struct {
	err error
	msg string
}

func (e inUseError) Error() string { return e.msg + ": " + e.err.Error() }
func (e inUseError) Unwrap() error { return e.err }

// lock implements `l` (FR-14).
func (m *Model) lock() tea.Cmd {
	repo, wt, ok := m.target()
	if !ok {
		return nil
	}
	if wt.IsMain {
		m.setMessage("cannot lock the main worktree")
		return nil
	}
	path, disp, rp := wt.Path, m.display(wt.Path), repo.Path
	if wt.Locked {
		lines := []string{fmt.Sprintf("Unlock %s?", disp)}
		if wt.LockReason != "" {
			lines = append(lines, "Locked: "+sanitize(wt.LockReason))
		}
		m.confirm(func(string) tea.Cmd {
			return m.run(path, "unlocking", rp, func(ctx context.Context, r gitx.Runner) (string, error) {
				return "unlocked " + disp, gitx.Unlock(ctx, r, rp, path)
			})
		}, lines...)
		return nil
	}
	m.prompt("", nil, "enter lock · esc cancel", func(reason string) tea.Cmd {
		return m.run(path, "locking", rp, func(ctx context.Context, r gitx.Runner) (string, error) {
			return "locked " + disp, gitx.Lock(ctx, r, rp, path, reason)
		})
	}, fmt.Sprintf("Lock %s — reason (optional):", disp))
	return nil
}

// prune implements `p` (FR-13): dry-run first, then confirm.
func (m *Model) prune() tea.Cmd {
	repo, _ := m.selection()
	if repo == nil {
		return nil
	}
	key, path := "r:"+repo.Path, repo.Path
	if l := m.busy[key]; l != "" {
		m.setMessage(l + " in progress")
		return nil
	}
	return m.startBusy(key, "checking prunable", func(ctx context.Context, r gitx.Runner) tea.Msg {
		lines, err := gitx.PruneDryRun(ctx, r, path)
		return pruneListMsg{repo: path, lines: lines, err: err}
	})
}

func (m *Model) openPrune(msg pruneListMsg) {
	switch {
	case msg.err != nil:
		m.setError(errSummary(msg.err))
		return
	case len(msg.lines) == 0:
		m.setMessage("nothing to prune")
		return
	}
	name := m.repoName(msg.repo)
	n := len(msg.lines)
	lines := []string{fmt.Sprintf("Prune %d stale worktree entr%s in %s?", n, pluralY(n), name)}
	for i, l := range msg.lines {
		if i == maxPruneListed {
			lines = append(lines, fmt.Sprintf("  +%d more", n-i))
			break
		}
		lines = append(lines, "  "+l)
	}
	path := msg.repo
	m.confirm(func(string) tea.Cmd {
		return m.run("r:"+path, "pruning", path, func(ctx context.Context, r gitx.Runner) (string, error) {
			out, err := gitx.Prune(ctx, r, path)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("pruned %d worktree entr%s in %s", len(out), pluralY(len(out)), name), nil
		})
	}, lines...)
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
