package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/gitx"
)

// opsRunner is a scripted git runner: it records "dir: args" for every call
// and answers from resp (keyed by the joined args); anything else (e.g. the
// post-action reload's worktree list / status) fails harmlessly.
type opsRunner struct {
	mu    sync.Mutex
	calls []string
	resp  map[string]opsResp
}

type opsResp struct {
	out, errOut string
	err         error
}

func (o *opsRunner) RunOutput(_ context.Context, dir string, args ...string) ([]byte, []byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	a := strings.Join(args, " ")
	o.calls = append(o.calls, dir+": "+a)
	if r, ok := o.resp[a]; ok {
		return []byte(r.out), []byte(r.errOut), r.err
	}
	if a == "worktree list --porcelain -z" {
		return []byte(fixturePorcelain(dir)), nil, nil
	}
	if strings.HasPrefix(a, "status") {
		return nil, nil, nil // clean
	}
	return nil, nil, nil
}

// fixturePorcelain renders fixtureRepos' worktree list for repo, so a
// post-action reload keeps the tree intact.
func fixturePorcelain(repo string) string {
	var b strings.Builder
	for _, r := range fixtureRepos() {
		if r.Path != repo {
			continue
		}
		for _, w := range r.Worktrees {
			b.WriteString("worktree " + w.Path + "\x00HEAD " + w.HEAD + "\x00")
			if w.Branch != "" {
				b.WriteString("branch refs/heads/" + w.Branch + "\x00")
			} else {
				b.WriteString("detached\x00")
			}
			if w.Locked {
				b.WriteString("locked\x00")
			}
			b.WriteString("\x00")
		}
	}
	return b.String()
}

func (o *opsRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, _, err := o.RunOutput(ctx, dir, args...)
	return out, err
}

// actions returns the recorded calls other than reload reads.
func (o *opsRunner) actions() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, c := range o.calls {
		if !strings.Contains(c, ": worktree list") && !strings.Contains(c, ": status") {
			out = append(out, c)
		}
	}
	return out
}

func (o *opsRunner) reloaded(repo string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.calls {
		if strings.HasPrefix(c, repo+": worktree list") {
			return true
		}
	}
	return false
}

func newOpsHarness(t *testing.T, resp map[string]opsResp) (*harness, *opsRunner) {
	t.Helper()
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	o := &opsRunner{resp: resp}
	h.m.deps.Runner = o
	h.settle(t, h.m.Init())
	return h, o
}

func wantCalls(t *testing.T, o *opsRunner, want ...string) {
	t.Helper()
	got := o.actions()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("git calls:\n%q\nwant:\n%q", got, want)
	}
}

func wantView(t *testing.T, h *harness, subs ...string) {
	t.Helper()
	v := h.view()
	for _, s := range subs {
		if !strings.Contains(v, s) {
			t.Errorf("view lacks %q:\n%s", s, v)
		}
	}
}

func TestDetachDirtyImmediate(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "s:claude:c1c1c1c1-aaaa-bbbb") // session → parent worktree alpha-feat (dirty, not live)
	h.key(t, "d")
	if h.m.dialog != nil {
		t.Fatal("dialog opened for a worktree without a live agent")
	}
	wantCalls(t, o, "/root/alpha-feat: checkout --detach")
	wantView(t, h, "detached alpha-feat at 2222222", "uncommitted changes kept")
	if !o.reloaded("/root/alpha") {
		t.Error("repo not reloaded after detach")
	}
	if len(h.m.busy) != 0 {
		t.Errorf("busy not cleared: %v", h.m.busy)
	}
}

func TestDetachCleanNoNote(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/beta")
	h.key(t, "d")
	wantCalls(t, o, "/root/beta: checkout --detach")
	if strings.Contains(h.m.message, "changes kept") {
		t.Errorf("spurious note: %q", h.m.message)
	}
}

func TestDetachLiveConfirms(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	h.m.bound.liveWT["/root/alpha-feat"] = true

	h.key(t, "d")
	wantView(t, h, "Detach HEAD of alpha-feat", "Uncommitted changes stay in this worktree.",
		"An agent is running here; it will now be on a detached HEAD.", "[y] confirm")
	h.key(t, "n")
	wantView(t, h, "cancelled")
	h.key(t, "d")
	h.key(t, "esc")
	wantCalls(t, o)

	h.key(t, "d")
	h.key(t, "y")
	wantCalls(t, o, "/root/alpha-feat: checkout --detach")
	wantView(t, h, "uncommitted changes kept")
}

