package codex

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

// TestMain points HOME at a throwaway dir so no test can touch ~/.codex.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "grove-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("CODEX_HOME")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

var _ sessions.Source = (*Source)(nil)

// Line builders modelled on Codex rollouts (docs/research.md).

func j(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func rec(typ string, payload map[string]any) string {
	return j(map[string]any{"timestamp": "2026-10-31T07:48:17.472Z", "type": typ, "payload": payload})
}

func meta(id, cwd string, extra map[string]any) string {
	p := map[string]any{
		"id": id, "timestamp": "2026-10-31T07:48:17.472Z", "cwd": cwd, "originator": "codex_cli_rs",
		"cli_version": "0.50.0", "source": "cli", "git": map[string]any{"branch": "feat-x", "commit_hash": "abc"},
	}
	for k, v := range extra {
		p[k] = v
	}
	return rec("session_meta", p)
}

func turnCtx(cwd string) string {
	return rec("turn_context", map[string]any{"cwd": cwd, "approval_policy": "on-request", "model": "gpt-5"})
}

func userMsg(text string) string {
	return rec("event_msg", map[string]any{"type": "user_message", "message": text, "images": []any{}})
}

// responseUser is the injected response_item user message that must not be used as a title.
func responseUser(text string) string {
	return rec("response_item", map[string]any{"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}}})
}

func agentMsg(text string) string {
	return rec("event_msg", map[string]any{"type": "agent_message", "message": text})
}

const (
	u1 = "11111111-1111-1111-1111-111111111111"
	u2 = "22222222-2222-2222-2222-222222222222"
	u3 = "33333333-3333-3333-3333-333333333333"
	u4 = "44444444-4444-4444-4444-444444444444"
	u5 = "55555555-5555-5555-5555-555555555555"
	u6 = "66666666-6666-6666-6666-666666666666"
	u7 = "77777777-7777-7777-7777-777777777777"
	u8 = "88888888-8888-8888-8888-888888888888"
)

func rolloutName(uuid string) string { return "rollout-2026-10-31T07-48-17-" + uuid + ".jsonl" }

func dayRel(uuid string) string {
	return filepath.Join("sessions", "2026", "10", "31", rolloutName(uuid))
}

type fixture struct {
	t   *testing.T
	dir string // fake ~/.codex
}

func newFixture(t *testing.T) *fixture { return &fixture{t: t, dir: t.TempDir()} }

func (f *fixture) write(rel string, mtime time.Time, lines ...string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, rel)
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

func (f *fixture) list(since time.Time, wantSkipped int) map[string]sessions.Session {
	f.t.Helper()
	ss, skipped, err := New(f.dir).List(context.Background(), since)
	if err != nil {
		f.t.Fatalf("List: %v", err)
	}
	if skipped != wantSkipped {
		f.t.Errorf("skipped = %d, want %d", skipped, wantSkipped)
	}
	m := make(map[string]sessions.Session, len(ss))
	for _, s := range ss {
		m[s.ID] = s
	}
	return m
}

func TestListCases(t *testing.T) {
	f := newFixture(t)
	now := time.Now().Truncate(time.Second)
	normal := f.write(dayRel(u1), now.Add(-time.Hour),
		meta(u1, "/x/repo", map[string]any{"session_id": "sess-1"}),
		responseUser("<user_instructions>AGENTS.md stuff</user_instructions>"),
		responseUser("<environment_context><cwd>/x/repo</cwd></environment_context>"),
		turnCtx("/x/repo"),
		userMsg("<environment_context>injected</environment_context>"),
		userMsg("  fix   the\nflaky test  "),
		agentMsg("ok"),
		`{"broken json`,
		"not json at all",
		turnCtx("/x/repo-wt/sub"),
		userMsg("second prompt"),
	)
	// payload.id wins over session_id (above); session_id absent → id; no
	// turn_context → session_meta cwd.
	f.write(dayRel(u2), time.Time{}, meta(u2, "/y", nil), userMsg("hello"))
	// Subagents excluded.
	f.write(dayRel(u3), time.Time{},
		meta(u3, "/y", map[string]any{"source": map[string]any{"subagent": "review"}}), userMsg("sub"))
	f.write(dayRel(u4), time.Time{},
		meta(u4, "/y", map[string]any{"thread_source": "subagent"}), userMsg("sub"))
	// No session_meta → filename id + turn_context cwd; meta without cwd → ignored.
	f.write(dayRel(u5), time.Time{}, turnCtx("/z"), userMsg("orphan"))
	f.write(dayRel(u6), time.Time{}, meta(u6, "", nil), userMsg("nocwd"))
	// Archived (flat) rollout.
	f.write(filepath.Join("archived_sessions", rolloutName(u7)), time.Time{}, meta(u7, "/arch", nil), turnCtx("/arch2"))
	// Compressed: one alone (counted), one with a .jsonl sibling (not counted).
	f.write(filepath.Join("sessions", "2026", "10", "30", "rollout-2026-10-30T01-00-00-"+u8+".jsonl.zst"), time.Time{}, "zstd-bytes")
	f.write(dayRel(u1)+".zst", time.Time{}, "zstd-bytes")
	// Non-rollout files ignored.
	f.write(filepath.Join("sessions", "2026", "10", "31", "notes.jsonl"), time.Time{}, meta("n", "/n", nil))
	f.write("history.jsonl", time.Time{}, meta("h", "/h", nil))

	got := f.list(time.Time{}, 1)
	if len(got) != 4 {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	s := got[u1]
	want := sessions.Session{Agent: sessions.AgentCodex, ID: u1, Cwd: "/x/repo-wt/sub",
		Title: "fix the flaky test", LastActivity: now.Add(-time.Hour), Path: normal}
	if !s.LastActivity.Equal(want.LastActivity) {
		t.Errorf("LastActivity = %v, want %v", s.LastActivity, want.LastActivity)
	}
	s.LastActivity = want.LastActivity
	if s != want {
		t.Errorf("normal:\n got %+v\nwant %+v", s, want)
	}
	if s := got[u2]; s.Cwd != "/y" || s.Title != "hello" {
		t.Errorf("no session_id: %+v", s)
	}
	if s := got[u7]; s.Cwd != "/arch2" || s.Title != "" {
		t.Errorf("archived: %+v", s)
	}
	if s := got[u5]; s.Cwd != "/z" || s.Title != "orphan" {
		t.Errorf("no meta: %+v", s)
	}
}

func TestSessionIDFallback(t *testing.T) {
	f := newFixture(t)
	f.write(dayRel(u1), time.Time{}, rec("session_meta", map[string]any{"session_id": "s-only", "cwd": "/a"}))
	if got := f.list(time.Time{}, 0); got["s-only"].Cwd != "/a" {
		t.Fatalf("got %+v", got)
	}
}

func TestSourceFilter(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		extra map[string]any
		keep  bool
	}{
		u1: {map[string]any{"source": "vscode"}, true},
		u2: {map[string]any{"source": map[string]any{"custom": "my-tool"}}, true},
		u3: {map[string]any{"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{}}}}, false},
		u4: {map[string]any{"source": map[string]any{"internal": "x"}}, false},
		u5: {map[string]any{"thread_source": "subagent"}, false},
		u6: {map[string]any{"thread_source": "guardian_review"}, false},
		u7: {map[string]any{"thread_source": "memory_consolidation"}, false},
		u8: {map[string]any{"thread_source": "user"}, true},
	}
	for id, c := range cases {
		f.write(dayRel(id), time.Time{}, meta(id, "/a", c.extra))
	}
	got := f.list(time.Time{}, 0)
	for id, c := range cases {
		if _, ok := got[id]; ok != c.keep {
			t.Errorf("%s %v: kept=%v want %v", id, c.extra, ok, c.keep)
		}
	}
}

func TestIDFromName(t *testing.T) {
	for in, want := range map[string]string{
		"rollout-2026-10-31T07-48-17-" + u1 + ".jsonl":            u1,
		"rollout-2026-10-31T07-48-17-" + u1 + "_" + u2 + ".jsonl": u1,
		"/x/y/rollout-2026-10-31T07-48-17-abc_def.jsonl":          "abc",
	} {
		if got := idFromName(in); got != want {
			t.Errorf("idFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupeByThreadKeepsNewest(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write(dayRel(u1), now.Add(-3*time.Hour), meta(u1, "/old", nil))
	// Revert: new rollout file for the same thread.
	newer := f.write(filepath.Join("sessions", "2026", "10", "31", "rollout-2026-10-31T09-00-00-"+u1+"_"+u2+".jsonl"),
		now.Add(-time.Hour), meta(u1, "/new", nil))
	// Same thread also archived, older.
	f.write(filepath.Join("archived_sessions", rolloutName(u1)), now.Add(-2*time.Hour), meta(u1, "/arch", nil))
	ss, _, err := New(f.dir).List(context.Background(), time.Time{})
	if err != nil || len(ss) != 1 || ss[0].Cwd != "/new" || ss[0].Path != newer {
		t.Fatalf("got %+v err %v", ss, err)
	}
}

// Real session_meta lines carry large base_instructions.
func TestLargeSessionMeta(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("i", 300<<10)
	f.write(dayRel(u1), time.Time{},
		meta(u1, "/meta-cwd", map[string]any{"base_instructions": map[string]any{"text": big}, "source": "cli"}),
		userMsg("big title"))
	f.write(dayRel(u2), time.Time{},
		meta(u2, "/x", map[string]any{"base_instructions": big, "source": map[string]any{"subagent": "x"}}))
	if s := f.list(time.Time{}, 0)[u1]; s.Cwd != "/meta-cwd" || s.Title != "big title" {
		t.Errorf("300KiB meta: %+v", s)
	}
}

// A session_meta beyond MetaLimit falls back to filename id + turn_context
// cwd; with no cwd at all it is counted as skipped.
func TestOversizedSessionMeta(t *testing.T) {
	f := newFixture(t)
	huge := strings.Repeat("i", MetaLimit+1)
	f.write(dayRel(u1), time.Time{}, meta("ignored", "/meta", map[string]any{"base_instructions": huge}),
		turnCtx("/tc"), userMsg("hi"))
	f.write(dayRel(u2), time.Time{}, meta("ignored", "/meta", map[string]any{"base_instructions": huge}),
		userMsg("no cwd"))
	got := f.list(time.Time{}, 1)
	if len(got) != 1 || got[u1].Cwd != "/tc" || got[u1].Title != "hi" {
		t.Errorf("got %+v", got)
	}
}

func TestMissingIDsFallBackToFilename(t *testing.T) {
	f := newFixture(t)
	f.write(dayRel(u1), time.Time{}, rec("session_meta", map[string]any{"cwd": "/a", "source": "exec"}))
	got := f.list(time.Time{}, 0)
	if got[u1].Cwd != "/a" {
		t.Fatalf("got %+v", got)
	}
}

func TestOrderAndTitleTruncation(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write(dayRel(u1), now.Add(-2*time.Hour), meta(u1, "/a", nil), userMsg(strings.Repeat("é", 200)))
	f.write(dayRel(u2), now.Add(-time.Hour), meta(u2, "/a", nil))
	ss, _, err := New(f.dir).List(context.Background(), time.Time{})
	if err != nil || len(ss) != 2 || ss[0].ID != u2 || ss[1].ID != u1 {
		t.Fatalf("order: %+v %v", ss, err)
	}
	if r := []rune(ss[1].Title); len(r) != sessions.TitleMaxRunes || r[len(r)-1] != '…' {
		t.Errorf("title not truncated: %q", ss[1].Title)
	}
}

func TestHistoryWindowNotOpened(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.write(dayRel(u1), now.Add(-24*time.Hour), meta(u1, "/a", nil))
	oldPath := f.write(filepath.Join("sessions", "2026", "09", "01", rolloutName(u2)), now.Add(-40*24*time.Hour), meta(u2, "/a", nil))
	// A resumed rollout in an old day dir but with a fresh mtime is kept.
	f.write(filepath.Join("sessions", "2026", "08", "01", rolloutName(u3)), now.Add(-time.Hour), meta(u3, "/a", nil))
	// Old compressed rollouts are not counted.
	f.write(filepath.Join("sessions", "2026", "09", "01", "rollout-x-"+u4+".jsonl.zst"), now.Add(-40*24*time.Hour), "z")

	var n, oldOpened atomic.Int32
	src := &Source{Dir: f.dir, open: func(name string) (*os.File, error) {
		n.Add(1)
		if name == oldPath {
			oldOpened.Add(1)
		}
		return os.Open(name)
	}}
	ss, skipped, err := src.List(context.Background(), now.Add(-30*24*time.Hour))
	if err != nil || skipped != 0 {
		t.Fatal(err, skipped)
	}
	if len(ss) != 2 || ss[0].ID != u3 || ss[1].ID != u1 {
		t.Errorf("got %+v", ss)
	}
	if oldOpened.Load() != 0 || n.Load() != 2 {
		t.Errorf("opened %d files, old opened %d times", n.Load(), oldOpened.Load())
	}
	ss, skipped, _ = New(f.dir).List(context.Background(), time.Time{})
	if len(ss) != 3 || skipped != 1 {
		t.Errorf("unlimited window: got %d sessions, %d skipped", len(ss), skipped)
	}
}

// Large file: meta + title in head, last turn_context only in tail; a
// turn_context in the unread middle must not matter.
func TestLargeFileBoundedReads(t *testing.T) {
	f := newFixture(t)
	filler := agentMsg(strings.Repeat("z", 4<<10))
	lines := []string{meta(u1, "/start", nil), userMsg("title here"), turnCtx("/head")}
	for range 100 {
		lines = append(lines, filler)
	}
	lines = append(lines, turnCtx("/middle"))
	for range 100 {
		lines = append(lines, filler)
	}
	lines = append(lines, turnCtx("/tail"), filler)
	p := f.write(dayRel(u1), time.Time{}, lines...)
	fi, _ := os.Stat(p)
	if fi.Size() < HeadLimit+TailLimit+100<<10 {
		t.Fatalf("fixture too small: %d", fi.Size())
	}
	got := f.list(time.Time{}, 0)
	if s := got[u1]; s.Cwd != "/tail" || s.Title != "title here" {
		t.Errorf("got %+v", s)
	}

	// No turn_context in tail: last one from head wins over session_meta.
	lines = []string{meta(u2, "/start", nil), turnCtx("/head")}
	for range 200 {
		lines = append(lines, filler)
	}
	f.write(dayRel(u2), time.Time{}, lines...)
	if s := f.list(time.Time{}, 0)[u2]; s.Cwd != "/head" {
		t.Errorf("head fallback: %+v", s)
	}
}

// File between HeadLimit and HeadLimit+TailLimit: the line straddling the head
// boundary is stitched, not lost.
func TestHeadTailStitch(t *testing.T) {
	f := newFixture(t)
	lines := []string{meta(u1, "/start", nil)}
	size := len(lines[0]) + 1
	for size < HeadLimit-200 {
		l := agentMsg(strings.Repeat("y", 1000))
		lines = append(lines, l)
		size += len(l) + 1
	}
	tc := rec("turn_context", map[string]any{"cwd": "/stitched", "pad": strings.Repeat("p", 1000)})
	lines = append(lines, tc, agentMsg("end"))
	f.write(dayRel(u1), time.Time{}, lines...)
	if s := f.list(time.Time{}, 0)[u1]; s.Cwd != "/stitched" {
		t.Errorf("got %+v", s)
	}
}

func TestMissingDirAndCancel(t *testing.T) {
	ss, skipped, err := New(filepath.Join(t.TempDir(), "nope")).List(context.Background(), time.Time{})
	if err != nil || skipped != 0 || len(ss) != 0 {
		t.Errorf("missing dir: %v %d %v", ss, skipped, err)
	}
	f := newFixture(t)
	f.write(dayRel(u1), time.Time{}, meta(u1, "/a", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(f.dir).List(ctx, time.Time{}); err == nil {
		t.Error("cancelled ctx: want error")
	}
}

func TestUnreadableFileReported(t *testing.T) {
	f := newFixture(t)
	f.write(dayRel(u1), time.Time{}, meta(u1, "/a", nil))
	bad := f.write(dayRel(u2), time.Time{}, meta(u2, "/a", nil))
	src := &Source{Dir: f.dir, open: func(name string) (*os.File, error) {
		if name == bad {
			return nil, os.ErrPermission
		}
		return os.Open(name)
	}}
	ss, _, err := src.List(context.Background(), time.Time{})
	if err == nil || len(ss) != 1 || ss[0].ID != u1 {
		t.Errorf("got %+v err %v", ss, err)
	}
}

func TestGlobMetacharsInDir(t *testing.T) {
	f := &fixture{t: t, dir: filepath.Join(t.TempDir(), "we[ird]*?dir")}
	f.write(dayRel(u1), time.Time{}, meta(u1, "/a", nil))
	f.write(filepath.Join("archived_sessions", rolloutName(u2)), time.Time{}, meta(u2, "/b", nil))
	got := f.list(time.Time{}, 0)
	if len(got) != 2 || got[u1].Cwd != "/a" || got[u2].Cwd != "/b" {
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex")
	if d, err := DefaultDir(); err != nil || d != "/custom/codex" {
		t.Errorf("CODEX_HOME: %q %v", d, err)
	}
	t.Setenv("CODEX_HOME", "")
	home, _ := os.UserHomeDir()
	if d, err := DefaultDir(); err != nil || d != filepath.Join(home, ".codex") {
		t.Errorf("default: %q %v", d, err)
	}
}

func TestDetailPromptAndBranch(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("codex prompt ", 20) + "END"
	p := f.write(dayRel("019a0000-0000-7000-8000-00000000d001"), time.Time{},
		meta("019a0000-0000-7000-8000-00000000d001", "/x/repo", nil),
		userMsg(long), turnCtx("/x/repo"))
	src := New(f.dir)
	var _ sessions.Detailer = src
	d, err := src.Detail(context.Background(), sessions.Session{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if d.Prompt != long || d.GitBranch != "feat-x" {
		t.Errorf("detail = %+v", d)
	}
}
