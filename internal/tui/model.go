// Package tui implements grove's Bubble Tea terminal UI: a project →
// worktree → session tree fed by a streaming scan, session sources and a
// periodic live poll.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
	"github.com/RafaelXokito/grove/internal/sessions/live"
	"github.com/RafaelXokito/grove/internal/update"
)

// DefaultLiveInterval is the live poll period (FR-6, live_interval).
const DefaultLiveInterval = 5 * time.Second

// maxSessionsShown is the per-worktree session cap before a "+N more" row.
const maxSessionsShown = 5

// Deps are the model's inputs and side-effect seams. Zero values mean
// defaults where noted.
type Deps struct {
	Roots []string
	// Scope is the main path of the repository grove was started in
	// (FR-22); "" means there is none and only the all-repos view exists.
	Scope      string
	Home       string // for ~ abbreviation; "" disables it
	MaxDepth   int
	Runner     gitx.Runner
	Limiter    scan.Limiter
	GitTimeout time.Duration

	Sources []sessions.Source
	Procs   sessions.ProcLister // nil disables live detection
	// HistoryDays is the session window in days; 0 means unlimited.
	HistoryDays int
	// LiveInterval is the live poll period; 0 means DefaultLiveInterval.
	LiveInterval time.Duration
	// Warnings (e.g. unknown config keys) are shown in the status line.
	Warnings []string
	// UpdateCheck returns a newer release tag, or "" (FR-23). It runs in
	// the background after the first frame; nil disables the check.
	UpdateCheck func(context.Context) string

	// Test seams; nil means the real implementation.
	Scan func(context.Context, scan.Options) <-chan scan.Event
	Now  func() time.Time
	Tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	// Getenv reads $GROVE_OPEN, $EDITOR and $SHELL for `o`.
	Getenv func(string) string
	// Environ is the environment for the `o` process.
	Environ func() []string
	// Exec runs the `o` process with the TUI suspended (tea.ExecProcess).
	Exec func(*exec.Cmd, tea.ExecCallback) tea.Cmd
}

// Model is the root Bubble Tea model. Use it through a pointer.
type Model struct {
	deps   Deps
	ctx    context.Context
	cancel context.CancelFunc

	// updateTag is a newer release (FR-23), "" when none is known.
	updateTag string

	// scan state
	gen        int
	scanCancel context.CancelFunc
	scanning   bool
	repos      []*scan.Repo // sorted by Name
	seen       map[string]bool
	discErrs   []error

	// sessions
	sessLoading bool
	sessions    []sessions.Session
	skipped     map[sessions.Agent]int
	sessErrs    []error

	// live
	procs   []sessions.LiveProc
	liveErr error

	// binding
	binder      *sessions.Binder
	binderStale bool
	bindSeq     int
	bindBusy    bool
	bindDirty   bool
	bound       bindResult

	// tree
	collapsed map[string]bool
	showAll   map[string]bool
	rows      []row
	cursor    int
	offset    int

	width, height int
	spinFrame     int
	spinning      bool
	message       string
	msgErr        bool              // message is an error (rendered as such)
	dialog        *dialog           // open confirm/input prompt, if any
	busy          map[string]string // worktree path or "r:"+repo path -> running action label
	selectPath    string            // worktree to put the cursor on once its repo reloads
	cursorMoved   bool              // the user moved the cursor; until then it stays on the top row
	homeCanon     string            // canonical Home, for ~ abbreviation of canonical paths
	rootsCanon    []string
	keys          keyMap
	scoped        bool   // showing only the Scope repo ("this repo" view, FR-22)
	scopeKey      string // pathx.FoldKey of the canonical Scope

	overlay     *overlay // details or help pane, if open
	detailsSeq  int
	filter      string // active tree filter (FR-17); "" = none
	filtering   bool   // the filter input has focus
	filterInput textinput.Model
}

