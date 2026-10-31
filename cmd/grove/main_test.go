package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RafaelXokito/grove/internal/tui"
)

func TestRun(t *testing.T) {
	var launched []tui.Deps
	orig := runTUI
	runTUI = func(d tui.Deps) error { launched = append(launched, d); return nil }
	t.Cleanup(func() { runTUI = orig })

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
			if tt.name == "help" && !strings.Contains(errOut.String(), "-version") {
				t.Errorf("help output missing -version: %q", errOut.String())
			}
		})
	}
}

func TestRunLaunchesTUIWithDefaults(t *testing.T) {
	var got []tui.Deps
	orig := runTUI
	runTUI = func(d tui.Deps) error { got = append(got, d); return nil }
	t.Cleanup(func() { runTUI = orig })

	if code := run(nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(got) != 1 {
		t.Fatalf("runTUI called %d times", len(got))
	}
	d := got[0]
	if len(d.Roots) != 1 || !strings.HasSuffix(d.Roots[0], "Documents/repos") {
		t.Errorf("Roots = %v", d.Roots)
	}
	if len(d.Sources) != 2 || d.Procs == nil || d.HistoryDays != 30 {
		t.Errorf("deps = %+v", d)
	}
	if code := run([]string{"--version"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || len(got) != 1 {
		t.Errorf("--version launched the TUI")
	}
}
