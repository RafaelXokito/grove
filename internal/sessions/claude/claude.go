// Package claude reads Claude Code transcripts (<dir>/projects/*/*.jsonl) as
// sessions.Session values. It is strictly read-only and reads at most
// HeadLimit + TailLimit bytes per file (FR-4, FR-18).
package claude

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

// Read bounds per transcript file.
const (
	HeadLimit = 256 << 10
	TailLimit = 64 << 10
)

// Source lists Claude Code sessions under Dir (normally ~/.claude; the caller
// resolves the default so tests never touch the real one).
type Source struct {
	Dir string

	// open is a test hook; nil means os.Open.
	open func(name string) (*os.File, error)
}

// New returns a Source rooted at dir.
func New(dir string) *Source { return &Source{Dir: dir} }

// Agent implements sessions.Source.
func (s *Source) Agent() sessions.Agent { return sessions.AgentClaude }

type candidate struct {
	path  string
	size  int64
	mtime time.Time
}

// List implements sessions.Source. Only top-level <dir>/projects/<enc>/*.jsonl
// files are considered, which excludes <sessionId>/subagents/ and memory/.
// Files with mtime before since are not opened (zero since = no window). A
// missing projects dir yields no sessions and no error. Unreadable files are
// skipped and reported via the joined error; sessions without any cwd are
// ignored silently. Result order: LastActivity desc, then ID.
func (s *Source) List(ctx context.Context, since time.Time) ([]sessions.Session, int, error) {
	var cands []candidate
	var errs []error
	matches, err := transcriptPaths(filepath.Join(s.Dir, "projects"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	for _, p := range matches {
		fi, err := os.Stat(p)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if !since.IsZero() && fi.ModTime().Before(since) {
			continue
		}
		cands = append(cands, candidate{path: p, size: fi.Size(), mtime: fi.ModTime()})
	}

	results := make([]*sessions.Session, len(cands))
	fileErrs := make([]error, len(cands))
	workers := min(runtime.NumCPU(), len(cands))
	idx := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				results[i], fileErrs[i] = s.parse(cands[i])
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
		if r != nil {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivity.Equal(out[j].LastActivity) {
			return out[i].LastActivity.After(out[j].LastActivity)
		}
		return out[i].ID < out[j].ID
	})
	return out, 0, errors.Join(errs...)
}

// transcriptPaths returns projects/<enc>/*.jsonl without glob matching, so
// metacharacters in the directory name are harmless. Unreadable project dirs
// are skipped.
func transcriptPaths(projects string) ([]string, error) {
	dirs, err := os.ReadDir(projects)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(projects, d.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				out = append(out, filepath.Join(sub, f.Name()))
			}
		}
	}
	return out, nil
}

// line holds the transcript fields grove cares about.
type line struct {
	Type          string          `json:"type"`
	Cwd           string          `json:"cwd"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	AITitle       string          `json:"aiTitle"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Origin        *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// scanInfo is what one chunk (head or tail) yields.
type scanInfo struct {
	firstCwd, lastCwd string
	lastTitle         string
	prompt            string
	hasPrompt         bool
}

func (s *Source) parse(c candidate) (*sessions.Session, error) {
	open := s.open
	if open == nil {
		open = os.Open
	}
	f, err := open(c.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	headN := min(c.size, HeadLimit)
	head := make([]byte, headN)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path, err)
	}
	h := scanChunk(head, false, c.size > headN)

	var cwd, title string
	if c.size <= headN {
		// Whole file was read: head scan saw every line.
		cwd, title = h.lastCwd, h.lastTitle
	} else {
		off := max(c.size-TailLimit, headN) // never re-read head bytes
		var tail []byte
		dropFirst := false
		if off > headN {
			// Read one byte early and drop through the first newline: this
			// keeps exactly the lines that start at or after off.
			off--
			dropFirst = true
		}
		tail = make([]byte, c.size-off)
		if _, err := f.ReadAt(tail, off); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", c.path, err)
		}
		if !dropFirst {
			// Contiguous with head: stitch the head's dropped partial last
			// line onto the tail (if head had no newline, it is one line
			// longer than HeadLimit and its remainder is dropped).
			if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
				tail = append(append([]byte{}, head[i+1:]...), tail...)
			} else {
				dropFirst = true
			}
		}
		t := scanChunk(tail, dropFirst, false)
		cwd = t.lastCwd
		if cwd == "" {
			cwd = h.lastCwd
		}
		title = t.lastTitle
		if title == "" {
			title = h.lastTitle
		}
	}
	if cwd == "" {
		return nil, nil
	}
	if title == "" && h.hasPrompt {
		title = h.prompt
	}
	return &sessions.Session{
		Agent:        sessions.AgentClaude,
		ID:           strings.TrimSuffix(filepath.Base(c.path), ".jsonl"),
		Cwd:          cwd,
		Title:        sessions.CleanTitle(title, sessions.TitleMaxRunes),
		LastActivity: c.mtime,
		Path:         c.path,
	}, nil
}

// scanChunk parses newline-separated JSON lines. dropFirst/dropLast discard a
// partial line at the chunk edges. Malformed and sidechain lines are skipped.
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
		if json.Unmarshal(raw, &l) != nil || l.IsSidechain {
			continue
		}
		if l.Cwd != "" {
			if info.firstCwd == "" {
				info.firstCwd = l.Cwd
			}
			info.lastCwd = l.Cwd
		}
		if l.Type == "ai-title" && strings.TrimSpace(l.AITitle) != "" {
			info.lastTitle = l.AITitle
		}
		if !info.hasPrompt {
			if p, ok := realPrompt(&l); ok {
				info.prompt, info.hasPrompt = p, true
			}
		}
	}
	return info
}

// realPrompt reports whether l is a human-typed prompt and returns its text.
func realPrompt(l *line) (string, bool) {
	if l.Type != "user" || l.IsMeta || l.IsSidechain || l.Message == nil {
		return "", false
	}
	if len(l.ToolUseResult) > 0 && string(l.ToolUseResult) != "null" {
		return "", false
	}
	if l.Origin != nil && l.Origin.Kind != "human" {
		return "", false
	}
	var text string
	if json.Unmarshal(l.Message.Content, &text) != nil {
		// Array content: a typed prompt with attachments (e.g. pasted image
		// + text) uses its first text block; tool_result arrays are skipped.
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return "", false
		}
		found := false
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return "", false
			}
			if b.Type == "text" && !found {
				text, found = b.Text, true
			}
		}
		if !found {
			return "", false
		}
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<") {
		return "", false
	}
	return text, true
}
