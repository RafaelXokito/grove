package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/RafaelXokito/grove/internal/pathx"
)

// TestMain makes git hermetic: the user's global/system config and HOME must
// not leak into fixture repos.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "grove-gitx-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "grove test",
		"GIT_AUTHOR_EMAIL":    "test@grove.invalid",
		"GIT_COMMITTER_NAME":  "grove test",
		"GIT_COMMITTER_EMAIL": "test@grove.invalid",
		"LC_ALL":              "C",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := NewExecRunner().Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// newRepo creates a non-bare repo with one commit on branch main.
func newRepo(t *testing.T, path string) {
	t.Helper()
	git(t, "", "init", "-q", "-b", "main", path)
	git(t, path, "commit", "-q", "--allow-empty", "-m", "initial")
}

// tempDir returns a canonical temp dir (macOS /var → /private/var).
func tempDir(t *testing.T) string {
	t.Helper()
	return pathx.Canonical(t.TempDir())
}

func TestListWorktreesFixture(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	r := NewExecRunner()

	t.Run("main plus one linked worktree", func(t *testing.T) {
		root := tempDir(t)
		repo := filepath.Join(root, "repo")
		newRepo(t, repo)
		wt := filepath.Join(root, "repo-feat")
		git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)

		got, err := ListWorktrees(ctx, r, repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d worktrees, want 2: %+v", len(got), got)
		}
		head := git(t, repo, "rev-parse", "HEAD")
		head = head[:len(head)-1]
		want := []Worktree{
			{Path: repo, HEAD: head, Branch: "main", IsMain: true},
			{Path: wt, HEAD: head, Branch: "feat"},
		}
		for i := range want {
			got[i].Path = pathx.Canonical(got[i].Path)
			if got[i] != want[i] {
				t.Errorf("worktree %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("detached, locked, prunable, spaces", func(t *testing.T) {
		root := tempDir(t)
		repo := filepath.Join(root, "my repo")
		newRepo(t, repo)
		det := filepath.Join(root, "det wt")
		locked := filepath.Join(root, "locked")
		gone := filepath.Join(root, "gone")
		git(t, repo, "worktree", "add", "-q", "--detach", det)
		git(t, repo, "worktree", "add", "-q", "-b", "lk", locked)
		git(t, repo, "worktree", "lock", "--reason", "agent busy\nsecond line", locked)
		git(t, repo, "worktree", "add", "-q", "-b", "gone", gone)
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}

		got, err := ListWorktrees(ctx, r, repo)
		if err != nil {
			t.Fatal(err)
		}
		byPath := map[string]Worktree{}
		for _, w := range got {
			byPath[pathx.Canonical(w.Path)] = w
		}
		if len(byPath) != 4 {
			t.Fatalf("got %d worktrees, want 4: %+v", len(got), got)
		}

		tests := []struct {
			path                    string
			main, detached          bool
			branch                  string
			locked                  bool
			lockReason              string
			prunable                bool
			wantPruneReasonNonEmpty bool
		}{
			{path: repo, main: true, branch: "main"},
			{path: det, detached: true},
			{path: locked, branch: "lk", locked: true, lockReason: "agent busy\nsecond line"},
			{path: gone, branch: "gone", prunable: true, wantPruneReasonNonEmpty: true},
		}
		for _, tt := range tests {
			w, ok := byPath[tt.path]
			if !ok {
				t.Errorf("missing worktree %q in %v", tt.path, got)
				continue
			}
			if w.IsMain != tt.main || w.Detached() != tt.detached || w.Branch != tt.branch ||
				w.Locked != tt.locked || w.LockReason != tt.lockReason || w.Prunable != tt.prunable ||
				(w.PruneReason != "") != tt.wantPruneReasonNonEmpty || len(w.HEAD) != 40 || w.IsBare {
				t.Errorf("worktree %q = %+v, want %+v", tt.path, w, tt)
			}
		}
	})

	t.Run("bare repo with linked worktree", func(t *testing.T) {
		root := tempDir(t)
		src := filepath.Join(root, "src")
		newRepo(t, src)
		bare := filepath.Join(root, "repo.git")
		git(t, "", "clone", "-q", "--bare", src, bare)
		wt := filepath.Join(root, "bare wt")
		git(t, bare, "worktree", "add", "-q", wt, "main")

		got, err := ListWorktrees(ctx, r, bare)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d worktrees, want 2: %+v", len(got), got)
		}
		if p := pathx.Canonical(got[0].Path); p != bare || !got[0].IsBare || !got[0].IsMain || got[0].HEAD != "" {
			t.Errorf("bare entry = %+v", got[0])
		}
		if p := pathx.Canonical(got[1].Path); p != wt || got[1].IsBare || got[1].IsMain || got[1].Branch != "main" {
			t.Errorf("linked entry = %+v", got[1])
		}
	})
}

