// Package config resolves grove's settings from flags, environment, the
// optional TOML config file and built-in defaults (SPEC FR-20), in that order
// of precedence.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/RafaelXokito/grove/internal/pathx"
)

// Defaults (FR-20).
const (
	DefaultRoot         = "~/Documents/repos"
	DefaultMaxDepth     = 3
	DefaultHistoryDays  = 30
	DefaultLiveInterval = 5 * time.Second
	DefaultGitTimeout   = 10 * time.Second

	// MinLiveInterval keeps the live poll from spinning.
	MinLiveInterval = 500 * time.Millisecond
)

// EnvRoot is the comma-separated roots environment variable.
const EnvRoot = "GROVE_ROOT"

// Config is the resolved configuration. Paths are absolute with ~ expanded.
type Config struct {
	Roots []string
	// MaxDepth is the scan depth below each root; 0 scans only the roots.
	MaxDepth     int
	HistoryDays  int // 0 = unlimited
	LiveInterval time.Duration
	GitTimeout   time.Duration
	ClaudeDir    string
	CodexDir     string

	// Warnings are non-fatal problems (e.g. unknown config keys).
	Warnings []string
}

// Flags holds command-line values; nil / empty means "not given".
type Flags struct {
	Roots       []string
	MaxDepth    *int
	HistoryDays *int
	ClaudeDir   *string
	CodexDir    *string
}

// File mirrors config.toml. Durations are strings such as "5s".
type File struct {
	Roots        []string `toml:"roots"`
	MaxDepth     *int     `toml:"max_depth"`
	HistoryDays  *int     `toml:"history_days"`
	LiveInterval string   `toml:"live_interval"`
	GitTimeout   string   `toml:"git_timeout"`
}

// Path returns the config file location: $XDG_CONFIG_HOME/grove/config.toml,
// else ~/.config/grove/config.toml.
func Path(getenv func(string) string, home string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "grove", "config.toml")
	}
	return filepath.Join(home, ".config", "grove", "config.toml")
}

// LoadFile reads and decodes path. A missing file yields a zero File and no
// error. Unknown keys are returned as warnings.
func LoadFile(path string) (File, []string, error) {
	var f File
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil, nil
	}
	if err != nil {
		return f, nil, fmt.Errorf("config %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return f, nil, fmt.Errorf("config %s: %w", path, err)
	}
	var warns []string
	for _, k := range md.Undecoded() {
		warns = append(warns, fmt.Sprintf("config %s: unknown key %q ignored", path, k.String()))
	}
	sort.Strings(warns)
	return f, warns, nil
}

// Resolve merges flags > env > file > defaults and validates the result.
// getenv supplies GROVE_ROOT and CODEX_HOME; home expands "~".
func Resolve(fl Flags, getenv func(string) string, file File, home string) (Config, error) {
	c := Config{
		Roots:        []string{DefaultRoot},
		MaxDepth:     DefaultMaxDepth,
		HistoryDays:  DefaultHistoryDays,
		LiveInterval: DefaultLiveInterval,
		GitTimeout:   DefaultGitTimeout,
		ClaudeDir:    filepath.Join(home, ".claude"),
		CodexDir:     filepath.Join(home, ".codex"),
	}
	if d := getenv("CODEX_HOME"); d != "" {
		c.CodexDir = d
	}

	// file
	if file.Roots != nil {
		if len(file.Roots) == 0 {
			return c, errors.New("config: roots must not be empty")
		}
		c.Roots = file.Roots
	}
	if file.MaxDepth != nil {
		c.MaxDepth = *file.MaxDepth
	}
	if file.HistoryDays != nil {
		c.HistoryDays = *file.HistoryDays
	}
	if file.LiveInterval != "" {
		d, err := time.ParseDuration(file.LiveInterval)
		if err != nil {
			return c, fmt.Errorf("config: live_interval %q: want a duration like \"5s\"", file.LiveInterval)
		}
		c.LiveInterval = d
	}
	if file.GitTimeout != "" {
		d, err := time.ParseDuration(file.GitTimeout)
		if err != nil {
			return c, fmt.Errorf("config: git_timeout %q: want a duration like \"10s\"", file.GitTimeout)
		}
		c.GitTimeout = d
	}

	// env
	if v := getenv(EnvRoot); strings.TrimSpace(v) != "" {
		var roots []string
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				roots = append(roots, r)
			}
		}
		if len(roots) == 0 {
			return c, fmt.Errorf("%s: no roots in %q", EnvRoot, v)
		}
		c.Roots = roots
	}

	// flags
	if len(fl.Roots) > 0 {
		c.Roots = fl.Roots
	}
	if fl.MaxDepth != nil {
		c.MaxDepth = *fl.MaxDepth
	}
	if fl.HistoryDays != nil {
		c.HistoryDays = *fl.HistoryDays
	}
	if fl.ClaudeDir != nil {
		c.ClaudeDir = *fl.ClaudeDir
	}
	if fl.CodexDir != nil {
		c.CodexDir = *fl.CodexDir
	}

	// validate + normalise
	if c.MaxDepth < 0 {
		return c, fmt.Errorf("max_depth must be >= 0, got %d", c.MaxDepth)
	}
	if c.HistoryDays < 0 {
		return c, fmt.Errorf("history_days must be >= 0 (0 = unlimited), got %d", c.HistoryDays)
	}
	if c.LiveInterval < MinLiveInterval {
		return c, fmt.Errorf("live_interval must be >= %s, got %s", MinLiveInterval, c.LiveInterval)
	}
	if c.GitTimeout <= 0 {
		return c, fmt.Errorf("git_timeout must be > 0, got %s", c.GitTimeout)
	}
	roots := make([]string, 0, len(c.Roots))
	for _, r := range c.Roots {
		if strings.TrimSpace(r) == "" {
			return c, errors.New("roots: empty path")
		}
		roots = append(roots, absPath(r, home))
	}
	c.Roots = roots
	c.ClaudeDir = absPath(c.ClaudeDir, home)
	c.CodexDir = absPath(c.CodexDir, home)
	return c, nil
}

func absPath(p, home string) string {
	p = pathx.ExpandHome(p, home)
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
