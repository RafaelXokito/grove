package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
)

// newBranchesMsg delivers the local branches for the new-worktree prompt.
type newBranchesMsg struct {
	repo     string
	branches []string
	err      error
}

// newWorktree implements `n` (FR-11): load the local branches, then prompt
// for the branch and then the path. It applies to the selected repo, also
// from a project row.
func (m *Model) newWorktree() tea.Cmd {
	repo, _ := m.selection()
	if repo == nil {
		return nil
	}
	key, path := "r:"+repo.Path, repo.Path
	if l := m.busy[key]; l != "" {
		m.setMessage(l + " in progress for " + repo.Name)
		return nil
	}
	return m.startBusy(key, "loading branches", func(ctx context.Context, r gitx.Runner) tea.Msg {
		bs, err := gitx.LocalBranches(ctx, r, path)
		return newBranchesMsg{repo: path, branches: bs, err: err}
	})
}

func (m *Model) findRepo(path string) *scan.Repo {
	for _, r := range m.repos {
		if r.Path == path {
			return r
		}
	}
	return nil
}

func (m *Model) openNewBranch(msg newBranchesMsg) {
	if msg.err != nil {
		m.setError(errSummary(msg.err))
		return
	}
	repo := m.findRepo(msg.repo)
	if repo == nil {
		return // vanished meanwhile
	}
	known := make(map[string]bool, len(msg.branches))
	for _, b := range msg.branches {
		known[b] = true
	}
	hint := "tab complete · enter next · esc cancel"
	if len(msg.branches) > 0 {
		hint = "branches: " + strings.Join(msg.branches, " ") + " · " + hint
	}
	m.prompt("", msg.branches, hint, func(branch string) tea.Cmd {
		switch {
		case branch == "":
			m.setMessage("no branch given")
			return nil
		case strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t"):
			m.setError("invalid branch name " + branch)
			return nil
		}
		if known[branch] {
			if _, h := m.holder(repo, branch, ""); h != nil {
				m.setError(fmt.Sprintf("%s is checked out in %s; press d there to detach", branch, m.display(h.Path)))
				return nil
			}
		}
		m.openNewPath(repo, branch, known[branch])
		return nil
	}, fmt.Sprintf("New worktree in %s — branch (existing or new):", repo.Name))
}

func (m *Model) openNewPath(repo *scan.Repo, branch string, exists bool) {
	rp, name := repo.Path, repo.Name
	what := "new branch " + branch + " from HEAD"
	if exists {
		what = "existing branch " + branch
	}
	// Repo paths are canonical, so abbreviate with the canonical home first.
	home := m.deps.Home
	def := pathx.AbbrevHome(pathx.AbbrevHome(defaultWorktreePath(rp, branch), m.homeCanon), home)
	// Every known repo/worktree path: a new worktree must not nest in one.
	var known []string
	for _, r := range m.repos {
		known = append(known, r.Path)
		for _, w := range r.Worktrees {
			known = append(known, w.Path)
		}
	}
	m.prompt(def, nil, "enter create · esc cancel", func(p string) tea.Cmd {
		if p == "" {
			m.setMessage("no path given")
			return nil
		}
		path := resolvePath(p, home, filepath.Dir(rp))
		key := "r:" + rp
		if l := m.busy[key]; l != "" {
			m.setMessage(l + " in progress for " + name)
			return nil
		}
		return m.startBusy(key, "creating worktree", func(ctx context.Context, r gitx.Runner) tea.Msg {
			done := actionDoneMsg{key: key, repo: rp}
			canon := pathx.Canonical(path)
			for _, k := range known {
				if pathx.IsWithinFold(k, canon) {
					done.err = fmt.Errorf("%s is inside existing worktree %s; choose a path outside it", path, k)
					return done
				}
			}
			if done.err = gitx.AddWorktree(ctx, r, rp, path, branch, !exists); done.err == nil {
				done.ok = fmt.Sprintf("created %s on %s", path, branch)
				done.selectPath = canon
			}
			return done
		})
	}, fmt.Sprintf("New worktree for %s — path:", what))
}

// defaultWorktreePath is the sibling directory <repo-parent>/<repo>-<branch
// slug> (FR-11).
func defaultWorktreePath(repo, branch string) string {
	base := strings.TrimSuffix(filepath.Base(repo), ".git")
	return filepath.Join(filepath.Dir(repo), base+"-"+slug(branch))
}

// slug turns a branch name into a single path component: anything other than
// letters, digits, '.', '_' and '-' (notably '/') becomes '-'.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r == '.' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !ok {
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		b.WriteRune(r)
		dash = false
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" || strings.Trim(out, ".") == "" {
		return "wt"
	}
	return out
}

