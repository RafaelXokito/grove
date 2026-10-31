package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
	"github.com/RafaelXokito/grove/internal/sessions/live"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// fakeSource is an in-memory sessions.Source.
type fakeSource struct {
	agent   sessions.Agent
	ss      []sessions.Session
	skipped int
	calls   atomic.Int32
}

func (f *fakeSource) Agent() sessions.Agent { return f.agent }
func (f *fakeSource) List(context.Context, time.Time) ([]sessions.Session, int, error) {
	f.calls.Add(1)
	return f.ss, f.skipped, nil
}

type fakeProcs struct {
	mu    sync.Mutex
	procs []sessions.LiveProc
	err   error
	calls int
}

func (f *fakeProcs) List(context.Context) ([]sessions.LiveProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.procs, f.err
}

// fakeScan returns pre-built events on a closed buffered channel and counts
// invocations (each one stands for a full git rescan).
type fakeScan struct {
	events []scan.Event
	calls  atomic.Int32
}

func (f *fakeScan) Scan(context.Context, scan.Options) <-chan scan.Event {
	f.calls.Add(1)
	ch := make(chan scan.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch
}

type harness struct {
	m     *Model
	scan  *fakeScan
	ticks []time.Duration
	quit  bool
}

// newHarness builds a model with fakes. Ticks are recorded, never fired, so
// settle runs every other command synchronously and deterministically.
func newHarness(t *testing.T, repos []*scan.Repo, srcs []sessions.Source, procs sessions.ProcLister) *harness {
	t.Helper()
	h := &harness{scan: &fakeScan{}}
	for _, r := range repos {
		h.scan.events = append(h.scan.events, scan.Event{Repo: r})
	}
	h.m = New(Deps{
		Roots:       []string{"/root"},
		Home:        "/home/u",
		Sources:     srcs,
		Procs:       procs,
		HistoryDays: 30,
		Scan:        h.scan.Scan,
		Now:         func() time.Time { return now },
		Tick: func(d time.Duration, _ func(time.Time) tea.Msg) tea.Cmd {
			h.ticks = append(h.ticks, d)
			return nil
		},
	})
	t.Cleanup(h.m.Close)
	return h
}

// settle executes cmd and feeds every resulting message back into Update
// until no commands remain.
func (h *harness) settle(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 10000 {
			t.Fatal("settle: too many commands")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch msg := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
			h.quit = true
		default:
			_, next := h.m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func (h *harness) send(t *testing.T, msg tea.Msg) {
	t.Helper()
	_, cmd := h.m.Update(msg)
	h.settle(t, cmd)
}

func (h *harness) key(t *testing.T, k string) {
	t.Helper()
	var msg tea.KeyMsg
	switch k {
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	h.send(t, msg)
}

func (h *harness) view() string { return h.m.View() }

func (h *harness) cursorKey() string {
	r, _ := h.m.current()
	return r.key
}

func (h *harness) selectKey(t *testing.T, key string) {
	t.Helper()
	for i, r := range h.m.rows {
		if r.key == key {
			h.m.cursor = i
			h.m.cursorMoved = true
			return
		}
	}
	t.Fatalf("no row %q; rows: %v", key, h.rowKeys())
}

func (h *harness) rowKeys() []string {
	var ks []string
	for _, r := range h.m.rows {
		ks = append(ks, r.key)
	}
	return ks
}

func fixtureRepos() []*scan.Repo {
	return []*scan.Repo{
		{
			Name: "alpha", Path: "/root/alpha",
			Worktrees: []gitx.Worktree{
				{Path: "/root/alpha", HEAD: "1111111aaaaaaa", Branch: "main", IsMain: true, Status: &gitx.Status{}},
				{Path: "/root/alpha-feat", HEAD: "2222222bbbbbbb", Branch: "feat", Locked: true,
					Status: &gitx.Status{Changed: 3, HasUpstream: true, Ahead: 2, Behind: 1}},
				{Path: "/root/alpha-det", HEAD: "abcdef0123456789",
					Status: &gitx.Status{}},
				{Path: "/root/alpha-gone", HEAD: "3333333ccccccc", Branch: "old", Prunable: true, PruneReason: "gitdir file points to non-existent location"},
			},
		},
		{
			Name: "beta", Path: "/root/beta",
			Worktrees: []gitx.Worktree{
				{Path: "/root/beta", HEAD: "4444444ddddddd", Branch: "trunk", IsMain: true, Status: &gitx.Status{}},
				{Path: "/root/beta-slow", HEAD: "5555555eeeeeee", Branch: "slow"},
			},
			Err: errors.Join(fmt.Errorf("status /root/beta-slow: timed out after 10s: %w", context.DeadlineExceeded)),
		},
	}
}

func fixtureSources() []sessions.Source {
	return []sessions.Source{
		&fakeSource{agent: sessions.AgentClaude, ss: []sessions.Session{
			{Agent: sessions.AgentClaude, ID: "c1c1c1c1-aaaa-bbbb", Cwd: "/root/alpha-feat/sub", Title: "Refactor the parser", LastActivity: now.Add(-3 * time.Minute)},
			{Agent: sessions.AgentClaude, ID: "c2c2c2c2-aaaa-bbbb", Cwd: "/root/alpha", Title: "Write docs", LastActivity: now.Add(-2 * time.Hour)},
			{Agent: sessions.AgentClaude, ID: "c3c3c3c3-unbound", Cwd: "/root", Title: "Launched in root", LastActivity: now.Add(-time.Hour)},
		}},
		&fakeSource{agent: sessions.AgentCodex, skipped: 2, ss: []sessions.Session{
			{Agent: sessions.AgentCodex, ID: "x1x1x1x1-cccc", Cwd: "/root/alpha-feat", Title: "Fix tests", LastActivity: now.Add(-5 * 24 * time.Hour)},
		}},
	}
}

func TestFirstViewShowsSpinner(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	v := h.view()
	if !strings.Contains(v, spinFrames[0]) || !strings.Contains(v, "Loading") {
		t.Fatalf("first view lacks spinner:\n%s", v)
	}
}

func TestTreeRendering(t *testing.T) {
	procs := &fakeProcs{procs: []sessions.LiveProc{{PID: 10, Agent: sessions.AgentClaude, SessionID: "c1c1c1c1-aaaa-bbbb", Busy: true}}}
	h := newHarness(t, fixtureRepos(), fixtureSources(), procs)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 40})
	v := h.view()

	for _, want := range []string{
		"alpha", "beta",
		"alpha-feat  feat", "dirty (3)", "↑2 ↓1", "[locked]",
		"detached @ abcdef0",
		"alpha  ★  main  clean",
		"missing", "[prunable]",
		"! status /root/beta-slow: timed out after 10s",
		"claude c1c1c1c1  Refactor the parser", "3m ago", "● busy",
		"claude c2c2c2c2", "2h ago",
		"codex  x1x1x1x1", "5d ago",
		"codex: 2 compressed sessions skipped",
		"1 live",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q\n%s", want, v)
		}
	}
	if strings.Contains(v, "c3c3c3c3") {
		t.Errorf("unbound session rendered:\n%s", v)
	}
	if got := len(h.m.bound.unbound); got != 1 {
		t.Errorf("unbound = %d, want 1", got)
	}
	// Worktree with a live session gets the live dot.
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "alpha-feat  feat") && !strings.Contains(line, "●") {
			t.Errorf("live worktree lacks ●: %q", line)
		}
	}
	// Sessions sorted newest first under alpha-feat: claude (3m) before codex (5d).
	if strings.Index(v, "c1c1c1c1") > strings.Index(v, "x1x1x1x1") {
		t.Errorf("sessions not sorted by last activity")
	}
	if h.m.loading() {
		t.Errorf("still loading after settle")
	}
}

