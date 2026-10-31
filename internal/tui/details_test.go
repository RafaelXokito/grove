package tui

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/sessions"
)

// detailSource is a fakeSource that also re-reads "transcripts" (by Path).
type detailSource struct {
	fakeSource
	details map[string]sessions.Detail
	reads   atomic.Int32
}

func (d *detailSource) Detail(_ context.Context, s sessions.Session) (sessions.Detail, error) {
	d.reads.Add(1)
	if det, ok := d.details[s.Path]; ok {
		return det, nil
	}
	return sessions.Detail{}, errors.New("no such transcript")
}

var longPrompt = "Please refactor the parser so that it handles every edge case we discussed yesterday, " +
	"including nested quotes, escaped newlines and the trailing-comma form. END-OF-PROMPT"

func detailSources() (*detailSource, *detailSource) {
	c := &detailSource{
		fakeSource: fakeSource{agent: sessions.AgentClaude, ss: []sessions.Session{
			{Agent: sessions.AgentClaude, ID: "c1c1c1c1-aaaa-bbbb-full", Cwd: "/root/alpha-feat/sub", Path: "/t/c1.jsonl",
				Title: sessions.CleanTitle(longPrompt, sessions.TitleMaxRunes), LastActivity: now.Add(-3 * time.Minute)},
		}},
		details: map[string]sessions.Detail{"/t/c1.jsonl": {Prompt: longPrompt + "\x1b[31m"}},
	}
	x := &detailSource{
		fakeSource: fakeSource{agent: sessions.AgentCodex, ss: []sessions.Session{
			{Agent: sessions.AgentCodex, ID: "x1x1x1x1-cccc-full", Cwd: "/root/alpha-feat", Path: "/t/x1.jsonl",
				Title: "Fix tests", LastActivity: now.Add(-5 * 24 * time.Hour)},
		}},
		details: map[string]sessions.Detail{"/t/x1.jsonl": {Prompt: "Fix tests", GitBranch: "feat-recorded"}},
	}
	return c, x
}

func newDetailsHarness(t *testing.T) (*harness, *opsRunner, *detailSource) {
	t.Helper()
	c, x := detailSources()
	h := newHarness(t, fixtureRepos(), []sessions.Source{c, x}, nil)
	o := &opsRunner{resp: map[string]opsResp{
		"log -1 --format=%h %s":                   {out: "2222222 Add parser\n"},
		"diff --stat HEAD":                        {out: " parser.go | 12 ++++++------\n 1 file changed, 6 insertions(+), 6 deletions(-)\n"},
		"ls-files --others --exclude-standard -z": {out: "a.txt\x00b.txt\x00"},
	}}
	h.m.deps.Runner = o
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 80})
	return h, o, c
}

func enterMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func TestDetailsWorktree(t *testing.T) {
	h, o, c := newDetailsHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	_, cmd := h.m.Update(enterMsg())
	wantView(t, h, "Worktree alpha-feat", "loading details")
	h.settle(t, cmd)
	wantView(t, h,
		"/root/alpha-feat", "2222222 Add parser", "dirty (3)", "↑2 ↓1",
		"parser.go | 12", "1 file changed", "untracked files: 2",
		"END-OF-PROMPT", // full prompt, beyond the 80-rune title
		"c1c1c1c1-aaaa-bbbb-full", "x1x1x1x1-cccc-full",
		"claude --resume c1c1c1c1-aaaa-bbbb-full", "codex resume x1x1x1x1-cccc-full",
		"feat-recorded", "/root/alpha-feat/sub", "Sessions (2)")
	if strings.Contains(h.view(), "\x1b[31m") {
		t.Error("prompt not sanitised")
	}
	if c.reads.Load() != 1 {
		t.Errorf("claude transcript reads = %d", c.reads.Load())
	}
	wantCalls(t, o,
		"/root/alpha-feat: log -1 --format=%h %s",
		"/root/alpha-feat: diff --stat HEAD",
		"/root/alpha-feat: ls-files --others --exclude-standard -z")
	// Global keys do not fire while the pane is open; enter closes it.
	h.key(t, "x")
	h.key(t, "q")
	if h.quit || h.m.overlay != nil {
		t.Fatalf("q: quit=%v overlay=%v", h.quit, h.m.overlay)
	}
	h.key(t, "enter")
	h.key(t, "esc")
	if h.m.dialog != nil || h.m.overlay != nil {
		t.Error("pane/dialog left open")
	}
}

