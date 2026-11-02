package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RafaelXokito/grove/internal/gitx"
)

// TestResolveScope covers FR-22 with real fixture repos.
func TestResolveScope(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	main := filepath.Join(root, "proj")
	newRepo(t, main)
	mkdir(t, filepath.Join(main, "src", "pkg"))
	wt := filepath.Join(root, "proj-feat")
	git(t, main, "worktree", "add", "-q", "-b", "feat", wt)
	mkdir(t, filepath.Join(wt, "sub"))

	bare := filepath.Join(root, "bare.git")
	git(t, "", "init", "-q", "--bare", "-b", "main", bare)
	git(t, main, "push", "-q", bare, "main")
	bareWT := filepath.Join(root, "bare-wt")
	git(t, bare, "worktree", "add", "-q", bareWT, "main")

	// super/libs/inner is a submodule whose own submodule is libs/inner/deep.
	deep := filepath.Join(root, "deep-src")
	newRepo(t, deep)
	inner := filepath.Join(root, "inner-src")
	newRepo(t, inner)
	git(t, inner, "-c", "protocol.file.allow=always", "submodule", "add", "-q", deep, "deep")
	git(t, inner, "commit", "-q", "-m", "add deep")
	super := filepath.Join(root, "super")
	newRepo(t, super)
	git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", inner, "libs/inner")
	git(t, super, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "--recursive")

	plain := filepath.Join(root, "plain", "dir")
	mkdir(t, plain)

	tests := []struct {
		name, dir, want string
	}{
		{"main worktree", main, main},
		{"subdir of main", filepath.Join(main, "src", "pkg"), main},
		{"linked worktree", wt, main},
		{"subdir of linked worktree", filepath.Join(wt, "sub"), main},
		{"bare repo dir", bare, bare},
		{"worktree of bare repo", bareWT, bare},
		{"submodule", filepath.Join(super, "libs", "inner"), super},
		{"nested submodule", filepath.Join(super, "libs", "inner", "deep"), super},
		{"non-git dir", plain, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ResolveScope(context.Background(), gitx.NewExecRunner(), tt.dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.want != "") || got != tt.want {
				t.Errorf("ResolveScope(%s) = %q, %v; want %q", tt.dir, got, ok, tt.want)
			}
			if !ok {
				return
			}
			// The scope feeds Scan.Repos: it must load as that repo.
			repo := LoadRepo(context.Background(), gitx.NewExecRunner(), NewLimiter(0), got, 0)
			if repo.Err != nil || repo.Path != tt.want {
				t.Errorf("LoadRepo(%s) = %q, %v", got, repo.Path, repo.Err)
			}
		})
	}
}

// hangRunner blocks until ctx ends.
type hangRunner struct{}

func (hangRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, &gitx.Error{Err: ctx.Err()}
}

func TestResolveScopeTimeout(t *testing.T) {
	start := time.Now()
	_, ok, err := ResolveScope(context.Background(), hangRunner{}, t.TempDir(), 50*time.Millisecond)
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ok = %v, err = %v; want a timeout", ok, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestResolveScopeGitMissing(t *testing.T) {
	r := &gitx.ExecRunner{Git: filepath.Join(t.TempDir(), "no-such-git")}
	_, ok, err := ResolveScope(context.Background(), r, t.TempDir(), 0)
	if ok || err == nil {
		t.Fatalf("ok = %v, err = %v; want an error", ok, err)
	}
}

// TestScanRepos: Options.Repos loads repos outside every root, once, along
// with those discovered.
func TestScanRepos(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	a := filepath.Join(root, "a")
	newRepo(t, a)
	outside := filepath.Join(tempDir(t), "elsewhere")
	newRepo(t, outside)

	repos, errs := scanAll(t, Options{Roots: []string{root}, Repos: []string{outside, a}, Runner: gitx.NewExecRunner()})
	if len(errs) != 0 || len(repos) != 2 || repos[0].Path != a || repos[1].Path != outside || repos[1].Name != "elsewhere" {
		t.Fatalf("repos = %+v, errs = %v", repos, errs)
	}
	repos, _ = scanAll(t, Options{Repos: []string{outside}, Runner: gitx.NewExecRunner()})
	if len(repos) != 1 || repos[0].Path != outside {
		t.Fatalf("repos only: %+v", repos)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
}
