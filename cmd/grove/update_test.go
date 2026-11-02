package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/RafaelXokito/grove/internal/update"
)

func TestCurrentVersion(t *testing.T) {
	isolate(t, nil)
	bi := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	cases := []struct {
		ldflags, buildInfo, want string
	}{
		{"v0.3.0", "v0.2.0", "v0.3.0"}, // -ldflags wins
		{"dev", "v0.2.0", "v0.2.0"},    // go install …@v0.2.0
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
	}
	for _, c := range cases {
		version, readBuildInfo = c.ldflags, bi(c.buildInfo)
		if got := currentVersion(); got != c.want {
			t.Errorf("ldflags %q, buildinfo %q: got %q, want %q", c.ldflags, c.buildInfo, got, c.want)
		}
	}
	version, readBuildInfo = "dev", bi("v0.2.0")
	var out bytes.Buffer
	if run([]string{"--version"}, &out, &bytes.Buffer{}); out.String() != "grove v0.2.0\n" {
		t.Errorf("--version = %q", out.String())
	}
}

// releaseServer serves releases/latest and its assets over TLS so download
// URLs are https on 127.0.0.1.
type releaseServer struct {
	srv  *httptest.Server
	tag  string
	tgz  []byte
	hits int
}

func newReleaseServer(t *testing.T, tag, binary string) *releaseServer {
	t.Helper()
	rs := &releaseServer{tag: tag}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "grove", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(binary))})
	tw.Write([]byte(binary))
	tw.Close()
	gz.Close()
	rs.tgz = buf.Bytes()
	name := update.AssetName(tag, "linux", "arm64")
	sum := sha256.Sum256(rs.tgz)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/RafaelXokito/grove/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		rs.hits++
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":"%s/dl/a.tgz"},{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt"}]}`,
			rs.tag, name, rs.srv.URL, rs.srv.URL)
	})
	mux.HandleFunc("/dl/a.tgz", func(w http.ResponseWriter, r *http.Request) { w.Write(rs.tgz) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	rs.srv = httptest.NewTLSServer(mux)
	t.Cleanup(rs.srv.Close)
	u, _ := url.Parse(rs.srv.URL)
	updateClient = func(v string) *update.Client {
		return &update.Client{APIBase: rs.srv.URL, HTTP: rs.srv.Client(), Version: v, Hosts: []string{u.Hostname()}}
	}
	goos, goarch = "linux", "arm64"
	return rs
}

// fakeExe creates a stand-in installed binary and points the seams at it;
// "running" a binary reads its second line.
func fakeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "grove")
	if err := os.WriteFile(exe, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return exe, nil }
	runVersion = func(_ context.Context, p string) ([]byte, error) {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		lines := strings.SplitN(string(data), "\n", 3)
		return []byte(strings.TrimPrefix(lines[1], "echo ") + "\n"), nil
	}
	return exe
}

func TestUpdateCommand(t *testing.T) {
	const newBin = "#!/bin/sh\necho grove v0.2.0\n"
	cases := []struct {
		name      string
		version   string
		args      []string
		wantCode  int
		wantOut   string
		wantErr   string
		installed bool
	}{
		{"up to date", "v0.2.0", nil, 0, "grove v0.2.0 is up to date\n", "", false},
		{"newer local", "v0.3.0", nil, 0, "grove v0.3.0 is up to date\n", "", false},
		{"check newer", "v0.1.0", []string{"--check"}, 0, "grove v0.1.0\nlatest: v0.2.0\nupdate available", "", false},
		{"check same", "v0.2.0", []string{"--check"}, 0, "grove v0.2.0\nlatest: v0.2.0\n", "", false},
		{"dev refuses", "dev", nil, 1, "", "development build", false},
		{"dev force", "dev", []string{"--force"}, 0, "grove updated to v0.2.0", "", true},
		{"update", "v0.1.0", nil, 0, "updating grove v0.1.0 -> v0.2.0\ngrove updated to v0.2.0\n", "", true},
		{"unexpected arg", "v0.1.0", []string{"now"}, 2, "", "unexpected argument", false},
		{"unknown flag", "v0.1.0", []string{"--nope"}, 2, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t, nil)
			newReleaseServer(t, "v0.2.0", newBin)
			exe := fakeExe(t)
			version = c.version
			var out, errOut bytes.Buffer
			code := run(append([]string{"update"}, c.args...), &out, &errOut)
			if code != c.wantCode {
				t.Fatalf("code = %d, want %d (stdout %q, stderr %q)", code, c.wantCode, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Errorf("stdout = %q, want %q", out.String(), c.wantOut)
			}
			if !strings.Contains(errOut.String(), c.wantErr) {
				t.Errorf("stderr = %q, want %q", errOut.String(), c.wantErr)
			}
			data, _ := os.ReadFile(exe)
			if installed := string(data) == newBin; installed != c.installed {
				t.Errorf("installed = %v, want %v (binary %q)", installed, c.installed, data)
			}
		})
	}
}

func TestUpdateCommandPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	isolate(t, nil)
	newReleaseServer(t, "v0.2.0", "#!/bin/sh\necho grove v0.2.0\n")
	exe := fakeExe(t)
	version = "v0.1.0"
	dir := filepath.Dir(exe)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	var errOut bytes.Buffer
	if code := run([]string{"update"}, &bytes.Buffer{}, &errOut); code != 1 {
		t.Fatalf("code = %d", code)
	}
	for _, s := range []string{"permission denied", "sudo grove update", "go install github.com/RafaelXokito/grove/cmd/grove@latest"} {
		if !strings.Contains(errOut.String(), s) {
			t.Errorf("stderr missing %q: %q", s, errOut.String())
		}
	}
}

func TestUpdateCommandNetworkError(t *testing.T) {
	isolate(t, nil) // client points at a closed port
	version = "v0.1.0"
	var errOut bytes.Buffer
	if code := run([]string{"update", "--check"}, &bytes.Buffer{}, &errOut); code != 1 || !strings.Contains(errOut.String(), "latest release") {
		t.Errorf("code = %d, stderr %q", code, errOut.String())
	}
}

// TestUpdateCheckWiring covers FR-23's enablement: release builds get a
// check; dev builds, update_check = false and GROVE_NO_UPDATE_CHECK do not.
func TestUpdateCheckWiring(t *testing.T) {
	cases := []struct {
		name    string
		version string
		env     map[string]string
		config  string
		enabled bool
	}{
		{"release build", "v0.1.0", nil, "", true},
		{"dev build", "dev", nil, "", false},
		{"config off", "v0.1.0", nil, "update_check = false\n", false},
		{"config on", "v0.1.0", nil, "update_check = true\n", true},
		{"env off", "v0.1.0", map[string]string{"GROVE_NO_UPDATE_CHECK": "1"}, "", false},
		{"env zero keeps it on", "v0.1.0", map[string]string{"GROVE_NO_UPDATE_CHECK": "0"}, "", true},
		{"env beats config", "v0.1.0", map[string]string{"GROVE_NO_UPDATE_CHECK": "true"}, "update_check = true\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, got := isolate(t, c.env)
			version = c.version
			if c.config != "" {
				cfg := filepath.Join(h, ".config", "grove", "config.toml")
				os.MkdirAll(filepath.Dir(cfg), 0o755)
				os.WriteFile(cfg, []byte(c.config), 0o644)
			}
			var errOut bytes.Buffer
			if code := run(nil, &bytes.Buffer{}, &errOut); code != 0 {
				t.Fatalf("code = %d (%s)", code, errOut.String())
			}
			if enabled := (*got)[0].UpdateCheck != nil; enabled != c.enabled {
				t.Errorf("update check enabled = %v, want %v", enabled, c.enabled)
			}
		})
	}
}

func TestUpdateCheckUsesCacheDir(t *testing.T) {
	cache := t.TempDir()
	_, got := isolate(t, map[string]string{"XDG_CACHE_HOME": cache})
	rs := newReleaseServer(t, "v0.2.0", "")
	version = "v0.1.0"
	if code := run(nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal(code)
	}
	check := (*got)[0].UpdateCheck
	for i := 0; i < 2; i++ {
		if tag := check(context.Background()); tag != "v0.2.0" {
			t.Fatalf("check = %q", tag)
		}
	}
	if rs.hits != 1 {
		t.Errorf("API hits = %d, want 1 (second from cache)", rs.hits)
	}
	if _, err := os.Stat(filepath.Join(cache, "grove", "update.json")); err != nil {
		t.Errorf("cache file: %v", err)
	}
}
