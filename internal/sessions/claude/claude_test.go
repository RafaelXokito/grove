package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// TestMain points HOME at a throwaway dir so no test can touch ~/.claude.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "grove-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

var _ sessions.Source = (*Source)(nil)

// Line builders modelled on real Claude Code transcripts (docs/research.md).

func j(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func msg(typ, cwd string, content any, extra map[string]any) string {
	m := map[string]any{
		"type": typ, "cwd": cwd, "sessionId": "s", "timestamp": "2026-10-31T10:00:00.000Z",
		"gitBranch": "HEAD", "isSidechain": false, "uuid": "u", "version": "2.1.314",
		"message": map[string]any{"role": typ, "content": content},
	}
	for k, v := range extra {
		m[k] = v
	}
	return j(m)
}

func user(cwd string, content any, extra map[string]any) string {
	return msg("user", cwd, content, extra)
}

func assistant(cwd, text string) string {
	return msg("assistant", cwd, []any{map[string]any{"type": "text", "text": text}}, nil)
}

var (
	modeLine     = j(map[string]any{"type": "mode", "mode": "default"})
	bridgeLine   = j(map[string]any{"type": "bridge-session", "id": "b"})
	snapshotLine = j(map[string]any{"type": "file-history-snapshot", "messageId": "m", "snapshot": map[string]any{}})
)

func aiTitle(s string) string { return j(map[string]any{"type": "ai-title", "aiTitle": s}) }

func toolResult(cwd string) string {
	return user(cwd, []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": "ok"}},
		map[string]any{"toolUseResult": map[string]any{"stdout": "ok"}})
}

type fixture struct {
	t   *testing.T
	dir string // fake ~/.claude
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, dir: t.TempDir()}
}

func (f *fixture) write(rel string, mtime time.Time, lines ...string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, "projects", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			f.t.Fatal(err)
		}
	}
	return p
}

func (f *fixture) list(since time.Time) map[string]sessions.Session {
	f.t.Helper()
	ss, skipped, err := New(f.dir).List(context.Background(), since)
	if err != nil {
		f.t.Fatalf("List: %v", err)
	}
	if skipped != 0 {
		f.t.Errorf("skipped = %d", skipped)
	}
	m := make(map[string]sessions.Session, len(ss))
	for _, s := range ss {
		m[s.ID] = s
	}
	return m
}

func TestListCases(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write("-x-repo/normal.jsonl", now.Add(-time.Hour),
		modeLine, bridgeLine, snapshotLine,
		user("/x/repo", "fix the bug", nil),
		assistant("/x/repo", "done"))
	f.write("-x-repo/cd.jsonl", now,
		user("/x/repo", "go to wt", nil),
		assistant("/x/repo", "cd"),
		toolResult("/x/repo-wt"),
		assistant("/x/repo-wt/sub", "working"),
		j(map[string]any{"type": "last-prompt", "lastPrompt": "x"}))
	f.write("-x-repo/meta.jsonl", now,
		user("/x/repo", "<command-name>/clear</command-name>", nil),
		user("/x/repo", "Caveat: injected", map[string]any{"isMeta": true}),
		user("/x/repo", "  <local-command-stdout>x</local-command-stdout>", nil),
		toolResult("/x/repo"),
		user("/x/repo", "sidechain prompt", map[string]any{"isSidechain": true}),
		user("/x/repo", "not human", map[string]any{"origin": map[string]any{"kind": "task-notification"}}),
		user("/x/repo", "real\n  prompt\there", map[string]any{"origin": map[string]any{"kind": "human"}}),
		user("/x/repo", "second prompt", nil))
	f.write("-x-repo/titled.jsonl", now,
		user("/x/repo", "the prompt", nil),
		aiTitle("Old title"),
		assistant("/x/repo", "a"),
		aiTitle("Fix login redirect"))
	f.write("-x-repo/malformed.jsonl", now,
		`{"type":"user","cwd":"/x/broken"`,
		`not json at all`,
		``,
		user("/x/repo", "after garbage", nil),
		`{"type":"assistant","cwd":`)
	f.write("-x-repo/image.jsonl", now,
		toolResult("/x/repo"),
		user("/x/repo", []any{map[string]any{"type": "text", "text": "<ide_opened_file>x</ide_opened_file>"}}, nil),
		user("/x/repo", []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "AAAA"}},
			map[string]any{"type": "text", "text": "what is in this screenshot"},
		}, nil))
	f.write("-x-repo/nocwd.jsonl", now, modeLine, aiTitle("orphan"), snapshotLine)
	f.write("-x-repo/sidecwd.jsonl", now,
		user("/x/repo", "main prompt", nil),
		msg("assistant", "/x/side", "s", map[string]any{"isSidechain": true}))
	// Excluded locations.
	f.write("-x-repo/normal/subagents/agent-abc.jsonl", now,
		user("/x/repo", "subagent", map[string]any{"isSidechain": true}))
	f.write("-x-repo/memory/notes.jsonl", now, user("/x/repo", "memory", nil))
	f.write("-x-repo/normal/subagents/agent-abc.meta.json", now, `{}`)

	got := f.list(time.Time{})
	want := map[string]struct{ cwd, title string }{
		"normal":    {"/x/repo", "fix the bug"},
		"cd":        {"/x/repo-wt/sub", "go to wt"},
		"meta":      {"/x/repo", "real prompt here"},
		"titled":    {"/x/repo", "Fix login redirect"},
		"malformed": {"/x/repo", "after garbage"},
		"sidecwd":   {"/x/repo", "main prompt"},
		"image":     {"/x/repo", "what is in this screenshot"},
	}
	if len(got) != len(want) {
		t.Errorf("got %d sessions %v, want %d", len(got), keys(got), len(want))
	}
	for id, w := range want {
		s, ok := got[id]
		if !ok {
			t.Errorf("session %q missing", id)
			continue
		}
		if s.Cwd != w.cwd || s.Title != w.title {
			t.Errorf("%s: cwd=%q title=%q, want cwd=%q title=%q", id, s.Cwd, s.Title, w.cwd, w.title)
		}
		if s.Agent != sessions.AgentClaude || s.Path == "" || s.LastActivity.IsZero() {
			t.Errorf("%s: incomplete session %+v", id, s)
		}
	}
	if s := got["normal"]; s.LastActivity.Sub(now.Add(-time.Hour)).Abs() > time.Second {
		t.Errorf("LastActivity = %v, want file mtime", s.LastActivity)
	}

	// Binding a cd'ed session goes to the worktree it ended in (prefix trap).
	b := sessions.NewBinder([]string{"/x/repo", "/x/repo-wt"})
	if wt, _ := b.Bind(got["cd"].Cwd); wt != "/x/repo-wt" {
		t.Errorf("cd session bound to %q", wt)
	}
}

