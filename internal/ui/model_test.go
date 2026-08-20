package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/yanchenko-igor/virtnet/internal/lab"
)

func newModel(t *testing.T) Model {
	t.Helper()
	l, err := lab.New15()
	if err != nil {
		t.Fatal(err)
	}
	m := New(l)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return nm.(Model)
}

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(Model)
}

func TestTypingAccumulates(t *testing.T) {
	m := newModel(t)
	for _, r := range "ping 10.0.0.20" {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.Input(); got != "ping 10.0.0.20" {
		t.Errorf("input = %q, want %q", got, "ping 10.0.0.20")
	}
}

// TestTypingSpace keeps arguments intact: Bubbletea reports space as
// KeySpace (with a rune payload), not KeyRunes.
func TestTypingSpace(t *testing.T) {
	m := newModel(t)
	for _, tt := range []tea.KeyMsg{
		{Type: tea.KeySpace, Runes: []rune{' '}},
		{Type: tea.KeyRunes, Runes: []rune("ping 10.0.0.1")},
	} {
		m = update(t, m, tt)
	}
	if got := m.Input(); got != " ping 10.0.0.1" {
		t.Errorf("input = %q, want %q", got, " ping 10.0.0.1")
	}
}

func TestBackspace(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a', 'b', 'c'}})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.Input(); got != "ab" {
		t.Errorf("input = %q, want %q", got, "ab")
	}
}

func TestSubmitRunsCommandDeterministically(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	transcript := m.Active().Console.Transcript()
	if !strings.Contains(transcript, "PING 10.0.0.20 (10.0.0.20) 56 bytes of data.") {
		t.Errorf("transcript missing ping header:\n%s", transcript)
	}
	// After submit, ping is incremental - only header printed.
	// First ping result appears on NEXT Update cycle.
	if got := m.Input(); got != "" {
		t.Errorf("input not cleared after submit: %q", got)
	}
}

// TestPingIncremental verifies that ping -c N prints one result per Update cycle.
func TestPingIncremental(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping -c 3 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// After Enter (first Update): header + first ping result
	transcript := m.Active().Console.Transcript()
	if !strings.Contains(transcript, "PING 10.0.0.20 (10.0.0.20) 56 bytes of data.") {
		t.Errorf("transcript missing ping header:\n%s", transcript)
	}
	if !strings.Contains(transcript, "64 bytes from 10.0.0.20: icmp_seq=1 ttl=64 time=90.000 ms") {
		t.Errorf("first ping result missing after Enter:\n%s", transcript)
	}

	// Update 1 (space): second ping result
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	transcript = m.Active().Console.Transcript()
	if !strings.Contains(transcript, "icmp_seq=2") || !strings.Contains(transcript, "time=40.000 ms") {
		t.Errorf("second ping result missing:\n%s", transcript)
	}

	// Update 2 (space): third ping result
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	transcript = m.Active().Console.Transcript()
	if !strings.Contains(transcript, "icmp_seq=3") || !strings.Contains(transcript, "time=40.000 ms") {
		t.Errorf("third ping result missing:\n%s", transcript)
	}

	// Update 3 (space): summary + prompt (process exits after 3 pings)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	transcript = m.Active().Console.Transcript()
	if !strings.Contains(transcript, "3 packets transmitted, 3 received, 0.0% packet loss") {
		t.Errorf("ping summary missing:\n%s", transcript)
	}
	if !strings.HasSuffix(strings.TrimRight(transcript, "\n"), "pc1$ ") {
		t.Errorf("final prompt missing:\n%s", transcript)
	}

	// Clock: 1ms cmd + 90 + 40 + 40 = 171ms
	if got := m.lab.Clock.Now(); got != 171*time.Millisecond {
		t.Errorf("clock = %v, want 171ms", got)
	}
}

func TestEmptySubmitDoesNothing(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.lab.Clock.Now(); got != 0 {
		t.Errorf("clock advanced %v on empty submit", got)
	}
	if m.Active().Console.Transcript() != "" {
		t.Errorf("empty submit produced output: %q", m.Active().Console.Transcript())
	}
}

