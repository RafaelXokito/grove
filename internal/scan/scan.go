package scan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
)

// Repo is a discovered repository with its worktrees.
type Repo struct {
	Name      string // dir name relative to the root it was found under
	Path      string // main worktree path (the repo dir itself if bare)
	Worktrees []gitx.Worktree
	// Err is set when the repo could not be fully loaded. If Worktrees is
	// non-empty the failure is partial (status of some worktrees, whose
	// Status stays nil); otherwise listing the worktrees failed.
	Err error
}

// Event is one streamed scan result: either a loaded Repo or a discovery
// error (e.g. an unreadable directory). Neither kind is fatal.
type Event struct {
	Repo *Repo
	Err  error
}

// Limiter bounds the number of concurrent git invocations. It is safe for
// concurrent use and may be shared between scans and on-demand reloads.
type Limiter chan struct{}

// NewLimiter returns a Limiter allowing n concurrent git calls;
// n <= 0 means runtime.NumCPU()*2 (FR-18).
func NewLimiter(n int) Limiter {
	if n <= 0 {
		n = runtime.NumCPU() * 2
	}
	return make(Limiter, n)
}

// Do runs f while holding a slot, or returns ctx.Err() if ctx ends first.
func (l Limiter) Do(ctx context.Context, f func() error) error {
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l }()
	return f()
}

// DefaultGitTimeout bounds each git call a scan makes (`worktree list` and
// each per-worktree `status`) so a hung repository cannot stall a scan or
// hold a Limiter slot forever.
const DefaultGitTimeout = 10 * time.Second

// Options configure Scan. The zero value of every field means its default.
type Options struct {
	Roots []string
	// Repos are repository paths loaded directly, without discovery, in
	// addition to those found under Roots (e.g. the cwd scope of FR-22,
	// which may lie outside every root). Duplicates are loaded once.
	Repos []string
	// MaxDepth is the directory depth scanned below each root: 0 means
	// DefaultMaxDepth, < 0 scans only the roots themselves.
	MaxDepth int
	Runner   gitx.Runner
	Limiter  Limiter // shared git concurrency bound; nil means NewLimiter(0)
	// GitTimeout bounds each git call, measured from when the call starts
	// (not while queued on the Limiter). 0 means DefaultGitTimeout; < 0
	// disables the timeout.
	GitTimeout time.Duration
}

// Scan discovers repos under opts.Roots and streams each one, with its
// worktrees and their status loaded, as soon as it is ready. Discovery
// errors (*DirError) are streamed as Events with Err set as they occur.
// Neither kind of error stops the scan. The channel is closed when the scan
// finishes or ctx is cancelled; once cancellation is observed no further
// Events are sent and in-flight git commands are killed.
//
// The consumer must drain the channel until it is closed or cancel ctx;
// otherwise the scan's goroutines block forever.
//
// A repo is identified by its main worktree path: a repo reached twice (two
// overlapping roots, differently-cased roots on darwin, or via one of its
// linked worktrees) is loaded and sent once.
func Scan(ctx context.Context, opts Options) <-chan Event {
	lim := opts.Limiter
	if lim == nil {
		lim = NewLimiter(0)
	}
	roots := make([]string, 0, len(opts.Roots))
	for _, r := range opts.Roots {
		roots = append(roots, pathx.Canonical(r))
	}

	events := make(chan Event)
	paths := make(chan string)
	send := func(ev Event) {
		if ctx.Err() != nil {
			return
		}
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}

	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)
	claim := func(p string) bool {
		key := pathx.FoldKey(pathx.Canonical(p))
		mu.Lock()
		defer mu.Unlock()
		if seen[key] {
			return false
		}
		seen[key] = true
		return true
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(paths)
		for _, p := range opts.Repos {
			if p = pathx.Canonical(p); p == "" {
				continue
			}
			select {
			case paths <- p:
			case <-ctx.Done():
				return
			}
		}
		discover(ctx, roots, opts.MaxDepth, paths, func(err error) { send(Event{Err: err}) })
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for p := range paths {
			if !claim(p) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				repo := LoadRepo(ctx, opts.Runner, lim, p, opts.GitTimeout)
				if ctx.Err() != nil {
					return
				}
				// The main worktree path normally equals p; if it differs
				// (e.g. core.worktree), make sure it is not sent twice.
				if pathx.FoldKey(pathx.Canonical(repo.Path)) != pathx.FoldKey(p) && !claim(repo.Path) {
					return
				}
				repo.Name = repoName(roots, repo.Path, p)
				send(Event{Repo: &repo})
			}()
		}
	}()
	go func() {
		wg.Wait()
		close(events)
	}()
	return events
}

// LoadRepo lists the worktrees of the repo at path and loads the status of
// each worktree concurrently, every git call bounded by lim and by timeout
// (0 means DefaultGitTimeout, < 0 no limit). A timeout becomes an error such
// as "status <path>: timed out after 10s" wrapping context.DeadlineExceeded.
// Failures are recorded in Repo.Err. Name is left empty for the caller to
// fill.
func LoadRepo(ctx context.Context, r gitx.Runner, lim Limiter, path string, timeout time.Duration) Repo {
	if timeout == 0 {
		timeout = DefaultGitTimeout
	}
	// call runs f under a slot of lim with the per-call timeout applied.
	call := func(what string, f func(context.Context) error) error {
		return lim.Do(ctx, func() error {
			cctx, cancel := ctx, context.CancelFunc(func() {})
			if timeout > 0 {
				cctx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			err := f(cctx)
			if err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s: timed out after %v: %w", what, timeout, context.DeadlineExceeded)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			return nil
		})
	}

	repo := Repo{Path: path}
	var wts []gitx.Worktree
	err := call("worktree list "+path, func(ctx context.Context) error {
		var err error
		wts, err = gitx.ListWorktrees(ctx, r, path)
		return err
	})
	if err != nil {
		repo.Err = err
		return repo
	}
	repo.Worktrees = wts
	if len(wts) > 0 && wts[0].IsMain && wts[0].Path != "" {
		repo.Path = wts[0].Path
	}

	errs := make([]error, len(wts))
	var wg sync.WaitGroup
	for i := range wts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = call("status "+wts[i].Path, func(ctx context.Context) error {
				st, err := gitx.LoadStatus(ctx, r, wts[i])
				wts[i].Status = st
				return err
			})
		}()
	}
	wg.Wait()
	repo.Err = errors.Join(errs...)
	return repo
}

// repoName returns found (the path discovery reported) relative to the first
// root containing it, falling back to the repo path, then to its base name.
func repoName(roots []string, path, found string) string {
	// found carries the casing of the root it was discovered under.
	for _, cand := range []string{found, pathx.Canonical(path)} {
		for _, root := range roots {
			if pathx.IsWithin(root, cand) {
				if rel, err := filepath.Rel(root, cand); err == nil {
					if rel == "." {
						return filepath.Base(cand)
					}
					return rel
				}
			}
		}
	}
	return filepath.Base(path)
}
