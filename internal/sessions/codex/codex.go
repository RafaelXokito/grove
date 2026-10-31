// Package codex reads Codex CLI rollouts (<dir>/sessions/YYYY/MM/DD/rollout-*.jsonl
// and <dir>/archived_sessions/) as sessions.Session values. It is strictly
// read-only and reads at most HeadLimit + TailLimit bytes per file (FR-5, FR-18).
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RafaelXokito/grove/internal/sessions"
)

// Read bounds per rollout file.
const (
	HeadLimit = 256 << 10
	TailLimit = 64 << 10
	// MetaLimit caps the first (session_meta) line, which carries large
	// base_instructions in real rollouts. It is read separately from the head.
	MetaLimit = 4 << 20
)

const (
	rolloutPrefix = "rollout-"
	jsonlExt      = ".jsonl"
	zstExt        = ".jsonl.zst"
)

// DefaultDir resolves the Codex home: $CODEX_HOME, else ~/.codex.
func DefaultDir() (string, error) {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// Source lists Codex sessions under Dir (normally $CODEX_HOME or ~/.codex; the
// caller resolves the default so tests never touch the real one).
type Source struct {
	Dir string

	// open is a test hook; nil means os.Open.
	open func(name string) (*os.File, error)
}

// New returns a Source rooted at dir.
func New(dir string) *Source { return &Source{Dir: dir} }

// Agent implements sessions.Source.
func (s *Source) Agent() sessions.Agent { return sessions.AgentCodex }

type candidate struct {
	path  string
	size  int64
	mtime time.Time
}

// List implements sessions.Source. Rollouts are collected from sessions/ (any
// depth, normally YYYY/MM/DD) and archived_sessions/ using os.ReadDir only, so
// glob metacharacters in paths are harmless. Files with mtime before since are
// not opened (zero since = no window). Date directories are not used to prune:
// a resumed rollout keeps its original day dir but gets a fresh mtime.
// Compressed .jsonl.zst rollouts within the window are counted in skipped
// unless a sibling .jsonl exists, as are rollouts whose session_meta is
// unreadable (e.g. beyond MetaLimit) and that have no turn_context cwd either.
// Subagent/internal sessions and sessions whose meta has no cwd are ignored
// silently; duplicate thread ids keep the newest rollout; unreadable files are reported via
// the joined error alongside the partial result. A missing dir yields nothing.
// Result order: LastActivity desc, then ID.
func (s *Source) List(ctx context.Context, since time.Time) ([]sessions.Session, int, error) {
	var cands []candidate
	var errs []error
	skipped := 0
	for _, sub := range []string{"sessions", "archived_sessions"} {
		c, sk, err := collect(ctx, filepath.Join(s.Dir, sub), since)
		cands = append(cands, c...)
		skipped += sk
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	results := make([]*sessions.Session, len(cands))
	fileSkipped := make([]bool, len(cands))
	fileErrs := make([]error, len(cands))
	workers := min(runtime.NumCPU(), len(cands))
	idx := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				results[i], fileSkipped[i], fileErrs[i] = s.parse(cands[i])
			}
		}()
	}
