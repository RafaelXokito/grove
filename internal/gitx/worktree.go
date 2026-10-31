package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path        string
	HEAD        string // full sha; "" for a bare entry
	Branch      string // "" if detached; short name ("feat"), not refs/heads/feat
	IsMain      bool   // first entry of the list (the main worktree or bare repo)
	IsBare      bool
	Locked      bool
	LockReason  string
	Prunable    bool
	PruneReason string
	Status      *Status // nil until loaded or if missing
}

// Detached reports whether the worktree has a detached HEAD.
func (w Worktree) Detached() bool { return !w.IsBare && w.Branch == "" }

// ShortHEAD returns the first 7 characters of HEAD.
func (w Worktree) ShortHEAD() string {
	if len(w.HEAD) > 7 {
		return w.HEAD[:7]
	}
	return w.HEAD
}

// Status is the dirty/upstream state of a worktree (parsed in T2 from
// `git status --porcelain=v2 --branch`).
type Status struct {
	Changed     int
	Upstream    string
	Ahead       int
	Behind      int
	HasUpstream bool
}

// ParseWorktreeList parses the output of `git worktree list --porcelain`,
// either newline-terminated (values may be C-quoted by git) or
// NUL-terminated (`-z`, values verbatim). The first entry is marked IsMain.
func ParseWorktreeList(porcelain []byte) ([]Worktree, error) {
	sep := byte('\n')
	if bytes.IndexByte(porcelain, 0) >= 0 {
		sep = 0
	}
	quoted := sep == '\n'

	var (
		out []Worktree
		cur *Worktree
	)
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for i, raw := range bytes.Split(porcelain, []byte{sep}) {
		line := string(raw)
		if quoted {
			line = strings.TrimSuffix(line, "\r")
		}
		if line == "" {
			flush()
			continue
		}
		key, val, hasVal := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			if !hasVal || val == "" {
				return nil, fmt.Errorf("gitx: worktree list line %d: missing path", i+1)
			}
			p, err := unquoteValue(val, quoted)
			if err != nil {
				return nil, fmt.Errorf("gitx: worktree list line %d: %w", i+1, err)
			}
			cur = &Worktree{Path: p}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("gitx: worktree list line %d: %q before any worktree line", i+1, line)
		}
		switch key {
		case "HEAD":
			cur.HEAD = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Branch = ""
		case "bare":
			cur.IsBare = true
		case "locked":
			cur.Locked = true
			r, err := unquoteValue(val, quoted)
			if err != nil {
				return nil, fmt.Errorf("gitx: worktree list line %d: %w", i+1, err)
			}
			cur.LockReason = r
		case "prunable":
			cur.Prunable = true
			r, err := unquoteValue(val, quoted)
			if err != nil {
				return nil, fmt.Errorf("gitx: worktree list line %d: %w", i+1, err)
			}
			cur.PruneReason = r
		default:
			// Unknown attributes are ignored for forward compatibility.
		}
	}
	flush()
	if len(out) > 0 {
		out[0].IsMain = true
	}
	return out, nil
}

// unquoteValue undoes git's C-style quoting, which is applied in non -z
// porcelain output when a value contains special characters (e.g. a newline
// in a lock reason).
func unquoteValue(v string, quoted bool) (string, error) {
	if !quoted || len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v, nil
	}
	s, err := strconv.Unquote(v)
	if err != nil {
		return "", fmt.Errorf("bad quoted value %s: %w", v, err)
	}
	return s, nil
}

// ListWorktrees runs `git -C repo worktree list --porcelain -z` and parses it.
// Git older than 2.36 lacks -z; on a usage error it retries without it.
func ListWorktrees(ctx context.Context, r Runner, repo string) ([]Worktree, error) {
	out, err := r.Run(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil && isUnknownSwitch(err) && ctx.Err() == nil {
		out, err = r.Run(ctx, repo, "worktree", "list", "--porcelain")
	}
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out)
}

// isUnknownSwitch reports whether err is git rejecting an option (usage
// error, exit status 129).
func isUnknownSwitch(err error) bool {
	var gerr *Error
	if !errors.As(err, &gerr) {
		return false
	}
	return strings.Contains(gerr.Stderr, "unknown switch") ||
		strings.Contains(gerr.Stderr, "unknown option") ||
		strings.HasPrefix(gerr.Stderr, "usage:")
}
