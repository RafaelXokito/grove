package tui

import (
	"strings"
	"testing"
)

func wantRows(t *testing.T, h *harness, want ...string) {
	t.Helper()
	if got := strings.Join(h.rowKeys(), " "); got != strings.Join(want, " ") {
		t.Errorf("rows:\n%s\nwant:\n%s", got, strings.Join(want, " "))
	}
}

func TestFilterByBranchKeepsProject(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	all := h.rowKeys()
	h.key(t, "/")
	typeText(t, h, "FEAT") // case-insensitive
	wantRows(t, h, "r:/root/alpha", "w:/root/alpha-feat",
		"s:claude:c1c1c1c1-aaaa-bbbb", "s:codex:x1x1x1x1-cccc")
	wantView(t, h, "/FEAT")
	h.key(t, "esc")
	if h.m.filtering || h.m.filter != "" {
		t.Error("esc did not clear")
	}
	wantRows(t, h, all...)
}

func TestFilterSessionTitleKeepsAncestors(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "left") // collapsed ancestors are forced open by a match below
	h.key(t, "/")
	typeText(t, h, "parser")
	wantRows(t, h, "r:/root/alpha", "w:/root/alpha-feat", "s:claude:c1c1c1c1-aaaa-bbbb")
	h.key(t, "enter") // keep the filter, back to navigation
	if h.m.filtering || h.m.filter != "parser" {
		t.Fatalf("filtering=%v filter=%q", h.m.filtering, h.m.filter)
	}
	wantView(t, h, `filter "parser"`)
	h.key(t, "j")
	if h.cursorKey() == "" {
		t.Error("navigation broken under filter")
	}
	h.key(t, "esc") // esc in navigation clears the filter
	if h.m.filter != "" {
		t.Error("esc did not clear filter")
	}
	if h.m.collapsed["w:/root/alpha-feat"] != true {
		t.Error("collapse state lost")
	}
}

func TestFilterByIDRepoAndPath(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.key(t, "/")
	typeText(t, h, "x1x1")
	wantRows(t, h, "r:/root/alpha", "w:/root/alpha-feat", "s:codex:x1x1x1x1-cccc")
	h.key(t, "esc")
	h.key(t, "/")
	typeText(t, h, "beta")
	wantRows(t, h, "r:/root/beta", "w:/root/beta", "w:/root/beta-slow", "ew:/root/beta-slow:0")
	h.key(t, "esc")
	h.key(t, "/")
	typeText(t, h, "alpha-det")
	wantRows(t, h, "r:/root/alpha", "w:/root/alpha-det")
	typeText(t, h, "zzz")
	wantRows(t, h)
	wantView(t, h, "No matches")
}

func TestFilterTypingIsolated(t *testing.T) {
	h, o := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "/")
	typeText(t, h, "qdxnplor?/")
	if h.quit || h.m.dialog != nil || h.m.overlay != nil || len(h.m.busy) > 0 {
		t.Fatalf("global key fired: quit=%v dialog=%v overlay=%v busy=%v", h.quit, h.m.dialog, h.m.overlay, h.m.busy)
	}
	wantCalls(t, o)
	if h.m.filter != "qdxnplor?/" {
		t.Errorf("filter = %q", h.m.filter)
	}
	h.key(t, "ctrl+c")
	if !h.quit {
		t.Error("ctrl+c should still quit")
	}
}

func TestFilterForcedRowsNotCollapsible(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.key(t, "/")
	typeText(t, h, "parser")
	h.key(t, "enter")
	h.selectKey(t, "r:/root/alpha")
	h.key(t, "left")
	h.key(t, "space")
	if h.m.collapsed["r:/root/alpha"] {
		t.Error("forced-open repo got collapsed")
	}
	h.selectKey(t, "w:/root/alpha-feat")
	h.key(t, "left") // forced open: moves to the parent instead
	if h.m.collapsed["w:/root/alpha-feat"] || h.cursorKey() != "r:/root/alpha" {
		t.Errorf("collapsed=%v cursor=%s", h.m.collapsed["w:/root/alpha-feat"], h.cursorKey())
	}
}

func TestFilterCursorFallsBackToTop(t *testing.T) {
	h, _ := newOpsHarness(t, nil)
	h.selectKey(t, "w:/root/beta-slow")
	h.key(t, "/")
	typeText(t, h, "alpha-det")
	if h.m.cursor != 0 || h.cursorKey() != "r:/root/alpha" {
		t.Errorf("cursor %d %s", h.m.cursor, h.cursorKey())
	}
}
