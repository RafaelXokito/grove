package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func hintHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 200, Height: 30})
	return h
}

// hintBar returns the rendered hint bar: the line above the status line.
func hintBar(t *testing.T, h *harness) string {
	t.Helper()
	lines := strings.Split(h.view(), "\n")
	if len(lines) < 2 {
		t.Fatalf("view too short: %q", h.view())
	}
	return ansi.Strip(lines[len(lines)-2])
}

func TestHintBarPerRow(t *testing.T) {
	cases := []struct {
		row  string
		want string
	}{
		{"r:/root/alpha", "←/→ fold · n new · p prune · / filter · r refresh · ? help · q quit"},
		// primary worktree on a branch: no lock, no remove
		{"w:/root/alpha", "enter details · o open · d detach · c checkout · n new · / filter · ? help · q quit"},
		// locked worktree on a branch
		{"w:/root/alpha-feat", "enter details · o open · d detach · c checkout · n new · l unlock · x remove · / filter · ? help · q quit"},
		// detached: no d
		{"w:/root/alpha-det", "enter details · o open · c checkout · n new · l lock · x remove · / filter · ? help · q quit"},
		// missing worktree: only prune applies
		{"w:/root/alpha-gone", "enter details · n new · p prune · / filter · ? help · q quit"},
		{"s:claude:c1c1c1c1-aaaa-bbbb", "enter details · ? help · q quit"},
	}
	for _, c := range cases {
		t.Run(c.row, func(t *testing.T) {
			h := hintHarness(t)
			h.selectKey(t, c.row)
			if got := hintBar(t, h); got != c.want {
				t.Errorf("hint bar\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestHintBarNoRows(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 100, Height: 20})
	if got, want := hintBar(t, h), "r refresh · ? help · q quit"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHintBarBusyWorktree(t *testing.T) {
	h := hintHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	h.m.busy["/root/alpha-feat"] = "detaching"
	got := hintBar(t, h)
	for _, k := range []string{"o open", "d detach", "c checkout", "l unlock", "x remove"} {
		if strings.Contains(got, k) {
			t.Errorf("busy worktree hint %q lists %q", got, k)
		}
	}
}

func TestHintBarHiddenInModes(t *testing.T) {
	h := hintHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "/")
	if v := h.view(); strings.Contains(v, "o open") {
		t.Errorf("hint bar shown while filtering:\n%s", v)
	}
	h.key(t, "esc")
	h.key(t, "?")
	if v := h.view(); strings.Contains(v, "o open") {
		t.Errorf("hint bar shown in help pane:\n%s", v)
	}
	h.key(t, "esc")
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "l") // locked: unlock confirmation
	if h.m.dialog == nil {
		t.Fatal("no dialog")
	}
	if v := h.view(); strings.Contains(v, "o open") || !strings.Contains(v, "[y] confirm") {
		t.Errorf("dialog mode view:\n%s", v)
	}
}

func TestHintBarNarrowWidth(t *testing.T) {
	h := hintHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	for _, w := range []int{80, 60, 40, 25, 12, 6} {
		h.send(t, tea.WindowSizeMsg{Width: w, Height: 30})
		got := hintBar(t, h)
		if ansi.StringWidth(got) > w {
			t.Errorf("w=%d: %q is %d cells wide", w, got, ansi.StringWidth(got))
		}
		if w >= 12 {
			if !strings.HasSuffix(got, "? help") || strings.Contains(got, "…") {
				t.Errorf("w=%d: want whole entries ending in \"? help\", got %q", w, got)
			}
			if !strings.HasPrefix(got, "enter details") && w >= 25 {
				t.Errorf("w=%d: highest-priority entry dropped: %q", w, got)
			}
		}
	}
	h.send(t, tea.WindowSizeMsg{Width: 40, Height: 30})
	if got, want := hintBar(t, h), "enter details · o open · ? help"; got != want {
		t.Errorf("w=40: got %q, want %q", got, want)
	}
}

func TestViewNeverExceedsHeight(t *testing.T) {
	h := hintHarness(t)
	h.selectKey(t, "w:/root/alpha-feat")
	check := func(what string) {
		t.Helper()
		v := h.view()
		if n := strings.Count(v, "\n") + 1; n > h.m.height {
			t.Errorf("%s: %d lines at height %d:\n%s", what, n, h.m.height, v)
		}
	}
	for _, ht := range []int{4, 5, 6, 8, 12, 24} {
		for _, w := range []int{20, 40, 80} {
			h.send(t, tea.WindowSizeMsg{Width: w, Height: ht})
			check("tree")
			if ht < 8 {
				// header + up to maxMessageLines + hint + status leave no
				// room below 8 lines; that predates the hint bar.
				continue
			}
			h.m.setError("a long error message that wraps over several lines in a narrow terminal so the footer grows")
			h.m.ensureVisible()
			check("message")
			h.m.setMessage("")
			h.m.ensureVisible()
		}
	}
	// the cursor row stays visible with the hint bar taking a line
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 6})
	h.selectKey(t, "w:/root/beta-slow")
	h.m.ensureVisible()
	if v := h.view(); !strings.Contains(v, "beta-slow") {
		t.Errorf("cursor row not visible:\n%s", v)
	}
}

func TestHintBarHiddenWhenTooShort(t *testing.T) {
	h := hintHarness(t)
	h.send(t, tea.WindowSizeMsg{Width: 200, Height: 3}) // header, 1 row, status
	if h.m.hintsVisible() {
		t.Error("hint bar shown at height 3")
	}
	if v := h.view(); !strings.Contains(v, "? help") {
		t.Errorf("no key hint anywhere at height 3:\n%s", v)
	}
}
