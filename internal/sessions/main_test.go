package sessions

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at a throwaway dir so no test can touch real agent state.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "grove-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
