// Command virtnet runs the virtual network simulator TUI: machine consoles
// as tabs over the §15 reference topology, with a clock display and a live
// packet panel (ARCHITECTURE.md §11).
package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbletea"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/ui"
)

func main() {
	l, err := lab.New15()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p := tea.NewProgram(ui.New(l), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
