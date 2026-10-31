package gitx

import (
	"context"
	"io"
	"strings"
	"testing"
)

type bufRunner struct{ out string }

func (b bufRunner) Run(context.Context, string, ...string) ([]byte, error) { return []byte(b.out), nil }

// streamRunner writes its output in small chunks, like a pipe.
type streamRunner struct {
	bufRunner
	streamed bool
}

func (s *streamRunner) RunStream(_ context.Context, _ string, w io.Writer, _ ...string) error {
	s.streamed = true
	for i := 0; i < len(s.out); i += 3 {
		if _, err := w.Write([]byte(s.out[i:min(i+3, len(s.out))])); err != nil {
			return err
		}
	}
	return nil
}

func TestRunCapped(t *testing.T) {
	out, tr, err := RunCapped(context.Background(), bufRunner{"abcdef"}, "", 4)
	if err != nil || string(out) != "abcd" || !tr {
		t.Errorf("got %q %v %v", out, tr, err)
	}
	sr := &streamRunner{bufRunner: bufRunner{"abcdefgh"}}
	out, tr, _ = RunCapped(context.Background(), sr, "", 100)
	if !sr.streamed || string(out) != "abcdefgh" || tr {
		t.Errorf("stream: %q %v", out, tr)
	}
}

func TestCountRecords(t *testing.T) {
	sr := &streamRunner{bufRunner: bufRunner{strings.Repeat("file\x00", 50)}}
	n, capped, err := CountRecords(context.Background(), sr, "", 10)
	if err != nil || n != 10 || !capped {
		t.Errorf("capped: %d %v %v", n, capped, err)
	}
	n, capped, _ = CountRecords(context.Background(), bufRunner{"a\x00b\x00"}, "", 10)
	if n != 2 || capped {
		t.Errorf("small: %d %v", n, capped)
	}
}

func TestExecRunnerStreams(t *testing.T) {
	var _ StreamRunner = (*ExecRunner)(nil)
	out, tr, err := RunCapped(context.Background(), NewExecRunner(), "", 3, "--version")
	if err != nil || string(out) != "git" || !tr {
		t.Errorf("got %q %v %v", out, tr, err)
	}
}
