package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/sessions"
)

// Details-pane bounds (FR-15).
const (
	detailsTimeout     = 10 * time.Second
	maxDiffStatLines   = 20
	maxDetailSessions  = 20   // sessions whose transcript is re-read per pane
	maxPromptRunes     = 4000 // the full prompt is shown, up to this cap
	maxDiffStatBytes   = 64 << 10
	maxUntracked       = 10000 // untracked files counted before "10000+"
	detailsLabelIndent = "  "
)

type overlayKind int

const (
	ovDetails overlayKind = iota
	ovHelp
)

// overlay is a scrollable pane that replaces the tree body (details, help).
// While it is open, keys scroll or close it; global keys do not fire.
type overlay struct {
	kind    overlayKind
	offset  int
	details *detailsState
}

// detailsState is the details pane of one worktree, or of one session (sess
// set) with its worktree as context.
type detailsState struct {
	seq     int
	loading bool
	wt      gitx.Worktree // snapshot at open
	sess    *sessions.Session
	nsess   int // sessions in the snapshot taken at open
	data    detailsData
}

type detailsData struct {
	log       string   // `git log -1 --format=%h %s`
	diffStat  []string // `git diff --stat HEAD`, capped at maxDiffStatLines
	untracked int
	untrMore  bool // the count stopped at maxUntracked
	diffErr   bool // `git diff` failed (e.g. unborn HEAD): no stat to show
	gitErrs   []string
	sess      []sessDetail
}

type sessDetail struct {
	s      sessions.Session
	prompt string // full first prompt re-read from s.Path ("" = use s.Title)
	branch string // Codex payload.git.branch
	err    error
}

// detailsMsg delivers the asynchronously loaded details.
type detailsMsg struct {
	seq  int
	data detailsData
}

// openDoneMsg reports that the process started by `o` exited.
type openDoneMsg struct {
	repo, path, what string
	err              error
}

// details implements `enter` on a worktree or session row (FR-15).
func (m *Model) details() tea.Cmd {
	r, ok := m.current()
	if !ok {
		return nil
	}
	if r.wt == nil {
		if r.expandable {
			m.setCollapsed(!m.collapsed[r.key])
		} else {
			m.setMessage("select a worktree or session")
		}
		return nil
	}
	m.detailsSeq++
	st := &detailsState{seq: m.detailsSeq, loading: true, wt: *r.wt}
	var ss []sessions.Session
	if r.kind == rowSession && r.sess != nil {
		s := *r.sess
		st.sess = &s
		ss = []sessions.Session{s}
	} else {
		ss = append(ss, m.bound.byWT[r.wt.Path]...)
	}
	st.nsess = len(ss)
	m.overlay = &overlay{kind: ovDetails, details: st}
	detailers := map[sessions.Agent]sessions.Detailer{}
	for _, src := range m.deps.Sources {
		if d, ok := src.(sessions.Detailer); ok {
			detailers[src.Agent()] = d
		}
	}
	wt, seq, ctx, runner := st.wt, st.seq, m.ctx, m.deps.Runner
	withGit := st.sess == nil && !wt.IsBare && !wt.Prunable && runner != nil
	return tea.Batch(m.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, detailsTimeout)
		defer cancel()
		var d detailsData
		if withGit {
			loadGitDetails(ctx, runner, wt.Path, &d)
		}
		for i, s := range ss {
			sd := sessDetail{s: s}
			if det := detailers[s.Agent]; det != nil && s.Path != "" && i < maxDetailSessions {
				info, err := det.Detail(ctx, s)
				sd.prompt, sd.branch, sd.err = info.Prompt, info.GitBranch, err
			}
			d.sess = append(d.sess, sd)
		}
		return detailsMsg{seq: seq, data: d}
	})
}

