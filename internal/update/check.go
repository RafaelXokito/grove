package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// CheckTimeout bounds the background release check (FR-23).
const CheckTimeout = 3 * time.Second

// CacheTTL is how long a cached check result is trusted.
const CacheTTL = 24 * time.Hour

// cacheSchema is the update.json format version. Readers ignore any other
// schema; the format only ever grows (SPEC §7).
const cacheSchema = 1

const cacheFile = "update.json"

type cacheData struct {
	Schema    int       `json:"schema"`
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// CacheDir returns grove's cache directory: $XDG_CACHE_HOME/grove when set
// (absolute), else the platform default under home (os.UserCacheDir rules).
func CacheDir(getenv func(string) string, home string) string {
	if x := getenv("XDG_CACHE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "grove")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "grove")
	}
	return filepath.Join(home, ".cache", "grove")
}

// Checker answers "is there a newer release?" using a 24 h file cache.
type Checker struct {
	Client   *Client
	CacheDir string // "" disables the cache
	Now      func() time.Time
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// LatestTag returns the latest release tag, from the cache when fresh.
func (c *Checker) LatestTag(ctx context.Context) (string, error) {
	if tag, ok := c.readCache(); ok {
		return tag, nil
	}
	rel, err := c.Client.Latest(ctx)
	if err != nil {
		return "", err
	}
	c.writeCache(rel.Tag)
	return rel.Tag, nil
}

// Available returns the latest tag if it is newer than current, else "".
// Every error is swallowed: the check is best-effort and silent.
func (c *Checker) Available(ctx context.Context, current string) string {
	if IsDev(current) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	tag, err := c.LatestTag(ctx)
	if err != nil || !Newer(tag, current) {
		return ""
	}
	return tag
}

func (c *Checker) readCache() (string, bool) {
	if c.CacheDir == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(c.CacheDir, cacheFile))
	if err != nil {
		return "", false
	}
	var d cacheData
	if json.Unmarshal(data, &d) != nil || d.Schema != cacheSchema || !ValidTag(d.Latest) {
		return "", false
	}
	age := c.now().Sub(d.CheckedAt)
	if age < 0 || age >= CacheTTL {
		return "", false
	}
	return d.Latest, true
}

func (c *Checker) writeCache(tag string) {
	if c.CacheDir == "" {
		return
	}
	data, err := json.Marshal(cacheData{Schema: cacheSchema, CheckedAt: c.now().UTC(), Latest: tag})
	if err != nil {
		return
	}
	if os.MkdirAll(c.CacheDir, 0o755) != nil {
		return
	}
	f, err := os.CreateTemp(c.CacheDir, ".update-*.json")
	if err != nil {
		return
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Rename(f.Name(), filepath.Join(c.CacheDir, cacheFile)) != nil {
		os.Remove(f.Name())
	}
}