func keys(m map[string]sessions.Session) []string {
	var k []string
	for id := range m {
		k = append(k, id)
	}
	return k
}

func TestListOrderAndTitleTruncation(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write("p/old.jsonl", now.Add(-2*time.Hour), user("/a", strings.Repeat("word ", 40), nil))
	f.write("p/new.jsonl", now, user("/a", "short", nil))
	ss, _, err := New(f.dir).List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != "new" || ss[1].ID != "old" {
		t.Fatalf("order: %+v", ss)
	}
	if n := len([]rune(ss[1].Title)); n != sessions.TitleMaxRunes || !strings.HasSuffix(ss[1].Title, "…") {
		t.Errorf("title not truncated to %d runes: %d %q", sessions.TitleMaxRunes, n, ss[1].Title)
	}
}

func TestHistoryWindowNotOpened(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write("p/recent.jsonl", now.Add(-24*time.Hour), user("/a", "recent", nil))
	oldPath := f.write("p/old.jsonl", now.Add(-40*24*time.Hour), user("/a", "old", nil))

	var n, oldOpened atomic.Int32
	src := &Source{Dir: f.dir, open: func(name string) (*os.File, error) {
		n.Add(1)
		if name == oldPath {
			oldOpened.Add(1)
		}
		return os.Open(name)
	}}
	ss, _, err := src.List(context.Background(), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != "recent" {
		t.Errorf("got %+v", ss)
	}
	if oldOpened.Load() != 0 || n.Load() != 1 {
		t.Errorf("opened %d files, old opened %d times", n.Load(), oldOpened.Load())
	}
	// Zero since: unlimited.
	ss, _, _ = New(f.dir).List(context.Background(), time.Time{})
	if len(ss) != 2 {
		t.Errorf("unlimited window: got %d sessions", len(ss))
	}
}

// pad returns an assistant line at cwd whose JSON is roughly n bytes.
func pad(cwd string, n int) string {
	return assistant(cwd, strings.Repeat("x", n))
}

func TestLargeFileBoundedReads(t *testing.T) {
	f := newFixture(t)
	var lines []string
	lines = append(lines, modeLine, user("/x/repo", "<command-name>/init</command-name>", nil))
	for range 70 { // ~280 KiB of filler before any real prompt
		lines = append(lines, pad("/x/repo", 4<<10))
	}
	lines = append(lines, user("/x/repo", "prompt past head limit", nil))
	for range 100 { // ~400 KiB middle, cwd moves away
		lines = append(lines, pad("/x/middle", 4<<10))
	}
	for range 5 {
		lines = append(lines, pad("/x/repo-wt/final", 4<<10))
	}
	p := f.write("p/big.jsonl", time.Time{}, lines...)

	fi, _ := os.Stat(p)
	if fi.Size() < 600<<10 {
		t.Fatalf("fixture too small: %d", fi.Size())
	}
	s := &Source{Dir: f.dir}
	ss, _, err := s.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions", len(ss))
	}
	if ss[0].Cwd != "/x/repo-wt/final" {
		t.Errorf("cwd = %q, want last cwd from tail", ss[0].Cwd)
	}
	if ss[0].Title != "" {
		t.Errorf("title = %q, want empty (prompt beyond head limit)", ss[0].Title)
	}
}

