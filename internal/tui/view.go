package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
	"github.com/RafaelXokito/grove/internal/scan"
	"github.com/RafaelXokito/grove/internal/sessions"
)

const (
	spinInterval  = 100 * time.Millisecond
	defaultWidth  = 80
	defaultHeight = 24
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleRepo     = lipgloss.NewStyle().Bold(true)
	styleBranch   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDetached = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleDirty    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleClean    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleFlag     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleLive     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Faint(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleStatus   = lipgloss.NewStyle().Faint(true)
	styleMsgErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleBusy     = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
)

type rowKind int

const (
	rowRepo rowKind = iota
	rowWorktree
	rowSession
	rowMore
	rowError
)

// row is one line of the flattened tree.
type row struct {
	kind       rowKind
	key        string // stable identity for cursor/collapse state
	parent     int    // index of the parent row, -1 for top level
	depth      int
	expandable bool
	forced     bool // shown expanded by the filter (a match below); not collapsible
	repo       *scan.Repo
	wt         *gitx.Worktree
	wtPath     string // owning worktree (worktree, session, more and wt-error rows)
	sess       *sessions.Session
	more       int
	err        string
}

// flatten builds the visible rows: project → worktrees → sessions. A future
// "unbound sessions" group would be appended here from m.bound.unbound.
func (m *Model) flatten() []row {
	var rows []row
	f := m.treeFilter()
	for _, repo := range m.repos {
		rkey := "r:" + repo.Path
		// Filter (FR-17): a matching node shows its whole subtree; a node
		// with a match below is shown (and forced open) as its ancestor.
		repoMatch := f.repo(repo)
		wtMatch := map[string]bool{}
		wtVisible := map[string]bool{}
		anyBelow := false
		for i := range repo.Worktrees {
			wt := &repo.Worktrees[i]
			wtMatch[wt.Path] = repoMatch || f.worktree(wt)
			vis := wtMatch[wt.Path]
			for j := range m.bound.byWT[wt.Path] {
				if f.session(&m.bound.byWT[wt.Path][j]) {
					vis = true
				}
			}
			wtVisible[wt.Path] = vis
			anyBelow = anyBelow || vis
		}
		if !repoMatch && !anyBelow {
			continue
		}
		ri := len(rows)
		rows = append(rows, row{kind: rowRepo, key: rkey, parent: -1, repo: repo, forced: f.active() && !repoMatch})
		wtErrs := map[string][]string{}
		var repoErrs []string
		for _, e := range splitErr(repo.Err) {
			msg := firstLine(e.Error()) // matched against raw paths; sanitised when rendered
			matched := false
			for _, wt := range repo.Worktrees {
				if strings.HasPrefix(msg, "status "+wt.Path+":") {
					wtErrs[wt.Path] = append(wtErrs[wt.Path], msg)
					matched = true
					break
				}
			}
			if !matched {
				repoErrs = append(repoErrs, msg)
			}
		}
		rows[ri].expandable = len(repo.Worktrees) > 0 || len(repoErrs) > 0
		if m.collapsed[rkey] && (repoMatch || !f.active()) {
			continue
		}
		if !repoMatch {
			repoErrs = nil
		}
		for i, msg := range repoErrs {
			rows = append(rows, row{kind: rowError, key: fmt.Sprintf("er:%s:%d", repo.Path, i), parent: ri, depth: 1, repo: repo, err: msg})
		}
		for i := range repo.Worktrees {
			wt := &repo.Worktrees[i]
			if !wtVisible[wt.Path] {
				continue
			}
			wkey := "w:" + wt.Path
			wi := len(rows)
			ss := m.bound.byWT[wt.Path]
			errs := wtErrs[wt.Path]
			selfMatch := wtMatch[wt.Path]
			if !selfMatch {
				var keep []sessions.Session
				for j := range ss {
					if f.session(&ss[j]) {
						keep = append(keep, ss[j])
					}
				}
				ss, errs = keep, nil
			}
			rows = append(rows, row{
				kind: rowWorktree, key: wkey, parent: ri, depth: 1, repo: repo, wt: wt, wtPath: wt.Path,
				expandable: len(ss) > 0 || len(errs) > 0, forced: f.active() && !selfMatch,
			})
			if m.collapsed[wkey] && (selfMatch || !f.active()) {
				continue
			}
			for j, msg := range errs {
				rows = append(rows, row{kind: rowError, key: fmt.Sprintf("ew:%s:%d", wt.Path, j), parent: wi, depth: 2, repo: repo, wt: wt, wtPath: wt.Path, err: msg})
			}
			shown := ss
			if len(ss) > maxSessionsShown && !m.showAll[wt.Path] && !f.active() {
				shown = ss[:maxSessionsShown]
			}
			for j := range shown {
				s := &shown[j]
				rows = append(rows, row{kind: rowSession, key: "s:" + string(s.Agent) + ":" + s.ID, parent: wi, depth: 2, repo: repo, wt: wt, wtPath: wt.Path, sess: s})
			}
			if n := len(ss) - len(shown); n > 0 {
				rows = append(rows, row{kind: rowMore, key: "m:" + wt.Path, parent: wi, depth: 2, repo: repo, wt: wt, wtPath: wt.Path, more: n})
			}
		}
	}
	return rows
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (m *Model) w() int {
	if m.width > 0 {
		return m.width
	}
	return defaultWidth
}

// rawBodyHeight is the room between header and status line left by the
// footer (message, dialog, filter input), before the key hint bar.
func (m *Model) rawBodyHeight() int {
	h := m.height
	if h <= 0 {
		h = defaultHeight
	}
	h -= 2 // header + status line
	return h - len(m.footer(m.w()))
}

// bodyHeight is the number of tree rows that fit between header and footer
// (including the key hint bar, when shown).
func (m *Model) bodyHeight() int {
	h := m.rawBodyHeight()
	if m.hintsVisible() {
		h--
	}
	if h < 1 {
		h = 1
	}
	return h
}

// View implements tea.Model.
func (m *Model) View() string {
	w := m.w()
	var b strings.Builder
	b.WriteString(fit(m.header(), w))
	b.WriteByte('\n')

	body := m.body(w)
	if m.overlay != nil {
		body = m.overlayBody(w)
	}
	b.WriteString(body)
	for _, l := range m.footer(w) {
		b.WriteString(fit(l, w))
		b.WriteByte('\n')
	}
	if m.hintsVisible() {
		b.WriteString(m.hintBar(w))
		b.WriteByte('\n')
	}
	status := m.statusLine()
	if m.overlay != nil {
		status = m.overlayStatus()
	}
	line := fit(styleStatus.Render(status), w)
	if !m.hintsVisible() {
		// no hint bar: the update label goes on the status line if it fits
		line, _ = withRightLabel(line, m.updateLabel(), w)
	}
	b.WriteString(line)
	return b.String()
}

// footer returns the lines between the tree and the status line: the
// (wrapped) message, then the open dialog if any.
func (m *Model) footer(w int) []string {
	var out []string
	if m.message != "" {
		style := styleDim
		if m.msgErr {
			style = styleMsgErr
		}
		lines := strings.Split(ansi.Wrap(m.message, w, " "), "\n")
		if len(lines) > maxMessageLines {
			lines = lines[:maxMessageLines]
		}
		for _, l := range lines {
			out = append(out, style.Render(l))
		}
	}
	if d := m.dialog; d != nil {
		for i, l := range d.lines {
			l = sanitize(l)
			if i == 0 {
				l = styleHeader.Render(l)
			}
			out = append(out, l)
		}
		if d.kind == dlgInput {
			out = append(out, d.input.View())
		}
		if t := d.toggle; t != nil {
			mark := "[ ]"
			if t.on {
				mark = "[x]"
			}
			out = append(out, sanitize(mark+" "+t.label))
		}
		out = append(out, styleDim.Render(sanitize(d.hint)))
	}
	if m.filtering {
		out = append(out, m.filterInput.View(), styleDim.Render("type to filter · enter keep · esc clear"))
	}
	return out
}

func (m *Model) header() string {
	roots := make([]string, len(m.deps.Roots))
	for i, r := range m.deps.Roots {
		roots[i] = pathx.AbbrevHome(r, m.deps.Home)
	}
	var h string
	if m.scoped {
		h = styleHeader.Render("grove") + "  repo: " + sanitize(m.scopeName())
	} else {
		h = styleHeader.Render("grove") + "  all repos: " + strings.Join(roots, ", ")
	}
	if m.loading() {
		h += "  " + m.spinner() + " " + m.loadingWhat()
	}
	return h
}

// scopeName names the started-in repo: as scanned once loaded, else the
// directory name.
func (m *Model) scopeName() string {
	for _, r := range m.repos {
		if r.Name != "" && m.isScopeRepo(r) {
			return r.Name
		}
	}
	return filepath.Base(m.deps.Scope)
}

func (m *Model) spinner() string { return spinFrames[m.spinFrame%len(spinFrames)] }

func (m *Model) loadingWhat() string {
	switch {
	case m.scanning && m.sessLoading:
		return "scanning repos and sessions…"
	case m.scanning:
		return "scanning repos…"
	default:
		return "loading sessions…"
	}
}

func (m *Model) body(w int) string {
	h := m.bodyHeight()
	var b strings.Builder
	if len(m.rows) == 0 {
		var msg string
		if m.loading() {
			msg = m.spinner() + " Loading…"
		} else if m.filter != "" {
			msg = "No matches for filter " + fmt.Sprintf("%q", sanitize(m.filter)) + " (esc clears)."
		} else {
			return m.emptyBody(w, h)
		}
		b.WriteString(fit(msg, w))
		b.WriteByte('\n')
		return b.String()
	}
	end := m.offset + h
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := m.offset; i < end; i++ {
		line := m.renderRow(m.rows[i], w)
		if i == m.cursor {
			line = styleSelected.Render(ansi.Strip(line) + strings.Repeat(" ", max(0, w-ansi.StringWidth(line))))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// emptyBody explains an empty, settled, unfiltered scan: the discovery
// errors (e.g. an unreadable root) if any, otherwise what was scanned. It
// renders at most h lines.
func (m *Model) emptyBody(w, h int) string {
	var lines []string
	if len(m.discErrs) == 0 {
		msg := "No git repositories found."
		if len(m.deps.Roots) > 0 {
			roots := make([]string, len(m.deps.Roots))
			for i, r := range m.deps.Roots {
				roots[i] = sanitize(pathx.AbbrevHome(r, m.deps.Home))
			}
			msg = fmt.Sprintf("No git repositories found under %s (depth %d).", strings.Join(roots, ", "), m.scanDepth())
		}
		lines = append(lines, msg)
	} else {
		lines = append(lines, "No git repositories found.")
		perm := false
		for _, err := range m.discErrs {
			lines = append(lines, styleErr.Render("✗ "+m.discErrText(err)))
			perm = perm || errors.Is(err, os.ErrPermission)
		}
		if perm && runtime.GOOS == "darwin" {
			for _, l := range strings.Split(ansi.Wordwrap(macPermHint, w, ""), "\n") {
				lines = append(lines, styleDim.Render(l))
			}
		}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(fit(l, w))
		b.WriteByte('\n')
	}
	return b.String()
}

const macPermHint = "macOS may be blocking this terminal from ~/Documents: System Settings → Privacy & Security → Files and Folders / Full Disk Access."

// scanDepth is the effective discovery depth (see scan.Options.MaxDepth).
func (m *Model) scanDepth() int {
	switch d := m.deps.MaxDepth; {
	case d == 0:
		return scan.DefaultMaxDepth
	case d < 0:
		return 0
	default:
		return d
	}
}

// discErrText renders a discovery error as "cannot read <dir>: <cause>",
// abbreviating the path; other errors are shown as-is.
func (m *Model) discErrText(err error) string {
	var de *scan.DirError
	if !errors.As(err, &de) {
		return sanitize(err.Error())
	}
	cause := de.Err
	var pe *fs.PathError
	if errors.As(cause, &pe) && pe.Err != nil {
		cause = pe.Err
	}
	if cause == nil {
		return sanitize(err.Error())
	}
	return sanitize("cannot read " + pathx.AbbrevHome(de.Path, m.deps.Home) + ": " + cause.Error())
}

func (m *Model) marker(r row) string {
	if !r.expandable {
		return "  "
	}
	if m.collapsed[r.key] && !r.forced {
		return "▸ "
	}
	return "▾ "
}

func (m *Model) renderRow(r row, w int) string {
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
	case rowRepo:
		s := indent + m.marker(r) + styleRepo.Render(sanitize(r.repo.Name))
		n := len(r.repo.Worktrees)
		s += styleDim.Render(fmt.Sprintf("  %d worktree%s", n, plural(n)))
		if r.repo.Err != nil {
			s += "  " + styleErr.Render("!")
		}
		if l := m.busy["r:"+r.repo.Path]; l != "" {
			s += "  " + styleBusy.Render(m.spinner()+" "+l+"…")
		}
		return fit(s, w)
	case rowWorktree:
		prefix := indent + m.marker(r)
		return fit(prefix+m.worktreeText(r.wt, w-ansi.StringWidth(prefix)), w)
	case rowSession:
		return m.sessionText(indent, r.sess, w)
	case rowMore:
		return fit(indent+"  "+styleDim.Render(fmt.Sprintf("+%d more (enter)", r.more)), w)
	case rowError:
		return fit(indent+"  "+styleErr.Render("! "+sanitize(r.err)), w)
	}
	return ""
}

// display renders p relative to its root, else ~-abbreviated. Both the
// configured and the canonical forms of roots and home are tried because scan
// reports canonical paths (e.g. /private/var vs /var).
func (m *Model) display(p string) string {
	for i, root := range m.deps.Roots {
		for _, r := range []string{root, m.rootsCanon[i]} {
			if r != "" && pathx.IsWithin(r, p) {
				return pathx.Display(p, r, "")
			}
		}
	}
	if m.homeCanon != "" && m.homeCanon != m.deps.Home && pathx.IsWithin(m.homeCanon, p) {
		return pathx.AbbrevHome(p, m.homeCanon)
	}
	return pathx.AbbrevHome(p, m.deps.Home)
}

// worktreeText renders a worktree row within w cells; the path is
// middle-truncated first so branch and status flags stay visible.
func (m *Model) worktreeText(wt *gitx.Worktree, w int) string {
	var parts []string
	if wt.IsMain && !wt.IsBare {
		parts = append(parts, styleFlag.Render(primaryMarker))
	}
	switch {
	case wt.IsBare:
		parts = append(parts, styleDim.Render("(bare)"))
	case wt.Detached():
		parts = append(parts, styleDetached.Render("detached @ "+sanitize(wt.ShortHEAD())))
	default:
		parts = append(parts, styleBranch.Render(sanitize(wt.Branch)))
	}
	switch st := wt.Status; {
	case wt.Prunable:
		parts = append(parts, styleErr.Render("missing"))
	case st == nil:
		if !wt.IsBare {
			parts = append(parts, styleDim.Render("?"))
		}
	case st.Changed > 0:
		parts = append(parts, styleDirty.Render(fmt.Sprintf("dirty (%d)", st.Changed)))
	default:
		parts = append(parts, styleClean.Render("clean"))
	}
	if st := wt.Status; st != nil && st.HasUpstream && (st.Ahead > 0 || st.Behind > 0) {
		parts = append(parts, fmt.Sprintf("↑%d ↓%d", st.Ahead, st.Behind))
	}
	if wt.Locked {
		parts = append(parts, styleFlag.Render("[locked]"))
	}
	if wt.Prunable {
		parts = append(parts, styleFlag.Render("[prunable]"))
	}
	if m.bound.liveWT[wt.Path] {
		parts = append(parts, styleLive.Render("●"))
	}
	if l := m.busy[wt.Path]; l != "" {
		parts = append(parts, styleBusy.Render(m.spinner()+" "+l+"…"))
	}
	rest := strings.Join(parts, "  ")
	path := sanitize(m.display(wt.Path))
	budget := w - 2 - ansi.StringWidth(rest)
	if budget < minPathWidth {
		budget = minPathWidth
	}
	return middleTruncate(path, budget) + "  " + rest
}

// primaryMarker tags the main (primary) worktree, distinct from a branch
// that happens to be called "main".
const primaryMarker = "★"

const minPathWidth = 12

// middleTruncate shortens plain s to at most w cells, keeping both ends.
func middleTruncate(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return ansi.Truncate(s, w, "")
	}
	rs := []rune(s)
	keepHead := (w - 1) / 2
	head := ansi.Truncate(s, keepHead, "")
	tailW := w - 1 - ansi.StringWidth(head)
	var tail []rune
	for i := len(rs) - 1; i >= 0; i-- {
		cand := string(rs[i:])
		if ansi.StringWidth(cand) > tailW {
			break
		}
		tail = rs[i:]
	}
	return head + "…" + string(tail)
}

// sanitize drops control runes (ESC, C0, C1, DEL) from transcript- or
// git-derived text so it cannot inject terminal sequences or skew widths.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// sessionText renders a session row, truncating the title so the relative
// time and live marker stay visible.
func (m *Model) sessionText(indent string, s *sessions.Session, w int) string {
	id := sanitize(s.ID)
	if len(id) > 8 {
		id = id[:8]
	}
	left := fmt.Sprintf("%s  %-6s %-8s  ", indent, s.Agent, id)
	right := "  " + relTime(m.deps.Now().Sub(s.LastActivity))
	if s.Live {
		switch {
		case s.Agent != sessions.AgentClaude:
			right += "  " + styleLive.Render("●")
		case s.Busy:
			right += "  " + styleLive.Render("● busy")
		default:
			right += "  " + styleDim.Render("○ idle")
		}
	}
	title := sanitize(s.Title)
	if title == "" {
		title = styleDim.Render("(no title)")
	}
	avail := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if avail < 1 {
		return fit(left+right, w)
	}
	title = ansi.Truncate(title, avail, "…")
	pad := avail - ansi.StringWidth(title)
	return fit(left+title+strings.Repeat(" ", pad)+styleDim.Render(right), w)
}

// relTime formats a duration as "3m ago", "2h ago", "5d ago".
func relTime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

func (m *Model) statusLine() string {
	nwt := 0
	for _, r := range m.repos {
		nwt += len(r.Worktrees)
	}
	nsess := 0
	for _, ss := range m.bound.byWT {
		nsess += len(ss)
	}
	parts := []string{
		fmt.Sprintf("%d repo%s", len(m.repos), plural(len(m.repos))),
		fmt.Sprintf("%d worktree%s", nwt, plural(nwt)),
		fmt.Sprintf("%d session%s", nsess, plural(nsess)),
	}
	if m.bound.live > 0 {
		parts = append(parts, fmt.Sprintf("%d live", m.bound.live))
	}
	if n := m.errorCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d error%s", n, plural(n)))
	}
	if n := m.skipped[sessions.AgentCodex]; n > 0 {
		parts = append(parts, fmt.Sprintf("codex: %d compressed session%s skipped", n, plural(n)))
	}
	for _, w := range m.deps.Warnings {
		parts = append(parts, "warning: "+sanitize(w))
	}
	if isUnavailable(m.liveErr) {
		parts = append(parts, "process detection unavailable")
	}
	if m.filter != "" {
		parts = append([]string{fmt.Sprintf("filter %q", sanitize(m.filter))}, parts...)
	}
	s := strings.Join(parts, " · ")
	// The keys are normally in the hint bar above; on a terminal too short
	// for it, keep the essentials here when they fit.
	if m.dialog == nil && !m.filtering && !m.hintsVisible() {
		if hint := " · r refresh · ? help · q quit"; ansi.StringWidth(s+hint) <= m.w() {
			s += hint
		}
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// fit truncates s (which may contain ANSI styles) to at most w cells.
func fit(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}