func TestDetachGuards(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	for _, tc := range []struct{ row, want string }{
		{"w:/root/alpha-det", "already detached"},
		{"r:/root/alpha", "select a worktree"},
		{"w:/root/alpha-gone", "press p to prune"},
	} {
		h.selectKey(t, tc.row)
		h.key(t, "d")
		if h.m.dialog != nil {
			t.Errorf("%s: dialog opened", tc.row)
		}
		wantView(t, h, tc.want)
	}
	wantCalls(t, o)
}

func TestBusyIgnoresDuplicates(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}) // not executed yet
	wantView(t, h, "detaching…")
	h.key(t, "d")
	wantView(t, h, "detaching in progress")
	h.settle(t, cmd)
	wantCalls(t, o, "/root/alpha-feat: checkout --detach")
}

func TestRepoBusyBlocksWorktreeActions(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}) // prune check pending
	for _, k := range []string{"d", "l", "c"} {
		h.key(t, k)
		wantView(t, h, "checking prunable in progress")
	}
	if h.m.dialog != nil {
		t.Error("dialog opened while repo busy")
	}
	h.settle(t, cmd)
	wantCalls(t, o, "/root/alpha: worktree prune --dry-run -v")
}

func TestLoadDoesNotReplaceOpenDialog(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: "feat\nmain\n"},
		"worktree prune --dry-run -v":                       {errOut: "Removing worktrees/x: gone\n"},
	})
	h.selectKey(t, "w:/root/alpha-det")
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")}) // branches pending
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "l") // lock prompt opens meanwhile
	lockDlg := h.m.dialog
	h.settle(t, cmd)
	if h.m.dialog != lockDlg {
		t.Fatal("branch load replaced the open dialog")
	}
	wantView(t, h, "press c again")
	if _, busy := h.m.busy["/root/alpha-det"]; busy {
		t.Error("busy not cleared")
	}
	h.key(t, "esc")

	h.selectKey(t, "r:/root/beta")
	_, cmd = h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	h.selectKey(t, "w:/root/alpha-det") // another repo: not blocked by beta's prune check
	h.key(t, "l")
	h.settle(t, cmd)
	if h.m.dialog == nil || h.m.dialog.kind != dlgInput {
		t.Error("prune list replaced the open dialog")
	}
	wantView(t, h, "press p again")
	for _, c := range o.actions() {
		if strings.Contains(c, "prune -v") && !strings.Contains(c, "dry-run") {
			t.Errorf("pruned: %q", c)
		}
	}
}

func TestMessagesSanitised(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.m.setMessage("a\x1b]0;pwn\x07b")
	h.m.setError("c\x1b[31md")
	if strings.ContainsRune(h.m.message, 0x1b) || h.m.message != "c[31md" {
		t.Errorf("message = %q", h.m.message)
	}
}

func TestCheckoutUnknownBranchRejected(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: "feat\nmain\n"},
	})
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "c")
	for _, r := range "foo" {
		h.key(t, string(r))
	}
	h.key(t, "enter")
	wantView(t, h, "no local branch foo")
	for _, c := range o.actions() {
		if strings.Contains(c, ": checkout ") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestCheckoutBranchInUse(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: "feat\nmain\nold\n"},
		// The grep pattern ends with a space, hence the double space.
		"reflog -1 --grep-reflog=checkout: moving from  --format=%gs": {out: "checkout: moving from feat to abcdef0\n"},
		"checkout feat --": {err: &gitx.Error{Args: []string{"checkout", "feat", "--"},
			Stderr: "fatal: 'feat' is already used by worktree at '/root/alpha-feat'"}},
	})
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "c")
	if h.m.dialog == nil || h.m.dialog.kind != dlgInput {
		t.Fatalf("no input dialog:\n%s", h.view())
	}
	if got := h.m.dialog.input.Value(); got != "feat" {
		t.Errorf("prefill = %q, want last branch feat", got)
	}
	wantView(t, h, "Checkout branch in alpha-det", "branches: feat main old")
	h.key(t, "enter")
	wantCalls(t, o,
		"/root/alpha-det: for-each-ref --format=%(refname:short) refs/heads",
		"/root/alpha-det: reflog -1 --grep-reflog=checkout: moving from  --format=%gs",
		"/root/alpha-det: checkout feat --")
	if want := "feat is checked out in alpha-feat; press d there to detach"; h.m.message != want {
		t.Errorf("message = %q, want %q", h.m.message, want)
	}
	if !h.m.msgErr {
		t.Error("message not flagged as error")
	}
}