// resolvePath expands ~ and makes p absolute relative to base.
func resolvePath(p, home, base string) string {
	if home != "" {
		p = pathx.ExpandHome(p, home)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// selectPending moves the cursor to the worktree created by `n` once its
// repo has reloaded.
func (m *Model) selectPending(repo *scan.Repo) {
	if m.selectPath == "" {
		return
	}
	for _, wt := range repo.Worktrees {
		if wt.Path != m.selectPath {
			continue
		}
		m.selectPath = ""
		m.collapsed["r:"+repo.Path] = false
		m.rows = m.flatten()
		for i, r := range m.rows {
			if r.key == "w:"+wt.Path {
				m.cursor, m.cursorMoved = i, true
				break
			}
		}
		m.ensureVisible()
		return
	}
}

// removeSnap is the worktree state a remove confirmation was based on.
type removeSnap struct {
	changed int // -1: status unknown
	live    bool
	locked  bool
}

// errChanged aborts a remove whose worktree changed after the dialog opened.
var errChanged = errors.New("worktree changed since confirmation — press x again")

// removeCheckMsg delivers the fresh status taken when `x` is pressed.
type removeCheckMsg struct {
	path string
	st   *gitx.Status
	err  error
}

// removeRisks lists what a remove would destroy or disrupt, and the number of
// --force flags git needs (FR-12): one when there are (or may be) uncommitted
// changes, two when locked. A live agent is a risk line only.
func (m *Model) removeRisks(wt *gitx.Worktree, snap removeSnap) (risks []string, force int) {
	switch {
	case snap.changed < 0:
		risks = append(risks, "status unknown — uncommitted changes may be LOST")
		force = 1
	case snap.changed > 0:
		n := snap.changed
		risks = append(risks, fmt.Sprintf("%d uncommitted change%s will be LOST", n, plural(n)))
		force = 1
	}
	if snap.live {
		seen := map[string]bool{}
		for _, s := range m.bound.byWT[wt.Path] {
			if !s.Live {
				continue
			}
			l := fmt.Sprintf("a %s agent is running here", s.Agent)
			if s.Agent == sessions.AgentClaude {
				state := "idle"
				if s.Busy {
					state = "busy"
				}
				l += " (" + state + ")"
			}
			if !seen[l] {
				seen[l] = true
				risks = append(risks, l)
			}
		}
		if len(seen) == 0 {
			risks = append(risks, "an agent is running here")
		}
	}
	if snap.locked {
		l := "worktree is locked"
		if wt.LockReason != "" {
			l += ": " + wt.LockReason
		}
		risks = append(risks, l)
		force = 2 // git needs --force twice for a locked worktree
	}
	return risks, force
}

// remove implements `x` (FR-12): it first takes a fresh `git status` of the
// worktree, then opens the confirmation (see openRemove).
func (m *Model) remove() tea.Cmd {
	_, wt, ok := m.target()
	if !ok {
		return nil
	}
	if wt.IsMain {
		m.setMessage("cannot remove the main worktree")
		return nil
	}
	path := wt.Path
	return m.startBusy(path, "checking status", func(ctx context.Context, r gitx.Runner) tea.Msg {
		st, err := gitx.WorktreeStatus(ctx, r, path)
		return removeCheckMsg{path: path, st: st, err: err}
	})
}

// openRemove shows the remove confirmation: always asked (it deletes a
// directory); risky removes spell out each risk and use --force after a
// single `y`. Right before removing, the status is taken again and the
// remove is aborted if the worktree got dirtier, live or locked meanwhile.
func (m *Model) openRemove(msg removeCheckMsg) {
	repo, wt := m.findWorktree(msg.path)
	if wt == nil {
		return // vanished meanwhile
	}
	snap := removeSnap{changed: -1, live: m.bound.liveWT[wt.Path], locked: wt.Locked}
	if msg.err == nil && msg.st != nil {
		snap.changed = msg.st.Changed
		wt.Status = msg.st
	}
	risks, force := m.removeRisks(wt, snap)
	path, disp, rp, branch := wt.Path, m.display(wt.Path), repo.Path, wt.Branch
	var lines []string
	if len(risks) == 0 {
		lines = []string{fmt.Sprintf("Remove worktree %s (%s)? Its directory will be deleted.", disp, m.branchText(wt))}
	} else {
		lines = []string{fmt.Sprintf("Remove worktree %s (%s)? Its directory will be deleted, and:", disp, m.branchText(wt))}
		for _, r := range risks {
			lines = append(lines, "  ! "+r)
		}
		if force > 0 {
			lines = append(lines, "y removes anyway (git worktree remove --force).")
		} else {
			lines = append(lines, "y removes anyway.")
		}
	}
	m.confirm(func(v string) tea.Cmd {
		// Live/lock state as the model knows it now, vs. when the dialog opened.
		_, cur := m.findWorktree(path)
		if cur == nil {
			return nil
		}
		if (m.bound.liveWT[path] && !snap.live) || (cur.Locked && !snap.locked) {
			m.setError(errChanged.Error())
			return nil
		}
		delBranch := v != "" && branch != ""
		return m.run(path, "removing", rp, func(ctx context.Context, r gitx.Runner) (string, error) {
			if snap.changed >= 0 {
				st, err := gitx.WorktreeStatus(ctx, r, path)
				if err != nil || st == nil || st.Changed > snap.changed {
					return "", errChanged
				}
			}
			if err := gitx.RemoveWorktree(ctx, r, rp, path, force); err != nil {
				return "", err
			}
			s := "removed " + disp
			switch {
			case branch == "":
			case !delBranch:
				s += "; branch " + branch + " kept"
			default:
				if err := gitx.DeleteBranch(ctx, r, rp, branch); err != nil {
					return "", fmt.Errorf("%s, but branch %s was not deleted: %s", s, branch, errSummary(err))
				}
				s += "; deleted branch " + branch
			}
			return s, nil
		})
	}, lines...)
	if branch != "" {
		m.dialog.toggle = &dialogToggle{key: "b", label: "also delete branch " + branch + " (git branch -d)"}
		m.dialog.hint = "[y] remove  [b] toggle branch delete  [n/esc] cancel"
	} else {
		m.dialog.hint = "[y] remove  [n/esc] cancel"
	}
}
