package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/RafaelXokito/grove/internal/tui"
	"github.com/RafaelXokito/grove/internal/update"
)

// isolate points home/env at a temp dir so tests never read the real config,
// and captures runTUI launches.
func isolate(t *testing.T, envm map[string]string) (string, *[]tui.Deps) {
	t.Helper()
	h := t.TempDir()
	var launched []tui.Deps
	origTUI, origEnv, origHome, origWd, origScope := runTUI, getenv, home, getwd, resolveScope
	origVersion, origBI, origClient, origHook := version, readBuildInfo, updateClient, resolvedHook
	origExe, origOS, origArch, origRunVersion := executable, goos, goarch, runVersion
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	// Never touch the network or the real binary.
	updateClient = func(v string) *update.Client {
		return &update.Client{APIBase: "http://127.0.0.1:1", Version: v, Hosts: []string{}}
	}
	executable = func() (string, error) { return "", errors.New("test: no executable") }
	runVersion = func(context.Context, string) ([]byte, error) { return nil, errors.New("test: no exec") }
	runTUI = func(d tui.Deps) error { launched = append(launched, d); return nil }
	getenv = func(k string) string { return envm[k] }
	home = func() string { return h }
	// The test binary runs inside grove's own repo: never resolve the
	// real cwd (FR-22); by default it is "not a repo".
	getwd = func() (string, error) { return h, nil }
	resolveScope = func(string, time.Duration) (string, bool, error) { return "", false, nil }
	t.Cleanup(func() {
		runTUI, getenv, home, getwd, resolveScope = origTUI, origEnv, origHome, origWd, origScope
		version, readBuildInfo, updateClient, resolvedHook = origVersion, origBI, origClient, origHook
		executable, goos, goarch, runVersion = origExe, origOS, origArch, origRunVersion
	})
	return h, &launched
}

func TestRun(t *testing.T) {
	isolate(t, nil)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"no args launches the TUI", nil, 0, ""},
		{"version flag", []string{"--version"}, 0, "grove dev\n"},
		{"help", []string{"--help"}, 0, ""},
		{"unknown flag", []string{"--nope"}, 2, ""},
		{"positional arg", []string{"extra"}, 2, ""},
		{"negative history", []string{"--history-days", "-1"}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if tt.name == "help" {
				for _, s := range []string{"-version", "-root", "GROVE_ROOT", "config.toml"} {
					if !strings.Contains(errOut.String(), s) {
						t.Errorf("help output missing %q: %q", s, errOut.String())
					}
				}
			}
		})
	}
}

