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

func TestAddRemoveArgs(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{}
	_ = AddWorktree(ctx, r, "/x/repo", "/x/wt", "feat", true)
	_ = AddWorktree(ctx, r, "/x/repo", "/x/wt", "feat", false)
	_ = RemoveWorktree(ctx, r, "/x/repo", "/x/wt", 0)
	_ = RemoveWorktree(ctx, r, "/x/repo", "/x/wt", 1)
	_ = RemoveWorktree(ctx, r, "/x/repo", "/x/wt", 2)
	_ = DeleteBranch(ctx, r, "/x/repo", "feat")
	want := []string{
		"/x/repo: worktree add -b feat -- /x/wt",
		"/x/repo: worktree add -- /x/wt feat",
		"/x/repo: worktree remove -- /x/wt",
		"/x/repo: worktree remove --force -- /x/wt",
		"/x/repo: worktree remove --force --force -- /x/wt",
		"/x/repo: branch -d -- feat",
	}
	if fmt.Sprint(r.calls) != fmt.Sprint(want) {
		t.Errorf("calls:\n%q\nwant:\n%q", r.calls, want)
	}
	n := len(r.calls)
	if AddWorktree(ctx, r, "/x/repo", "/x/wt", "-b", true) == nil ||
		AddWorktree(ctx, r, "/x/repo", "rel/wt", "feat", true) == nil ||
		DeleteBranch(ctx, r, "/x/repo", "-D") == nil || DeleteBranch(ctx, r, "/x/repo", "") == nil {
		t.Error("invalid input accepted")
	}
	if len(r.calls) != n {
		t.Error("invalid input reached git")
	}
}

func TestAddRemoveFixture(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	r := NewExecRunner()
	root := tempDir(t)
	repo := filepath.Join(root, "repo")
	newRepo(t, repo)
	git(t, repo, "branch", "existing")

	count := func() int {
		wts, err := ListWorktrees(ctx, r, repo)
		if err != nil {
			t.Fatal(err)
		}
		return len(wts)
	}

	newWT := filepath.Join(root, "repo-new")
	if err := AddWorktree(ctx, r, repo, newWT, "new", true); err != nil {
		t.Fatalf("add new branch: %v", err)
	}
	if b := branchOf(t, newWT); b != "new" {
		t.Errorf("new worktree on %q", b)
	}
	exWT := filepath.Join(root, "repo-existing")
	if err := AddWorktree(ctx, r, repo, exWT, "existing", false); err != nil {
		t.Fatalf("add existing branch: %v", err)
	}
	if b := branchOf(t, exWT); b != "existing" {
		t.Errorf("existing worktree on %q", b)
	}

	t.Run("path exists", func(t *testing.T) {
		busy := filepath.Join(root, "occupied")
		if err := os.MkdirAll(busy, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(busy, "f"), "x")
		if err := AddWorktree(ctx, r, repo, busy, "other", true); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("err = %v", err)
		}
		if err := AddWorktree(ctx, r, repo, filepath.Join(busy, "f"), "other", true); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("file path: err = %v", err)
		}
		if count() != 3 {
			t.Error("worktree created")
		}
		if bs, _ := LocalBranches(ctx, r, repo); fmt.Sprint(bs) != "[existing main new]" {
			t.Errorf("branch created: %v", bs)
		}
	})

	t.Run("branch held elsewhere", func(t *testing.T) {
		err := AddWorktree(ctx, r, repo, filepath.Join(root, "repo-dup"), "new", false)
		if !IsBranchInUse(err) {
			t.Errorf("err = %v, want branch-in-use", err)
		}
	})

	t.Run("remove clean, delete merged branch", func(t *testing.T) {
		if err := RemoveWorktree(ctx, r, repo, exWT, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(exWT); !os.IsNotExist(err) {
			t.Errorf("directory still there: %v", err)
		}
		if err := DeleteBranch(ctx, r, repo, "existing"); err != nil {
			t.Errorf("delete merged branch: %v", err)
		}
	})

	t.Run("remove dirty needs force, unmerged branch kept", func(t *testing.T) {
		writeFile(t, filepath.Join(newWT, "wip.txt"), "wip")
		git(t, newWT, "add", "wip.txt")
		git(t, newWT, "commit", "-q", "-m", "unmerged")
		writeFile(t, filepath.Join(newWT, "more.txt"), "dirty")
		if err := RemoveWorktree(ctx, r, repo, newWT, 0); err == nil {
			t.Fatal("dirty worktree removed without --force")
		}
		if err := RemoveWorktree(ctx, r, repo, newWT, 1); err != nil {
			t.Fatal(err)
		}
		var ge *Error
		if err := DeleteBranch(ctx, r, repo, "new"); !errors.As(err, &ge) || !strings.Contains(ge.Stderr, "not fully merged") {
			t.Errorf("unmerged delete: %v", err)
		}
		if bs, _ := LocalBranches(ctx, r, repo); fmt.Sprint(bs) != "[main new]" {
			t.Errorf("branches = %v", bs)
		}
	})

	t.Run("remove locked needs double force", func(t *testing.T) {
		lk := filepath.Join(root, "repo-locked")
		if err := AddWorktree(ctx, r, repo, lk, "lk", true); err != nil {
			t.Fatal(err)
		}
		if err := Lock(ctx, r, repo, lk, "keep"); err != nil {
			t.Fatal(err)
		}
		if err := RemoveWorktree(ctx, r, repo, lk, 1); err == nil {
			t.Fatal("locked worktree removed with a single --force")
		}
		if err := RemoveWorktree(ctx, r, repo, lk, 2); err != nil {
			t.Fatal(err)
		}
		if count() != 1 {
			t.Errorf("%d worktrees left", count())
		}
	})
}