func TestExecRunnerError(t *testing.T) {
	requireGit(t)
	dir := tempDir(t)
	_, err := NewExecRunner().Run(context.Background(), dir, "worktree", "list", "--porcelain")
	var gerr *Error
	if !errors.As(err, &gerr) {
		t.Fatalf("err = %v (%T), want *gitx.Error", err, err)
	}
	if gerr.Stderr == "" || gerr.Dir != dir {
		t.Errorf("Error = %+v, want stderr and dir set", gerr)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("err does not unwrap to *exec.ExitError: %v", err)
	}
}

func TestExecRunnerContextCanceled(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewExecRunner().Run(ctx, "", "version")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	r := &ExecRunner{Git: filepath.Join(t.TempDir(), "no-such-git")}
	_, err := r.Run(context.Background(), "", "version")
	var gerr *Error
	if !errors.As(err, &gerr) {
		t.Fatalf("err = %v, want *gitx.Error", err)
	}
}

// fakeRunner records calls; used to check ListWorktrees' exact arguments.
type fakeRunner struct {
	dir  string
	args []string
	out  []byte
	err  error
}

func (f *fakeRunner) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	f.dir, f.args = dir, args
	return f.out, f.err
}

func TestListWorktreesArgs(t *testing.T) {
	f := &fakeRunner{out: []byte("worktree /x/repo\x00HEAD " + sha1 + "\x00branch refs/heads/main\x00\x00")}
	got, err := ListWorktrees(context.Background(), f, "/x/repo")
	if err != nil {
		t.Fatal(err)
	}
	if f.dir != "/x/repo" || fmt.Sprint(f.args) != "[worktree list --porcelain -z]" {
		t.Errorf("ran dir=%q args=%v", f.dir, f.args)
	}
	if len(got) != 1 || got[0].Branch != "main" {
		t.Errorf("got %+v", got)
	}

	f = &fakeRunner{err: errors.New("boom")}
	if _, err := ListWorktrees(context.Background(), f, "/x"); err == nil {
		t.Error("want error from runner")
	}
}

// scriptedRunner returns canned responses in order and records every call.
type scriptedRunner struct {
	calls [][]string
	resps []struct {
		out []byte
		err error
	}
}

func (s *scriptedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, args)
	r := s.resps[len(s.calls)-1]
	return r.out, r.err
}

func TestListWorktreesZFallback(t *testing.T) {
	plain := []byte("worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n")
	type resp = struct {
		out []byte
		err error
	}
	tests := []struct {
		name      string
		resps     []resp
		wantCalls []string
		wantErr   bool
	}{
		{
			name:      "old git rejects -z, retries plain",
			resps:     []resp{{nil, &Error{Stderr: "error: unknown switch `z'\nusage: git worktree list [<options>]"}}, {plain, nil}},
			wantCalls: []string{"[worktree list --porcelain -z]", "[worktree list --porcelain]"},
		},
		{
			name:      "usage-only stderr also retries",
			resps:     []resp{{nil, &Error{Stderr: "usage: git worktree list [<options>]"}}, {plain, nil}},
			wantCalls: []string{"[worktree list --porcelain -z]", "[worktree list --porcelain]"},
		},
		{
			name:      "other git error does not retry",
			resps:     []resp{{nil, &Error{Stderr: "fatal: not a git repository"}}},
			wantCalls: []string{"[worktree list --porcelain -z]"},
			wantErr:   true,
		},
		{
			name:      "non-git error does not retry",
			resps:     []resp{{nil, errors.New("boom")}},
			wantCalls: []string{"[worktree list --porcelain -z]"},
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &scriptedRunner{resps: tt.resps}
			got, err := ListWorktrees(context.Background(), s, "/x/repo")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var calls []string
			for _, c := range s.calls {
				calls = append(calls, fmt.Sprint(c))
			}
			if fmt.Sprint(calls) != fmt.Sprint(tt.wantCalls) {
				t.Errorf("calls = %v, want %v", calls, tt.wantCalls)
			}
			if !tt.wantErr && (len(got) != 1 || got[0].Branch != "main") {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		e    *Error
		want string
	}{
		{&Error{Dir: "/x/my repo", Args: []string{"checkout", "feat"}, Stderr: "fatal: nope"}, "git -C /x/my repo checkout feat: fatal: nope"},
		{&Error{Args: []string{"version"}, Err: errors.New("exec: not found")}, "git version: exec: not found"},
	}
	for _, tt := range tests {
		if got := tt.e.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}