func TestIdleMarker(t *testing.T) {
	procs := &fakeProcs{procs: []sessions.LiveProc{{PID: 10, Agent: sessions.AgentClaude, SessionID: "c2c2c2c2-aaaa-bbbb"}}}
	h := newHarness(t, fixtureRepos(), fixtureSources(), procs)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 40})
	if v := h.view(); !strings.Contains(v, "○ idle") {
		t.Errorf("missing idle marker:\n%s", v)
	}
}

func TestNavigationCollapseExpand(t *testing.T) {
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 40})

	if got := h.cursorKey(); got != "r:/root/alpha" {
		t.Fatalf("initial cursor = %q", got)
	}
	h.key(t, "down")
	if got := h.cursorKey(); got != "w:/root/alpha" {
		t.Fatalf("after down = %q", got)
	}
	h.key(t, "j")
	h.key(t, "j")
	if got := h.cursorKey(); got != "w:/root/alpha-feat" {
		t.Fatalf("after j j = %q (rows %v)", got, h.rowKeys())
	}
	h.key(t, "k")
	if got := h.cursorKey(); got != "s:claude:c2c2c2c2-aaaa-bbbb" {
		t.Fatalf("after k = %q", got)
	}
	// ← on a leaf moves to its parent.
	h.key(t, "left")
	if got := h.cursorKey(); got != "w:/root/alpha" {
		t.Fatalf("left on leaf = %q", got)
	}
	// ← on an expanded worktree collapses it.
	h.key(t, "left")
	if !h.m.collapsed["w:/root/alpha"] || strings.Contains(h.view(), "c2c2c2c2") {
		t.Fatalf("worktree not collapsed:\n%s", h.view())
	}
	if !strings.Contains(h.view(), "▸ alpha  ★") {
		t.Errorf("collapsed marker missing:\n%s", h.view())
	}
	// ← on a collapsed node moves to its parent.
	h.key(t, "left")
	if got := h.cursorKey(); got != "r:/root/alpha" {
		t.Fatalf("left on collapsed = %q", got)
	}
	// ← collapses the repo; its worktrees disappear.
	h.key(t, "left")
	if strings.Contains(h.view(), "alpha-feat") {
		t.Fatalf("repo not collapsed:\n%s", h.view())
	}
	// → expands it again.
	h.key(t, "right")
	if !strings.Contains(h.view(), "alpha-feat") {
		t.Fatalf("repo not expanded:\n%s", h.view())
	}
	// space toggles.
	h.key(t, "space")
	if strings.Contains(h.view(), "alpha-feat") {
		t.Fatalf("space did not collapse")
	}
	h.key(t, "space")
	if !strings.Contains(h.view(), "alpha-feat") {
		t.Fatalf("space did not expand")
	}
	// `h` is unbound: no movement.
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "h")
	if got := h.cursorKey(); got != "w:/root/alpha-feat" {
		t.Errorf("h moved cursor to %q", got)
	}
	// up at top stays at top.
	h.m.cursor = 0
	h.key(t, "up")
	if h.m.cursor != 0 {
		t.Errorf("cursor = %d", h.m.cursor)
	}
}

