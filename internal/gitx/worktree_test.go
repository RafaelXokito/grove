package gitx

import (
	"reflect"
	"strings"
	"testing"
)

const (
	sha1 = "cba4826bc944def0b1f4275dd2d28d764097f334"
	sha2 = "0123456789abcdef0123456789abcdef01234567"
)

func TestParseWorktreeList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Worktree
	}{
		{
			name:  "empty",
			input: "",
			want:  nil,
		},
		{
			name:  "main only",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n",
			want:  []Worktree{{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true}},
		},
		{
			name: "main and linked",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/repo-feat\nHEAD " + sha2 + "\nbranch refs/heads/feat\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/repo-feat", HEAD: sha2, Branch: "feat"},
			},
		},
		{
			name:  "branch with slashes keeps hierarchy",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/user/feat-1\n\n",
			want:  []Worktree{{Path: "/x/repo", HEAD: sha1, Branch: "user/feat-1", IsMain: true}},
		},
		{
			name: "detached",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/det\nHEAD " + sha2 + "\ndetached\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/det", HEAD: sha2},
			},
		},
		{
			name: "locked with and without reason",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/a\nHEAD " + sha1 + "\nbranch refs/heads/a\nlocked agent running\n\n" +
				"worktree /x/b\nHEAD " + sha1 + "\nbranch refs/heads/b\nlocked\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/a", HEAD: sha1, Branch: "a", Locked: true, LockReason: "agent running"},
				{Path: "/x/b", HEAD: sha1, Branch: "b", Locked: true},
			},
		},
		{
			name: "locked with quoted multi-line reason",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/a\nHEAD " + sha1 + "\ndetached\nlocked \"why\\nmulti \\\"q\\\"\"\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/a", HEAD: sha1, Locked: true, LockReason: "why\nmulti \"q\""},
			},
		},
		{
			name: "prunable",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/gone\nHEAD " + sha1 + "\nbranch refs/heads/gone\nprunable gitdir file points to non-existent location\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/gone", HEAD: sha1, Branch: "gone", Prunable: true, PruneReason: "gitdir file points to non-existent location"},
			},
		},
		{
			name: "locked and prunable",
			input: "worktree /x/repo\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n" +
				"worktree /x/gone\nHEAD " + sha1 + "\ndetached\nlocked keep\nprunable gone\n\n",
			want: []Worktree{
				{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/gone", HEAD: sha1, Locked: true, LockReason: "keep", Prunable: true, PruneReason: "gone"},
			},
		},
		{
			name: "bare with linked worktree",
			input: "worktree /x/repo.git\nbare\n\n" +
				"worktree /x/wt\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n",
			want: []Worktree{
				{Path: "/x/repo.git", IsMain: true, IsBare: true},
				{Path: "/x/wt", HEAD: sha1, Branch: "main"},
			},
		},
		{
			name:  "path with spaces",
			input: "worktree /x/my repo/wt one\nHEAD " + sha1 + "\nbranch refs/heads/main\n\n",
			want:  []Worktree{{Path: "/x/my repo/wt one", HEAD: sha1, Branch: "main", IsMain: true}},
		},
		{
			name:  "no trailing blank line, CRLF tolerated, unknown attr ignored",
			input: "worktree /x/repo\r\nHEAD " + sha1 + "\r\nfuture-attr x\r\nbranch refs/heads/main",
			want:  []Worktree{{Path: "/x/repo", HEAD: sha1, Branch: "main", IsMain: true}},
		},
		{
			name: "NUL-terminated (-z), values verbatim",
			input: "worktree /x/my repo\x00HEAD " + sha1 + "\x00branch refs/heads/main\x00\x00" +
				"worktree /x/det\x00HEAD " + sha2 + "\x00detached\x00locked why\nmulti\x00\x00" +
				"worktree /x/\"q\"\x00HEAD " + sha2 + "\x00branch refs/heads/q\x00prunable gone\x00\x00",
			want: []Worktree{
				{Path: "/x/my repo", HEAD: sha1, Branch: "main", IsMain: true},
				{Path: "/x/det", HEAD: sha2, Locked: true, LockReason: "why\nmulti"},
				{Path: "/x/\"q\"", HEAD: sha2, Branch: "q", Prunable: true, PruneReason: "gone"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWorktreeList([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseWorktreeListErrors(t *testing.T) {
	tests := []struct {
		name, input, wantSub string
	}{
		{"attribute before worktree", "HEAD " + sha1 + "\n", "before any worktree"},
		{"worktree without path", "worktree\n", "missing path"},
		{"bad quoting", "worktree /x\nlocked \"bad\\q\"\n", "bad quoted value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWorktreeList([]byte(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantSub)
			}
		})
	}
}

func TestWorktreeHelpers(t *testing.T) {
	tests := []struct {
		name         string
		wt           Worktree
		wantDetached bool
		wantShort    string
	}{
		{"on branch", Worktree{HEAD: sha1, Branch: "main"}, false, "cba4826"},
		{"detached", Worktree{HEAD: sha2}, true, "0123456"},
		{"bare", Worktree{IsBare: true}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.wt.Detached(); got != tt.wantDetached {
				t.Errorf("Detached() = %v, want %v", got, tt.wantDetached)
			}
			if got := tt.wt.ShortHEAD(); got != tt.wantShort {
				t.Errorf("ShortHEAD() = %q, want %q", got, tt.wantShort)
			}
		})
	}
}
