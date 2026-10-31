package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RafaelXokito/grove/internal/gitx"
	"github.com/RafaelXokito/grove/internal/pathx"
)

// TestMain makes git hermetic: the user's global/system config and HOME must
// not leak into fixture repos.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "grove-scan-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "grove test",
		"GIT_AUTHOR_EMAIL":    "test@grove.invalid",
		"GIT_COMMITTER_NAME":  "grove test",
		"GIT_COMMITTER_EMAIL": "test@grove.invalid",
		"LC_ALL":              "C",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitx.NewExecRunner().Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func newRepo(t *testing.T, path string) {
	t.Helper()
	git(t, "", "init", "-q", "-b", "main", path)
	git(t, path, "commit", "-q", "--allow-empty", "-m", "initial")
}

func tempDir(t *testing.T) string {
	t.Helper()
	return pathx.Canonical(t.TempDir())
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func discoverAll(t *testing.T, roots []string, depth int) ([]string, error) {
	t.Helper()
	out := make(chan string)
	var got []string
	done := make(chan struct{})
	go func() {
		for p := range out {
			got = append(got, p)
		}
		close(done)
	}()
	err := Discover(context.Background(), roots, depth, out)
	close(out)
	<-done
	slices.Sort(got)
	return got, err
}

func scanAll(t *testing.T, opts Options) (repos []*Repo, errs []error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ev := range Scan(ctx, opts) {
		if ev.Repo != nil {
			repos = append(repos, ev.Repo)
		} else {
			errs = append(errs, ev.Err)
		}
	}
	slices.SortFunc(repos, func(a, b *Repo) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return repos, errs
}

// TestScanAcceptance is the T3 acceptance fixture: 2 repos, 1 non-repo, 1
// linked worktree inside the root, 1 repo beyond depth → exactly 2 repos.
func TestScanAcceptance(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "group", "b")
	newRepo(t, a)
	newRepo(t, b)
	mkdir(t, filepath.Join(root, "c", "src"))
	wt := filepath.Join(root, "a-feat")
	git(t, a, "worktree", "add", "-q", "-b", "feat", wt)
	newRepo(t, filepath.Join(root, "x", "y", "z", "deep")) // depth 4
	// Skipped directories, even though they contain repos.
	newRepo(t, filepath.Join(root, "node_modules", "pkg"))
	newRepo(t, filepath.Join(root, "vendor", "lib"))
	newRepo(t, filepath.Join(root, ".hidden", "repo"))
	// Dirty a worktree.
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos, errs := scanAll(t, Options{Roots: []string{root}, MaxDepth: 3, Runner: gitx.NewExecRunner()})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	if repos[0].Name != "a" || repos[1].Name != filepath.Join("group", "b") {
		t.Errorf("names = %q, %q", repos[0].Name, repos[1].Name)
	}
	ra := repos[0]
	if ra.Err != nil || pathx.Canonical(ra.Path) != a || len(ra.Worktrees) != 2 {
		t.Fatalf("repo a = %+v", ra)
	}
	for _, w := range ra.Worktrees {
		if w.Status == nil {
			t.Fatalf("status not loaded for %s", w.Path)
		}
		want := 0
		if pathx.Canonical(w.Path) == wt {
			want = 1
		}
		if w.Status.Changed != want {
			t.Errorf("%s: changed = %d, want %d", w.Path, w.Status.Changed, want)
		}
	}
}

func TestDiscoverDepthAndRootRepo(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	newRepo(t, root) // the root itself is a repo
	newRepo(t, filepath.Join(root, "one"))
	newRepo(t, filepath.Join(root, "one", "nested")) // repo nested in a repo
	newRepo(t, filepath.Join(root, "p", "q", "r"))   // depth 3

	for _, tc := range []struct {
		depth int
		want  []string
	}{
		{-1, []string{root}},
		{1, []string{root, filepath.Join(root, "one")}},
		{3, []string{root, filepath.Join(root, "one"), filepath.Join(root, "one", "nested"), filepath.Join(root, "p", "q", "r")}},
		{0, []string{root, filepath.Join(root, "one"), filepath.Join(root, "one", "nested"), filepath.Join(root, "p", "q", "r")}}, // default 3
	} {
		got, err := discoverAll(t, []string{root}, tc.depth)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(tc.want)
		if !slices.Equal(got, tc.want) {
			t.Errorf("depth %d: got %v, want %v", tc.depth, got, tc.want)
		}
	}
}

func TestDiscoverLinkedWorktreeResolvesMain(t *testing.T) {
	requireGit(t)
	outside := tempDir(t)
	root := tempDir(t)
	main := filepath.Join(outside, "main")
	newRepo(t, main)
	git(t, main, "worktree", "add", "-q", "-b", "feat", filepath.Join(root, "wt"))
	// A submodule-style .git file without commondir is ignored.
	sub := filepath.Join(root, "sub")
	mkdir(t, sub)
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := discoverAll(t, []string{root}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{main}) {
		t.Errorf("got %v, want [%s]", got, main)
	}
}

func TestScanOverlappingRootsAndSymlinkDedupe(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	repo := filepath.Join(root, "org", "repo")
	newRepo(t, repo)
	git(t, repo, "worktree", "add", "-q", "-b", "feat", filepath.Join(root, "org", "repo-feat"))
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	repos, errs := scanAll(t, Options{
		Roots:    []string{root, filepath.Join(root, "org"), link, root + "/"},
		MaxDepth: 3,
		Runner:   gitx.NewExecRunner(),
	})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1: %+v", len(repos), repos)
	}
	if repos[0].Name != filepath.Join("org", "repo") {
		t.Errorf("name = %q", repos[0].Name)
	}
}