feed:
	for i := range cands {
		select {
		case <-ctx.Done():
			break feed
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	var out []sessions.Session
	for i, r := range results {
		if fileErrs[i] != nil {
			errs = append(errs, fileErrs[i])
		}
		if fileSkipped[i] {
			skipped++
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivity.Equal(out[j].LastActivity) {
			return out[i].LastActivity.After(out[j].LastActivity)
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Path < out[j].Path
	})
	// One thread can own several rollouts (revert writes a new one; a thread
	// may appear in both sessions/ and archived_sessions/): keep the newest.
	seen := make(map[string]bool, len(out))
	dedup := out[:0]
	for _, s := range out {
		if !seen[s.ID] {
			seen[s.ID] = true
			dedup = append(dedup, s)
		}
	}
	return dedup, skipped, errors.Join(errs...)
}

// collect walks root with os.ReadDir (no globbing, no symlink following) and
// returns rollout candidates within the window plus the count of compressed
// rollouts skipped. A missing root is not an error; unreadable subdirectories
// are skipped.
func collect(ctx context.Context, root string, since time.Time) ([]candidate, int, error) {
	var cands []candidate
	skipped := 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		names := make(map[string]bool, len(ents))
		for _, e := range ents {
			names[e.Name()] = true
		}
		for _, e := range ents {
			name := e.Name()
			p := filepath.Join(dir, name)
			if e.IsDir() {
				if depth < 4 {
					_ = walk(p, depth+1) // unreadable subdirs are skipped
				}
				continue
			}
			if !e.Type().IsRegular() || !strings.HasPrefix(name, rolloutPrefix) {
				continue
			}
			isZst := strings.HasSuffix(name, zstExt)
			if !isZst && !strings.HasSuffix(name, jsonlExt) {
				continue
			}
			if isZst && names[strings.TrimSuffix(name, ".zst")] {
				continue // prefer the uncompressed sibling
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if !since.IsZero() && fi.ModTime().Before(since) {
				continue
			}
			if isZst {
				skipped++
				continue
			}
			cands = append(cands, candidate{path: p, size: fi.Size(), mtime: fi.ModTime()})
		}
		return nil
	}
	err := walk(root, 0)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return cands, skipped, err
}

// line is one rollout record.
type line struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type metaPayload struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	Cwd          string          `json:"cwd"`
	Source       json.RawMessage `json:"source"`
	ThreadSource string          `json:"thread_source"`
	Git          *struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type turnContextPayload struct {
	Cwd string `json:"cwd"`
}

type eventPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// scanInfo is what one chunk (head or tail) yields.
type scanInfo struct {
	meta      *metaPayload
	lastCwd   string // last turn_context cwd
	prompt    string
	hasPrompt bool
}

// parse reads one rollout. skipped reports a rollout that could not be used
// at all (unreadable session_meta and no cwd anywhere).
func (s *Source) parse(c candidate) (_ *sessions.Session, skipped bool, _ error) {
	open := s.open
	if open == nil {
		open = os.Open
	}
	f, err := open(c.path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	m, h, head, headEnd, err := readHead(f, c.size)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", c.path, err)
	}
	if m != nil && isSubagent(m) {
		return nil, false, nil
	}

	cwd := h.lastCwd
	if c.size > headEnd {
		off := max(c.size-TailLimit, headEnd) // never re-read head bytes
		tailDrop := false
		if off > headEnd {
			// Read one byte early and drop through the first newline: this
			// keeps exactly the lines that start at or after off.
			off--
			tailDrop = true
		}
		tail := make([]byte, c.size-off)
		if _, err := f.ReadAt(tail, off); err != nil && !errors.Is(err, io.EOF) {
			return nil, false, fmt.Errorf("%s: %w", c.path, err)
		}
		if !tailDrop {
			// Contiguous with head: stitch the head's dropped partial last line.
			if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
				tail = append(append([]byte{}, head[i+1:]...), tail...)
			} else {
				tailDrop = true
			}
		}
		if t := scanChunk(tail, tailDrop, false); t.lastCwd != "" {
			cwd = t.lastCwd
		}
	}
	var id string
	if m != nil {
		if cwd == "" {
			cwd = m.Cwd
		}
		// payload.id is the thread id (what `codex resume` takes).
		id = m.ID
		if id == "" {
			id = m.SessionID
		}
	}
	if cwd == "" {
		return nil, m == nil, nil
	}
	if id == "" {
		id = idFromName(c.path)
	}
	return &sessions.Session{
		Agent:        sessions.AgentCodex,
		ID:           id,
		Cwd:          cwd,
		Title:        sessions.CleanTitle(h.prompt, sessions.TitleMaxRunes),
		LastActivity: c.mtime,
		Path:         c.path,
	}, false, nil
}

// readHead reads the session_meta line (own MetaLimit cap, only the needed
// fields decoded) and then up to HeadLimit bytes after it, which it scans.
// m is the session_meta payload from line 1, else from the head scan.
func readHead(f io.ReaderAt, size int64) (m *metaPayload, h scanInfo, head []byte, headEnd int64, err error) {
	var headStart int64
	dropFirst := false
	var first struct {
		Type    string      `json:"type"`
		Payload metaPayload `json:"payload"`
	}
	dec := json.NewDecoder(io.NewSectionReader(f, 0, min(size, MetaLimit)))
	if err := dec.Decode(&first); err != nil {
		// Oversized or malformed first line: start the head after it.
		headStart, dropFirst = skipFirstLine(f, min(size, MetaLimit+1))
	} else if first.Type == "session_meta" {
		m = &first.Payload
		headStart = dec.InputOffset()
	}
	headN := max(min(size-headStart, HeadLimit), 0)
	head = make([]byte, headN)
	if _, err := f.ReadAt(head, headStart); err != nil && !errors.Is(err, io.EOF) {
		return nil, scanInfo{}, nil, 0, err
	}
	headEnd = headStart + headN
	h = scanChunk(head, dropFirst, size > headEnd)
	if m == nil {
		m = h.meta
	}
	return m, h, head, headEnd, nil
}

// Detail implements sessions.Detailer: it re-reads the rollout's meta line
// and head (same bounds as List) and returns the full first prompt and
// payload.git.branch.
func (s *Source) Detail(ctx context.Context, sess sessions.Session) (sessions.Detail, error) {
	if err := ctx.Err(); err != nil {
		return sessions.Detail{}, err
	}
	open := s.open
	if open == nil {
		open = os.Open
	}
	f, err := open(sess.Path)
	if err != nil {
		return sessions.Detail{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return sessions.Detail{}, err
	}
	m, h, _, _, err := readHead(f, fi.Size())
	if err != nil {
		return sessions.Detail{}, fmt.Errorf("%s: %w", sess.Path, err)
	}
	d := sessions.Detail{Prompt: h.prompt}
	if m != nil && m.Git != nil {
		d.GitBranch = m.Git.Branch
	}
	return d, nil
}

// skipFirstLine returns the offset just past the first newline within the
// first limit bytes (bounded read). With no newline there it returns limit
// and dropFirst=true so the head scan discards the rest of that line.
func skipFirstLine(r io.ReaderAt, limit int64) (int64, bool) {
	buf := make([]byte, 64<<10)
	for off := int64(0); off < limit; {
		n, err := r.ReadAt(buf[:min(int64(len(buf)), limit-off)], off)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return off + int64(i) + 1, false
		}
		off += int64(n)
		if err != nil || n == 0 {
			break
		}
	}
	return limit, true
}

// isSubagent reports whether the session_meta describes a subagent or
// internal thread. SessionSource serialises SubAgent as {"subagent":…} and
// Internal as {"internal":…}; {"custom":…} is a real user session.
func isSubagent(m *metaPayload) bool {
	switch m.ThreadSource {
	case "subagent", "guardian_review", "memory_consolidation":
		return true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(m.Source, &obj) != nil {
		return false // string ("cli", "vscode", …) or absent
	}
	_, sub := obj["subagent"]
	_, internal := obj["internal"]
	return sub || internal
}

// idFromName extracts the thread id from rollout-<ts>-<threadId>.jsonl or
// rollout-<ts>-<threadId>_<rolloutId>.jsonl (<ts> is 19 chars).
func idFromName(p string) string {
	base := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(p), jsonlExt), rolloutPrefix)
	if len(base) > 20 && base[19] == '-' {
		base = base[20:]
	}
	id, _, _ := strings.Cut(base, "_")
	return id
}

