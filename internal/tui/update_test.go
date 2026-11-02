package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const updLabel = "↑ v0.2.0 available · grove update"

func updateHarness(t *testing.T, tag string) *harness {
	t.Helper()
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	h.m.deps.UpdateCheck = func(context.Context) string { return tag }
	h.settle(t, h.m.Init())
	return h
}

func TestUpdateLabelRightAlignedOnHintBar(t *testing.T) {
	h := updateHarness(t, "v0.2.0")
	h.send(t, tea.WindowSizeMsg{Width: 200, Height: 30})
	h.selectKey(t, "w:/root/alpha-feat")
	got := hintBar(t, h)
	if !strings.HasSuffix(got, updLabel) {
		t.Fatalf("hint bar %q does not end with the label", got)
	}
	if ansi.StringWidth(got) != 200 {
		t.Errorf("label not flush right: width %d", ansi.StringWidth(got))
	}
	if !strings.HasPrefix(got, "enter details · o open") {
		t.Errorf("hints missing: %q", got)
	}
}

func TestUpdateLabelWidthHandling(t *testing.T) {
	h := updateHarness(t, "v0.2.0")
	h.selectKey(t, "w:/root/alpha-feat")
	for _, w := range []int{120, 80, 60, 45, 41, 40, 30, 12} {
		h.send(t, tea.WindowSizeMsg{Width: w, Height: 30})
		got := hintBar(t, h)
		if ansi.StringWidth(got) > w {
			t.Errorf("w=%d: %q is %d cells", w, got, ansi.StringWidth(got))
		}
		labelFits := w >= ansi.StringWidth(updLabel)+1+len("? help")
		if strings.Contains(got, updLabel) != labelFits {
			t.Errorf("w=%d: label shown=%v, want %v: %q", w, !labelFits, labelFits, got) // shown != labelFits
		}
		if w >= 12 && !strings.Contains(got, "? help") {
			t.Errorf("w=%d: help hint dropped: %q", w, got)
		}
		if strings.Contains(got, "…") {
			t.Errorf("w=%d: truncated: %q", w, got)
		}
	}
	// the hints yield width to the label, dropping from the end
	h.send(t, tea.WindowSizeMsg{Width: 80, Height: 30})
	got := hintBar(t, h)
	want := "enter details · o open · d detach · ? help"
	if !strings.HasPrefix(got, want+" ") || !strings.HasSuffix(got, " "+updLabel) {
		t.Errorf("w=80: got %q, want %q … %q", got, want, updLabel)
	}
}

func TestUpdateLabelOnStatusLineWhenHintsHidden(t *testing.T) {
	h := updateHarness(t, "v0.2.0")
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 30})
	h.key(t, "/") // filtering hides the hint bar
	lines := strings.Split(h.view(), "\n")
	status := ansi.Strip(lines[len(lines)-1])
	if !strings.HasSuffix(status, updLabel) || ansi.StringWidth(status) != 120 {
		t.Errorf("status line %q: want label flush right", status)
	}
	// too narrow for status text + label: omitted, status untouched
	h.send(t, tea.WindowSizeMsg{Width: 50, Height: 30})
	lines = strings.Split(h.view(), "\n")
	status = ansi.Strip(lines[len(lines)-1])
	if strings.Contains(status, "available") || ansi.StringWidth(status) > 50 {
		t.Errorf("w=50 status %q", status)
	}
}

func TestUpdateLabelAbsent(t *testing.T) {
	for _, tag := range []string{"", "v0.2.0\x1b[2J", "v0.2", "latest", "v0.2.0-rc1"} {
		h := updateHarness(t, tag)
		h.send(t, tea.WindowSizeMsg{Width: 200, Height: 30})
		if v := h.view(); strings.Contains(v, "available") || strings.Contains(v, "grove update") {
			t.Errorf("tag %q: label rendered:\n%s", tag, v)
		}
	}
}

func TestUpdateCheckNotBlockingFirstFrame(t *testing.T) {
	h := newHarness(t, fixtureRepos(), fixtureSources(), nil)
	release := make(chan struct{})
	h.m.deps.UpdateCheck = func(context.Context) string { <-release; return "v0.2.0" }
	cmd := h.m.Init() // must return without running the check
	if v := h.view(); strings.Contains(v, "available") {
		t.Fatal("label before the check finished")
	}
	close(release)
	h.settle(t, cmd)
	h.send(t, tea.WindowSizeMsg{Width: 200, Height: 30})
	if !strings.Contains(h.view(), updLabel) {
		t.Error("label missing after the check")
	}
}
