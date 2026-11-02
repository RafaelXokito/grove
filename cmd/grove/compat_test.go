package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/RafaelXokito/grove/internal/config"
	"github.com/RafaelXokito/grove/internal/update"
)

// Compatibility snapshots (SPEC §7): testdata/compat/<release>/ holds the
// config file and invocations every release must keep accepting with the
// same meaning. Each release adds a directory; old ones are never edited.

const compatDir = "../../testdata/compat"

type compatWant struct {
	Roots        []string `json:"roots"`
	MaxDepth     *int     `json:"max_depth"`
	HistoryDays  *int     `json:"history_days"`
	LiveInterval string   `json:"live_interval"`
	GitTimeout   string   `json:"git_timeout"`
	ClaudeDir    string   `json:"claude_dir"`
	CodexDir     string   `json:"codex_dir"`
	UpdateCheck  *bool    `json:"update_check"`
	Scoped       *bool    `json:"scoped"`
}

type compatCase struct {
	Name   string            `json:"name"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Config string            `json:"config"` // "", "home" or "xdg"
	Exit   int               `json:"exit"`
	NoTUI  bool              `json:"no_tui"`
	Want   *compatWant       `json:"want"`
}

type compatSnapshot struct {
	Release     string       `json:"release"`
	Flags       []string     `json:"flags"`
	Env         []string     `json:"env"`
	Subcommands []string     `json:"subcommands"`
	UpdateFlags []string     `json:"update_flags"`
	Cases       []compatCase `json:"cases"`
}

func loadSnapshots(t *testing.T) (dirs []string, snaps []compatSnapshot) {
	t.Helper()
	ents, err := os.ReadDir(compatDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if !update.ValidTag(e.Name()) {
			t.Fatalf("compat dir %q: want a vX.Y.Z name", e.Name())
		}
		dir := filepath.Join(compatDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "invocations.json"))
		if err != nil {
			t.Fatal(err)
		}
		var s compatSnapshot
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if s.Release != e.Name() {
			t.Fatalf("%s: release %q does not match its directory", dir, s.Release)
		}
		dirs = append(dirs, dir)
		snaps = append(snaps, s)
	}
	if len(snaps) == 0 {
		t.Fatal("no compat snapshots")
	}
	// newest last
	idx := make([]int, len(snaps))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return update.Compare(snaps[idx[a]].Release, snaps[idx[b]].Release) < 0 })
	var d2 []string
	var s2 []compatSnapshot
	for _, i := range idx {
		d2, s2 = append(d2, dirs[i]), append(s2, snaps[i])
	}
	return d2, s2
}

// TestCompatSnapshots replays every release's invocations against today's
// grove and expects the same exit codes and resolved settings.
func TestCompatSnapshots(t *testing.T) {
	dirs, snaps := loadSnapshots(t)
	for i, snap := range snaps {
		cfgData, err := os.ReadFile(filepath.Join(dirs[i], "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		// the snapshot config must load without unknown-key warnings
		if f, warns, err := config.LoadFile(filepath.Join(dirs[i], "config.toml")); err != nil || len(warns) > 0 {
			t.Errorf("%s config.toml: err %v, warnings %v", snap.Release, err, warns)
		} else if _, err := config.Resolve(config.Flags{}, func(string) string { return "" }, f, "/h"); err != nil {
			t.Errorf("%s config.toml: %v", snap.Release, err)
		}
		for _, c := range snap.Cases {
			t.Run(snap.Release+"/"+c.Name, func(t *testing.T) {
				runCompatCase(t, c, cfgData)
			})
		}
	}
}

func runCompatCase(t *testing.T, c compatCase, cfgData []byte) {
	t.Helper()
	env := map[string]string{}
	h, got := isolate(t, env)
	for k, v := range c.Env {
		env[k] = strings.ReplaceAll(v, "$HOME", h)
	}
	version = "v0.1.0" // a release build, so update_check is observable
	resolveScope = func(string, time.Duration) (string, bool, error) { return "/repo", true, nil }
	var cfg *config.Config
	resolvedHook = func(c config.Config) { cfg = &c }
	switch c.Config {
	case "home":
		writeFile(t, filepath.Join(h, ".config", "grove", "config.toml"), cfgData)
	case "xdg":
		writeFile(t, filepath.Join(env["XDG_CONFIG_HOME"], "grove", "config.toml"), cfgData)
	}

	var out, errOut bytes.Buffer
	if code := run(c.Args, &out, &errOut); code != c.Exit {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, c.Exit, errOut.String())
	}
	if c.NoTUI {
		if len(*got) != 0 {
			t.Error("TUI launched")
		}
		return
	}
	if len(*got) != 1 || cfg == nil {
		t.Fatalf("TUI launches = %d", len(*got))
	}
	if strings.Contains(errOut.String(), "warning") {
		t.Errorf("unexpected warning: %q", errOut.String())
	}
	w := c.Want
	if w == nil {
		return
	}
	abs := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(h, p[2:])
		}
		return p
	}
	if w.Roots != nil {
		var want []string
		for _, r := range w.Roots {
			want = append(want, abs(r))
		}
		if !reflect.DeepEqual(cfg.Roots, want) {
			t.Errorf("roots = %v, want %v", cfg.Roots, want)
		}
	}
	if w.MaxDepth != nil && cfg.MaxDepth != *w.MaxDepth {
		t.Errorf("max_depth = %d, want %d", cfg.MaxDepth, *w.MaxDepth)
	}
	if w.HistoryDays != nil && cfg.HistoryDays != *w.HistoryDays {
		t.Errorf("history_days = %d, want %d", cfg.HistoryDays, *w.HistoryDays)
	}
	dur := func(name, s string, got time.Duration) {
		if s == "" {
			return
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != d {
			t.Errorf("%s = %s, want %s", name, got, d)
		}
	}
	dur("live_interval", w.LiveInterval, cfg.LiveInterval)
	dur("git_timeout", w.GitTimeout, cfg.GitTimeout)
	if w.ClaudeDir != "" && cfg.ClaudeDir != abs(w.ClaudeDir) {
		t.Errorf("claude_dir = %q, want %q", cfg.ClaudeDir, abs(w.ClaudeDir))
	}
	if w.CodexDir != "" && cfg.CodexDir != abs(w.CodexDir) {
		t.Errorf("codex_dir = %q, want %q", cfg.CodexDir, abs(w.CodexDir))
	}
	if w.UpdateCheck != nil {
		if cfg.UpdateCheck != *w.UpdateCheck || ((*got)[0].UpdateCheck != nil) != *w.UpdateCheck {
			t.Errorf("update_check = %v, want %v", cfg.UpdateCheck, *w.UpdateCheck)
		}
	}
	if w.Scoped != nil && ((*got)[0].Scope != "") != *w.Scoped {
		t.Errorf("scoped = %v, want %v", (*got)[0].Scope != "", *w.Scoped)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCompatSnapshotIsComplete keeps the newest snapshot in step with the
// code: every flag, env var and config key grove has today must be listed,
// so the next release's snapshot freezes it.
func TestCompatSnapshotIsComplete(t *testing.T) {
	dirs, snaps := loadSnapshots(t)
	newest, dir := snaps[len(snaps)-1], dirs[len(dirs)-1]
	isolate(t, nil)
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	flagRE := regexp.MustCompile(`(?m)^  -([a-z][a-z-]*)`)

	var help bytes.Buffer
	run([]string{"--help"}, &bytes.Buffer{}, &help)
	for _, m := range flagRE.FindAllStringSubmatch(help.String(), -1) {
		if !has(newest.Flags, m[1]) {
			t.Errorf("flag -%s missing from %s/invocations.json", m[1], dir)
		}
	}
	env := help.String()[strings.Index(help.String(), "Environment:"):]
	env = env[:strings.Index(env, "\n\n")]
	for _, m := range regexp.MustCompile(`(?m)^  ([A-Z_]+)`).FindAllStringSubmatch(env, -1) {
		if !has(newest.Env, m[1]) {
			t.Errorf("env %s missing from %s/invocations.json", m[1], dir)
		}
	}

	var uhelp bytes.Buffer
	run([]string{"update", "--help"}, &bytes.Buffer{}, &uhelp)
	for _, m := range flagRE.FindAllStringSubmatch(uhelp.String(), -1) {
		if !has(newest.UpdateFlags, m[1]) {
			t.Errorf("update flag -%s missing from %s/invocations.json", m[1], dir)
		}
	}
	if !has(newest.Subcommands, "update") {
		t.Errorf("subcommand update missing from %s", dir)
	}

	var keys map[string]any
	if _, err := toml.DecodeFile(filepath.Join(dir, "config.toml"), &keys); err != nil {
		t.Fatal(err)
	}
	ft := reflect.TypeOf(config.File{})
	for i := 0; i < ft.NumField(); i++ {
		k := strings.Split(ft.Field(i).Tag.Get("toml"), ",")[0]
		if _, ok := keys[k]; !ok {
			t.Errorf("config key %q missing from %s/config.toml", k, dir)
		}
	}
}