// When the tail holds no cwd (one huge metadata line), fall back to the last
// cwd found in the head, past leading no-cwd lines.
func TestLargeFileFirstCwdFallback(t *testing.T) {
	f := newFixture(t)
	big := j(map[string]any{"type": "file-history-snapshot", "blob": strings.Repeat("y", 400<<10)})
	f.write("p/fallback.jsonl", time.Time{},
		modeLine, snapshotLine,
		user("/x/first", "hello", nil),
		assistant("/x/second", "hi"),
		big)
	ss, _, err := New(f.dir).List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Cwd != "/x/second" || ss[0].Title != "hello" {
		t.Fatalf("got %+v", ss)
	}
}

// Tail boundaries: a line straddling the head/tail split (contiguous case)
// and a tail start falling exactly on a line start must both be parsed.
func TestTailBoundaries(t *testing.T) {
	for _, size := range []int{HeadLimit + 10, HeadLimit + TailLimit/2, HeadLimit + TailLimit + 1, HeadLimit + 3*TailLimit} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := newFixture(t)
			first := user("/x/a", "p", nil)
			last := assistant("/x/z", "end")
			fillN := size - len(first) - len(last) - 3
			filler := pad("/x/m", fillN-len(pad("/x/m", 0)))
			p := f.write("p/b.jsonl", time.Time{}, first, filler, last)
			fi, _ := os.Stat(p)
			if d := int(fi.Size()) - size; d < -2 || d > 2 {
				t.Fatalf("size %d want ~%d", fi.Size(), size)
			}
			ss, _, err := New(f.dir).List(context.Background(), time.Time{})
			if err != nil || len(ss) != 1 || ss[0].Cwd != "/x/z" {
				t.Fatalf("got %+v err %v", ss, err)
			}
		})
	}
	// Last line is exactly TailLimit bytes incl. newline: tail begins on a
	// line start and must not be dropped.
	f := newFixture(t)
	last := pad("/x/z", 0)
	last = pad("/x/z", TailLimit-1-len(last))
	if len(last) != TailLimit-1 {
		t.Fatalf("last len %d", len(last))
	}
	first := user("/x/a", "p", nil)
	filler := pad("/x/m", 300<<10)
	f.write("p/exact.jsonl", time.Time{}, first, filler, last)
	ss, _, _ := New(f.dir).List(context.Background(), time.Time{})
	if len(ss) != 1 || ss[0].Cwd != "/x/z" {
		t.Fatalf("exact boundary: got %+v", ss)
	}
}

func TestMissingDirAndCancel(t *testing.T) {
	ss, skipped, err := New(filepath.Join(t.TempDir(), "nope")).List(context.Background(), time.Time{})
	if err != nil || skipped != 0 || len(ss) != 0 {
		t.Errorf("missing dir: %v %d %v", ss, skipped, err)
	}
	f := newFixture(t)
	f.write("p/a.jsonl", time.Time{}, user("/a", "x", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(f.dir).List(ctx, time.Time{}); err == nil {
		t.Error("cancelled ctx: want error")
	}
}

func TestUnreadableFileReported(t *testing.T) {
	f := newFixture(t)
	f.write("p/ok.jsonl", time.Time{}, user("/a", "x", nil))
	bad := f.write("p/bad.jsonl", time.Time{}, user("/a", "y", nil))
	src := &Source{Dir: f.dir, open: func(name string) (*os.File, error) {
		if name == bad {
			return nil, os.ErrPermission
		}
		return os.Open(name)
	}}
	ss, _, err := src.List(context.Background(), time.Time{})
	if err == nil || len(ss) != 1 || ss[0].ID != "ok" {
		t.Errorf("got %+v err %v", ss, err)
	}
}

// Head-fallback with many no-cwd lines filling more than the tail window.
func TestFallbackHeadLastCwd(t *testing.T) {
	f := newFixture(t)
	lines := []string{user("/first", "hi", nil), assistant("/second", "ok")}
	noCwd := j(map[string]any{"type": "file-history-snapshot", "blob": strings.Repeat("z", 4<<10)})
	for range 100 { // ~400 KiB, beyond head and tail windows
		lines = append(lines, noCwd)
	}
	f.write("p/fb.jsonl", time.Time{}, lines...)
	ss, _, err := New(f.dir).List(context.Background(), time.Time{})
	if err != nil || len(ss) != 1 || ss[0].Cwd != "/second" {
		t.Fatalf("got %+v err %v", ss, err)
	}
}

func TestGlobMetacharsInDir(t *testing.T) {
	f := &fixture{t: t, dir: filepath.Join(t.TempDir(), "we[ird]*?dir")}
	f.write("-a[b]/s1.jsonl", time.Time{}, user("/a", "x", nil))
	f.write("-a[b]/notes.txt", time.Time{}, "x")
	got := f.list(time.Time{})
	if len(got) != 1 || got["s1"].Cwd != "/a" {
		t.Fatalf("got %+v", got)
	}
}