func loadGitDetails(ctx context.Context, r gitx.Runner, path string, d *detailsData) {
	errf := func(err error) { d.gitErrs = append(d.gitErrs, errSummary(err)) }
	if out, err := r.Run(ctx, path, "log", "-1", "--format=%h %s"); err != nil {
		errf(err)
	} else {
		d.log = strings.TrimSpace(string(out))
	}
	// Output size is bounded: diff --stat is capped in bytes and untracked
	// files are counted while streaming, never buffered.
	if out, truncated, err := gitx.RunCapped(ctx, r, path, maxDiffStatBytes, "diff", "--stat", "HEAD"); err != nil {
		errf(err)
		d.diffErr = true
	} else {
		if truncated {
			// The summary line was cut off; drop the partial last line.
			if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
				out = out[:i]
			}
			out = append(out, "\n … output truncated"...)
		}
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		if n := len(lines); n > maxDiffStatLines {
			// Keep the head and git's summary line.
			lines = append(append(lines[:maxDiffStatLines-2:maxDiffStatLines-2],
				fmt.Sprintf(" … %d more", n-maxDiffStatLines+1)), lines[n-1])
		}
		d.diffStat = lines
	}
	if n, capped, err := gitx.CountRecords(ctx, r, path, maxUntracked, "ls-files", "--others", "--exclude-standard", "-z"); err != nil {
		errf(err)
	} else {
		d.untracked, d.untrMore = n, capped
	}
}

// overlayKey handles a key while the details or help pane is open.
func (m *Model) overlayKey(msg tea.KeyMsg) tea.Cmd {
	o, k := m.overlay, m.keys
	if msg.Type == tea.KeyCtrlC {
		m.Close()
		return tea.Quit
	}
	page := m.bodyHeight()
	// Clamp first: a resize may have left offset past the end.
	o.offset = clampOffset(o.offset, len(m.overlayLines(m.w())), page)
	switch {
	case msg.Type == tea.KeyEsc, msg.String() == "q",
		key.Matches(msg, k.Enter) && o.kind == ovDetails,
		key.Matches(msg, k.Help) && o.kind == ovHelp:
		m.overlay = nil
	case key.Matches(msg, k.Up):
		o.offset--
	case key.Matches(msg, k.Down):
		o.offset++
	case key.Matches(msg, k.PageUp):
		o.offset -= page
	case key.Matches(msg, k.PageDown):
		o.offset += page
	case key.Matches(msg, k.Home):
		o.offset = 0
	case key.Matches(msg, k.End):
		o.offset = len(m.overlayLines(m.w()))
	}
	if m.overlay != nil {
		o.offset = clampOffset(o.offset, len(m.overlayLines(m.w())), page)
	}
	return nil
}

func clampOffset(off, n, h int) int {
	if off > n-h {
		off = n - h
	}
	if off < 0 {
		off = 0
	}
	return off
}

func (m *Model) overlayLines(w int) []string {
	if m.overlay.kind == ovHelp {
		return m.helpLines(w)
	}
	return m.detailsLines(w)
}

// overlayBody renders the visible window of the open pane.
func (m *Model) overlayBody(w int) string {
	lines := m.overlayLines(w)
	h := m.bodyHeight()
	off := clampOffset(m.overlay.offset, len(lines), h)
	var b strings.Builder
	for i := off; i < len(lines) && i < off+h; i++ {
		b.WriteString(fit(lines[i], w))
		b.WriteByte('\n')
	}
	return b.String()
}

// overlayStatus is the status line while a pane is open.
func (m *Model) overlayStatus() string {
	lines := len(m.overlayLines(m.w()))
	h := m.bodyHeight()
	name := "details"
	if m.overlay.kind == ovHelp {
		name = "help"
	}
	s := name
	if lines > h {
		off := clampOffset(m.overlay.offset, lines, h)
		s += fmt.Sprintf(" · %d–%d/%d · ↑/↓ pgup/pgdn scroll", off+1, min(off+h, lines), lines)
	}
	return s + " · esc close"
}