func TestRunLaunchesTUIWithDefaults(t *testing.T) {
	h, got := isolate(t, nil)
	if code := run(nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(*got) != 1 {
		t.Fatalf("runTUI called %d times", len(*got))
	}
	d := (*got)[0]
	if !reflect.DeepEqual(d.Roots, []string{filepath.Join(h, "Documents", "repos")}) {
		t.Errorf("Roots = %v", d.Roots)
	}
	if len(d.Sources) != 2 || d.Procs == nil || d.HistoryDays != 30 || d.MaxDepth != 3 ||
		d.LiveInterval != 5*time.Second || d.GitTimeout != 10*time.Second {
		t.Errorf("deps = %+v", d)
	}
	if code := run([]string{"--version"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || len(*got) != 1 {
		t.Errorf("--version launched the TUI")
	}
}

func TestRunConfigPrecedence(t *testing.T) {
	h, got := isolate(t, map[string]string{"GROVE_ROOT": "/env1,/env2"})
	cfg := filepath.Join(h, ".config", "grove", "config.toml")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("roots = [\"/file\"]\nhistory_days = 7\nlive_interval = \"2s\"\nmax_depth = 0\nbogus = 1\n"), 0o644)

	var errOut bytes.Buffer
	if code := run(nil, &bytes.Buffer{}, &errOut); code != 0 {
		t.Fatalf("code = %d (%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), `unknown key "bogus"`) {
		t.Errorf("no unknown-key warning: %q", errOut.String())
	}
	d := (*got)[0]
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "bogus") {
		t.Errorf("Deps.Warnings = %v", d.Warnings)
	}
	if !reflect.DeepEqual(d.Roots, []string{"/env1", "/env2"}) || d.HistoryDays != 7 ||
		d.LiveInterval != 2*time.Second || d.MaxDepth != -1 {
		t.Errorf("env/file deps = %+v", d)
	}

	if code := run([]string{"--root", "/a", "--root", "/b", "--history-days", "0", "--depth", "2"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	d = (*got)[1]
	if !reflect.DeepEqual(d.Roots, []string{"/a", "/b"}) || d.HistoryDays != 0 || d.MaxDepth != 2 {
		t.Errorf("flag deps = %+v", d)
	}

	// warnings still printed when validation fails
	os.WriteFile(cfg, []byte("history_days = -1\nbogus = 1\n"), 0o644)
	errOut.Reset()
	if code := run(nil, &bytes.Buffer{}, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown key "bogus"`) {
		t.Errorf("invalid config: code = %d, stderr %q", code, errOut.String())
	}

	os.WriteFile(cfg, []byte("roots = \n"), 0o644)
	errOut.Reset()
	if code := run(nil, &bytes.Buffer{}, &errOut); code != 2 || !strings.Contains(errOut.String(), "config.toml") {
		t.Errorf("bad TOML: code = %d, stderr %q", code, errOut.String())
	}
}

func TestRunEmptyGroveRoot(t *testing.T) {
	_, got := isolate(t, map[string]string{"GROVE_ROOT": " , "})
	var errOut bytes.Buffer
	if code := run(nil, &bytes.Buffer{}, &errOut); code != 2 || !strings.Contains(errOut.String(), "GROVE_ROOT: no roots") {
		t.Errorf("code = %d, stderr %q", code, errOut.String())
	}
	if len(*got) != 0 {
		t.Error("TUI launched with no roots")
	}
}

// TestRunScope covers FR-22's precedence: cwd scoping applies by default and
// with GROVE_ROOT, but --root and -a/--all disable it; errors fall back to
// the all-repos view with a warning.
func TestRunScope(t *testing.T) {
	h, got := isolate(t, map[string]string{"GROVE_ROOT": "/env"})
	var calls []string
	var gotTimeout time.Duration
	resolveScope = func(dir string, timeout time.Duration) (string, bool, error) {
		calls = append(calls, dir)
		gotTimeout = timeout
		return "/repos/grove", true, nil
	}
	tests := []struct {
		args      []string
		wantScope string
	}{
		{nil, "/repos/grove"},
		{[]string{"--depth", "1"}, "/repos/grove"},
		{[]string{"--root", "/x"}, ""},
		{[]string{"--all"}, ""},
		{[]string{"-a"}, ""},
		{[]string{"--all", "--root", "/x"}, ""},
	}
	for _, tt := range tests {
		calls = nil
		n := len(*got)
		if code := run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || len(*got) != n+1 {
			t.Fatalf("%v: code = %d", tt.args, code)
		}
		d := (*got)[n]
		if d.Scope != tt.wantScope {
			t.Errorf("%v: Scope = %q, want %q", tt.args, d.Scope, tt.wantScope)
		}
		if wantCalls := tt.wantScope != ""; wantCalls != (len(calls) == 1) {
			t.Errorf("%v: resolveScope calls = %v", tt.args, calls)
		}
		if tt.wantScope != "" && (calls[0] != h || gotTimeout != 10*time.Second) {
			t.Errorf("%v: resolveScope(%q, %v)", tt.args, calls[0], gotTimeout)
		}
		if tt.wantScope != "" && !reflect.DeepEqual(d.Roots, []string{"/env"}) {
			t.Errorf("%v: GROVE_ROOT must still define the all view: %v", tt.args, d.Roots)
		}
	}

	// Not in a repo, or detection failed: all view (failure is a warning).
	resolveScope = func(string, time.Duration) (string, bool, error) { return "", false, nil }
	run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if d := (*got)[len(*got)-1]; d.Scope != "" || len(d.Warnings) != 0 {
		t.Errorf("non-repo cwd: Scope = %q, warnings %v", d.Scope, d.Warnings)
	}
	resolveScope = func(string, time.Duration) (string, bool, error) { return "", false, errors.New("timed out") }
	run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if d := (*got)[len(*got)-1]; d.Scope != "" || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "timed out") {
		t.Errorf("failed detection: Scope = %q, warnings %v", d.Scope, d.Warnings)
	}
	getwd = func() (string, error) { return "", errors.New("gone") }
	run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if d := (*got)[len(*got)-1]; d.Scope != "" || len(d.Warnings) != 1 {
		t.Errorf("getwd error: Scope = %q, warnings %v", d.Scope, d.Warnings)
	}
}