func TestDetailsSessionRow(t *testing.T) {
	h, o, _ := newDetailsHarness(t)
	h.selectKey(t, "s:claude:c1c1c1c1-aaaa-bbbb-full")
	h.key(t, "enter")
	wantView(t, h, "Session claude · c1c1c1c1-aaaa-bbbb-full", "END-OF-PROMPT",
		"claude --resume c1c1c1c1-aaaa-bbbb-full", "not running", "3m ago")
	if strings.Contains(h.view(), "x1x1x1x1") {
		t.Error("session pane lists other sessions")
	}
	wantCalls(t, o) // no git for a session pane
	h.key(t, "esc")
	if h.m.overlay != nil {
		t.Error("esc did not close")
	}
}

func TestDetailsScrolls(t *testing.T) {
	h, _, _ := newDetailsHarness(t)
	h.send(t, tea.WindowSizeMsg{Width: 60, Height: 10})
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "enter")
	n := len(h.m.overlayLines(60))
	if n <= h.m.bodyHeight() {
		t.Fatalf("pane too short to scroll: %d", n)
	}
	h.key(t, "end")
	if want := n - h.m.bodyHeight(); h.m.overlay.offset != want {
		t.Errorf("offset = %d, want %d", h.m.overlay.offset, want)
	}
	wantView(t, h, "codex resume")
	for _, l := range strings.Split(h.view(), "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line wider than 60: %q", l)
		}
	}
	h.key(t, "home")
	if h.m.overlay.offset != 0 {
		t.Errorf("home offset = %d", h.m.overlay.offset)
	}
}

func TestDetailsRepoRowToggles(t *testing.T) {
	h, _, _ := newDetailsHarness(t)
	h.selectKey(t, "r:/root/alpha")
	h.key(t, "enter")
	if h.m.overlay != nil || !h.m.collapsed["r:/root/alpha"] {
		t.Error("enter on repo row should toggle collapse")
	}
}

func TestOpenCommandPrecedence(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for _, tc := range []struct {
		env      map[string]string
		what     string
		wantArgs []string
	}{
		{map[string]string{"GROVE_OPEN": "tmux new-window", "EDITOR": "vim", "SHELL": "/bin/zsh"}, "$GROVE_OPEN",
			[]string{"/bin/sh", "-c", "tmux new-window", "grove-open", "/w"}},
		{map[string]string{"EDITOR": "code --wait", "SHELL": "/bin/zsh"}, "$EDITOR",
			[]string{"/bin/sh", "-c", `code --wait "$@"`, "code --wait", "/w"}},
		{map[string]string{"SHELL": "/bin/zsh"}, "$SHELL", []string{"/bin/zsh"}},
		{map[string]string{"EDITOR": "  "}, "$SHELL", []string{"/bin/sh"}},
	} {
		c, what := openCommand(env(tc.env), []string{"FOO=bar"}, "/w")
		if what != tc.what || !reflect.DeepEqual(c.Args, tc.wantArgs) || c.Dir != "/w" {
			t.Errorf("%v: got %s %q dir=%s", tc.env, what, c.Args, c.Dir)
		}
		if !reflect.DeepEqual(c.Env, []string{"FOO=bar", "GROVE_WORKTREE=/w"}) {
			t.Errorf("%v: GROVE_WORKTREE missing", tc.env)
		}
	}
}

