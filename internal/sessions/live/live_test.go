package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// TestMain points HOME at a throwaway dir so no test can touch real agent state.
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

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func aliveSet(pids ...int) func(int) bool {
	return func(p int) bool {
		for _, x := range pids {
			if x == p {
				return true
			}
		}
		return false
	}
}

func regFixture(t *testing.T) string {
	dir := t.TempDir()
	writeFile(t, dir+"/100.json", `{"pid":100,"sessionId":"busy-s","cwd":"/r/a","procStart":"Thu Oct 29 20:40:07 2026","status":"busy","kind":"interactive"}`)
	writeFile(t, dir+"/101.json", `{"pid":101,"sessionId":"idle-s","cwd":"/r/b","procStart":"Thu Oct 29 19:08:41 2026","status":"idle"}`)
	writeFile(t, dir+"/102.json", `{"pid":102,"sessionId":"dead-s","cwd":"/r/c","status":"busy"}`)
	writeFile(t, dir+"/103.json", `{"pid":103,"sessionId":`) // malformed
	writeFile(t, dir+"/104.json", `{"pid":104,"sessionId":"reused","cwd":"/r/d","procStart":"Mon Jan  1 00:00:00 2024","status":"busy"}`)
	writeFile(t, dir+"/105.json", `{"sessionId":"from-name","cwd":"/r/e","status":"idle"}`) // pid from filename
	writeFile(t, dir+"/100.abc.key", `secret`)
	return dir
}

func TestRegistry(t *testing.T) {
	dir := regFixture(t)
	r := &Registry{
		Dir:   dir,
		Alive: aliveSet(100, 101, 103, 104, 105),
		StartTimes: func(_ context.Context, pids []int) (map[int]string, error) {
			return map[int]string{100: "Thu Oct 29 20:40:07 2026", 104: "Fri Oct 30 10:00:00 2026"}, nil
		},
	}
	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"busy-s": true, "idle-s": false, "from-name": false}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, p := range got {
		busy, ok := want[p.SessionID]
		if !ok || p.Busy != busy || p.Agent != sessions.AgentClaude {
			t.Errorf("unexpected %+v", p)
		}
		if p.SessionID == "from-name" && p.PID != 105 {
			t.Errorf("pid from filename: %+v", p)
		}
	}
}