// New returns a Model ready to be started by Init.
func New(d Deps) *Model {
	if d.Scan == nil {
		d.Scan = scan.Scan
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Tick == nil {
		d.Tick = tea.Tick
	}
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.Environ == nil {
		d.Environ = os.Environ
	}
	if d.Exec == nil {
		d.Exec = tea.ExecProcess
	}
	if d.LiveInterval <= 0 {
		d.LiveInterval = DefaultLiveInterval
	}
	if d.Limiter == nil {
		d.Limiter = scan.NewLimiter(0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Canonicalise home and roots once here (not in View): scan reports
	// canonical paths, e.g. /private/var for a /var home on macOS.
	var homeCanon string
	if d.Home != "" {
		homeCanon = pathx.Canonical(d.Home)
	}
	rootsCanon := make([]string, len(d.Roots))
	for i, r := range d.Roots {
		rootsCanon[i] = pathx.Canonical(r)
	}
	var scopeKey string
	if d.Scope != "" {
		d.Scope = pathx.Canonical(d.Scope)
		scopeKey = pathx.FoldKey(d.Scope)
	}
	return &Model{
		scoped:      d.Scope != "",
		scopeKey:    scopeKey,
		homeCanon:   homeCanon,
		rootsCanon:  rootsCanon,
		deps:        d,
		ctx:         ctx,
		cancel:      cancel,
		scanning:    true, // the first frame shows the spinner (FR-18)
		sessLoading: len(d.Sources) > 0,
		collapsed:   map[string]bool{},
		showAll:     map[string]bool{},
		busy:        map[string]string{},
		skipped:     map[sessions.Agent]int{},
		keys:        defaultKeys(),
	}
}

// Close cancels any in-flight scan, session load and live poll.
func (m *Model) Close() { m.cancel() }

// Init starts the scan, the session load and the live poll.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.rescan()}
	if m.deps.Procs != nil {
		cmds = append(cmds, pollLive(m.ctx, m.deps.Procs))
	}
	if m.deps.UpdateCheck != nil {
		cmds = append(cmds, checkUpdate(m.ctx, m.deps.UpdateCheck))
	}
	return tea.Batch(cmds...)
}

// rescan cancels the running scan (so its goroutines exit) and starts a full
// git + session rescan (FR-19).
func (m *Model) rescan() tea.Cmd {
	if m.scanCancel != nil {
		m.scanCancel()
	}
	m.gen++
	ctx, cancel := context.WithCancel(m.ctx)
	m.scanCancel = cancel
	m.scanning = true
	m.seen = map[string]bool{}
	m.discErrs = nil
	opts := scan.Options{
		Roots:      m.deps.Roots,
		MaxDepth:   m.deps.MaxDepth,
		Runner:     m.deps.Runner,
		Limiter:    m.deps.Limiter,
		GitTimeout: m.deps.GitTimeout,
	}
	if m.deps.Scope != "" {
		// The scoped repo is always loaded, even outside the roots.
		opts.Repos = []string{m.deps.Scope}
	}
	if m.scoped {
		opts.Roots = nil
	}
	ch := m.deps.Scan(ctx, opts)
	cmds := []tea.Cmd{waitScan(m.gen, ch)}
	if len(m.deps.Sources) > 0 {
		m.sessLoading = true
		var since time.Time
		if m.deps.HistoryDays > 0 {
			since = m.deps.Now().Add(-time.Duration(m.deps.HistoryDays) * 24 * time.Hour)
		}
		cmds = append(cmds, loadSessions(ctx, m.gen, m.deps.Sources, since))
	}
	cmds = append(cmds, m.startSpinner())
	return tea.Batch(cmds...)
}

func (m *Model) loading() bool { return m.scanning || m.sessLoading }

func (m *Model) startSpinner() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return m.spinTick()
}

func (m *Model) spinTick() tea.Cmd {
	return m.deps.Tick(spinInterval, func(time.Time) tea.Msg { return spinTickMsg{} })
}

func (m *Model) liveTick() tea.Cmd {
	return m.deps.Tick(m.deps.LiveInterval, func(time.Time) tea.Msg { return liveTickMsg{} })
}

// requestBind recomputes bindings asynchronously, coalescing requests while
// one is in flight.
func (m *Model) requestBind() tea.Cmd {
	if m.bindBusy {
		m.bindDirty = true
		return nil
	}
	m.bindBusy = true
	m.bindSeq++
	binder := m.binder
	if m.binderStale || binder == nil {
		binder = nil
		m.binderStale = false
	}
	var paths []string
	for _, r := range m.repos {
		for _, wt := range r.Worktrees {
			paths = append(paths, wt.Path)
		}
	}
	return bindCmd(m.bindSeq, binder, paths, m.sessions, m.procs)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case updateAvailableMsg:
		if update.ValidTag(msg.tag) { // network data: only plain vX.Y.Z
			m.updateTag = msg.tag
		}
		return m, nil

	case spinTickMsg:
		m.spinFrame++
		if m.loading() || len(m.busy) > 0 || m.detailsLoading() {
			return m, m.spinTick()
		}
		m.spinning = false
		return m, nil

	case scanEventMsg:
		if msg.gen != m.gen {
			return m, nil // superseded scan: cancelled, do not re-arm
		}
		if msg.ev.Repo != nil {
			m.upsertRepo(msg.ev.Repo)
		} else if msg.ev.Err != nil {
			m.discErrs = append(m.discErrs, msg.ev.Err)
		}
		m.rebuild()
		return m, tea.Batch(waitScan(msg.gen, msg.ch), m.requestBind())

	case scanDoneMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.scanning = false
		if m.message == msgRefreshing {
			m.message = ""
		}
		kept := m.repos[:0]
		for _, r := range m.repos {
			if m.seen[r.Path] {
				kept = append(kept, r)
			}
		}
		m.repos = kept
		m.binderStale = true
		m.rebuild()
		return m, m.requestBind()

	case sessionsLoadedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.sessLoading = false
		m.sessions = msg.ss
		m.skipped = msg.skipped
		m.sessErrs = msg.errs
		return m, m.requestBind()

	case repoLoadedMsg:
		if msg.repo.Name == "" {
			msg.repo.Name = m.repoName(msg.repo.Path)
		}
		m.upsertRepo(msg.repo)
		m.rebuild()
		m.selectPending(msg.repo)
		return m, m.requestBind()

	case liveTickMsg:
		if m.deps.Procs == nil {
			return m, nil
		}
		return m, pollLive(m.ctx, m.deps.Procs)

	case liveLoadedMsg:
		m.procs = msg.procs
		m.liveErr = msg.err
		if msg.once {
			return m, m.requestBind()
		}
		return m, tea.Batch(m.requestBind(), m.liveTick())

	case detailsMsg:
		if o := m.overlay; o != nil && o.kind == ovDetails && o.details.seq == msg.seq {
			o.details.loading = false
			o.details.data = msg.data
		}
		return m, nil

	case openDoneMsg:
		var exitErr *exec.ExitError
		if msg.what == "$SHELL" && errors.As(msg.err, &exitErr) {
			msg.err = nil // a shell's exit status is the last command's, not a failure
		}
		if msg.err != nil {
			m.setError(fmt.Sprintf("%s in %s: %v", msg.what, m.display(msg.path), msg.err))
		} else {
			m.setMessage(fmt.Sprintf("back from %s in %s", msg.what, m.display(msg.path)))
		}
		m.ensureVisible()
		return m, m.reloadRepo(msg.repo)

	case bindMsg:
		if msg.seq != m.bindSeq {
			return m, nil
		}
		m.bindBusy = false
		m.binder = msg.binder
		m.bound = msg.res
		m.rebuild()
		if m.bindDirty {
			m.bindDirty = false
			return m, m.requestBind()
		}
		return m, nil
	}
	if cmd, ok := m.handleActionMsg(msg); ok {
		return m, cmd
	}
	return m, nil
}