func TestMoreRow(t *testing.T) {
	var ss []sessions.Session
	for i := range 8 {
		ss = append(ss, sessions.Session{Agent: sessions.AgentClaude, ID: fmt.Sprintf("s%07d-x", i), Cwd: "/root/alpha", Title: "t", LastActivity: now.Add(-time.Duration(i) * time.Minute)})
	}
	h := newHarness(t, fixtureRepos(), []sessions.Source{&fakeSource{agent: sessions.AgentClaude, ss: ss}}, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 100, Height: 60})
	v := h.view()
	if !strings.Contains(v, "+3 more") || strings.Contains(v, "s0000007") {
		t.Fatalf("expected +3 more:\n%s", v)
	}
	h.selectKey(t, "m:/root/alpha")
	h.key(t, "enter")
	v = h.view()
	if strings.Contains(v, "more") || !strings.Contains(v, "s0000007") {
		t.Fatalf("enter did not expand more:\n%s", v)
	}
}

func TestNoWrapAt80Columns(t *testing.T) {
	repos := fixtureRepos()
	repos[0].Worktrees[1].Path = "/root/" + strings.Repeat("very-long-worktree-name-", 6)
	srcs := fixtureSources()
	srcs[0].(*fakeSource).ss[0].Title = strings.Repeat("a very long title that goes on ", 5)
	srcs[0].(*fakeSource).ss[0].Cwd = repos[0].Worktrees[1].Path
	procs := &fakeProcs{procs: []sessions.LiveProc{{PID: 1, Agent: sessions.AgentClaude, SessionID: "c1c1c1c1-aaaa-bbbb", Busy: true}}, err: &live.UnavailableError{Err: errors.New("no ps")}}
	h := newHarness(t, repos, srcs, procs)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 30})
	v := h.view()
	for i, line := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("line %d is %d cols: %q", i, w, line)
		}
	}
	if !strings.Contains(v, "…") {
		t.Errorf("expected truncation ellipsis:\n%s", v)
	}
	// Title truncated but time and busy marker kept.
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "c1c1c1c1") && (!strings.Contains(line, "3m ago") || !strings.Contains(line, "● busy")) {
			t.Errorf("session row lost suffix: %q", line)
		}
	}
	h.send(t, tea.WindowSizeMsg{Width: 200, Height: 30})
	if !strings.Contains(h.view(), "process detection unavailable") {
		t.Errorf("missing unavailable note:\n%s", h.view())
	}
}

