package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestBindLexical(t *testing.T) {
	wts := []string{"/x/repo", "/x/repo-wt", "/x/repo/nested-wt", "/y/"}
	tests := []struct {
		cwd, want string
		ok        bool
	}{
		{"/x/repo", "/x/repo", true},
		{"/x/repo/", "/x/repo", true},
		{"/x/repo/src/pkg", "/x/repo", true},
		{"/x/repo-wt/sub", "/x/repo-wt", true}, // prefix trap
		{"/x/repo-wt", "/x/repo-wt", true},
		{"/x/repo/nested-wt/a", "/x/repo/nested-wt", true}, // longest wins
		{"/x/repo/nested-wtx", "/x/repo", true},
		{"/y/z", "/y/", true}, // trailing slash on worktree side
		{"/x", "", false},
		{"/x/rep", "", false},
		{"", "", false},
	}
	b := NewBinder(wts)
	for _, tt := range tests {
		got, ok := b.Bind(tt.cwd)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Bind(%q) = %q,%v want %q,%v", tt.cwd, got, ok, tt.want, tt.ok)
		}
		if g2, ok2 := Bind(wts, tt.cwd); g2 != got || ok2 != ok {
			t.Errorf("package Bind(%q) = %q,%v differs from Binder", tt.cwd, g2, ok2)
		}
	}
}

func TestBindOrderIndependent(t *testing.T) {
	a := NewBinder([]string{"/x/repo/wt", "/x/repo"})
	b := NewBinder([]string{"/x/repo", "/x/repo/wt"})
	for _, bb := range []*Binder{a, b} {
		if got, _ := bb.Bind("/x/repo/wt/q"); got != "/x/repo/wt" {
			t.Errorf("got %q", got)
		}
	}
}

func TestBindSymlinks(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real", "repo")
	realWT := filepath.Join(base, "real", "repo-wt")
	for _, d := range []string{filepath.Join(real, "sub"), filepath.Join(realWT, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), linkDir); err != nil {
		t.Fatal(err)
	}

	// Symlink on the session side: worktrees given as real paths.
	b := NewBinder([]string{real, realWT})
	if got, ok := b.Bind(filepath.Join(linkDir, "repo-wt", "sub")); !ok || got != realWT {
		t.Errorf("session-side symlink: got %q,%v want %q", got, ok, realWT)
	}
	// Symlink on the worktree side: result is the path as given.
	linkWT := filepath.Join(linkDir, "repo-wt")
	b = NewBinder([]string{filepath.Join(linkDir, "repo"), linkWT})
	if got, ok := b.Bind(filepath.Join(realWT, "sub")); !ok || got != linkWT {
		t.Errorf("worktree-side symlink: got %q,%v want %q", got, ok, linkWT)
	}
	// Session cwd that no longer exists under a symlinked parent still binds.
	if got, ok := b.Bind(filepath.Join(realWT, "deleted", "dir")); !ok || got != linkWT {
		t.Errorf("missing cwd: got %q,%v", got, ok)
	}
}

func TestBindCaseFolding(t *testing.T) {
	b := NewBinder([]string{"/x/Repo"})
	_, ok := b.Bind("/x/repo/sub")
	if want := runtime.GOOS == "darwin"; ok != want {
		t.Errorf("case-different cwd bound=%v, want %v on %s", ok, want, runtime.GOOS)
	}
}

func TestBinderConcurrent(t *testing.T) {
	b := NewBinder([]string{"/x/repo", "/x/repo-wt"})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cwd := "/x/repo-wt/" + strings.Repeat("d", i%4)
			if got, _ := b.Bind(cwd); got != "/x/repo-wt" {
				t.Errorf("Bind(%q) = %q", cwd, got)
			}
		}()
	}
	wg.Wait()
}

func TestCleanTitle(t *testing.T) {
	if got := CleanTitle("  a\n\tb  c ", 80); got != "a b c" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", 100)
	got := CleanTitle(long, 80)
	if n := len([]rune(got)); n != 80 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated to %d runes: %q", n, got)
	}
}
