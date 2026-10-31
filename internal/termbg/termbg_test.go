package termbg

import "testing"

func TestDark(t *testing.T) {
	for in, want := range map[string]bool{"": true, "15;0": true, "0;15": false, "0;7": false, "12;default;0": true, "junk": true, "0;8": true} {
		if got := Dark(in); got != want {
			t.Errorf("Dark(%q) = %v, want %v", in, got, want)
		}
	}
}
