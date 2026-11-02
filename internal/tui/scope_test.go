package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/scan"
)

// optScan is a fake scan that honours Options like scan.Scan: repos under
// Roots (here: all of `under`) plus those named in Repos.
type optScan struct {
	mu    sync.Mutex
	under []*scan.Repo
	extra []*scan.Repo // loadable via Options.Repos only
	opts  []scan.Options
}

func (f *optScan) Scan(_ context.Context, o scan.Options) <-chan scan.Event {
	f.mu.Lock()
	f.opts = append(f.opts, o)
	f.mu.Unlock()
	var out []*scan.Repo
	for _, r := range append(append([]*scan.Repo(nil), f.under...), f.extra...) {
		inRoots := len(o.Roots) > 0 && slices.Contains(f.under, r)
		if inRoots || slices.Contains(o.Repos, r.Path) {
			c := *r
			out = append(out, &c)
		}
	}
	ch := make(chan scan.Event, len(out))
	for _, r := range out {
		ch <- scan.Event{Repo: r}
	}
	close(ch)
	return ch
}

func (f *optScan) last() scan.Options {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opts[len(f.opts)-1]
}

func scopeHarness(t *testing.T, scope string) (*harness, *optScan) {
	t.Helper()
	fs := &optScan{
		under: fixtureRepos(),
		extra: []*scan.Repo{{Name: "gamma", Path: "/elsewhere/gamma", Worktrees: []gitx.Worktree{
			{Path: "/elsewhere/gamma", HEAD: "6666666fffffff", Branch: "main", IsMain: true, Status: &gitx.Status{}},
		}}},
	}
	h := &harness{}
	h.m = New(Deps{
		Roots:       []string{"/root"},
		Scope:       scope,
		Home:        "/home/u",
		Sources:     fixtureSources(),
		HistoryDays: 30,
		Scan:        fs.Scan,
		Now:         func() time.Time { return now },
		Tick:        func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil },
	})
	t.Cleanup(h.m.Close)
	h.settle(t, h.m.Init())
	h.send(t, tea.WindowSizeMsg{Width: 120, Height: 30})
	return h, fs
}

func repoKeys(h *harness) []string {
	var ks []string
	for _, k := range h.rowKeys() {
		if strings.HasPrefix(k, "r:") {
			ks = append(ks, k)
		}
	}
	return ks
}

func headerLine(h *harness) string {
	return ansi.Strip(strings.SplitN(h.view(), "\n", 2)[0])
}

func TestScopeStartsOnThisRepo(t *testing.T) {
	h, fs := scopeHarness(t, "/root/alpha")
	if o := fs.last(); len(o.Roots) != 0 || !slices.Equal(o.Repos, []string{"/root/alpha"}) {
		t.Fatalf("scoped scan options = %+v", o)
	}
	if got := repoKeys(h); !slices.Equal(got, []string{"r:/root/alpha"}) {
		t.Fatalf("repos = %v", got)
	}
	// expanded, with all worktrees and the bound sessions
	wantView(t, h, "alpha-feat", "alpha-det", "Refactor the parser")
	if hd := headerLine(h); !strings.Contains(hd, "grove  repo: alpha") || strings.Contains(hd, "all repos") {
		t.Errorf("header = %q", hd)
	}
	if bar := hintBar(t, h); !strings.Contains(bar, "a all repos") {
		t.Errorf("hint bar = %q", bar)
	}
}

func TestScopeToggle(t *testing.T) {
	h, fs := scopeHarness(t, "/root/alpha")
	h.selectKey(t, "w:/root/alpha-feat")

	h.key(t, "a")
	if o := fs.last(); !slices.Equal(o.Roots, []string{"/root"}) || !slices.Equal(o.Repos, []string{"/root/alpha"}) {
		t.Fatalf("all scan options = %+v", o)
	}
	if got := repoKeys(h); !slices.Equal(got, []string{"r:/root/alpha", "r:/root/beta"}) {
		t.Fatalf("all repos = %v", got)
	}
	if h.cursorKey() != "w:/root/alpha-feat" {
		t.Errorf("cursor = %q, want it kept on the worktree", h.cursorKey())
	}
	if hd := headerLine(h); !strings.Contains(hd, "all repos: /root") {
		t.Errorf("header = %q", hd)
	}
	if bar := hintBar(t, h); !strings.Contains(bar, "a this repo") {
		t.Errorf("hint bar = %q", bar)
	}

	// r in the all view rescans the roots
	h.key(t, "r")
	if o := fs.last(); len(o.Roots) != 1 {
		t.Errorf("refresh in all view: %+v", o)
	}

	h.key(t, "a")
	if got := repoKeys(h); !slices.Equal(got, []string{"r:/root/alpha"}) {
		t.Fatalf("back to this repo: %v", got)
	}
	if h.cursorKey() != "w:/root/alpha-feat" {
		t.Errorf("cursor = %q after toggling back", h.cursorKey())
	}
	h.key(t, "r")
	if o := fs.last(); len(o.Roots) != 0 || len(fs.opts) != 5 {
		t.Errorf("refresh in scoped view: %+v (%d scans)", o, len(fs.opts))
	}
	if !strings.Contains(headerLine(h), "repo: alpha") {
		t.Errorf("header = %q", headerLine(h))
	}
}

// A scope outside the roots stays listed in the all view.
func TestScopeOutsideRoots(t *testing.T) {
	h, _ := scopeHarness(t, "/elsewhere/gamma")
	if got := repoKeys(h); !slices.Equal(got, []string{"r:/elsewhere/gamma"}) {
		t.Fatalf("repos = %v", got)
	}
	if !strings.Contains(headerLine(h), "repo: gamma") {
		t.Errorf("header = %q", headerLine(h))
	}
	h.key(t, "a")
	if got := repoKeys(h); !slices.Equal(got, []string{"r:/root/alpha", "r:/root/beta", "r:/elsewhere/gamma"}) {
		t.Fatalf("all repos = %v", got)
	}
}

func TestScopeToggleWithoutScope(t *testing.T) {
	h, fs := scopeHarness(t, "")
	n := len(fs.opts)
	if got := repoKeys(h); len(got) != 2 {
		t.Fatalf("repos = %v", got)
	}
	if !strings.Contains(headerLine(h), "all repos: /root") {
		t.Errorf("header = %q", headerLine(h))
	}
	if bar := hintBar(t, h); strings.Contains(bar, "a ") {
		t.Errorf("hint bar offers a without a scope: %q", bar)
	}
	h.key(t, "a")
	if len(fs.opts) != n {
		t.Error("a rescanned without a scope")
	}
	wantView(t, h, "not started inside a git repo")
}