func (m *Model) upsertRepo(r *scan.Repo) {
	m.seen[r.Path] = true
	m.binderStale = true
	for i, old := range m.repos {
		if old.Path == r.Path {
			m.repos[i] = r
			return
		}
	}
	m.repos = append(m.repos, r)
	sort.SliceStable(m.repos, func(i, j int) bool { return m.repos[i].Name < m.repos[j].Name })
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	m.setMessage("")
	var cmd tea.Cmd
	switch {
	case m.dialog != nil:
		cmd = m.dialogKey(msg)
	case m.filtering:
		cmd = m.filterKey(msg)
	case m.overlay != nil:
		cmd = m.overlayKey(msg)
	default:
		cmd = m.dispatchKey(msg)
	}
	m.ensureVisible() // the message line changes the body height
	return cmd
}

func (m *Model) dispatchKey(msg tea.KeyMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.Quit):
		m.Close()
		return tea.Quit
	case key.Matches(msg, k.Up):
		m.move(-1)
	case key.Matches(msg, k.Down):
		m.move(1)
	case key.Matches(msg, k.PageUp):
		m.move(-m.bodyHeight())
	case key.Matches(msg, k.PageDown):
		m.move(m.bodyHeight())
	case key.Matches(msg, k.Home):
		m.move(-len(m.rows))
	case key.Matches(msg, k.End):
		m.move(len(m.rows))
	case key.Matches(msg, k.Collapse):
		m.collapseOrParent()
	case key.Matches(msg, k.Expand):
		m.setCollapsed(false)
	case key.Matches(msg, k.Toggle):
		if r, ok := m.current(); ok && r.expandable {
			m.setCollapsed(!m.collapsed[r.key])
		}
	case key.Matches(msg, k.Refresh):
		m.message = msgRefreshing
		return m.rescan()
	case key.Matches(msg, k.Scope):
		return m.toggleScope()
	case key.Matches(msg, k.Enter):
		if r, ok := m.current(); ok && r.kind == rowMore {
			m.showAll[r.wtPath] = true
			m.rebuild()
			return nil
		}
		return m.details()
	case key.Matches(msg, k.Detach):
		return m.detach()
	case key.Matches(msg, k.Checkout):
		return m.checkout()
	case key.Matches(msg, k.Lock):
		return m.lock()
	case key.Matches(msg, k.Prune):
		return m.prune()
	case key.Matches(msg, k.New):
		return m.newWorktree()
	case key.Matches(msg, k.Remove):
		return m.remove()
	case key.Matches(msg, k.Open):
		return m.open()
	case key.Matches(msg, k.Filter):
		return m.startFilter()
	case key.Matches(msg, k.Help):
		m.overlay = &overlay{kind: ovHelp}
	case msg.Type == tea.KeyEsc && m.filter != "":
		m.setFilter("")
		m.setMessage("filter cleared")
	}
	return nil
}