func TestTabCyclesMachines(t *testing.T) {
	m := newModel(t)
	if got := m.Active().Hostname; got != "pc1" {
		t.Fatalf("initial active = %s, want pc1", got)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.Active().Hostname; got != "pc2" {
		t.Errorf("after tab active = %s, want pc2", got)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.Active().Hostname; got != "pc1" {
		t.Errorf("after wrap active = %s, want pc1", got)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := m.Active().Hostname; got != "pc3" {
		t.Errorf("after shift-tab active = %s, want pc3", got)
	}
}

func TestCtrlPTogglesPacketPanel(t *testing.T) {
	m := newModel(t)
	if m.ShowPackets() {
		t.Fatal("packet panel visible by default")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.ShowPackets() {
		t.Error("ctrl+p did not show packet panel")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.ShowPackets() {
		t.Error("ctrl+p did not hide packet panel")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyType
	}{
		{"ctrl+d", tea.KeyCtrlD},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(t)
			nm, cmd := m.Update(tea.KeyMsg{Type: tt.key})
			if cmd == nil {
				t.Fatal("expected Quit command")
			}
			_ = nm
		})
	}
}

// TestCtrlCInterruptsPing verifies Ctrl+C interrupts a running ping
// and prints the statistics summary (like real ping).
func TestCtrlCInterruptsPing(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping -c 100 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Let it run a few pings
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	// Ctrl+C should interrupt and print summary
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	transcript := m.Active().Console.Transcript()
	// Should have started ping, some results, then interrupt
	if !strings.Contains(transcript, "PING 10.0.0.20") {
		t.Errorf("missing ping header:\n%s", transcript)
	}
	if !strings.Contains(transcript, "64 bytes from") {
		t.Errorf("missing ping results before interrupt:\n%s", transcript)
	}
	// Should have summary after interrupt
	if !strings.Contains(transcript, "ping statistics") {
		t.Errorf("missing ping statistics after interrupt:\n%s", transcript)
	}
	if !strings.Contains(transcript, "packets transmitted") {
		t.Errorf("missing packets transmitted in summary:\n%s", transcript)
	}
	// Should have prompt back after interrupt
	if !strings.HasSuffix(strings.TrimRight(transcript, "\n"), "pc1$ ") {
		t.Errorf("prompt missing after interrupt:\n%s", transcript)
	}
}

func TestViewRendersTabsAndStatus(t *testing.T) {
	m := newModel(t)
	v := m.View()
	for _, want := range []string{"[PC1]", " PC2 ", " PC3 ", "T=0s", "ctrl+c quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestPacketPanelShowsCapturedTraffic(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	v := m.View()
	if !strings.Contains(v, "── captured packets ──") {
		t.Errorf("packet panel missing header:\n%s", v)
	}
	if !strings.Contains(v, "ARP") {
		t.Errorf("packet panel missing ARP records:\n%s", v)
	}
	if got := m.lab.Capture.Len(); got == 0 {
		t.Error("capture empty after ping")
	}
}

// TestStatusOnOwnLine guards that the status bar is fixed at the bottom
// of the terminal (separated from console content by filler lines).
func TestStatusOnOwnLine(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	v := m.View()
	// Status bar should be the last line
	lines := strings.Split(v, "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "T=") {
		t.Errorf("status bar is not the last line:\n%s", v)
	}
	// Status bar should be separated from content by filler lines
	if !strings.Contains(v, "pc1$ ▌") {
		t.Errorf("missing prompt:\n%s", v)
	}
}

// TestNoDoublePrompt guards against the transcript's trailing prompt being
// rendered alongside the live input line's prompt.
func TestNoDoublePrompt(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("echo test")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	v := m.View()
	if strings.Contains(v, "pc1$ pc1$") {
		t.Errorf("double prompt rendered:\n%s", v)
	}
	if !strings.Contains(v, "test\npc1$ ▌") {
		t.Errorf("missing expected single-prompt output:\n%s", v)
	}
}

// TestEmptyConsoleRendersInputLine only: a fresh machine must show
// the input line at the top of the console area, with status bar at bottom.
func TestEmptyConsoleRendersInputLine(t *testing.T) {
	m := newModel(t)
	v := m.View()
	// Input line should be visible
	if !strings.Contains(v, "pc1$ ▌") {
		t.Errorf("empty console must show input line:\n%s", v)
	}
	// Status bar should be at bottom
	lines := strings.Split(v, "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "T=") {
		t.Errorf("status bar not at bottom:\n%s", v)
	}
}
