package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Detach detaches HEAD of the worktree at wt at its current commit
// (`git -C <wt> checkout --detach`), freeing its branch for use elsewhere.
// Uncommitted changes stay in the worktree.
func Detach(ctx context.Context, r Runner, wt string) error {
	_, err := r.Run(ctx, wt, "checkout", "--detach")
	return err
}

// Checkout checks out branch in the worktree at wt (`git -C <wt> checkout
// <branch> --`). The trailing "--" makes git treat the name strictly as a
// revision, never as a pathspec (which would discard changes to a file of
// that name). Names starting with "-" are rejected so they cannot be read as
// options.
func Checkout(ctx context.Context, r Runner, wt, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return errors.New("invalid branch name " + `"` + branch + `"`)
	}
	_, err := r.Run(ctx, wt, "checkout", branch, "--")
	return err
}

// Lock locks the worktree at path (`git -C <repo> worktree lock [--reason r]
// <path>`). git refuses to lock the main worktree.
func Lock(ctx context.Context, r Runner, repo, path, reason string) error {
	args := []string{"worktree", "lock"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err := r.Run(ctx, repo, append(args, path)...)
	return err
}

// Unlock unlocks the worktree at path (`git -C <repo> worktree unlock <path>`).
func Unlock(ctx context.Context, r Runner, repo, path string) error {
	_, err := r.Run(ctx, repo, "worktree", "unlock", path)
	return err
}

// PruneDryRun lists what `git worktree prune` would remove in repo, one
// entry per line of `git worktree prune --dry-run -v` (e.g.
// "Removing worktrees/x: gitdir file points to non-existent location").
func PruneDryRun(ctx context.Context, r Runner, repo string) ([]string, error) {
	return runLines(ctx, r, repo, "worktree", "prune", "--dry-run", "-v")
}

// Prune runs `git worktree prune -v` in repo and returns the reported lines.
func Prune(ctx context.Context, r Runner, repo string) ([]string, error) {
	return runLines(ctx, r, repo, "worktree", "prune", "-v")
}

// runLines runs a command whose report git writes to stderr and returns the
// non-empty lines of stdout and stderr.
func runLines(ctx context.Context, r Runner, dir string, args ...string) ([]string, error) {
	var out, errOut []byte
	var err error
	if or, ok := r.(OutputRunner); ok {
		out, errOut, err = or.RunOutput(ctx, dir, args...)
	} else {
		out, err = r.Run(ctx, dir, args...)
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, b := range [][]byte{out, errOut} {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	return lines, nil
}

// LocalBranches lists the local branch names of the repository containing dir.
func LocalBranches(ctx context.Context, r Runner, dir string) ([]string, error) {
	out, err := r.Run(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var bs []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			bs = append(bs, l)
		}
	}
	return bs, nil
}

// LastBranch returns the branch the worktree at wt was on before its most
// recent checkout, from the HEAD reflog ("checkout: moving from X to Y").
// Best-effort: "" on any error or if there is no such entry.
func LastBranch(ctx context.Context, r Runner, wt string) string {
	out, err := r.Run(ctx, wt, "reflog", "-1", "--grep-reflog=checkout: moving from ", "--format=%gs")
	if err != nil {
		return ""
	}
	return ParseLastBranch(string(out))
}

var reflogCheckout = regexp.MustCompile(`^checkout: moving from (\S+) to \S+`)

// ParseLastBranch extracts X from a "checkout: moving from X to Y" reflog
// subject; "" if it does not match.
func ParseLastBranch(subject string) string {
	m := reflogCheckout.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return ""
	}
	return m[1]
}

// IsBranchInUse reports whether err is git refusing a checkout because the
// branch is checked out in another worktree.
func IsBranchInUse(err error) bool {
	var ge *Error
	if !errors.As(err, &ge) {
		return false
	}
	return strings.Contains(ge.Stderr, "is already used by worktree at") ||
		strings.Contains(ge.Stderr, "is already checked out at")
}

// validRef rejects empty and option-like names before they reach git.
func validRef(kind, name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return errors.New("invalid " + kind + ` "` + name + `"`)
	}
	return nil
}

// AddWorktree creates a worktree at the absolute path (`git -C <repo>
// worktree add`). With newBranch it creates branch from the current HEAD of
// repo (`-b <branch> -- <path>`); otherwise it checks out the existing
// branch (`-- <path> <branch>`). a non-empty existing path is refused before
// running git; git refuses a branch checked out in another worktree.
func AddWorktree(ctx context.Context, r Runner, repo, path, branch string, newBranch bool) error {
	if err := validRef("branch name", branch); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("worktree path must be absolute: " + path)
	}
	// git creates the -b branch before noticing the path is taken, leaving a
	// stray branch behind; refuse a non-empty path up front instead.
	if fi, err := os.Stat(path); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("'%s' already exists", path)
		}
		if ents, _ := os.ReadDir(path); len(ents) > 0 {
			return fmt.Errorf("'%s' already exists and is not empty", path)
		}
	}
	args := []string{"worktree", "add"}
	if newBranch {
		args = append(args, "-b", branch, "--", path)
	} else {
		args = append(args, "--", path, branch)
	}
	_, err := r.Run(ctx, repo, args...)
	return err
}

// RemoveWorktree removes the worktree at path (`git -C <repo> worktree remove
// [--force [--force]] -- <path>`). force is the number of --force flags: 1
// discards uncommitted changes, 2 also overrides a lock (git's rule).
func RemoveWorktree(ctx context.Context, r Runner, repo, path string, force int) error {
	args := []string{"worktree", "remove"}
	for i := 0; i < force && i < 2; i++ {
		args = append(args, "--force")
	}
	_, err := r.Run(ctx, repo, append(args, "--", path)...)
	return err
}

// DeleteBranch safely deletes a local branch (`git -C <repo> branch -d --
// <branch>`); git refuses if it is not fully merged. Never -D.
func DeleteBranch(ctx context.Context, r Runner, repo, branch string) error {
	if err := validRef("branch name", branch); err != nil {
		return err
	}
	_, err := r.Run(ctx, repo, "branch", "-d", "--", branch)
	return err
}