// isScopeRepo reports whether r is the repo grove was started in. Scan
// reports git's (canonical) paths, so no filesystem access is needed here.
func (m *Model) isScopeRepo(r *scan.Repo) bool {
	return m.scopeKey != "" && pathx.FoldKey(filepath.Clean(r.Path)) == m.scopeKey
}

// toggleScope implements `a`: switch between the started-in repo and all
// repos under the roots (FR-22), keeping the cursor on the same row.
func (m *Model) toggleScope() tea.Cmd {
	if m.deps.Scope == "" {
		m.setMessage("not started inside a git repo: already showing all repos")
		return nil
	}
	m.scoped = !m.scoped
	if len(m.rows) > 0 {
		m.cursorMoved = true // keep the selection while repos stream in
	}
	if m.scoped {
		kept := m.repos[:0]
		for _, r := range m.repos {
			if m.isScopeRepo(r) {
				kept = append(kept, r)
			}
		}
		m.repos = kept
		m.binderStale = true
		m.rebuild()
		m.setMessage("showing this repo (a: all repos)")
	} else {
		m.setMessage("showing all repos (a: this repo)")
	}
	return tea.Batch(m.rescan(), m.requestBind())
}

func (m *Model) detailsLoading() bool {
	return m.overlay != nil && m.overlay.kind == ovDetails && m.overlay.details.loading
}

