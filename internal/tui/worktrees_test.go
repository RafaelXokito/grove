package tui

import (
	"strings"
	"testing"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/sessions"
)

const branchesOut = "feat\nmain\nold\nspare\n"

const statusArgs = "status --porcelain=v2 --branch --untracked-files=normal --ignore-submodules=dirty"

// dirtyStatus is a porcelain v2 status with n untracked files.
func dirtyStatus(n int) opsResp {
	return opsResp{out: strings.Repeat("? f\n", n)}
}

func setResp(o *opsRunner, args string, r opsResp) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.resp == nil {
		o.resp = map[string]opsResp{}
	}
	o.resp[args] = r
}

func typeText(t *testing.T, h *harness, s string) {
	t.Helper()
	for _, r := range s {
		h.key(t, string(r))
	}
}

func TestRemoveMainRefused(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha")
	h.key(t, "x")
	if h.m.dialog != nil {
		t.Fatal("dialog opened for the main worktree")
	}
	wantView(t, h, "cannot remove the main worktree")
	wantCalls(t, o)
}

func TestRemoveGuards(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	for _, tc := range []struct{ row, want string }{
		{"r:/root/alpha", "select a worktree"},
		{"w:/root/alpha-gone", "press p to prune"},
	} {
		h.selectKey(t, tc.row)
		h.key(t, "x")
		if h.m.dialog != nil {
			t.Errorf("%s: dialog opened", tc.row)
		}
		wantView(t, h, tc.want)
	}
	wantCalls(t, o)
}

func TestRemoveCleanConfirms(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "x")
	wantView(t, h, "Remove worktree beta-slow (slow)? Its directory will be deleted.",
		"[ ] also delete branch slow (git branch -d)", "[y] remove  [b] toggle branch delete")
	if strings.Contains(h.view(), "LOST") || strings.Contains(h.view(), "--force") {
		t.Errorf("clean remove shows risks:\n%s", h.view())
	}
	h.key(t, "y")
	wantCalls(t, o, "/root/beta: worktree remove -- /root/beta-slow")
	wantView(t, h, "removed beta-slow; branch slow kept")
	if !o.reloaded("/root/beta") {
		t.Error("repo not reloaded")
	}
}

func TestRemoveCancelRunsNothing(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	h.m.bound.liveWT["/root/alpha-feat"] = true
	for _, k := range []string{"esc", "n"} {
		h.key(t, "x")
		h.key(t, "b")
		h.key(t, k)
		if h.m.dialog != nil {
			t.Fatalf("%s did not close the dialog", k)
		}
		wantView(t, h, "cancelled")
	}
	if o.reloaded("/root/alpha") {
		t.Error("reloaded after cancel")
	}
	wantCalls(t, o) // only the fresh status reads ran

}

