//go:build !darwin

package pathx

// caseInsensitive: Linux filesystems are case-sensitive.
const caseInsensitive = false
