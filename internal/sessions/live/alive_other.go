//go:build !unix

package live

// PIDAlive is unsupported off unix; registry entries are never considered alive.
func PIDAlive(pid int) bool { return false }