func TestRemoveRiskCombos(t *testing.T) {
	const (
		dirty  = "3 uncommitted changes will be LOST"
		claude = "a claude agent is running here (busy)"
		codex  = "a codex agent is running here"
		anyAg  = "an agent is running here"
		locked = "worktree is locked: agent at work"
		unk    = "status unknown — uncommitted changes may be LOST"
	)
	all := []string{dirty, claude, codex, anyAg, locked, unk}
	for _, tc := range []struct {
		name                  string
		dirty, locked, unk    bool
		live, claude, codexOn bool
		want                  []string
		force                 string
	}{
		{name: "dirty", dirty: true, want: []string{dirty}, force: "--force "},
		{name: "status unknown", unk: true, want: []string{unk}, force: "--force "},
		{name: "live+clean: no force", live: true, want: []string{anyAg}, force: ""},
		{name: "live claude+clean: no force", live: true, claude: true, want: []string{claude}, force: ""},
		{name: "locked", locked: true, want: []string{locked}, force: "--force --force "},
		{name: "dirty+live", dirty: true, live: true, claude: true, want: []string{dirty, claude}, force: "--force "},
		{name: "all", dirty: true, live: true, claude: true, codexOn: true, locked: true,
			want: []string{dirty, claude, codex, locked}, force: "--force --force "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, o := newOpsHarness(t, nil)
			_, wt := h.m.findWorktree("/root/beta-slow")
			if tc.dirty {
				setResp(o, statusArgs, dirtyStatus(3))
			}
			if tc.unk {
				setResp(o, statusArgs, opsResp{err: &gitx.Error{Args: []string{"status"}, Stderr: "fatal: boom"}})
			}
			if tc.locked {
				wt.Locked, wt.LockReason = true, "agent at work"
			}
			p := "/root/beta-slow"
			h.m.bound.liveWT[p] = tc.live
			if tc.claude {
				h.m.bound.byWT[p] = append(h.m.bound.byWT[p], liveSession("claude", true))
			}
			if tc.codexOn {
				h.m.bound.byWT[p] = append(h.m.bound.byWT[p], liveSession("codex", false))
			}
			h.selectKey(t, "w:"+p)
			h.key(t, "x")
			last := "y removes anyway (git worktree remove --force)."
			if tc.force == "" {
				last = "y removes anyway."
			}
			wantView(t, h, append([]string{"Remove worktree beta-slow (slow)? Its directory will be deleted, and:", last}, tc.want...)...)
			v := h.view()
			for _, s := range all {
				wanted := false
				for _, w := range tc.want {
					wanted = wanted || w == s
				}
				if !wanted && strings.Contains(v, s) {
					t.Errorf("unexpected risk %q:\n%s", s, v)
				}
			}
			h.key(t, "y")
			wantCalls(t, o, "/root/beta: worktree remove "+tc.force+"-- /root/beta-slow")
		})
	}
}

func TestRemoveDirtyLiveLockedNoBranchDelete(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{statusArgs: dirtyStatus(3)})
	p := "/root/alpha-feat" // locked, branch feat
	h.m.bound.liveWT[p] = true
	h.m.bound.byWT[p][0].Live, h.m.bound.byWT[p][0].Busy = true, true
	h.selectKey(t, "w:"+p)
	h.key(t, "x")
	wantView(t, h, "3 uncommitted changes will be LOST", "a claude agent is running here (busy)",
		"worktree is locked", "[ ] also delete branch feat")
	h.key(t, "y")
	wantCalls(t, o, "/root/alpha: worktree remove --force --force -- /root/alpha-feat")
	wantView(t, h, "removed alpha-feat; branch feat kept")
	if h.cursorKey() != "r:/root/alpha" && !strings.HasPrefix(h.cursorKey(), "w:/root/alpha") {
		t.Errorf("cursor on %q", h.cursorKey())
	}
}

func TestRemoveBranchToggle(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "x")
	h.key(t, "b")
	wantView(t, h, "[x] also delete branch slow")
	h.key(t, "b")
	wantView(t, h, "[ ] also delete branch slow")
	h.key(t, "b")
	h.key(t, "y")
	wantCalls(t, o, "/root/beta: worktree remove -- /root/beta-slow", "/root/beta: branch -d -- slow")
	wantView(t, h, "removed beta-slow; deleted branch slow")
}

func TestRemoveBranchUnmerged(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"branch -d -- slow": {err: &gitx.Error{Args: []string{"branch", "-d"},
			Stderr: "error: the branch 'slow' is not fully merged\nhint: If you are sure you want to delete it, run 'git branch -D slow'"}},
	})
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "x")
	h.key(t, "b")
	h.key(t, "y")
	wantCalls(t, o, "/root/beta: worktree remove -- /root/beta-slow", "/root/beta: branch -d -- slow")
	if !h.m.msgErr || !strings.Contains(h.m.message, "removed beta-slow, but branch slow was not deleted: error: the branch 'slow' is not fully merged") {
		t.Errorf("message = %q (err %v)", h.m.message, h.m.msgErr)
	}
	for _, c := range o.calls {
		if strings.Contains(c, "-D") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestRemoveDetachedHidesToggle(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "x")
	wantView(t, h, "Remove worktree alpha-det (detached @ abcdef0)?", "[y] remove  [n/esc] cancel")
	if strings.Contains(h.view(), "delete branch") {
		t.Errorf("branch toggle shown for detached:\n%s", h.view())
	}
	h.key(t, "b")
	h.key(t, "y")
	wantCalls(t, o, "/root/alpha: worktree remove -- /root/alpha-det")
}