// wrapText sanitises s line by line (keeping its line breaks) and wraps each
// to w cells with indent prepended.
func wrapText(s, indent string, w int) []string {
	avail := max(w-ansi.StringWidth(indent), 10)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		para = strings.TrimRight(sanitize(strings.ReplaceAll(para, "\t", "    ")), " ")
		if para == "" {
			out = append(out, "")
			continue
		}
		for _, l := range strings.Split(ansi.Wrap(para, avail, " "), "\n") {
			out = append(out, indent+l)
		}
	}
	return out
}

// kv renders a "label  value" line, wrapping the (sanitised) value.
func kv(label, value string, w int) []string {
	pad := detailsLabelIndent + fmt.Sprintf("%-10s ", label)
	lines := wrapText(value, strings.Repeat(" ", len(pad)), w)
	if len(lines) == 0 {
		return []string{pad}
	}
	lines[0] = styleDim.Render(pad) + strings.TrimLeft(lines[0], " ")
	return lines
}

func (m *Model) detailsLines(w int) []string {
	st := m.overlay.details
	wt := st.wt
	var out []string
	add := func(ls ...string) { out = append(out, ls...) }
	title := "Worktree " + sanitize(m.display(wt.Path))
	if st.sess != nil {
		title = fmt.Sprintf("Session %s · %s", st.sess.Agent, sanitize(st.sess.ID))
	}
	add(styleHeader.Render(fit(title, w)))
	if st.loading {
		add(m.spinner() + " loading details…")
	}

	add(kv("path", wt.Path, w)...)
	add(kv("branch", m.branchText(&wt), w)...)
	if st.sess == nil {
		if wt.HEAD != "" {
			head := wt.HEAD
			if st.data.log != "" {
				head = st.data.log
			}
			add(kv("HEAD", head, w)...)
		}
		switch s := wt.Status; {
		case s == nil:
		case s.HasUpstream:
			add(kv("upstream", fmt.Sprintf("%s ↑%d ↓%d", s.Upstream, s.Ahead, s.Behind), w)...)
		default:
			add(kv("upstream", "none", w)...)
		}
		if s := wt.Status; s != nil {
			state := "clean"
			if s.Changed > 0 {
				state = fmt.Sprintf("dirty (%d)", s.Changed)
			}
			add(kv("status", state, w)...)
		}
		if wt.IsMain {
			add(kv("main", "yes (cannot be removed or locked)", w)...)
		}
		if wt.Locked {
			add(kv("locked", orNone(wt.LockReason, "(no reason)"), w)...)
		}
		if wt.Prunable {
			add(kv("prunable", orNone(wt.PruneReason, "(no reason)"), w)...)
		}
		for _, e := range st.data.gitErrs {
			add(wrapText("! "+e, detailsLabelIndent, w)...)
		}
		if !st.loading && !wt.IsBare && !wt.Prunable {
			add("", styleHeader.Render("Changes (git diff --stat HEAD)"))
			if len(st.data.diffStat) == 0 && !st.data.diffErr {
				add(detailsLabelIndent + "no changes to tracked files")
			}
			for _, l := range st.data.diffStat {
				add(fit(detailsLabelIndent+sanitize(l), w))
			}
			more := ""
			if st.data.untrMore {
				more = "+"
			}
			add(fmt.Sprintf("%suntracked files: %d%s", detailsLabelIndent, st.data.untracked, more))
		}
		add("", styleHeader.Render(fmt.Sprintf("Sessions (%d)", st.nsess)))
		if st.nsess == 0 {
			add(detailsLabelIndent + "no agent sessions bound to this worktree")
		}
	}
	if st.loading {
		return out
	}
	for i, sd := range st.data.sess {
		if st.sess == nil {
			if i > 0 {
				add("")
			}
			add(detailsLabelIndent + styleRepo.Render(fmt.Sprintf("%s %s", sd.s.Agent, sanitize(sd.s.ID))))
		}
		add(m.sessionLines(sd, w)...)
	}
	return out
}