func TestCheckoutTabComplete(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: "feat\nmain\n"},
	})
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "c")
	if v := h.m.dialog.input.Value(); v != "" {
		t.Errorf("prefill = %q, want empty (no reflog entry)", v)
	}
	h.key(t, "q") // typed into the prompt, does not quit
	if h.quit {
		t.Fatal("q quit while typing")
	}
	h.send(t, tea.KeyMsg{Type: tea.KeyBackspace})
	h.key(t, "m")
	h.key(t, "tab")
	if v := h.m.dialog.input.Value(); v != "main" {
		t.Errorf("after tab = %q", v)
	}
	h.key(t, "enter")
	acts := o.actions()
	if last := acts[len(acts)-1]; last != "/root/alpha-det: checkout main --" {
		t.Errorf("last call %q", last)
	}
	wantView(t, h, "checked out main in alpha-det")
}

func TestCheckoutSameBranchNoop(t *testing.T) {
	h, o := newOpsHarness(t, map[string]opsResp{
		"for-each-ref --format=%(refname:short) refs/heads": {out: "feat\nmain\n"},
	})
	h.selectKey(t, "w:/root/alpha")
	h.key(t, "c")
	for _, r := range "main" {
		h.key(t, string(r))
	}
	h.key(t, "enter")
	wantView(t, h, "already on main")
	for _, c := range o.actions() {
		if strings.Contains(c, ": checkout ") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestLockUnlock(t *testing.T) {
	h, o := newOpsHarness(t, nil)

	h.selectKey(t, "w:/root/alpha")
	h.key(t, "l")
	wantView(t, h, "cannot lock the main worktree")

	h.selectKey(t, "w:/root/alpha-feat") // locked
	h.key(t, "l")
	wantView(t, h, "Unlock alpha-feat?")
	h.key(t, "y")
	wantView(t, h, "unlocked alpha-feat")

	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "l")
	wantView(t, h, "Lock alpha-det — reason (optional):")
	for _, r := range "wip" {
		h.key(t, string(r))
	}
	h.key(t, "enter")
	wantView(t, h, "locked alpha-det")

	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "l")
	h.key(t, "enter") // no reason
	wantCalls(t, o,
		"/root/alpha: worktree unlock /root/alpha-feat",
		"/root/alpha: worktree lock --reason wip /root/alpha-det",
		"/root/beta: worktree lock /root/beta-slow")
}

func TestLockErrorSummarised(t *testing.T) {
	h, _ := newOpsHarness(t, map[string]opsResp{
		"worktree lock /root/alpha-det": {err: &gitx.Error{Args: []string{"worktree", "lock"},
			Stderr: "fatal: one\x1b[31m\nhint: two\nhint: three\nhint: four"}},
	})
	h.selectKey(t, "w:/root/alpha-det")
	h.key(t, "l")
	h.key(t, "enter")
	if got, want := h.m.message, "fatal: one[31m · hint: two · hint: three"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestPrune(t *testing.T) {
	stale := "Removing worktrees/alpha-gone: gitdir file points to non-existent location"
	h, o := newOpsHarness(t, map[string]opsResp{
		"worktree prune --dry-run -v": {errOut: stale + "\n"},
		"worktree prune -v":           {errOut: stale + "\n"},
	})
	h.selectKey(t, "w:/root/alpha-feat") // any row of the repo
	h.key(t, "p")
	wantView(t, h, "Prune 1 stale worktree entry in alpha?", stale)
	h.key(t, "y")
	wantCalls(t, o, "/root/alpha: worktree prune --dry-run -v", "/root/alpha: worktree prune -v")
	wantView(t, h, "pruned 1 worktree entry in alpha")
	if !o.reloaded("/root/alpha") {
		t.Error("not reloaded")
	}
}

func TestPruneNothing(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "r:/root/beta")
	h.key(t, "p")
	if h.m.dialog != nil {
		t.Error("dialog for nothing to prune")
	}
	wantView(t, h, "nothing to prune")
	wantCalls(t, o, "/root/beta: worktree prune --dry-run -v")
}

func TestDialogFitsWidth(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 12})
	h.selectKey(t, "w:/root/alpha-feat")
	h.m.bound.liveWT["/root/alpha-feat"] = true
	h.key(t, "d")
	if h.m.dialog == nil {
		t.Fatal("no dialog")
	}
	lines := strings.Split(h.view(), "\n")
	if len(lines) > 12 {
		t.Errorf("view has %d lines for height 12", len(lines))
	}
	for _, l := range lines {
		if w := len([]rune(l)); w > 80 {
			t.Errorf("line too wide (%d): %q", w, l)
		}
	}
}