func TestRegistryStartTimesFailureSkipsCheck(t *testing.T) {
	r := &Registry{
		Dir:        regFixture(t),
		Alive:      aliveSet(104),
		StartTimes: func(context.Context, []int) (map[int]string, error) { return nil, errors.New("ps gone") },
	}
	got, err := r.List(context.Background())
	if err != nil || len(got) != 1 || got[0].SessionID != "reused" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRegistryMissingDir(t *testing.T) {
	r := &Registry{Dir: filepath.Join(t.TempDir(), "nope"), Alive: aliveSet()}
	got, err := r.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

const psSample = `    1     0 /sbin/launchd
65015     1 /Users/u/.local/bin/claude daemon run --json-path /Users/u/.claude/daemon.json
65119 65015 claude bg-pty-host --bg-pty-host /tmp/cc/spare/0a.pty.sock 200 50 -- /Users/u/.local/share/claude/versions/2.1.316 --bg-spare /tmp/cc
65121 65119 claude bg-spare --bg-spare /tmp/cc/spare/0a.claim.sock
79258     1 /Users/u/.local/share/claude/ClaudeCode.app/Contents/MacOS/claude --bg-pty-host /tmp/cc/pty/0b.sock 196 47 -- /x
21805  1974 claude --remote-control
79345 79258 /Users/u/.local/share/claude/versions/2.1.312 --session-id 0bd00b45 --resume /x
 5000  4999 node /usr/local/lib/node_modules/@openai/codex/bin/codex.js
 5001  5000 /usr/local/lib/node_modules/@openai/codex/vendor/aarch64-apple-darwin/codex/codex
 5002     1 /opt/homebrew/bin/codex-aarch64-apple-darwin exec
 6000     1 /usr/bin/vim claude.md
garbage line
`

func TestParsePSClassify(t *testing.T) {
	got := map[int]sessions.Agent{}
	for _, e := range ParsePS([]byte(psSample)) {
		if ag, ok := Classify(e.Args); ok {
			got[e.PID] = ag
		}
	}
	want := map[int]sessions.Agent{21805: "claude", 79345: "claude", 5001: "codex", 5002: "codex"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

const lsofSample = "p21805\nfcwd\nn/Users/u/Documents/repos\np5001\nfcwd\nn/x/repo wt\n"

func TestParseLsof(t *testing.T) {
	got := ParseLsofCwd([]byte(lsofSample))
	want := map[int]string{21805: "/Users/u/Documents/repos", 5001: "/x/repo wt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestParsePSStart(t *testing.T) {
	got := ParsePSStart([]byte("21805 Thu Oct 29 20:40:07 2026\n  101 Mon Jan  5 09:00:00 2026\n"))
	if got[21805] != "Thu Oct 29 20:40:07 2026" || normSpace(got[101]) != "Mon Jan 5 09:00:00 2026" {
		t.Errorf("got %v", got)
	}
}

type fakeExec struct {
	out   map[string]string
	err   map[string]error
	calls []string
}

func (f *fakeExec) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return []byte(f.out[name]), f.err[name]
}

func TestPSScanner(t *testing.T) {
	fe := &fakeExec{
		out: map[string]string{"ps": psSample, "lsof": lsofSample},
		err: map[string]error{"lsof": errors.New("exit status 1")}, // some pid vanished
	}
	got, err := (&PSScanner{Exec: fe.run}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []sessions.LiveProc{
		{PID: 21805, Agent: "claude", Cwd: "/Users/u/Documents/repos"},
		{PID: 5001, Agent: "codex", Cwd: "/x/repo wt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if len(fe.calls) != 2 || fe.calls[1] != "lsof -a -p 21805,79345,5001,5002 -d cwd -Fn" {
		t.Errorf("calls %q", fe.calls)
	}
}

func TestListerScanFailureKeepsRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		fe   *fakeExec
	}{
		{"ps", &fakeExec{err: map[string]error{"ps": errors.New("not found")}}},
		{"lsof", &fakeExec{out: map[string]string{"ps": psSample}, err: map[string]error{"lsof": errors.New("not found")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := &PSScanner{Exec: tc.fe.run}
			l := &Lister{
				Registry: &Registry{Dir: regFixture(t), Alive: aliveSet(100), StartTimes: sc.StartTimes},
				Scanner:  sc,
			}
			got, err := l.List(context.Background())
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v", err)
			}
			var ue *UnavailableError
			if !errors.As(err, &ue) {
				t.Fatalf("not typed: %T", err)
			}
			if len(got) != 1 || got[0].SessionID != "busy-s" || !got[0].Busy {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestListerDedupesRegistryPIDs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/21805.json", `{"pid":21805,"sessionId":"S","cwd":"/reg","status":"idle"}`)
	fe := &fakeExec{out: map[string]string{"ps": psSample, "lsof": lsofSample}}
	sc := &PSScanner{Exec: fe.run}
	l := &Lister{Registry: &Registry{Dir: dir, Alive: aliveSet(21805)}, Scanner: sc}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []sessions.LiveProc{
		{PID: 5001, Agent: "codex", Cwd: "/x/repo wt"},
		{PID: 21805, Agent: "claude", Cwd: "/reg", SessionID: "S"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestProcFSScanner(t *testing.T) {
	root := t.TempDir()
	mk := func(pid int, cmdline, cwd, stat string) {
		d := filepath.Join(root, fmt.Sprint(pid))
		writeFile(t, d+"/cmdline", cmdline)
		if stat != "" {
			writeFile(t, d+"/stat", stat)
		}
		if cwd != "" {
			if err := os.Symlink(cwd, d+"/cwd"); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(10, "claude\x00--remote-control\x00", "/x/repo", "10 (claude) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 99")
	mk(11, "/home/u/.local/share/claude/versions/2.1.312\x00--resume\x00x\x00", "/x/repo-wt", "")
	mk(12, "claude bg-spare --bg-spare /tmp/s", "/x", "")
	mk(13, "node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js\x00", "/x", "")
	mk(14, "/usr/lib/node_modules/@openai/codex/vendor/x86_64-unknown-linux-musl/codex/codex\x00", "/x/repo-wt", "")
	mk(15, "", "", "") // kernel thread
	writeFile(t, root+"/self/cmdline", "claude")
	sc := &ProcFSScanner{Root: root}
	got, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []sessions.LiveProc{
		{PID: 10, Agent: "claude", Cwd: "/x/repo"},
		{PID: 11, Agent: "claude", Cwd: "/x/repo-wt"},
		{PID: 14, Agent: "codex", Cwd: "/x/repo-wt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	st, _ := sc.StartTimes(context.Background(), []int{10, 11})
	if !reflect.DeepEqual(st, map[int]string{10: "424242"}) {
		t.Errorf("start %v", st)
	}
	if _, err := (&ProcFSScanner{Root: root + "/nope"}).Scan(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestParseStatStartWeirdComm(t *testing.T) {
	s := "7 (a) b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 1"
	if got, ok := parseStatStart(s); !ok || got != "777" {
		t.Errorf("got %q %v", got, ok)
	}
}

func TestPIDAliveSelf(t *testing.T) {
	if !PIDAlive(os.Getpid()) || PIDAlive(-1) {
		t.Error("PIDAlive")
	}
}

func TestStartMismatch(t *testing.T) {
	for _, tc := range []struct {
		reg, got string
		want     bool
	}{
		{"Thu Oct 29 20:40:07 2026", "Thu Oct 29 20:40:07 2026", false},
		{"Mon Jan  5 09:00:00 2026", "Mon Jan 5 09:00:01 2026", false}, // within 1s, spacing differs
		{"Mon Jan  5 09:00:00 2026", "Mon Jan 5 09:00:05 2026", true},
		{"Thu Oct 29 20:40:07 2026", "Fri Oct 30 10:00:00 2026", true},
		{"Do 29 Okt 20:40:07 2026", "Thu Oct 29 20:40:07 2026", false}, // locale-different: skip
		{"Thu Oct 29 20:40:07 2026", "jeu 29 oct 20:40:07 2026", false},
		{"garbage", "Thu Oct 29 20:40:07 2026", false},
		{"424242", "424242", false},
		{"424242", "424243", true},
		{"424242", "Thu Oct 29 20:40:07 2026", false}, // mixed: skip
		{"", "1", false},
	} {
		if got := startMismatch(tc.reg, tc.got); got != tc.want {
			t.Errorf("startMismatch(%q, %q) = %v", tc.reg, tc.got, got)
		}
	}
}

func TestRegistryUnparsableStartKept(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/7.json", `{"pid":7,"sessionId":"S","procStart":"Do 29 Okt 20:40:07 2026","status":"busy"}`)
	r := &Registry{Dir: dir, Alive: aliveSet(7), StartTimes: func(context.Context, []int) (map[int]string, error) {
		return map[int]string{7: "Thu Oct 29 21:00:00 2026"}, nil
	}}
	got, _ := r.List(context.Background())
	if len(got) != 1 || !got[0].Busy {
		t.Errorf("got %+v", got)
	}
}

func TestStartTimesUsesCLocale(t *testing.T) {
	fe := &fakeExec{out: map[string]string{"env": "5 Thu Oct 29 20:40:07 2026\n"}}
	got, err := (&PSScanner{Exec: fe.run}).StartTimes(context.Background(), []int{5, 6})
	if err != nil || got[5] != "Thu Oct 29 20:40:07 2026" {
		t.Fatalf("got %v %v", got, err)
	}
	if fe.calls[0] != "env LC_ALL=C ps -o pid=,lstart= -p 5,6" {
		t.Errorf("call %q", fe.calls[0])
	}
}

func TestClassifyHelpersAndArgs(t *testing.T) {
	for _, tc := range []struct {
		line string
		want sessions.Agent // "" = not an agent
	}{
		{"1 0 claude -p fix the daemon", "claude"},
		{"1 0 /Users/u/.local/bin/claude daemon run", ""},
		{"1 0 codex app-server", ""},
		{"1 0 /opt/bin/codex mcp-server", ""},
		{"1 0 codex mcp", ""},
		{"1 0 codex exec fix the mcp server", "codex"},
		{"1 0 /Applications/Claude Code.app/Contents/MacOS/claude --resume x", "claude"},
		{"1 0 /Users/u/Library/Application Support/claude/versions/2.1.3 --session-id s", "claude"},
		{"1 0 /opt/my tools/codex", "codex"},
		{"1 0 /opt/my tools/codex mcp", ""},
		{"1 0 /Applications/Claude Code.app/Contents/MacOS/claude --bg-pty-host /s", ""},
		{"1 0 /Applications/Claude.app/Contents/MacOS/Claude", ""},
	} {
		es := ParsePS([]byte(tc.line))
		if len(es) != 1 {
			t.Fatalf("%q: parsed %d", tc.line, len(es))
		}
		ag, ok := Classify(es[0].Args)
		if (tc.want == "") == ok || ag != tc.want {
			t.Errorf("%q: got %q %v want %q", tc.line, ag, ok, tc.want)
		}
	}
	es := ParsePS([]byte("1 0 /opt/my tools/codex exec x"))
	if !reflect.DeepEqual(es[0].Args, []string{"/opt/my tools/codex", "exec", "x"}) {
		t.Errorf("args %q", es[0].Args)
	}
}

func TestParsePSLongLines(t *testing.T) {
	long := "9 1 /usr/bin/java " + strings.Repeat("x", 3<<20)
	out := "1 0 claude\n" + long + "\n2 0 codex\n"
	es := ParsePS([]byte(out))
	if len(es) != 3 || es[0].PID != 1 || es[1].PID != 9 || es[2].PID != 2 {
		t.Fatalf("got %d entries", len(es))
	}
}
