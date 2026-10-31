//go:build !darwin && !linux

package live

import (
	"context"
	"errors"

	"github.com/RafaelXokito/grove/internal/sessions"
)

type noScanner struct{}

func (noScanner) Scan(context.Context) ([]sessions.LiveProc, error) {
	return nil, &UnavailableError{Err: errors.New("unsupported OS")}
}

func (noScanner) StartTimes(context.Context, []int) (map[int]string, error) { return nil, nil }

// DefaultScanner returns a scanner that always reports unavailable.
func DefaultScanner() ProcScanner { return noScanner{} }
