package gitx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// ParseStatus parses the output of `git status --porcelain=v2 --branch`
// (newline-terminated, not -z).
//
// Changed counts every changed entry: ordinary (1), renamed/copied (2),
// unmerged (u) and untracked (?) lines; ignored (!) lines and unknown record
// types are not counted.
// Upstream is set from "# branch.upstream". HasUpstream (and Ahead/Behind)
// are set only when git reports "# branch.ab", i.e. the upstream is
// configured and exists; a configured but deleted ("gone") upstream yields
// Upstream != "" with HasUpstream == false so no ahead/behind is shown.
func ParseStatus(porcelainV2 []byte) (Status, error) {
	var st Status
	for i, line := range strings.Split(string(porcelainV2), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if hdr, ok := strings.CutPrefix(line, "# "); ok {
			key, val, _ := strings.Cut(hdr, " ")
			switch key {
			case "branch.upstream":
				st.Upstream = val
			case "branch.ab":
				a, b, err := parseAB(val)
				if err != nil {
					return Status{}, fmt.Errorf("gitx: status line %d: %w", i+1, err)
				}
				st.Ahead, st.Behind, st.HasUpstream = a, b, true
			default:
				// branch.oid, branch.head, stash, ... are not needed.
			}
			continue
		}
		switch line[0] {
		case '1', '2', 'u', '?':
			if len(line) < 2 || line[1] != ' ' {
				return Status{}, fmt.Errorf("gitx: status line %d: malformed entry %q", i+1, line)
			}
			st.Changed++
		case '!':
			// ignored file (only with --ignored); not a change.
		default:
			// Unknown record types (e.g. added by a future git) are skipped
			// rather than failing the whole status.
		}
	}
	return st, nil
}

// parseAB parses the value of "# branch.ab +<ahead> -<behind>".
func parseAB(v string) (ahead, behind int, err error) {
	as, bs, ok := strings.Cut(v, " ")
	if !ok || !strings.HasPrefix(as, "+") || !strings.HasPrefix(bs, "-") {
		return 0, 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	if ahead, err = strconv.Atoi(as[1:]); err != nil || ahead < 0 {
		return 0, 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	if behind, err = strconv.Atoi(bs[1:]); err != nil || behind < 0 {
		return 0, 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	return ahead, behind, nil
}

// LoadStatus runs `git -C <wt.Path> status --porcelain=v2 --branch` and
// parses it. It returns (nil, nil) without running git when the worktree has
// no working tree to inspect: a bare entry, a prunable worktree, or one whose
// directory no longer exists (the UI shows these as "missing"/not actionable).
func LoadStatus(ctx context.Context, r Runner, wt Worktree) (*Status, error) {
	if wt.IsBare || wt.Prunable {
		return nil, nil
	}
	if _, err := os.Stat(wt.Path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	// --untracked-files=normal overrides a user's status.showUntrackedFiles
	// so untracked files are always counted as changes.
	// --ignore-submodules=dirty keeps scans fast (no recursion into
	// submodule work trees) while still reporting moved submodule commits.
	out, err := r.Run(ctx, wt.Path, "status", "--porcelain=v2", "--branch", "--untracked-files=normal", "--ignore-submodules=dirty")
	if err != nil {
		return nil, err
	}
	st, err := ParseStatus(out)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
