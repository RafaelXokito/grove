package pathx

import (
	"runtime"
	"testing"
)

func TestIsWithinFold(t *testing.T) {
	darwin := runtime.GOOS == "darwin"
	tests := []struct {
		parent, child string
		want          bool
	}{
		{"/x/repo", "/x/repo/sub", true},
		{"/x/repo", "/x/repo-wt", false},
		{"/X/Repo", "/x/repo/sub", darwin},
		{"/x/repo", "/x/REPO", darwin},
		{"/x/REPO", "/x/repo-WT", false},
	}
	for _, tt := range tests {
		if got := IsWithinFold(tt.parent, tt.child); got != tt.want {
			t.Errorf("IsWithinFold(%q, %q) = %v, want %v", tt.parent, tt.child, got, tt.want)
		}
	}
	if got := FoldKey("/A/b"); darwin && got != "/a/b" || !darwin && got != "/A/b" {
		t.Errorf("FoldKey(/A/b) = %q", got)
	}
}
