package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareAndNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		newer           bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.0.9", "v0.1.0", false},
		{"v0.10.0", "v0.9.0", true}, // numeric, not lexical
		{"v1.0.0", "v0.99.99", true},
		{"garbage", "v0.1.0", false},
		{"v0.2.0-rc1", "v0.1.0", false},
		{"v0.2.0", "dev", false},
		{"v0.2.0\x1b[31m", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.newer)
		}
	}
	for _, v := range []string{"dev", "(devel)", "", "v0.0.0-20261102-abcdef", "0.1.0"} {
		if !IsDev(v) {
			t.Errorf("IsDev(%q) = false", v)
		}
	}
	if IsDev("v0.1.0") {
		t.Error("IsDev(v0.1.0) = true")
	}
}

// api serves releases/latest with tag and counts requests.
func api(t *testing.T, body string) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		if r.URL.Path != "/repos/RafaelXokito/grove/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "grove/") {
			t.Errorf("bad headers: %v", r.Header)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func tagBody(tag string) string { return fmt.Sprintf(`{"tag_name":%q}`, tag) }

func TestAvailable(t *testing.T) {
	cases := []struct {
		name, body, current, want string
	}{
		{"newer", tagBody("v0.2.0"), "v0.1.0", "v0.2.0"},
		{"same", tagBody("v0.1.0"), "v0.1.0", ""},
		{"older", tagBody("v0.0.5"), "v0.1.0", ""},
		{"garbage tag", tagBody("v0.2.0; rm -rf /"), "v0.1.0", ""},
		{"escape in tag", tagBody("v9.9.9\x1b]0;x\x07"), "v0.1.0", ""},
		{"bad json", `{"tag_name":`, "v0.1.0", ""},
		{"prerelease", `{"tag_name":"v0.3.0","prerelease":true}`, "v0.1.0", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := api(t, c.body)
			ch := &Checker{Client: &Client{APIBase: srv.URL, Version: c.current}, CacheDir: t.TempDir()}
			if got := ch.Available(context.Background(), c.current); got != c.want {
				t.Errorf("Available = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAvailableDevBuildSkipsNetwork(t *testing.T) {
	srv, n := api(t, tagBody("v0.2.0"))
	ch := &Checker{Client: &Client{APIBase: srv.URL}, CacheDir: t.TempDir()}
	for _, v := range []string{"dev", "(devel)", ""} {
		if got := ch.Available(context.Background(), v); got != "" {
			t.Errorf("dev %q: got %q", v, got)
		}
	}
	if *n != 0 {
		t.Errorf("dev build made %d requests", *n)
	}
}

func TestAvailableHTTPErrorSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ch := &Checker{Client: &Client{APIBase: srv.URL}, CacheDir: dir}
	if got := ch.Available(context.Background(), "v0.1.0"); got != "" {
		t.Errorf("got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); err == nil {
		t.Error("failed fetch was cached")
	}
}

func TestAvailableTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ch := &Checker{Client: &Client{APIBase: srv.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := ch.Available(ctx, "v0.1.0"); got != "" {
		t.Errorf("got %q", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestCache(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	write := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fresh := fmt.Sprintf(`{"schema":1,"checked_at":%q,"latest":"v0.5.0"}`, now.Add(-time.Hour).Format(time.RFC3339))
	expired := fmt.Sprintf(`{"schema":1,"checked_at":%q,"latest":"v0.5.0"}`, now.Add(-25*time.Hour).Format(time.RFC3339))
	future := fmt.Sprintf(`{"schema":1,"checked_at":%q,"latest":"v0.5.0"}`, now.Add(time.Hour).Format(time.RFC3339))
	unknown := fmt.Sprintf(`{"schema":99,"checked_at":%q,"latest":"v0.5.0"}`, now.Format(time.RFC3339))
	badTag := fmt.Sprintf(`{"schema":1,"checked_at":%q,"latest":"evil"}`, now.Format(time.RFC3339))
	cases := []struct {
		name, cache string
		wantTag     string
		wantReqs    int32
	}{
		{"hit", fresh, "v0.5.0", 0},
		{"expired", expired, "v0.2.0", 1},
		{"future", future, "v0.2.0", 1},
		{"corrupt", "{not json", "v0.2.0", 1},
		{"unknown schema", unknown, "v0.2.0", 1},
		{"bad cached tag", badTag, "v0.2.0", 1},
		{"missing", "", "v0.2.0", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, n := api(t, tagBody("v0.2.0"))
			dir := t.TempDir()
			if c.cache != "" {
				write(t, dir, c.cache)
			}
			ch := &Checker{Client: &Client{APIBase: srv.URL}, CacheDir: dir, Now: func() time.Time { return now }}
			if got := ch.Available(context.Background(), "v0.1.0"); got != c.wantTag {
				t.Errorf("Available = %q, want %q", got, c.wantTag)
			}
			if *n != c.wantReqs {
				t.Errorf("requests = %d, want %d", *n, c.wantReqs)
			}
			// the cache is (re)written after a fetch and readable again
			data, err := os.ReadFile(filepath.Join(dir, cacheFile))
			if err != nil {
				t.Fatal(err)
			}
			var d cacheData
			if err := json.Unmarshal(data, &d); err != nil {
				t.Fatalf("cache %q: %v", data, err)
			}
			if c.wantReqs > 0 && (d.Schema != 1 || d.Latest != "v0.2.0" || !d.CheckedAt.Equal(now)) {
				t.Errorf("cache not rewritten: %s", data)
			}
		})
	}
}

func TestCacheDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := CacheDir(env(map[string]string{"XDG_CACHE_HOME": "/x/cache"}), "/h"); got != "/x/cache/grove" {
		t.Errorf("xdg: %q", got)
	}
	want := "/h/.cache/grove"
	if runtime.GOOS == "darwin" {
		want = "/h/Library/Caches/grove"
	}
	if got := CacheDir(env(map[string]string{"XDG_CACHE_HOME": "rel"}), "/h"); got != want {
		t.Errorf("default: %q, want %q", got, want)
	}
}

// --- self-update ---

type tarEntry struct {
	name     string
	typ      byte
	body     string
	linkname string
}

func makeArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Size: int64(len(e.body)), Linkname: e.linkname}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fixture struct {
	srv     *httptest.Server
	client  *Client
	rel     Release
	archive []byte
	sums    string
	exe     string
}

const newBinary = "#!/bin/sh\necho grove v0.2.0\n"

// newFixture serves a release over TLS (so URLs are https on 127.0.0.1) and
// creates a fake installed binary in a temp dir.
func newFixture(t *testing.T, archive []byte) *fixture {
	t.Helper()
	f := &fixture{archive: archive}
	name := AssetName("v0.2.0", "linux", "amd64")
	sum := sha256.Sum256(archive)
	f.sums = hex.EncodeToString(sum[:]) + "  " + name + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(f.archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, f.sums) })
	mux.HandleFunc("/redirect-out", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/grove.tar.gz", http.StatusFound)
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	u, _ := url.Parse(f.srv.URL)
	f.client = &Client{HTTP: f.srv.Client(), Hosts: []string{u.Hostname()}}
	f.rel = Release{Tag: "v0.2.0", Assets: []Asset{
		{Name: name, URL: f.srv.URL + "/dl/" + name},
		{Name: "checksums.txt", URL: f.srv.URL + "/dl/checksums.txt"},
	}}
	dir := t.TempDir()
	f.exe = filepath.Join(dir, "grove")
	if err := os.WriteFile(f.exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) installer() *Installer {
	return &Installer{
		Client: f.client, Exe: f.exe, GOOS: "linux", GOARCH: "amd64",
		RunVersion: func(_ context.Context, p string) ([]byte, error) {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			// the fake binary is a script whose echo line is its output
			return []byte(strings.TrimPrefix(strings.Split(string(data), "\n")[1], "echo ") + "\n"), nil
		},
	}
}

func (f *fixture) exeContent(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoTemps(t *testing.T, exe string) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".grove-update-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestInstallHappyPath(t *testing.T) {
	f := newFixture(t, makeArchive(t,
		tarEntry{name: "README.md", body: "docs"},
		tarEntry{name: "grove", body: newBinary},
	))
	// install through a symlink: the target is replaced, not the link
	link := filepath.Join(t.TempDir(), "grove-link")
	if err := os.Symlink(f.exe, link); err != nil {
		t.Fatal(err)
	}
	in := f.installer()
	in.Exe = link
	if err := in.Install(context.Background(), f.rel); err != nil {
		t.Fatal(err)
	}
	if got := f.exeContent(t); got != newBinary {
		t.Errorf("binary = %q", got)
	}
	st, _ := os.Stat(f.exe)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", st.Mode())
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
	assertNoTemps(t, f.exe)
}

func TestInstallFailures(t *testing.T) {
	good := func(t *testing.T) []byte { return makeArchive(t, tarEntry{name: "grove", body: newBinary}) }
	cases := []struct {
		name    string
		archive func(t *testing.T) []byte
		mutate  func(f *fixture)
		wantErr string
	}{
		{"checksum mismatch", good, func(f *fixture) { f.archive = append([]byte(nil), f.archive...); f.archive[len(f.archive)-1] ^= 0xff }, "checksum mismatch"},
		{"no checksum entry", good, func(f *fixture) { f.sums = strings.Repeat("0", 64) + "  other.tar.gz\n" }, "no entry"},
		{"bad host", good, func(f *fixture) { f.rel.Assets[0].URL = "https://evil.example/grove.tar.gz" }, "unexpected host"},
		{"http scheme", good, func(f *fixture) { f.rel.Assets[0].URL = strings.Replace(f.rel.Assets[0].URL, "https://", "http://", 1) }, "non-https"},
		{"redirect off-host", good, func(f *fixture) { f.rel.Assets[0].URL = f.srv.URL + "/redirect-out" }, "unexpected host"},
		{"missing platform asset", good, func(f *fixture) { f.rel.Assets = f.rel.Assets[1:] }, "unsupported platform"},
		{"path traversal", func(t *testing.T) []byte {
			return makeArchive(t, tarEntry{name: "../grove", body: newBinary})
		}, nil, "unsafe path"},
		{"absolute path", func(t *testing.T) []byte {
			return makeArchive(t, tarEntry{name: "/usr/local/bin/grove", body: newBinary})
		}, nil, "unsafe path"},
		{"symlink entry", func(t *testing.T) []byte {
			return makeArchive(t, tarEntry{name: "grove", typ: tar.TypeSymlink, linkname: "/bin/sh"})
		}, nil, "link entry"},
		{"no binary", func(t *testing.T) []byte {
			return makeArchive(t, tarEntry{name: "README.md", body: "x"})
		}, nil, "no grove binary"},
		{"wrong version", func(t *testing.T) []byte {
			return makeArchive(t, tarEntry{name: "grove", body: "#!/bin/sh\necho grove v0.1.9\n"})
		}, nil, "reports"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.archive(t))
			if c.mutate != nil {
				c.mutate(f)
			}
			err := f.installer().Install(context.Background(), f.rel)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if got := f.exeContent(t); got != "old" {
				t.Errorf("binary changed: %q", got)
			}
			assertNoTemps(t, f.exe)
		})
	}
}

func TestInstallPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newFixture(t, makeArchive(t, tarEntry{name: "grove", body: newBinary}))
	dir := filepath.Dir(f.exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := f.installer().Install(context.Background(), f.rel)
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	if got := f.exeContent(t); got != "old" {
		t.Errorf("binary changed: %q", got)
	}
}

func TestLatestParsesAssets(t *testing.T) {
	srv, _ := api(t, `{"tag_name":"v0.2.0","assets":[{"name":"checksums.txt","browser_download_url":"https://github.com/x/checksums.txt"}]}`)
	rel, err := (&Client{APIBase: srv.URL}).Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.2.0" || len(rel.Assets) != 1 || rel.Assets[0].URL != "https://github.com/x/checksums.txt" {
		t.Errorf("rel = %+v", rel)
	}
}
