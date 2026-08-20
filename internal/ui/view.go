package ui

import (
	"fmt"
	"strings"

	"github.com/yanchenko-igor/virtnet/internal/capture"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// View implements bubbletea.Model. It renders the tab bar, the active
// machine's console (or the packet panel), and the status bar.
func (m Model) View() string {
	if m.width == 0 {
		m.width = 80
	}
	if m.height == 0 {
		m.height = 24
	}
	var b strings.Builder
	b.WriteString(m.tabBar())
	b.WriteByte('\n')

	bodyH := m.height - 2
	body := m.consoleBody(bodyH)
	if m.showPkt {
		body = m.packetPanel(bodyH)
	}
	b.WriteString(body)
	b.WriteString(m.statusBar())
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
func (m Model) statusBar() string {
	mach := m.Active()
	return fmt.Sprintf("T=%v | tab %d/%d %s | frames %d | Tab switch · Enter run · ctrl+p packets · ctrl+c quit",
		m.lab.Clock.Now(),
		m.active+1, len(m.lab.Machines), mach.Hostname,
		m.lab.Capture.Len())
}
