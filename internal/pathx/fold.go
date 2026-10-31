package pathx

import "strings"

// FoldKey returns the form of p used for path comparisons: lower-cased on
// platforms whose default filesystem is case-insensitive (darwin, see
// caseInsensitive), unchanged elsewhere. It does not query the filesystem, so
// on a case-sensitive APFS volume two distinct paths differing only in case
// would compare equal; this rare false match is accepted (SPEC T4 note b).
func FoldKey(p string) string {
	if caseInsensitive {
		return strings.ToLower(p)
	}
	return p
}

// IsWithinFold is IsWithin applied to FoldKey'd paths.
func IsWithinFold(parent, child string) bool {
	return IsWithin(FoldKey(parent), FoldKey(child))
}