func orNone(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

func (m *Model) sessionLines(sd sessDetail, w int) []string {
	s := sd.s
	var out []string
	add := func(ls ...string) { out = append(out, ls...) }
	add(kv("agent", string(s.Agent), w)...)
	add(kv("id", s.ID, w)...)
	prompt := sd.prompt
	if prompt == "" {
		prompt = orNone(s.Title, "(none)")
	}
	if r := []rune(prompt); len(r) > maxPromptRunes {
		prompt = string(r[:maxPromptRunes]) + "…"
	}
	if sd.prompt != "" && s.Title != "" && sessions.CleanTitle(sd.prompt, sessions.TitleMaxRunes) != s.Title {
		add(kv("title", s.Title, w)...)
	}
	add(kv("prompt", prompt, w)...)
	if sd.err != nil {
		add(kv("", "! could not re-read transcript: "+firstLine(sd.err.Error()), w)...)
	}
	add(kv("last cwd", s.Cwd, w)...)
	if sd.branch != "" {
		add(kv("git branch", sd.branch+" (recorded by codex)", w)...)
	}
	act := "unknown"
	if !s.LastActivity.IsZero() {
		act = s.LastActivity.Local().Format("2006-01-02 15:04") + " (" + relTime(m.deps.Now().Sub(s.LastActivity)) + ")"
	}
	add(kv("activity", act, w)...)
	add(kv("state", m.liveText(s), w)...)
	add(kv("resume", resumeCommand(s)+"  (run it yourself in the worktree)", w)...)
	return out
}

// liveText reports the session's current live state from the latest bind.
func (m *Model) liveText(s sessions.Session) string {
	for _, list := range m.bound.byWT {
		for _, b := range list {
			if b.Agent != s.Agent || b.ID != s.ID {
				continue
			}
			switch {
			case !b.Live:
				return "not running"
			case b.Agent != sessions.AgentClaude:
				return "running"
			case b.Busy:
				return "running · busy"
			default:
				return "running · idle"
			}
		}
	}
	return "not running"
}

// resumeCommand is shown as a hint only; grove never launches agents.
func resumeCommand(s sessions.Session) string {
	if s.Agent == sessions.AgentCodex {
		return "codex resume " + s.ID
	}
	return "claude --resume " + s.ID
}

// open implements `o` (FR-16): suspend the TUI and run $GROVE_OPEN, else
// $EDITOR <path>, else $SHELL, with cwd = the worktree.
func (m *Model) open() tea.Cmd {
	repo, wt, ok := m.target()
	if !ok {
		return nil
	}
	c, what := openCommand(m.deps.Getenv, m.deps.Environ(), wt.Path)
	rp, path := repo.Path, wt.Path
	return m.deps.Exec(c, func(err error) tea.Msg {
		return openDoneMsg{repo: rp, path: path, what: what, err: err}
	})
}

// openCommand builds the process for `o`. $GROVE_OPEN and $EDITOR run via
// sh (like git runs $EDITOR) so they may carry arguments; the worktree path
// is "$1" for $GROVE_OPEN, the argument for $EDITOR, and GROVE_WORKTREE in
// the environment of all three.
func openCommand(getenv func(string) string, environ []string, path string) (*exec.Cmd, string) {
	var c *exec.Cmd
	var what string
	if v := strings.TrimSpace(getenv("GROVE_OPEN")); v != "" {
		c, what = exec.Command("/bin/sh", "-c", v, "grove-open", path), "$GROVE_OPEN"
	} else if ed := strings.TrimSpace(getenv("EDITOR")); ed != "" {
		c, what = exec.Command("/bin/sh", "-c", ed+` "$@"`, ed, path), "$EDITOR"
	} else {
		sh := strings.TrimSpace(getenv("SHELL"))
		if sh == "" {
			sh = "/bin/sh"
		}
		c, what = exec.Command(sh), "$SHELL"
	}
	c.Dir = path
	c.Env = append(append([]string(nil), environ...), "GROVE_WORKTREE="+path)
	return c, what
}
