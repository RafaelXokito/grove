package scan

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
)

// BenchmarkScan is the FR-18 scan benchmark: 50 repos × 3 worktrees (main +
// 2 linked). Target: a full scan well under 2 s on an SSD. Not a CI gate.
func BenchmarkScan(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git not found in PATH")
	}
	root := pathx.Canonical(b.TempDir())
	run := func(dir string, args ...string) {
		if _, err := gitx.NewExecRunner().Run(context.Background(), dir, args...); err != nil {
			b.Fatalf("git %v: %v", args, err)
		}
	}
	for i := range 50 {
		repo := filepath.Join(root, fmt.Sprintf("repo%02d", i))
		run("", "init", "-q", "-b", "main", repo)
		run(repo, "commit", "-q", "--allow-empty", "-m", "initial")
		for j := range 2 {
			run(repo, "worktree", "add", "-q", "-b", fmt.Sprintf("wt%d", j),
				filepath.Join(root, fmt.Sprintf("repo%02d-wt%d", i, j)))
		}
	}
	opts := Options{Roots: []string{root}, Runner: gitx.NewExecRunner()}
	for b.Loop() {
		n := 0
		for ev := range Scan(context.Background(), opts) {
			if ev.Repo != nil {
				n++
			}
		}
		if n != 50 {
			b.Fatalf("scanned %d repos, want 50", n)
		}
	}
}
