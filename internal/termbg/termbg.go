// Package termbg decides the terminal's dark/light background from
// $COLORFGBG at init time, so Lip Gloss never sends the OSC 11 background
// query. Bubble Tea v1 triggers that query from its own package init, and a
// terminal that does not answer stalls startup for termenv's ~5s timeout.
//
// Import it for side effects from main. Go initialises packages in import
// path order once dependencies are satisfied; this package depends only on
// lipgloss and its path (github.com/RafaelXokito/...) sorts before
// github.com/charmbracelet/bubbletea, so its init runs first.
package termbg

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func init() {
	lipgloss.SetHasDarkBackground(Dark(os.Getenv("COLORFGBG")))
}

// Dark interprets $COLORFGBG ("fg;bg" or "fg;default;bg"): a background
// colour index of 7 or 9–15 means light. Unset or unparsable means dark.
func Dark(colorfgbg string) bool {
	if colorfgbg == "" {
		return true
	}
	parts := strings.Split(colorfgbg, ";")
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return true
	}
	return !(bg == 7 || (bg >= 9 && bg <= 15))
}
