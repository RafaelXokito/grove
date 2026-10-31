package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"no args", nil, 0, "grove dev\n"},
		{"version flag", []string{"--version"}, 0, "grove dev\n"},
		{"help", []string{"--help"}, 0, ""},
		{"unknown flag", []string{"--nope"}, 2, ""},
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
