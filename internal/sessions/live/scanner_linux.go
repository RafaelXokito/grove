//go:build linux

package live

// DefaultScanner returns the /proc scanner.
func DefaultScanner() ProcScanner { return &ProcFSScanner{Root: "/proc"} }