func TestNewWorktreeNewBranch(t *testing.T) {
	newPorcelain := fixturePorcelain("/root/alpha") +
		"worktree /root/alpha-fix-nqxj\x00HEAD 1111111aaaaaaa\x00branch refs/heads/fix/nqxj\x00\x00"
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
		"worktree list --porcelain -z":                      {out: newPorcelain},
	})
	h.selectKey(t, "r:/root/alpha") // project row: n applies to the repo
	h.key(t, "n")
	wantView(t, h, "New worktree in alpha — branch (existing or new):", "branches: feat main old")
	typeText(t, h, "fix/nqxj") // n, q, x, j are typed, not actions
	if h.quit || h.m.dialog == nil || h.m.dialog.input.Value() != "fix/nqxj" {
		t.Fatalf("typing escaped the prompt: quit=%v\n%s", h.quit, h.view())
	}
	h.key(t, "enter")
	wantView(t, h, "New worktree for new branch fix/nqxj from HEAD — path:")
	if v := h.m.dialog.input.Value(); v != "/root/alpha-fix-nqxj" {
		t.Errorf("default path = %q", v)
	}
	h.key(t, "enter")
	wantCalls(t, o,
		"/root/alpha: for-each-ref --format=%(refname:short) refs/heads",
		"/root/alpha: worktree add -b fix/nqxj -- /root/alpha-fix-nqxj")
	wantView(t, h, "created /root/alpha-fix-nqxj on fix/nqxj")
	if got := h.cursorKey(); got != "w:/root/alpha-fix-nqxj" {
		t.Errorf("cursor on %q, want the new worktree", got)
	}
	if len(h.m.busy) != 0 {
		t.Errorf("busy not cleared: %v", h.m.busy)
	}
}

func TestNewWorktreeExistingBranchHeld(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
	})
	h.selectKey(t, "s:claude:c1c1c1c1-aaaa-bbbb") // session row → its repo
	h.key(t, "n")
	typeText(t, h, "fe")
	h.key(t, "tab")
	h.key(t, "enter")
	if h.m.dialog != nil {
		t.Fatalf("path prompt opened for a held branch:\n%s", h.view())
	}
	if want := "feat is checked out in alpha-feat; press d there to detach"; h.m.message != want || !h.m.msgErr {
		t.Errorf("message = %q, want %q", h.m.message, want)
	}
	wantCalls(t, o, "/root/alpha: for-each-ref --format=%(refname:short) refs/heads")
}

func TestNewWorktreeExistingBranch(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
	})
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "n")
	typeText(t, h, "spare")
	h.key(t, "enter")
	wantView(t, h, "New worktree for existing branch spare — path:")
	h.m.dialog.input.SetValue("elsewhere/x") // relative to the repo's parent
	h.key(t, "enter")
	wantCalls(t, o,
		"/root/alpha: for-each-ref --format=%(refname:short) refs/heads",
		"/root/alpha: worktree add -- /root/elsewhere/x spare")
}

func TestNewWorktreeInsideWorktreeRefused(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
	})
	for _, p := range []string{"/root/alpha-feat/sub", "/root/alpha", "/root/beta/x"} {
		h.selectKey(t, "r:/root/alpha")
		h.key(t, "n")
		typeText(t, h, "topic")
		h.key(t, "enter")
		h.m.dialog.input.SetValue(p)
		h.key(t, "enter")
		if !h.m.msgErr || !strings.Contains(h.m.message, "is inside existing worktree") {
			t.Errorf("%s: message = %q", p, h.m.message)
		}
	}
	for _, c := range o.actions() {
		if strings.Contains(c, "worktree add") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestRemoveAbortsWhenChanged(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{statusArgs: dirtyStatus(1)})
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "x")
	wantView(t, h, "1 uncommitted change will be LOST")
	setResp(o, statusArgs, dirtyStatus(2)) // more work appeared meanwhile
	h.key(t, "y")
	wantView(t, h, "worktree changed since confirmation — press x again")
	wantCalls(t, o)

	// Became live after the dialog opened: aborted before any git call.
	setResp(o, statusArgs, opsResp{})
	h.key(t, "x")
	h.m.bound.liveWT["/root/beta-slow"] = true
	h.key(t, "y")
	wantView(t, h, "worktree changed since confirmation — press x again")
	wantCalls(t, o)

	// Fewer changes (e.g. committed) is fine.
	delete(h.m.bound.liveWT, "/root/beta-slow")
	setResp(o, statusArgs, dirtyStatus(2))
	h.key(t, "x")
	setResp(o, statusArgs, opsResp{})
	h.key(t, "y")
	wantCalls(t, o, "/root/beta: worktree remove --force -- /root/beta-slow")
}