// scanChunk parses newline-separated JSON lines. dropFirst/dropLast discard a
// partial line at the chunk edges. Malformed lines are skipped. Only the first
// session_meta and first real user_message are kept; the last turn_context cwd
// wins.
func scanChunk(b []byte, dropFirst, dropLast bool) scanInfo {
	if dropFirst {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return scanInfo{}
		}
		b = b[i+1:]
	}
	if dropLast {
		i := bytes.LastIndexByte(b, '\n')
		if i < 0 {
			return scanInfo{}
		}
		b = b[:i]
	}
	var info scanInfo
	for len(b) > 0 {
		var raw []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			raw, b = b[:i], b[i+1:]
		} else {
			raw, b = b, nil
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		switch l.Type {
		case "session_meta":
			if info.meta == nil {
				var m metaPayload
				if json.Unmarshal(l.Payload, &m) == nil {
					info.meta = &m
				}
			}
		case "turn_context":
			var tc turnContextPayload
			if json.Unmarshal(l.Payload, &tc) == nil && tc.Cwd != "" {
				info.lastCwd = tc.Cwd
			}
		case "event_msg":
			if info.hasPrompt {
				continue
			}
			var ev eventPayload
			if json.Unmarshal(l.Payload, &ev) != nil || ev.Type != "user_message" {
				continue
			}
			text := strings.TrimSpace(ev.Message)
			if text == "" || strings.HasPrefix(text, "<") {
				continue // injected context (e.g. <environment_context>)
			}
			info.prompt, info.hasPrompt = text, true
		}
	}
	return info
}
