package scan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
)

// maxSuperprojectHops bounds the submodule → superproject walk.
const maxSuperprojectHops = 32

// ResolveScope reports the repository dir belongs to (FR-22): for a main or
// linked worktree, any subdirectory of one, or a bare repository, it returns
// the main repo path (canonical). Inside a submodule it returns the
// outermost superproject. ok is false (with a nil error) when dir is not
// inside a git repository. All git calls share one timeout (0 means
// DefaultGitTimeout, < 0 none); err is set on a timeout or when git cannot
// run, and callers should then fall back to the all-repos view.
func ResolveScope(ctx context.Context, r gitx.Runner, dir string, timeout time.Duration) (repo string, ok bool, err error) {
	if timeout == 0 {
		timeout = DefaultGitTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	run := func(dir string, args ...string) (string, error) {
		out, err := r.Run(ctx, dir, args...)
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("resolve repo of %s: timed out after %v: %w", dir, timeout, context.DeadlineExceeded)
		}
		return strings.TrimSpace(string(out)), err
	}

	cur := dir
	// The superproject walk doubles as the "is this a repo" probe: git
	// fails with "not a git repository" outside one.
	for range maxSuperprojectHops {
		super, err := run(cur, "rev-parse", "--show-superproject-working-tree")
		if err != nil {
			return "", false, notRepo(err)
		}
		if super == "" {
			break
		}
		cur = super
	}

	common, err := run(cur, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", false, notRepo(err)
	}
	if common == "" {
		return "", false, nil
	}
	switch {
	case filepath.Base(common) == ".git":
		repo = filepath.Dir(common)
	default:
		// Asked in the common dir itself: from a linked worktree of a
		// bare repo, git answers "false".
		bare, err := run(common, "rev-parse", "--is-bare-repository")
		if err != nil {
			return "", false, notRepo(err)
		}
		if bare == "true" {
			repo = common
		} else {
			// e.g. --separate-git-dir: any worktree path will do,
			// LoadRepo normalises it to the main worktree.
			if repo, err = run(cur, "rev-parse", "--show-toplevel"); err != nil || repo == "" {
				return "", false, notRepo(err)
			}
		}
	}
	return pathx.Canonical(repo), true, nil
}

// notRepo maps git exiting with a failure status ("not a git repository")
// to a nil error; anything else (timeout, git missing) is returned.
func notRepo(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}
