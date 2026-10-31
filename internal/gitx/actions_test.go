package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recRunner records every call and returns nothing.
type recRunner struct{ calls []string }

func (r *recRunner) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, dir+": "+strings.Join(args, " "))
	return nil, nil
}

func TestActionArgs(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{}
	_ = Detach(ctx, r, "/x/wt")
	_ = Checkout(ctx, r, "/x/wt", "feat")
	_ = Lock(ctx, r, "/x/repo", "/x/wt", "")
	_ = Lock(ctx, r, "/x/repo", "/x/wt", "agent busy")
	_ = Unlock(ctx, r, "/x/repo", "/x/wt")
	_, _ = PruneDryRun(ctx, r, "/x/repo")
	_, _ = Prune(ctx, r, "/x/repo")
	want := []string{
		"/x/wt: checkout --detach",
		"/x/wt: checkout feat --",
		"/x/repo: worktree lock /x/wt",
		"/x/repo: worktree lock --reason agent busy /x/wt",
		"/x/repo: worktree unlock /x/wt",
		"/x/repo: worktree prune --dry-run -v",
		"/x/repo: worktree prune -v",
	}
	if fmt.Sprint(r.calls) != fmt.Sprint(want) {
		t.Errorf("calls:\n%q\nwant:\n%q", r.calls, want)
	}
	if err := Checkout(ctx, r, "/x/wt", "--orphan"); err == nil {
		t.Error("option-like branch accepted")
	}
	if len(r.calls) != len(want) {
		t.Error("option-like branch reached git")
	}
}

func TestParseLastBranch(t *testing.T) {
	for in, want := range map[string]string{
		"checkout: moving from feat to 1234abc\n": "feat",
		"checkout: moving from a/b to main":       "a/b",
		"commit: x":                               "",
		"":                                        "",
	} {
		if got := ParseLastBranch(in); got != want {
			t.Errorf("ParseLastBranch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBranchInUse(t *testing.T) {
	if !IsBranchInUse(&Error{Stderr: "fatal: 'f' is already used by worktree at '/x'"}) {
		t.Error("new git message not recognised")
	}
	if !IsBranchInUse(fmt.Errorf("wrap: %w", &Error{Stderr: "fatal: 'f' is already checked out at '/x'"})) {
		t.Error("old git message not recognised")
	}
	if IsBranchInUse(errors.New("is already used by worktree at")) || IsBranchInUse(&Error{Stderr: "fatal: nope"}) {
		t.Error("false positive")
	}
}

func branchOf(t *testing.T, dir string) string {
	t.Helper()
	wts, err := ListWorktrees(context.Background(), NewExecRunner(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wts {
		if w.Path == dir {
			return w.Branch
		}
	}
	t.Fatalf("worktree %s not listed", dir)
	return ""
}

func TestActionsFixture(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	r := NewExecRunner()
	root := tempDir(t)
	repo := filepath.Join(root, "repo")
	newRepo(t, repo)
	wt := filepath.Join(root, "repo-feat")
	git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)

	t.Run("branch in use elsewhere", func(t *testing.T) {
		err := Checkout(ctx, r, repo, "feat")
		if !IsBranchInUse(err) {
			t.Fatalf("err = %v, want branch-in-use", err)
		}
		if !strings.Contains(err.Error(), wt) {
			t.Errorf("error does not name holder: %v", err)
		}
		if b := branchOf(t, repo); b != "main" {
			t.Errorf("main worktree branch changed to %q", b)
		}
	})

	t.Run("dirty detach frees the branch and keeps changes", func(t *testing.T) {
		writeFile(t, filepath.Join(wt, "wip.txt"), "wip")
		if err := Detach(ctx, r, wt); err != nil {
			t.Fatal(err)
		}
		if b := branchOf(t, wt); b != "" {
			t.Errorf("still on %q after detach", b)
		}
		if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
			t.Errorf("uncommitted file lost: %v", err)
		}
		if got := LastBranch(ctx, r, wt); got != "feat" {
			t.Errorf("LastBranch = %q, want feat", got)
		}
		if err := Checkout(ctx, r, repo, "feat"); err != nil {
			t.Fatalf("feat not freed: %v", err)
		}
		if b := branchOf(t, repo); b != "feat" {
			t.Errorf("main on %q", b)
		}
		git(t, repo, "checkout", "-q", "main")
		if err := Checkout(ctx, r, wt, "feat"); err != nil {
			t.Fatalf("reattach: %v", err)
		}
		if b := branchOf(t, wt); b != "feat" {
			t.Errorf("reattach left %q", b)
		}
	})

	t.Run("name matching a file is never a pathspec", func(t *testing.T) {
		foo := filepath.Join(repo, "foo")
		writeFile(t, foo, "committed\n")
		git(t, repo, "add", "foo")
		git(t, repo, "commit", "-q", "-m", "add foo")
		writeFile(t, foo, "uncommitted\n")
		if err := Checkout(ctx, r, repo, "foo"); err == nil {
			t.Fatal("checkout of non-branch foo succeeded")
		}
		b, err := os.ReadFile(foo)
		if err != nil || string(b) != "uncommitted\n" {
			t.Errorf("foo = %q, %v; uncommitted change lost", b, err)
		}
		git(t, repo, "checkout", "-q", "--", "foo")
	})

	t.Run("local branches", func(t *testing.T) {
		bs, err := LocalBranches(ctx, r, wt)
		if err != nil || fmt.Sprint(bs) != "[feat main]" {
			t.Errorf("branches = %v, %v", bs, err)
		}
	})

	t.Run("lock and unlock", func(t *testing.T) {
		if err := Lock(ctx, r, repo, wt, "agent at work"); err != nil {
			t.Fatal(err)
		}
		wts, _ := ListWorktrees(ctx, r, repo)
		if !wts[1].Locked || wts[1].LockReason != "agent at work" {
			t.Errorf("not locked: %+v", wts[1])
		}
		if err := Unlock(ctx, r, repo, wt); err != nil {
			t.Fatal(err)
		}
		wts, _ = ListWorktrees(ctx, r, repo)
		if wts[1].Locked {
			t.Error("still locked")
		}
		var ge *Error
		if err := Lock(ctx, r, repo, repo, ""); !errors.As(err, &ge) || !strings.Contains(ge.Stderr, "main working tree") {
			t.Errorf("locking main: %v", err)
		}
	})

	t.Run("prune", func(t *testing.T) {
		lines, err := PruneDryRun(ctx, r, repo)
		if err != nil || len(lines) != 0 {
			t.Fatalf("clean repo dry-run = %v, %v", lines, err)
		}
		gone := filepath.Join(root, "repo-gone")
		git(t, repo, "worktree", "add", "-q", "-b", "gone", gone)
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		lines, err = PruneDryRun(ctx, r, repo)
		if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "repo-gone") {
			t.Fatalf("dry-run = %q, %v", lines, err)
		}
		if wts, _ := ListWorktrees(ctx, r, repo); len(wts) != 3 {
			t.Errorf("dry-run pruned: %d worktrees", len(wts))
		}
		lines, err = Prune(ctx, r, repo)
		if err != nil || len(lines) != 1 {
			t.Fatalf("prune = %q, %v", lines, err)
		}
		if wts, _ := ListWorktrees(ctx, r, repo); len(wts) != 2 {
			t.Errorf("after prune: %d worktrees", len(wts))
		}
	})
}
