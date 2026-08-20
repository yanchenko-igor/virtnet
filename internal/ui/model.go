// Package ui is the terminal UI for VirtNet (ARCHITECTURE.md §11): machine
// consoles as tabs, a virtual-clock display, and a live packet panel fed by
// the lab's deterministic capture.
//
// The UI is pure presentation. Every simulation action happens synchronously
// inside a key handler (submit → HandleInput → RunQuiescent), so the virtual
// world only advances when the user drives it and never depends on host time.
package ui

import (
	"github.com/charmbracelet/bubbletea"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/machine"
)

// pktPanelLimit is how many capture records the packet panel renders.
const pktPanelLimit = 12

// Model is the Bubbletea state for the whole TUI.
type Model struct {
	lab     *lab.Lab
	active  int
	input   []rune
	showPkt bool
	width   int
	height  int
}

// New returns a model bound to the given lab. The active machine is the first
// one in the lab's machine list.
func New(l *lab.Lab) Model {
	return Model{lab: l}
}

// Init implements bubbletea.Model.
func (m Model) Init() tea.Cmd {
	return nil
}

// Active returns the machine of the focused tab.
func (m Model) Active() *machine.Machine {
	return m.lab.Machines[m.active]
}

// Input returns the current unsubmitted command line.
func (m Model) Input() string {
	return string(m.input)
}

// ShowPackets reports whether the packet panel is visible.
func (m Model) ShowPackets() bool {
	return m.showPkt
}

// Next advances to the next machine tab.
func (m *Model) Next() {
	m.active = (m.active + 1) % len(m.lab.Machines)
}

// Prev moves to the previous machine tab.
func (m *Model) Prev() {
	m.active = (m.active - 1 + len(m.lab.Machines)) % len(m.lab.Machines)
}

// Submit runs the buffered command line on the active machine. Foreground
// processes (like ping) are started but NOT driven to completion; they will
// be stepped by Update via StepForeground so output appears incrementally.
// The final prompt is written when the process exits (in CopyForegroundOutput).
func (m *Model) Submit() {
	line := string(m.input)
	m.input = nil
	if line == "" {
		return
	}
	mach := m.Active()
	mach.Console.WritePrompt()
	mach.Console.Write(line + "\n")
	p := mach.Execute(line)
	if p != nil {
		if s := p.Stdout.String(); s != "" {
			mach.Console.Write(s)
			p.Stdout.Reset()
		}
		if s := p.Stderr.String(); s != "" {
			mach.Console.Write(s)
			p.Stderr.Reset()
		}
	}
	// Do NOT write final prompt here; it's written when foreground process exits
}

// Rune appends a character to the command line.
func (m *Model) Rune(r rune) {
	m.input = append(m.input, r)
}

// Backspace removes the last character of the command line.
func (m *Model) Backspace() {
	if len(m.input) > 0 {
		m.input = m.input[:len(m.input)-1]
	}
}

// TogglePackets shows or hides the packet panel.
func (m *Model) TogglePackets() {
	m.showPkt = !m.showPkt
}

// Update implements bubbletea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc, tea.KeyCtrlQ:
			return m, tea.Quit
		case tea.KeyCtrlP:
			m.TogglePackets()
		case tea.KeyEnter:
			m.Submit()
		case tea.KeyBackspace:
			m.Backspace()
		case tea.KeyTab:
			m.Next()
		case tea.KeyShiftTab:
			m.Prev()
		default:
			// Printable characters arrive either as KeyRunes or as a named
			// key carrying runes (space is KeySpace with Runes [' ']).
			for _, r := range msg.Runes {
				m.Rune(r)
			}
		}
	}
	// Step foreground processes (e.g. incremental ping) once per Update
	// cycle so output appears incrementally. Copy their stdout/stderr to
	// the machine console so it renders.
	for _, mach := range m.lab.Machines {
		mach.StepForeground()
		mach.CopyForegroundOutput()
	}
	return m, nil
}
