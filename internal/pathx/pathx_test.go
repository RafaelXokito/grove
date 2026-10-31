package pathx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWithin(t *testing.T) {
	tests := []struct {
		parent, child string
		want          bool
	}{
		{"/x/repo", "/x/repo", true},
		{"/x/repo", "/x/repo/", true},
		{"/x/repo/", "/x/repo/sub/deep", true},
		{"/x/repo", "/x/repo-wt", false},
		{"/x/repo", "/x/repo-wt/sub", false},
		{"/x/repo", "/x", false},
		{"/x/repo", "/y/repo", false},
		{"/x/repo", "/x/repo/../other", false},
		{"/", "/anything", true},
		{"", "/x", false},
		{"/x", "", false},
		{"/x/my repo", "/x/my repo/sub dir", true},
		{"/x/a", "/x/a..b", false},
	}
	for _, tt := range tests {
		if got := IsWithin(tt.parent, tt.child); got != tt.want {
			t.Errorf("IsWithin(%q, %q) = %v, want %v", tt.parent, tt.child, got, tt.want)
		}
	}
}

func TestIsWithinDotDotPrefixedName(t *testing.T) {
	// A child named "..foo" is inside parent, not an escape.
	if !IsWithin("/x", "/x/..foo") {
		t.Error(`IsWithin("/x", "/x/..foo") = false, want true`)
	}
}

func TestAbbrevHome(t *testing.T) {
	tests := []struct {
		p, home, want string
	}{
		{"/Users/me", "/Users/me", "~"},
		{"/Users/me/repos/a", "/Users/me", "~/repos/a"},
		{"/Users/me/repos/a", "/Users/me/", "~/repos/a"},
		{"/Users/meow/a", "/Users/me", "/Users/meow/a"},
		{"/opt/x", "/Users/me", "/opt/x"},
		{"/opt/x", "", "/opt/x"},
		{"/a", "/", "~/a"},
		{"/a/b", "/", "~/a/b"},
		{"/", "/", "~"},
	}
	for _, tt := range tests {
		if got := AbbrevHome(tt.p, tt.home); got != tt.want {
			t.Errorf("AbbrevHome(%q, %q) = %q, want %q", tt.p, tt.home, got, tt.want)
		}
	}
}

func TestDisplay(t *testing.T) {
	tests := []struct {
		p, root, home, want string
	}{
		{"/Users/me/repos/a", "/Users/me/repos", "/Users/me", "a"},
		{"/Users/me/repos/a/b", "/Users/me/repos", "/Users/me", "a/b"},
		{"/Users/me/repos", "/Users/me/repos", "/Users/me", "."},
		{"/Users/me/other/a", "/Users/me/repos", "/Users/me", "~/other/a"},
		{"/Users/me/repos-x/a", "/Users/me/repos", "/Users/me", "~/repos-x/a"},
		{"/opt/a", "/Users/me/repos", "/Users/me", "/opt/a"},
	}
	for _, tt := range tests {
		if got := Display(tt.p, tt.root, tt.home); got != tt.want {
			t.Errorf("Display(%q, %q, %q) = %q, want %q", tt.p, tt.root, tt.home, got, tt.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	tests := []struct{ p, want string }{
		{"~", "/h"},
		{"~/a/b", "/h/a/b"},
		{"~other/a", "~other/a"},
		{"/abs", "/abs"},
		{"rel", "rel"},
	}
	for _, tt := range tests {
		if got := ExpandHome(tt.p, "/h"); got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestCanonical(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real dir")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", real, real},
		{"trailing slash and dots", real + "/./sub/../", real},
		{"symlink", link, real},
		{"inside symlink", filepath.Join(link, "sub"), filepath.Join(real, "sub")},
		{"missing under symlink", filepath.Join(link, "gone", "deeper"), filepath.Join(real, "gone", "deeper")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Canonical(tt.in); got != tt.want {
				t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCanonicalRelative(t *testing.T) {
	got := Canonical("does-not-exist-xyz")
	if !filepath.IsAbs(got) {
		t.Errorf("Canonical(relative) = %q, want absolute", got)
	}
}