func TestScanBareRepo(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	src := filepath.Join(tempDir(t), "src")
	newRepo(t, src)
	bare := filepath.Join(root, "proj.git")
	git(t, "", "clone", "-q", "--bare", src, bare)
	git(t, bare, "worktree", "add", "-q", filepath.Join(root, "proj-main"), "main")

	repos, errs := scanAll(t, Options{Roots: []string{root}, MaxDepth: 3, Runner: gitx.NewExecRunner()})
	if len(errs) != 0 || len(repos) != 1 {
		t.Fatalf("repos=%+v errs=%v", repos, errs)
	}
	r := repos[0]
	if r.Err != nil || len(r.Worktrees) != 2 || !r.Worktrees[0].IsBare || r.Name != "proj.git" {
		t.Fatalf("bare repo = %+v", r)
	}
}

func TestScanUnreadableDirNotFatal(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root can read any directory")
	}
	root := tempDir(t)
	newRepo(t, filepath.Join(root, "ok"))
	locked := filepath.Join(root, "locked")
	newRepo(t, filepath.Join(locked, "inner"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	repos, errs := scanAll(t, Options{Roots: []string{root}, MaxDepth: 3, Runner: gitx.NewExecRunner()})
	if len(repos) != 1 || repos[0].Name != "ok" {
		t.Fatalf("repos = %+v", repos)
	}
	var de *DirError
	if len(errs) != 1 || !errors.As(errs[0], &de) || de.Path != locked {
		t.Fatalf("errs = %v, want one DirError for %s", errs, locked)
	}
}

func TestScanRepoErrorNotFatal(t *testing.T) {
	requireGit(t)
	root := tempDir(t)
	newRepo(t, filepath.Join(root, "good"))
	// A .git directory that is not a valid repository.
	mkdir(t, filepath.Join(root, "broken", ".git"))

	repos, errs := scanAll(t, Options{Roots: []string{root}, MaxDepth: 3, Runner: gitx.NewExecRunner()})
	if len(errs) != 0 || len(repos) != 2 {
		t.Fatalf("repos=%+v errs=%v", repos, errs)
	}
	if repos[0].Name != "broken" || repos[0].Err == nil || len(repos[0].Worktrees) != 0 {
		t.Errorf("broken = %+v", repos[0])
	}
	if repos[1].Name != "good" || repos[1].Err != nil {
		t.Errorf("good = %+v", repos[1])
	}
}

// blockingRunner blocks every call until ctx is done and counts concurrency.
type blockingRunner struct {
	cur, max atomic.Int32
	started  chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	n := r.cur.Add(1)
	defer r.cur.Add(-1)
	for {
		m := r.max.Load()
		if n <= m || r.max.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestScanCancellationAndBound(t *testing.T) {
	root := tempDir(t)
	for i := range 20 {
		mkdir(t, filepath.Join(root, fmt.Sprintf("r%02d", i), ".git"))
	}
	r := &blockingRunner{started: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	events := Scan(ctx, Options{Roots: []string{root}, MaxDepth: 3, Runner: r, Limiter: NewLimiter(3)})

	select {
	case <-r.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no git call started")
	}
	time.Sleep(50 * time.Millisecond) // let the pool fill up
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if m := r.max.Load(); m > 3 {
					t.Errorf("max concurrent git calls = %d, want <= 3", m)
				}
				return
			}
			t.Errorf("unexpected event after cancel: %+v", ev)
		case <-deadline:
			t.Fatal("events channel not closed after cancel")
		}
	}
}

func TestDiscoverCancelled(t *testing.T) {
	root := tempDir(t)
	for i := range 10 {
		mkdir(t, filepath.Join(root, fmt.Sprintf("r%d", i), ".git"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan string) // never read: Discover must not block
	if err := Discover(ctx, []string{root}, 3, out); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// hangingStatusRunner answers `worktree list` with two worktrees and makes
// `status` hang (until ctx ends) for the second one only.
type hangingStatusRunner struct{ main, hung string }

func (r *hangingStatusRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if args[0] == "worktree" {
		return []byte("worktree " + r.main + "\x00HEAD 0123456789abcdef0123456789abcdef01234567\x00branch refs/heads/main\x00\x00" +
			"worktree " + r.hung + "\x00HEAD 0123456789abcdef0123456789abcdef01234567\x00branch refs/heads/feat\x00\x00"), nil
	}
	if dir == r.hung {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte("# branch.oid 0123456789abcdef0123456789abcdef01234567\n# branch.head main\n"), nil
}

func TestScanStatusTimeout(t *testing.T) {
	root := tempDir(t)
	main := filepath.Join(root, "repo")
	hung := filepath.Join(root, "repo-feat")
	mkdir(t, filepath.Join(main, ".git"))
	mkdir(t, hung)

	start := time.Now()
	repos, errs := scanAll(t, Options{
		Roots:      []string{root},
		MaxDepth:   3,
		Runner:     &hangingStatusRunner{main: main, hung: hung},
		GitTimeout: 100 * time.Millisecond,
	})
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("scan took %v; timeout not applied", el)
	}
	if len(errs) != 0 || len(repos) != 1 {
		t.Fatalf("repos=%+v errs=%v", repos, errs)
	}
	r := repos[0]
	if len(r.Worktrees) != 2 || r.Worktrees[0].Status == nil || r.Worktrees[1].Status != nil {
		t.Fatalf("worktrees = %+v", r.Worktrees)
	}
	if !errors.Is(r.Err, context.DeadlineExceeded) || !strings.Contains(r.Err.Error(), "timed out after 100ms") {
		t.Fatalf("err = %v, want status timeout", r.Err)
	}
}

// hangingListRunner makes every `git worktree list` hang until ctx ends.
type hangingListRunner struct{ calls atomic.Int32 }

func (r *hangingListRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestScanWorktreeListTimeoutFreesLimiter(t *testing.T) {
	root := tempDir(t)
	for _, n := range []string{"a", "b", "c"} {
		mkdir(t, filepath.Join(root, n, ".git"))
	}
	r := &hangingListRunner{}
	start := time.Now()
	// One slot: each hung call must time out and release it for the next.
	repos, errs := scanAll(t, Options{Roots: []string{root}, Runner: r, Limiter: NewLimiter(1), GitTimeout: 100 * time.Millisecond})
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("scan took %v", el)
	}
	if len(errs) != 0 || len(repos) != 3 || r.calls.Load() != 3 {
		t.Fatalf("repos=%+v errs=%v calls=%d", repos, errs, r.calls.Load())
	}
	for _, repo := range repos {
		if !errors.Is(repo.Err, context.DeadlineExceeded) || !strings.Contains(repo.Err.Error(), "worktree list") ||
			!strings.Contains(repo.Err.Error(), "timed out after 100ms") {
			t.Errorf("%s: err = %v", repo.Name, repo.Err)
		}
	}
}

func TestScanCaseFoldedRootsDarwin(t *testing.T) {
	requireGit(t)
	if runtime.GOOS != "darwin" {
		t.Skip("case folding applies on darwin only")
	}
	base := tempDir(t)
	upper := filepath.Join(base, "Repos")
	newRepo(t, filepath.Join(upper, "proj"))
	lower := filepath.Join(base, "repos")
	if _, err := os.Stat(lower); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	repos, errs := scanAll(t, Options{Roots: []string{upper, lower}, Runner: gitx.NewExecRunner()})
	if len(errs) != 0 || len(repos) != 1 || repos[0].Name != "proj" {
		t.Fatalf("repos=%+v errs=%v", repos, errs)
	}
}
