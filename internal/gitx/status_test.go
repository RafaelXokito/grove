package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseStatus(t *testing.T) {
	const hdr = "# branch.oid " + sha1 + "\n# branch.head main\n"
	tests := []struct {
		name    string
		input   string
		want    Status
		wantErr bool
	}{
		{name: "empty", input: "", want: Status{}},
		{name: "clean no upstream", input: hdr, want: Status{}},
		{
			name:  "clean with upstream in sync",
			input: hdr + "# branch.upstream origin/main\n# branch.ab +0 -0\n",
			want:  Status{Upstream: "origin/main", HasUpstream: true},
		},
		{
			name:  "ahead and behind",
			input: hdr + "# branch.upstream origin/main\n# branch.ab +2 -1\n",
			want:  Status{Upstream: "origin/main", Ahead: 2, Behind: 1, HasUpstream: true},
		},
		{
			name:  "upstream gone",
			input: hdr + "# branch.upstream origin/deleted\n",
			want:  Status{Upstream: "origin/deleted"},
		},
		{
			name:  "initial commit detached headers",
			input: "# branch.oid (initial)\n# branch.head (detached)\n",
			want:  Status{},
		},
		{
			name: "all entry kinds, ignored not counted",
			input: hdr + "# stash 3\n" +
				"1 .M N... 100644 100644 100644 " + sha1 + " " + sha1 + " a.txt\n" +
				"1 M. N... 100644 100644 100644 " + sha1 + " " + sha2 + " \"we\\tird\"\n" +
				"2 R. N... 100644 100644 100644 " + sha1 + " " + sha1 + " R100 new.txt\told.txt\n" +
				"u UU N... 100644 100644 100644 100644 " + sha1 + " " + sha2 + " " + sha1 + " c.txt\n" +
				"? untracked.txt\n" +
				"! ignored.log\n",
			want: Status{Changed: 5},
		},
		{name: "crlf tolerated", input: "# branch.ab +1 -0\r\n? x\r\n", want: Status{Ahead: 1, HasUpstream: true, Changed: 1}},
		{name: "bad ab", input: "# branch.ab 2 1\n", wantErr: true},
		{name: "bad ab number", input: "# branch.ab +x -1\n", wantErr: true},
		{name: "unknown entry skipped", input: "Z what\n? x\n", want: Status{Changed: 1}},
		{name: "malformed entry", input: "1x\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStatus([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadStatusArgsAndSkips(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	f := &fakeRunner{out: []byte("# branch.ab +1 -2\n? x\n")}
	st, err := LoadStatus(ctx, f, Worktree{Path: dir, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if f.dir != dir || fmt.Sprint(f.args) != "[status --porcelain=v2 --branch --untracked-files=normal --ignore-submodules=dirty]" {
		t.Errorf("ran dir=%q args=%v", f.dir, f.args)
	}
	if st == nil || *st != (Status{Changed: 1, Ahead: 1, Behind: 2, HasUpstream: true}) {
		t.Errorf("status = %+v", st)
	}

	f = &fakeRunner{err: errors.New("boom")}
	if _, err := LoadStatus(ctx, f, Worktree{Path: dir}); err == nil {
		t.Error("want runner error")
	}
	f = &fakeRunner{out: []byte("# branch.ab bad\n")}
	if _, err := LoadStatus(ctx, f, Worktree{Path: dir}); err == nil {
		t.Error("want parse error")
	}

	for _, wt := range []Worktree{
		{Path: dir, Prunable: true, PruneReason: "gitdir file points to non-existent location"},
		{Path: dir, IsBare: true, IsMain: true},
		{Path: filepath.Join(dir, "does-not-exist")},
	} {
		f = &fakeRunner{err: errors.New("must not run")}
		st, err := LoadStatus(ctx, f, wt)
		if st != nil || err != nil || f.args != nil {
			t.Errorf("LoadStatus(%+v) = %+v, %v; ran %v; want nil, nil, no git call", wt, st, err, f.args)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadStatusFixture(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	r := NewExecRunner()

	load := func(t *testing.T, wt Worktree) *Status {
		t.Helper()
		st, err := LoadStatus(ctx, r, wt)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	t.Run("clean, no upstream", func(t *testing.T) {
		repo := filepath.Join(tempDir(t), "repo")
		newRepo(t, repo)
		st := load(t, Worktree{Path: repo, Branch: "main"})
		if st == nil || *st != (Status{}) {
			t.Errorf("status = %+v, want clean without upstream", st)
		}
	})

	t.Run("dirty (3): 2 modified + 1 untracked", func(t *testing.T) {
		repo := filepath.Join(tempDir(t), "repo")
		newRepo(t, repo)
		writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
		writeFile(t, filepath.Join(repo, "b.txt"), "b\n")
		git(t, repo, "add", "a.txt", "b.txt")
		git(t, repo, "commit", "-q", "-m", "files")
		writeFile(t, filepath.Join(repo, "a.txt"), "a2\n") // modified, unstaged
		writeFile(t, filepath.Join(repo, "b.txt"), "b2\n")
		git(t, repo, "add", "b.txt") // modified, staged
		writeFile(t, filepath.Join(repo, "new.txt"), "n\n")
		st := load(t, Worktree{Path: repo, Branch: "main"})
		if st == nil || st.Changed != 3 || st.HasUpstream {
			t.Errorf("status = %+v, want Changed=3 and no upstream", st)
		}
	})

	t.Run("ahead/behind vs local bare remote, then no upstream", func(t *testing.T) {
		root := tempDir(t)
		remote := filepath.Join(root, "remote.git")
		git(t, "", "init", "-q", "--bare", "-b", "main", remote)
		repo := filepath.Join(root, "repo")
		newRepo(t, repo)
		git(t, repo, "remote", "add", "origin", remote)
		git(t, repo, "push", "-q", "-u", "origin", "main")

		// Another clone pushes one commit: we are 1 behind.
		other := filepath.Join(root, "other")
		git(t, "", "clone", "-q", remote, other)
		git(t, other, "commit", "-q", "--allow-empty", "-m", "theirs")
		git(t, other, "push", "-q", "origin", "main")
		git(t, repo, "fetch", "-q", "origin")
		// Two local commits: 2 ahead.
		git(t, repo, "commit", "-q", "--allow-empty", "-m", "mine 1")
		git(t, repo, "commit", "-q", "--allow-empty", "-m", "mine 2")

		st := load(t, Worktree{Path: repo, Branch: "main"})
		want := Status{Upstream: "origin/main", Ahead: 2, Behind: 1, HasUpstream: true}
		if st == nil || *st != want {
			t.Errorf("status = %+v, want %+v", st, want)
		}

		// A linked worktree on a branch without upstream shows no ahead/behind.
		wt := filepath.Join(root, "repo-feat")
		git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
		st = load(t, Worktree{Path: wt, Branch: "feat"})
		if st == nil || *st != (Status{}) {
			t.Errorf("feat status = %+v, want clean without upstream", st)
		}
	})

	t.Run("detached worktree", func(t *testing.T) {
		root := tempDir(t)
		repo := filepath.Join(root, "repo")
		newRepo(t, repo)
		det := filepath.Join(root, "det")
		git(t, repo, "worktree", "add", "-q", "--detach", det)
		writeFile(t, filepath.Join(det, "u.txt"), "u\n")
		st := load(t, Worktree{Path: det})
		if st == nil || *st != (Status{Changed: 1}) {
			t.Errorf("status = %+v, want Changed=1 without upstream", st)
		}
	})

	t.Run("prunable worktree from listing is not queried", func(t *testing.T) {
		root := tempDir(t)
		repo := filepath.Join(root, "repo")
		newRepo(t, repo)
		gone := filepath.Join(root, "gone")
		git(t, repo, "worktree", "add", "-q", "-b", "gone", gone)
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		wts, err := ListWorktrees(ctx, r, repo)
		if err != nil {
			t.Fatal(err)
		}
		var seen int
		for _, w := range wts {
			st, err := LoadStatus(ctx, r, w)
			if err != nil {
				t.Fatalf("LoadStatus(%s): %v", w.Path, err)
			}
			if w.Prunable {
				seen++
				if st != nil {
					t.Errorf("prunable %s: status = %+v, want nil", w.Path, st)
				}
			} else if st == nil {
				t.Errorf("%s: status nil, want loaded", w.Path)
			}
		}
		if seen != 1 {
			t.Errorf("saw %d prunable worktrees, want 1: %+v", seen, wts)
		}
	})
}