func TestLiveTickDoesNotRescan(t *testing.T) {
	procs := &fakeProcs{}
	src := &fakeSource{agent: sessions.AgentClaude, ss: fixtureSources()[0].(*fakeSource).ss}
	h := newHarness(t, fixtureRepos(), []sessions.Source{src}, procs)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(h.view(), "● busy") {
		t.Fatal("busy before any live proc")
	}
	if len(h.ticks) == 0 || h.ticks[len(h.ticks)-1] != DefaultLiveInterval {
		t.Fatalf("live tick not scheduled at 5s: %v", h.ticks)
	}

	procs.mu.Lock()
	procs.procs = []sessions.LiveProc{{PID: 7, Agent: sessions.AgentClaude, SessionID: "c1c1c1c1-aaaa-bbbb", Busy: true}}
	procs.mu.Unlock()
	h.send(t, liveTickMsg{})

	if !strings.Contains(h.view(), "● busy") {
		t.Errorf("live tick did not apply:\n%s", h.view())
	}
	if n := h.scan.calls.Load(); n != 1 {
		t.Errorf("scan calls = %d, want 1 (live tick must not rescan git)", n)
	}
	if n := src.calls.Load(); n != 1 {
		t.Errorf("session source calls = %d, want 1", n)
	}
	if procs.calls != 2 {
		t.Errorf("proc lister calls = %d, want 2", procs.calls)
	}
}

func TestRefreshRescansAndKeepsCursor(t *testing.T) {
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.settle(t, h.m.Init())
	h.selectKey(t, "w:/root/alpha-det")
	// The next scan drops beta.
	h.scan.events = h.scan.events[:1]
	h.key(t, "r")
	if n := h.scan.calls.Load(); n != 2 {
		t.Fatalf("scan calls = %d", n)
	}
	if got := h.cursorKey(); got != "w:/root/alpha-det" {
		t.Errorf("cursor = %q", got)
	}
	if strings.Contains(h.view(), "beta") {
		t.Errorf("removed repo still shown:\n%s", h.view())
	}
}

func TestStaleScanEventsIgnored(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	h.settle(t, h.m.Init())
	stale := make(chan scan.Event)
	_, cmd := h.m.Update(scanEventMsg{gen: h.m.gen - 1, ev: scan.Event{Repo: fixtureRepos()[0]}, ch: stale})
	if cmd != nil || len(h.m.repos) != 0 {
		t.Errorf("stale event applied or re-armed")
	}
}

func TestDiscoveryErrorCounted(t *testing.T) {
	h := newHarness(t, fixtureRepos()[:1], nil, nil)
	h.scan.events = append(h.scan.events, scan.Event{Err: &scan.DirError{Path: "/root/x", Err: os.ErrPermission}})
	h.settle(t, h.m.Init())
	if v := h.view(); !strings.Contains(v, "1 error") {
		t.Errorf("error not counted:\n%s", v)
	}
}

func TestScrolling(t *testing.T) {
	var repos []*scan.Repo
	for i := range 30 {
		p := fmt.Sprintf("/root/r%02d", i)
		repos = append(repos, &scan.Repo{Name: fmt.Sprintf("r%02d", i), Path: p,
			Worktrees: []gitx.Worktree{{Path: p, Branch: "main", IsMain: true, HEAD: "abc1234567", Status: &gitx.Status{}}}})
	}
	h := newHarness(t, repos, nil, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 10})
	if got := len(strings.Split(h.view(), "\n")); got > 10 {
		t.Fatalf("view has %d lines for height 10", got)
	}
	for range 25 {
		h.key(t, "j")
	}
	v := h.view()
	if !strings.Contains(v, h.m.rows[h.m.cursor].repo.Name) || strings.Contains(v, "r00 ") {
		t.Errorf("cursor row not visible after scrolling:\n%s", v)
	}
	if got := len(strings.Split(v, "\n")); got > 10 {
		t.Errorf("view has %d lines for height 10", got)
	}
}

func TestQuitCancels(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	h.settle(t, h.m.Init())
	h.key(t, "q")
	if !h.quit || h.m.ctx.Err() == nil {
		t.Errorf("quit=%v ctxErr=%v", h.quit, h.m.ctx.Err())
	}
}

func TestRelTime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:   "just now",
		3 * time.Minute:    "3m ago",
		2 * time.Hour:      "2h ago",
		5 * 24 * time.Hour: "5d ago",
	} {
		if got := relTime(d); got != want {
			t.Errorf("relTime(%v) = %q, want %q", d, got, want)
		}
	}
}

type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{dir}, args...))
	if len(args) > 1 && args[0] == "worktree" {
		return []byte("worktree /root/alpha\x00HEAD 1111111aaaaaaa\x00branch refs/heads/renamed\x00\x00"), nil
	}
	return nil, errors.New("unexpected")
}

