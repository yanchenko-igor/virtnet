package ui

import (
	"fmt"
	"strings"

	"github.com/yanchenko-igor/virtnet/internal/capture"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// View implements bubbletea.Model. It renders the tab bar, the active
// machine's console (or the packet panel), and a fixed status bar at bottom.
func (m Model) View() string {
	if m.width == 0 {
		m.width = 80
	}
	if m.height == 0 {
		m.height = 24
	}

	tabBar := m.tabBar()
	statusBar := m.statusBar()

	// Calculate available height for console body
	// tabBar (1 line) + statusBar (1 line) + 1 blank line separator
	usedHeight := 3
	bodyH := m.height - usedHeight
	if bodyH < 1 {
		bodyH = 1
	}

	body := m.consoleBody(bodyH)
	if m.showPkt {
		body = m.packetPanel(bodyH)
	}

	// Build full screen content
	var b strings.Builder
	b.WriteString(tabBar)
	b.WriteByte('\n')

	// Console body
	b.WriteString(body)

	// Fill remaining space to push status bar to bottom
	allContent := tabBar + "\n" + body
	renderedLines := strings.Count(allContent, "\n")

	// Fill to push status bar to bottom
	for renderedLines < m.height-1 {
		b.WriteByte('\n')
		renderedLines++
	}

	b.WriteString(statusBar)
	return b.String()
}

// tabBar renders one tab per machine, highlighting the active one.
func (m Model) tabBar() string {
	var b strings.Builder
	for i, mach := range m.lab.Machines {
		if i == m.active {
			b.WriteString("[" + strings.ToUpper(mach.Hostname) + "]")
		} else {
			b.WriteString(" " + strings.ToUpper(mach.Hostname) + " ")
		}
	}
	b.WriteString("  —  SW ── R1 ── PC3")
	return b.String()
}

// consoleBody renders the active machine's transcript and the input line.
func (m Model) consoleBody(height int) string {
	mach := m.Active()
	transcript := mach.Console.Transcript()
	lines := strings.Split(strings.TrimSuffix(transcript, "\n"), "\n")
	// HandleInput leaves a trailing prompt in the transcript; the live input
	// line renders it again, so drop the redundant copy.
	if len(lines) > 0 && lines[len(lines)-1] == mach.Console.Prompt() {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if len(lines) > height-1 {
		lines = lines[len(lines)-(height-1):]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(mach.Console.Prompt())
	b.WriteString(string(m.input))
	b.WriteString("▌")
	b.WriteByte('\n')
	return b.String()
}

// packetPanel renders the most recent captured link traversals.
func (m Model) packetPanel(height int) string {
	recs := m.lab.Capture.Records()
	start := 0
	if len(recs) > pktPanelLimit {
		start = len(recs) - pktPanelLimit
	}
	var b strings.Builder
	b.WriteString("── captured packets ──\n")
	if start == len(recs) {
		b.WriteString("(no traffic yet)\n")
	} else {
		for _, r := range recs[start:] {
			b.WriteString(formatRecord(m.lab, r))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// formatRecord renders one capture record as a packet-inspection line
// (ARCHITECTURE.md §11.3).
func formatRecord(l *lab.Lab, r capture.Record) string {
	typ := "?"
	switch r.Frame.Type {
	case ethernet.EtherTypeARP:
		typ = "ARP"
	case ethernet.EtherTypeIPv4:
		typ = "IPv4"
	}
	return fmt.Sprintf("T=%s  %s → %s  %s",
		r.Time, l.Label(r.Src), l.Label(r.Dst), typ)
}

// statusBar renders the clock, machine, and key hints.
// Adapts to terminal width to avoid wrapping.
func (m Model) statusBar() string {
	mach := m.Active()
	clockStr := fmt.Sprintf("T=%v", m.lab.Clock.Now())
	tabStr := fmt.Sprintf("tab %d/%d %s", m.active+1, len(m.lab.Machines), mach.Hostname)
	framesStr := fmt.Sprintf("frames %d", m.lab.Capture.Len())

	// Calculate available space for hints
	used := len(clockStr) + len(tabStr) + len(framesStr) + 6 // separators
	avail := m.width - used

	var hints string
	if avail >= 55 {
		hints = "Tab · Enter · ctrl+p · ctrl+c quit"
	} else if avail >= 35 {
		hints = "Tab · Enter · ctrl+c"
	} else if avail >= 20 {
		hints = "ctrl+c"
	} else {
		hints = ""
	}

	if hints != "" {
		return fmt.Sprintf("%s | %s | %s | %s", clockStr, tabStr, framesStr, hints)
	}
	return fmt.Sprintf("%s | %s | %s", clockStr, tabStr, framesStr)
}
