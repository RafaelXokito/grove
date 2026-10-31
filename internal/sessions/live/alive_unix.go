//go:build unix

package live

import (
	"errors"
	"syscall"
)

// PIDAlive reports whether pid exists (EPERM means it exists but belongs to
// another user).
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
