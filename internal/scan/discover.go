// Package scan discovers git repositories under one or more roots and loads
// their worktrees and status concurrently, streaming results so the UI can
// render repos as soon as they are found (FR-1, FR-18).
package scan

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/RafaelXokito/grove/internal/pathx"
)

// DefaultMaxDepth is the default directory depth scanned below each root
// (the root itself is depth 0).
const DefaultMaxDepth = 3

// skipDirs are directory names never descended into (in addition to hidden
// directories).
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".git":         true,
}

// DirError records a directory that could not be read during discovery.
type DirError struct {
	Path string
	Err  error
}

func (e *DirError) Error() string { return fmt.Sprintf("scan %s: %v", e.Path, e.Err) }
func (e *DirError) Unwrap() error { return e.Err }

// Discover walks each root down to maxDepth levels and sends the canonical
// path of every repository it finds to out: directories containing a .git
// directory, and bare repositories. A directory whose .git is a file (a
// linked worktree) is not a repo of its own; instead its main repository is
// resolved via the shared git dir and sent, so a worktree under a root whose
// main repo lives elsewhere still surfaces that repo. Every path is sent at
// most once, even when reachable from several roots (compared with
// pathx.FoldKey, so case-insensitively on darwin).
//
// Directories named node_modules, vendor, .git and hidden directories are
// skipped; symlinks are not followed. Unreadable directories do not stop the
// walk: their errors (*DirError) are joined into the returned error. If ctx
// is cancelled, Discover stops promptly and returns ctx.Err(). Discover does
// not close out. maxDepth 0 means DefaultMaxDepth; a negative maxDepth scans
// only the roots themselves.
func Discover(ctx context.Context, roots []string, maxDepth int, out chan<- string) error {
	var (
		mu   sync.Mutex
		errs []error
	)
	discover(ctx, roots, maxDepth, out, func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// discover is Discover with errors reported to onErr as they occur (onErr
// may be called concurrently).
func discover(ctx context.Context, roots []string, maxDepth int, out chan<- string, onErr func(error)) {
	switch {
	case maxDepth == 0:
		maxDepth = DefaultMaxDepth
	case maxDepth < 0:
		maxDepth = 0
	}
	d := &discoverer{
		ctx:      ctx,
		out:      out,
		onErr:    onErr,
		maxDepth: maxDepth,
		sem:      make(chan struct{}, runtime.NumCPU()),
		seen:     map[string]bool{},
	}
	seenRoot := map[string]bool{}
	for _, root := range roots {
		root = pathx.Canonical(root)
		if root == "" || seenRoot[pathx.FoldKey(root)] {
			continue
		}
		seenRoot[pathx.FoldKey(root)] = true
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.walk(root, 0)
		}()
	}
	d.wg.Wait()
}

type discoverer struct {
	ctx      context.Context
	out      chan<- string
	onErr    func(error)
	maxDepth int
	sem      chan struct{} // bounds extra walker goroutines
	wg       sync.WaitGroup

	mu   sync.Mutex
	seen map[string]bool
}

func (d *discoverer) walk(dir string, depth int) {
	if d.ctx.Err() != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if d.ctx.Err() != nil {
			return
		}
		d.onErr(&DirError{Path: dir, Err: err})
		// ReadDir may return partial results alongside an error.
		if len(entries) == 0 {
			return
		}
	}

	if isBare(entries) {
		d.emit(dir)
		return
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			continue
		}
		switch {
		case e.IsDir():
			d.emit(dir)
		case e.Type().IsRegular():
			if main := mainRepoOfLinked(filepath.Join(dir, ".git")); main != "" {
				d.emit(main)
			}
		}
		break
	}

	if depth >= d.maxDepth {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// DirEntry types come from lstat, so symlinks are never IsDir.
		if !e.IsDir() || skipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(dir, name)
		select {
		case d.sem <- struct{}{}:
			d.wg.Add(1)
			go func() {
				defer func() { <-d.sem; d.wg.Done() }()
				d.walk(child, depth+1)
			}()
		default:
			d.walk(child, depth+1)
		}
	}
}

// emit sends path to out unless it was already sent or ctx is done.
func (d *discoverer) emit(path string) {
	key := pathx.FoldKey(path)
	d.mu.Lock()
	dup := d.seen[key]
	d.seen[key] = true
	d.mu.Unlock()
	if dup || d.ctx.Err() != nil {
		return
	}
	select {
	case d.out <- path:
	case <-d.ctx.Done():
	}
}

// isBare reports whether a directory listing looks like a bare repository
// (HEAD file plus objects/ and refs/ directories).
func isBare(entries []fs.DirEntry) bool {
	var head, objects, refs bool
	for _, e := range entries {
		switch e.Name() {
		case "HEAD":
			head = e.Type().IsRegular()
		case "objects":
			objects = e.IsDir()
		case "refs":
			refs = e.IsDir()
		}
	}
	return head && objects && refs
}

// mainRepoOfLinked resolves the main repository of a linked worktree from
// its .git file ("gitdir: <repo>/.git/worktrees/<name>", whose "commondir"
// file points at the shared git dir). It returns "" for anything else, e.g.
// a submodule's .git file, which has no commondir.
func mainRepoOfLinked(gitFile string) string {
	gitdir := readGitdir(gitFile)
	if gitdir == "" {
		return ""
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(gitFile), gitdir)
	}
	b, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	common = pathx.Canonical(common)
	if fi, err := os.Stat(common); err != nil || !fi.IsDir() {
		return ""
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common // bare repository
}

func readGitdir(gitFile string) string {
	f, err := os.Open(gitFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "gitdir:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
