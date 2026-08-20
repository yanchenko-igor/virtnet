// Command virtnet runs the virtual network simulator TUI: machine consoles
// as tabs over the §15 reference topology, with a clock display and a live
// packet panel (ARCHITECTURE.md §11).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/ui"
)

func main() {
	start := flag.String("start", "", "simulation start timestamp (RFC3339 like 2026-01-15T08:00:00Z, or Unix seconds); defaults to the Unix epoch")
	flag.Parse()

	startTime, err := parseStart(*start)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	l, err := lab.New15At(startTime)
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

// parseStart parses the -start flag. An empty value uses the Unix epoch; a
// bare integer is treated as Unix seconds; anything else must be an RFC3339
// timestamp.
func parseStart(s string) (time.Time, error) {
	if s == "" {
		return time.Unix(0, 0), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("virtnet: cannot parse start time %q (use RFC3339 like 2026-01-15T08:00:00Z, or Unix seconds)", s)
}