func TestOpenUsesExecProcess(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	var got []*exec.Cmd
	var fail error
	h.m.deps.Getenv = func(k string) string { return map[string]string{"EDITOR": "vim"}[k] }
	h.m.deps.Exec = func(c *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		got = append(got, c)
		return func() tea.Msg { return cb(fail) }
	}
	h.selectKey(t, "r:/root/alpha")
	h.key(t, "o")
	wantView(t, h, "select a worktree")
	h.selectKey(t, "w:/root/alpha-gone")
	h.key(t, "o")
	if len(got) != 0 {
		t.Fatalf("exec on repo/prunable row: %d", len(got))
	}
	h.selectKey(t, "s:claude:c1c1c1c1-aaaa-bbbb") // session row → its worktree
	h.key(t, "o")
	if len(got) != 1 || got[0].Dir != "/root/alpha-feat" || got[0].Args[len(got[0].Args)-1] != "/root/alpha-feat" {
		t.Fatalf("exec cmds: %+v", got)
	}
	wantView(t, h, "back from $EDITOR in alpha-feat")
	if !o.reloaded("/root/alpha") {
		t.Error("repo not reloaded after open")
	}
	fail = errors.New("exit status 1")
	h.key(t, "o")
	wantView(t, h, "$EDITOR in alpha-feat: exit status 1")
}

func TestHelpListsAllBindings(t *testing.T) {
	k := defaultKeys()
	listed := map[string]bool{}
	for _, b := range k.all() {
		listed[strings.Join(b.Keys(), ",")] = true
	}
	v := reflect.ValueOf(k)
	for i := 0; i < v.NumField(); i++ {
		b := v.Field(i).Interface().(interface{ Keys() []string })
		if !listed[strings.Join(b.Keys(), ",")] {
			t.Errorf("binding %s missing from keyMap.all", v.Type().Field(i).Name)
		}
	}

	h := newHarness(t, fixtureRepos(), nil, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 100, Height: 60})
	h.key(t, "?")
	for _, b := range k.all() {
		wantView(t, h, b.Help().Key, b.Help().Desc)
	}
	h.key(t, "d") // global keys do not fire under help
	if h.m.dialog != nil || len(h.m.busy) > 0 {
		t.Error("d fired under help")
	}
	h.key(t, "?")
	if h.m.overlay != nil {
		t.Error("? did not close help")
	}
}

func TestOpenShellExitStatusIsSuccess(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.m.deps.Getenv = func(k string) string { return map[string]string{"SHELL": "/bin/sh"}[k] }
	h.m.deps.Environ = func() []string { return nil }
	exitErr := exec.Command("/bin/sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(exitErr, &ee) {
		t.Fatalf("no ExitError: %v", exitErr)
	}
	h.m.deps.Exec = func(_ *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return cb(exitErr) }
	}
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "o")
	wantView(t, h, "back from $SHELL in alpha-feat")
	if h.m.msgErr {
		t.Error("shell exit status shown as error")
	}
}

func TestDetailsBoundedAndUnbornHead(t *testing.T) {
	h, o, _ := newDetailsHarness(t)
	o.resp["ls-files --others --exclude-standard -z"] = opsResp{out: strings.Repeat("f\x00", maxUntracked+5)}
	o.resp["diff --stat HEAD"] = opsResp{err: &gitx.Error{Args: []string{"diff"}, Stderr: "fatal: bad revision 'HEAD'"}}
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "enter")
	wantView(t, h, "untracked files: 10000+", "fatal: bad revision")
	if strings.Contains(h.view(), "no changes to tracked files") {
		t.Error("unborn HEAD claims no changes")
	}
}

func TestDetailsSessionCountIsSnapshot(t *testing.T) {
	h, _, _ := newDetailsHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "enter")
	h.m.bound.byWT["/root/alpha-feat"] = nil // a rebind after open
	wantView(t, h, "Sessions (2)", "x1x1x1x1-cccc-full")
}

func TestDetailsScrollClampsAfterResize(t *testing.T) {
	h, _, _ := newDetailsHarness(t)
	h.send(t, tea.WindowSizeMsg{Width: 60, Height: 10})
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "enter")
	h.key(t, "end")
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 30})
	n := len(h.m.overlayLines(120))
	h.key(t, "up")
	if want := max(n-h.m.bodyHeight()-1, 0); h.m.overlay.offset != want {
		t.Errorf("offset = %d, want %d (n=%d h=%d)", h.m.overlay.offset, want, n, h.m.bodyHeight())
	}
}