func (m *Model) current() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

func (m *Model) move(d int) {
	m.cursorMoved = true
	m.cursor += d
	m.clampCursor()
	m.ensureVisible()
}

func (m *Model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) setCollapsed(c bool) {
	r, ok := m.current()
	if !ok || !r.expandable || r.forced || m.collapsed[r.key] == c {
		return
	}
	m.collapsed[r.key] = c
	m.rebuild()
}

// collapseOrParent implements ←: collapse an expanded node, otherwise move to
// the parent row (FR-8).
func (m *Model) collapseOrParent() {
	r, ok := m.current()
	if !ok {
		return
	}
	if r.expandable && !r.forced && !m.collapsed[r.key] {
		m.setCollapsed(true)
		return
	}
	if r.parent >= 0 {
		m.cursorMoved = true
		m.cursor = r.parent
		m.ensureVisible()
	}
}

// rebuild re-flattens the tree, keeping the cursor on the same row key when
// it still exists (FR-19).
func (m *Model) rebuild() {
	var want []string // the row itself, then its worktree, then its repo
	if r, ok := m.current(); ok && m.cursorMoved {
		want = append(want, r.key)
		if r.wtPath != "" {
			want = append(want, "w:"+r.wtPath)
		}
		if r.repo != nil {
			want = append(want, "r:"+r.repo.Path)
		}
	}
	m.rows = m.flatten()
	if !m.cursorMoved {
		m.cursor = 0 // stay on the top row while repos stream in and get sorted
	}
	idx := make(map[string]int, len(m.rows))
	for i, r := range m.rows {
		idx[r.key] = i
	}
	found := false
	for _, k := range want {
		if i, ok := idx[k]; ok {
			m.cursor, found = i, true
			break
		}
	}
	if len(want) > 0 && !found {
		m.cursor = 0 // nothing survived (e.g. filtered out): top row
	}
	m.clampCursor()
	m.ensureVisible()
}

func (m *Model) ensureVisible() {
	h := m.bodyHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if max := len(m.rows) - h; m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// splitErr flattens an errors.Join tree one level.
func splitErr(err error) []error {
	if err == nil {
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range u.Unwrap() {
			if e != nil {
				out = append(out, e)
			}
		}
		return out
	}
	return []error{err}
}

// errorCount counts every recorded error for the status line.
func (m *Model) errorCount() int {
	n := len(m.discErrs) + len(m.sessErrs)
	for _, r := range m.repos {
		n += len(splitErr(r.Err))
	}
	if m.liveErr != nil && !isUnavailable(m.liveErr) {
		n++
	}
	return n
}

// isUnavailable reports a process-detection-unavailable error (shown as a
// note rather than counted as an error).
func isUnavailable(err error) bool {
	return errors.Is(err, live.ErrUnavailable)
}

const msgRefreshing = "refreshing…"

// repoLoadedMsg carries one repo reloaded by reloadRepo.
type repoLoadedMsg struct{ repo *scan.Repo }

// reloadRepo reloads a single repo (worktrees + status) after an action
// (FR-19), without a full rescan.
func (m *Model) reloadRepo(path string) tea.Cmd {
	d := m.deps
	ctx := m.ctx
	return func() tea.Msg {
		r := scan.LoadRepo(ctx, d.Runner, d.Limiter, path, d.GitTimeout)
		return repoLoadedMsg{repo: &r}
	}
}

// repoName keeps the name of an already-known repo, else derives it from the
// roots like scan does.
func (m *Model) repoName(path string) string {
	for _, r := range m.repos {
		if r.Path == path && r.Name != "" {
			return r.Name
		}
	}
	return m.display(path)
}

// selection returns the repo and worktree the cursor applies to. On a
// session, "+N more" or worktree error row it is the parent worktree; on a
// repo row (or repo error row) wt is nil.
func (m *Model) selection() (*scan.Repo, *gitx.Worktree) {
	r, ok := m.current()
	if !ok {
		return nil, nil
	}
	return r.repo, r.wt
}
