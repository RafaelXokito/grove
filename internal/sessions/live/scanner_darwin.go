//go:build darwin

package live

// DefaultScanner returns the macOS ps+lsof scanner.
func DefaultScanner() ProcScanner { return &PSScanner{} }
