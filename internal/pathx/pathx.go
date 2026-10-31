// Package pathx provides path canonicalisation and display helpers shared by
// the git, scan and session layers.
package pathx

import (
	"os"
	"path/filepath"
	"strings"
)

// Canonical returns an absolute, cleaned path with symlinks resolved, so that
// paths from different sources compare equal (e.g. /tmp vs /private/tmp on
// macOS). If p (or a trailing part of it) does not exist, the longest existing
// ancestor is resolved and the missing remainder is appended unchanged.
func Canonical(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	// Walk up to the nearest existing ancestor and resolve that.
	var rest []string
	cur := abs
	for {
		parent := filepath.Dir(cur)
		rest = append(rest, filepath.Base(cur))
		if parent == cur {
			return abs
		}
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r
		}
	}
}

// IsWithin reports whether child is equal to parent or located inside it.
// Both paths are compared lexically after filepath.Clean; callers that need
// symlink-insensitive comparison should pass Canonical paths. It never
// false-matches on a shared prefix ("/x/repo" does not contain "/x/repo-wt").
func IsWithin(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// AbbrevHome replaces a leading home directory in p with "~".
func AbbrevHome(p, home string) string {
	if home == "" || p == "" {
		return p
	}
	home = filepath.Clean(home)
	p = filepath.Clean(p)
	if p == home {
		return "~"
	}
	if IsWithin(home, p) {
		if rel, err := filepath.Rel(home, p); err == nil {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}

// Display renders p relative to root when it lies within root, otherwise
// home-abbreviated (FR-7). The root itself renders as ".".
func Display(p, root, home string) string {
	if root != "" && IsWithin(root, p) {
		if rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p)); err == nil {
			return rel
		}
	}
	return AbbrevHome(p, home)
}

// ExpandHome expands a leading "~" or "~/" in p using home.
func ExpandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// UserHome returns the current user's home directory, or "" if unknown.
func UserHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
