package tui

import (
	"context"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
)

// scanEventMsg carries one streamed scan result. ch is the stream it came
// from so the reader can be re-armed; gen identifies the scan (stale scans,
// superseded by a rescan, are ignored and not re-armed).
type scanEventMsg struct {
	gen int
	ev  scan.Event
	ch  <-chan scan.Event
}

// scanDoneMsg reports that the scan stream of generation gen closed.
type scanDoneMsg struct{ gen int }

// sessionsLoadedMsg is the result of listing every session source.
type sessionsLoadedMsg struct {
	gen     int
	ss      []sessions.Session
	skipped map[sessions.Agent]int
	errs    []error
}

// liveTickMsg fires every live interval and triggers a live poll.
type liveTickMsg struct{}

// liveLoadedMsg is the result of one live poll (FR-6).
type liveLoadedMsg struct {
	procs []sessions.LiveProc
	err   error
	once  bool // an extra poll after an action: does not re-arm the tick
}

// spinTickMsg advances the loading spinner.
type spinTickMsg struct{}

// bindResult is the in-memory product of binding sessions to worktrees.
type bindResult struct {
	byWT   map[string][]sessions.Session // worktree path -> sessions, newest first
	liveWT map[string]bool
	// unbound holds sessions whose cwd binds to no worktree. Not rendered yet;
	// kept so an "unbound" group can be added later.
	unbound []sessions.Session
	live    int // number of live sessions (bound or not)
}

// bindMsg delivers a bindResult computed off the UI goroutine (binding
// canonicalises paths, which touches the filesystem).
type bindMsg struct {
	seq    int
	binder *sessions.Binder
	res    bindResult
}

// waitScan reads the next event of a scan stream.
func waitScan(gen int, ch <-chan scan.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return scanDoneMsg{gen: gen}
		}
		return scanEventMsg{gen: gen, ev: ev, ch: ch}
	}
}

// loadSessions lists all sources in parallel.
func loadSessions(ctx context.Context, gen int, srcs []sessions.Source, since time.Time) tea.Cmd {
	return func() tea.Msg {
		type res struct {
			ss      []sessions.Session
			skipped int
			err     error
		}
		out := make([]res, len(srcs))
		var wg sync.WaitGroup
		for i, s := range srcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ss, sk, err := s.List(ctx, since)
				out[i] = res{ss, sk, err}
			}()
		}
		wg.Wait()
		msg := sessionsLoadedMsg{gen: gen, skipped: map[sessions.Agent]int{}}
		for i, r := range out {
			msg.ss = append(msg.ss, r.ss...)
			msg.skipped[srcs[i].Agent()] += r.skipped
			if r.err != nil {
				msg.errs = append(msg.errs, r.err)
			}
		}
		return msg
	}
}

// liveTimeout bounds one live poll.
const liveTimeout = 4 * time.Second

// pollLiveOnce is pollLive for an out-of-band refresh (after an action).
func pollLiveOnce(ctx context.Context, pl sessions.ProcLister) tea.Cmd {
	poll := pollLive(ctx, pl)
	return func() tea.Msg {
		msg := poll().(liveLoadedMsg)
		msg.once = true
		return msg
	}
}

func pollLive(ctx context.Context, pl sessions.ProcLister) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, liveTimeout)
		defer cancel()
		procs, err := pl.List(ctx)
		return liveLoadedMsg{procs: procs, err: err}
	}
}

// bindCmd binds sessions (with live state applied) to worktrees. binder is
// reused when the worktree set is unchanged so cwd canonicalisation stays
// cached; nil builds a new one from paths.
func bindCmd(seq int, binder *sessions.Binder, paths []string, ss []sessions.Session, procs []sessions.LiveProc) tea.Cmd {
	return func() tea.Msg {
		if binder == nil {
			binder = sessions.NewBinder(paths)
		}
		live, liveWT := sessions.ApplyLive(ss, procs, binder)
		res := bindResult{byWT: map[string][]sessions.Session{}, liveWT: liveWT}
		for _, s := range live {
			if s.Live {
				res.live++
			}
			if wt, ok := binder.Bind(s.Cwd); ok {
				res.byWT[wt] = append(res.byWT[wt], s)
			} else {
				res.unbound = append(res.unbound, s)
			}
		}
		for _, list := range res.byWT {
			sortSessions(list)
		}
		sortSessions(res.unbound)
		return bindMsg{seq: seq, binder: binder, res: res}
	}
}

func sortSessions(ss []sessions.Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		if !ss[i].LastActivity.Equal(ss[j].LastActivity) {
			return ss[i].LastActivity.After(ss[j].LastActivity)
		}
		return ss[i].ID < ss[j].ID
	})
}