func TestNewWorktreeCancel(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
	})
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "n")
	h.key(t, "esc")
	h.key(t, "n")
	typeText(t, h, "topic")
	h.key(t, "enter")
	h.key(t, "esc")
	h.key(t, "n")
	h.key(t, "enter") // empty branch
	wantView(t, h, "no branch given")
	for _, c := range o.actions() {
		if !strings.HasSuffix(c, "for-each-ref --format=%(refname:short) refs/heads") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestDefaultWorktreePath(t *testing.T) {
	for _, tc := range []struct{ repo, branch, want string }{
		{"/r/grove", "feat", "/r/grove-feat"},
		{"/r/grove", "user/fix login", "/r/grove-user-fix-login"},
		{"/r/proj.git", "a//b", "/r/proj-a-b"},
		{"/r/grove", "..", "/r/grove-wt"},
	} {
		if got := defaultWorktreePath(tc.repo, tc.branch); got != tc.want {
			t.Errorf("defaultWorktreePath(%q, %q) = %q, want %q", tc.repo, tc.branch, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"~/w/x", "/home/u/w/x"},
		{"x", "/base/x"},
		{"/abs/../y", "/y"},
	} {
		if got := resolvePath(tc.in, "/home/u", "/base"); got != tc.want {
			t.Errorf("resolvePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func liveSession(agent string, busy bool) sessions.Session {
	return sessions.Session{Agent: sessions.Agent(agent), ID: agent + "-live", Cwd: "/root/beta-slow", Live: true, Busy: busy}
}

func TestRemoveRefreshesLiveOnce(t *testing.T) {
	procs := &fakeProcs{procs: []sessions.LiveProc{{PID: 10, Agent: sessions.AgentClaude, SessionID: "c1c1c1c1-aaaa-bbbb", Busy: true}}}
	h := newHarness(t, fixtureRepos(), fixtureSources(), procs)
	o := &opsRunner{}
	h.m.deps.Runner = o
	h.settle(t, h.m.Init())
	calls, ticks := procs.calls, len(h.ticks)
	liveTicks := func() int {
		n := 0
		for _, d := range h.ticks {
			if d == h.m.deps.LiveInterval {
				n++
			}
		}
		return n
	}
	before := liveTicks()
	procs.mu.Lock()
	procs.procs = nil // the agent exited with its worktree
	procs.mu.Unlock()
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "x")
	h.key(t, "y")
	wantCalls(t, o, "/root/beta: worktree remove -- /root/beta-slow")
	if procs.calls != calls+1 {
		t.Errorf("live polls after remove = %d, want 1", procs.calls-calls)
	}
	if liveTicks() != before {
		t.Errorf("extra live tick armed (%d → %d of %d ticks)", before, liveTicks(), ticks)
	}
	if h.m.bound.live != 0 {
		t.Errorf("live count = %d after refresh", h.m.bound.live)
	}
}

func TestNewWorktreeDefaultPathAbbrevHome(t *testing.T) {
	h, _ := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: branchesOut},
	})
	h.m.homeCanon = "/root" // pretend the canonical home holds the repos
	h.selectKey(t, "r:/root/alpha")
	h.key(t, "n")
	typeText(t, h, "topic")
	h.key(t, "enter")
	if v := h.m.dialog.input.Value(); v != "~/alpha-topic" {
		t.Errorf("default path = %q, want ~/alpha-topic", v)
	}
}