func TestReloadRepoKeepsName(t *testing.T) {
	h := newHarness(t, fixtureRepos(), nil, nil)
	fr := &fakeRunner{}
	h.m.deps.Runner = fr
	h.settle(t, h.m.Init())
	h.selectKey(t, "w:/root/alpha-feat")
	h.settle(t, h.m.reloadRepo("/root/alpha"))
	if len(fr.calls) == 0 {
		t.Fatal("no git calls")
	}
	if n := h.scan.calls.Load(); n != 1 {
		t.Errorf("reload triggered a full scan")
	}
	v := h.view()
	if !strings.Contains(v, "renamed") || strings.Contains(v, "alpha-feat") {
		t.Errorf("repo not reloaded:\n%s", v)
	}
	if h.m.repos[0].Name != "alpha" {
		t.Errorf("name = %q", h.m.repos[0].Name)
	}
	// The selected worktree vanished: cursor falls back to its repo.
	if got := h.cursorKey(); got != "r:/root/alpha" {
		t.Errorf("cursor = %q, want repo fallback", got)
	}
}

func TestCursorFallsBackToWorktree(t *testing.T) {
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.settle(t, h.m.Init())
	h.selectKey(t, "s:codex:x1x1x1x1-cccc")
	// The session disappears on rescan; its worktree stays.
	h.m.deps.Sources = []sessions.Source{&fakeSource{agent: sessions.AgentClaude}}
	h.key(t, "r")
	if got := h.cursorKey(); got != "w:/root/alpha-feat" {
		t.Errorf("cursor = %q", got)
	}
	if strings.Contains(h.view(), "refreshing") {
		t.Errorf("refreshing message not cleared")
	}
}

func TestCursorStaysOnTopWhileStreaming(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	h.settle(t, h.m.Init())
	repos := fixtureRepos()
	gen := h.m.gen
	ch := make(chan scan.Event)
	h.m.Update(scanEventMsg{gen: gen, ev: scan.Event{Repo: repos[1]}, ch: ch}) // beta first
	h.m.Update(scanEventMsg{gen: gen, ev: scan.Event{Repo: repos[0]}, ch: ch}) // alpha sorts above
	if got := h.cursorKey(); got != "r:/root/alpha" {
		t.Errorf("cursor = %q, want top row", got)
	}
}

func TestSelection(t *testing.T) {
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.settle(t, h.m.Init())
	for _, k := range []string{"s:claude:c1c1c1c1-aaaa-bbbb", "ew:/root/beta-slow:0", "w:/root/alpha-feat"} {
		h.selectKey(t, k)
		r, wt := h.m.selection()
		if r == nil || wt == nil || wt.Path != h.m.rows[h.m.cursor].wtPath {
			t.Errorf("%s: selection = %v, %v", k, r, wt)
		}
	}
	h.selectKey(t, "r:/root/beta")
	if r, wt := h.m.selection(); r == nil || r.Path != "/root/beta" || wt != nil {
		t.Errorf("repo selection = %v, %v", r, wt)
	}
}

func TestSanitize(t *testing.T) {
	repos := fixtureRepos()
	repos[0].Worktrees[1].Branch = "fe\x1b]0;pwn\x07at"
	srcs := fixtureSources()
	srcs[0].(*fakeSource).ss[0].Title = "evil\x1b[2Jtitle\u009b31m"
	h := newHarness(t, repos, srcs, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 40})
	v := h.view()
	if strings.ContainsAny(v, "\x1b\x07\u009b") {
		t.Errorf("control characters rendered: %q", v)
	}
	if !strings.Contains(v, "evil[2Jtitle31m") || !strings.Contains(v, "fe]0;pwnat") {
		t.Errorf("sanitised text missing:\n%s", v)
	}
}

func TestLongPathKeepsFlags(t *testing.T) {
	repos := fixtureRepos()
	repos[0].Worktrees[1].Path = "/root/" + strings.Repeat("very-long-worktree-name-", 6) + "end"
	h := newHarness(t, repos, nil, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 30})
	found := false
	for _, line := range strings.Split(h.view(), "\n") {
		if strings.Contains(line, "very-long") {
			found = true
			if !strings.Contains(line, "…") || !strings.Contains(line, "end") || !strings.Contains(line, "[locked]") || !strings.Contains(line, "dirty (3)") {
				t.Errorf("long path row lost flags: %q", line)
			}
		}
	}
	if !found {
		t.Fatal("row not found")
	}
}

func TestDisplayCanonicalHome(t *testing.T) {
	home := t.TempDir() // on macOS under /var -> /private/var
	canon := pathx.Canonical(home)
	h := newHarness(t, nil, nil, nil)
	h.m.deps.Home = home
	h.m.homeCanon = canon
	if got := h.m.display(canon + "/proj/x"); got != "~/proj/x" {
		t.Errorf("display = %q", got)
	}
}
