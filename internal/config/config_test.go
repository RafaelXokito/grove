package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func ip(i int) *int { return &i }

func TestResolvePrecedence(t *testing.T) {
	const home = "/home/u"
	tests := []struct {
		name  string
		flags Flags
		env   map[string]string
		file  File
		roots []string
		depth int
		hist  int
	}{
		{"defaults", Flags{}, nil, File{}, []string{"/home/u/Documents/repos"}, 3, 30},
		{"file", Flags{}, nil, File{Roots: []string{"~/a", "/b"}, MaxDepth: ip(5), HistoryDays: ip(0)},
			[]string{"/home/u/a", "/b"}, 5, 0},
		{"env over file", Flags{}, map[string]string{"GROVE_ROOT": "/e1, /e2,"}, File{Roots: []string{"/f"}},
			[]string{"/e1", "/e2"}, 3, 30},
		{"flag over env (spec example)", Flags{Roots: []string{"/b"}}, map[string]string{"GROVE_ROOT": "/a"}, File{},
			[]string{"/b"}, 3, 30},
		{"flags over file", Flags{Roots: []string{"/x", "/y"}, MaxDepth: ip(1), HistoryDays: ip(7)}, nil,
			File{Roots: []string{"/f"}, MaxDepth: ip(5), HistoryDays: ip(90)}, []string{"/x", "/y"}, 1, 7},
		{"blank env ignored", Flags{}, map[string]string{"GROVE_ROOT": "  "}, File{Roots: []string{"/f"}},
			[]string{"/f"}, 3, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Resolve(tt.flags, env(tt.env), tt.file, home)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.Roots, tt.roots) || c.MaxDepth != tt.depth || c.HistoryDays != tt.hist {
				t.Errorf("got roots=%v depth=%d hist=%d", c.Roots, c.MaxDepth, c.HistoryDays)
			}
		})
	}
}

func TestResolveDurationsAndDirs(t *testing.T) {
	c, err := Resolve(Flags{}, env(map[string]string{"CODEX_HOME": "/cx"}),
		File{LiveInterval: "2s", GitTimeout: "30s"}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if c.LiveInterval != 2*time.Second || c.GitTimeout != 30*time.Second {
		t.Errorf("durations = %v %v", c.LiveInterval, c.GitTimeout)
	}
	if c.ClaudeDir != "/h/.claude" || c.CodexDir != "/cx" {
		t.Errorf("dirs = %q %q", c.ClaudeDir, c.CodexDir)
	}
	cd, xd := "/c", "~/x"
	c, _ = Resolve(Flags{ClaudeDir: &cd, CodexDir: &xd}, env(map[string]string{"CODEX_HOME": "/cx"}), File{}, "/h")
	if c.ClaudeDir != "/c" || c.CodexDir != "/h/x" {
		t.Errorf("flag dirs = %q %q", c.ClaudeDir, c.CodexDir)
	}
	c, _ = Resolve(Flags{}, env(nil), File{}, "/h")
	if c.LiveInterval != DefaultLiveInterval || c.GitTimeout != DefaultGitTimeout || c.CodexDir != "/h/.codex" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestResolveValidation(t *testing.T) {
	tests := []struct {
		name  string
		flags Flags
		file  File
		want  string
	}{
		{"negative history", Flags{HistoryDays: ip(-1)}, File{}, "history_days"},
		{"negative depth", Flags{}, File{MaxDepth: ip(-2)}, "max_depth"},
		{"bad live interval", Flags{}, File{LiveInterval: "soon"}, "live_interval"},
		{"tiny live interval", Flags{}, File{LiveInterval: "1ms"}, "live_interval"},
		{"bad git timeout", Flags{}, File{GitTimeout: "0s"}, "git_timeout"},
		{"empty roots", Flags{}, File{Roots: []string{}}, "roots"},
		{"blank root", Flags{}, File{Roots: []string{" "}}, "roots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(tt.flags, env(nil), tt.file, "/h")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want mention of %q", err, tt.want)
			}
		})
	}
	for _, v := range []string{",", " , ", ",,"} {
		_, err := Resolve(Flags{}, env(map[string]string{"GROVE_ROOT": v}), File{}, "/h")
		if err == nil || !strings.Contains(err.Error(), "GROVE_ROOT: no roots") {
			t.Errorf("GROVE_ROOT=%q: err = %v", v, err)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()

	f, warns, err := LoadFile(filepath.Join(dir, "missing.toml"))
	if err != nil || warns != nil || f.Roots != nil {
		t.Fatalf("missing file: %+v %v %v", f, warns, err)
	}

	good := filepath.Join(dir, "good.toml")
	os.WriteFile(good, []byte("roots = [\"~/a\", \"/b\"]\nhistory_days = 0\nlive_interval = \"3s\"\ncolour = \"red\"\n"), 0o644)
	f, warns, err = LoadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Roots, []string{"~/a", "/b"}) || f.HistoryDays == nil || *f.HistoryDays != 0 || f.LiveInterval != "3s" {
		t.Errorf("file = %+v", f)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `"colour"`) {
		t.Errorf("warns = %v", warns)
	}

	bad := filepath.Join(dir, "bad.toml")
	os.WriteFile(bad, []byte("roots = [\n"), 0o644)
	if _, _, err := LoadFile(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("bad TOML err = %v", err)
	}
	wrongType := filepath.Join(dir, "type.toml")
	os.WriteFile(wrongType, []byte("history_days = \"thirty\"\n"), 0o644)
	if _, _, err := LoadFile(wrongType); err == nil {
		t.Error("wrong type accepted")
	}
}

func TestPath(t *testing.T) {
	if p := Path(env(nil), "/h"); p != "/h/.config/grove/config.toml" {
		t.Errorf("default = %q", p)
	}
	if p := Path(env(map[string]string{"XDG_CONFIG_HOME": "/x"}), "/h"); p != "/x/grove/config.toml" {
		t.Errorf("xdg = %q", p)
	}
	if p := Path(env(map[string]string{"XDG_CONFIG_HOME": "rel"}), "/h"); p != "/h/.config/grove/config.toml" {
		t.Errorf("relative xdg = %q", p)
	}
}
