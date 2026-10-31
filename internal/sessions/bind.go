package sessions

import (
	"sort"
	"sync"

	"github.com/RafaelXokito/grove/internal/pathx"
)

type bindEntry struct {
	orig string // worktree path as given by the caller (git's path)
	key  string // pathx.FoldKey(pathx.Canonical(orig))
}

// Binder maps cwds to the worktree with the longest path equal to or an
// ancestor of the cwd. Worktree paths are canonicalised once at construction
// (SPEC T4 note a); each looked-up cwd is canonicalised once and memoised.
// Comparisons are case-insensitive on darwin (pathx.FoldKey). A Binder is
// safe for concurrent use.
type Binder struct {
	entries []bindEntry // sorted by key length, longest first

	mu    sync.Mutex
	cache map[string]string // cwd -> folded canonical key
}

// NewBinder builds a Binder for the given worktree paths. Empty paths are
// ignored; duplicates (after canonicalisation) keep the first occurrence.
func NewBinder(worktrees []string) *Binder {
	b := &Binder{cache: make(map[string]string)}
	seen := make(map[string]bool, len(worktrees))
	for _, wt := range worktrees {
		if wt == "" {
			continue
		}
		key := pathx.FoldKey(pathx.Canonical(wt))
		if seen[key] {
			continue
		}
		seen[key] = true
		b.entries = append(b.entries, bindEntry{orig: wt, key: key})
	}
	sort.SliceStable(b.entries, func(i, j int) bool {
		return len(b.entries[i].key) > len(b.entries[j].key)
	})
	return b
}

// Bind returns the worktree path (as passed to NewBinder) that cwd belongs to.
func (b *Binder) Bind(cwd string) (wt string, ok bool) {
	if cwd == "" || len(b.entries) == 0 {
		return "", false
	}
	key := b.canon(cwd)
	for _, e := range b.entries {
		if pathx.IsWithin(e.key, key) {
			return e.orig, true
		}
	}
	return "", false
}

func (b *Binder) canon(cwd string) string {
	b.mu.Lock()
	k, ok := b.cache[cwd]
	b.mu.Unlock()
	if ok {
		return k
	}
	k = pathx.FoldKey(pathx.Canonical(cwd))
	b.mu.Lock()
	b.cache[cwd] = k
	b.mu.Unlock()
	return k
}

// Bind is a one-shot convenience over NewBinder(worktrees).Bind(cwd). Callers
// binding many cwds should build a Binder once per scan instead.
func Bind(worktrees []string, cwd string) (wt string, ok bool) {
	return NewBinder(worktrees).Bind(cwd)
}
