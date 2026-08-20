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
	return New(l)
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
	if !strings.Contains(transcript, "64 bytes from 10.0.0.20: icmp_seq=1 ttl=64 time=90.000 ms") {
		t.Errorf("transcript missing ping success:\n%s", transcript)
	}
	if got := m.lab.Clock.Now(); got != 91*time.Millisecond {
		t.Errorf("clock after ping = %v, want 91ms", got)
	}
	if got := m.Input(); got != "" {
		t.Errorf("input not cleared after submit: %q", got)
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
		{"ctrl+c", tea.KeyCtrlC},
		{"esc", tea.KeyEsc},
		{"ctrl+q", tea.KeyCtrlQ},
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

// TestStatusOnOwnLine guards against the status bar being glued to the input
// line: the body and the status bar must be separate lines.
func TestStatusOnOwnLine(t *testing.T) {
	m := newModel(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ping 10.0.0.20")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	v := m.View()
	if !strings.Contains(v, "pc1$ ▌\nT=") {
		t.Errorf("status bar not on its own line:\n%s", v)
	}
	lines := strings.Split(v, "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "T=") {
		t.Errorf("status bar is not the last line:\n%s", v)
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

// TestEmptyConsoleRendersInputLine only: a fresh machine must not show a
// stray blank line above the prompt.
func TestEmptyConsoleRendersInputLine(t *testing.T) {
	m := newModel(t)
	v := m.View()
	if !strings.Contains(v, "pc1$ ▌\nT=") {
		t.Errorf("empty console must go straight to the input line:\n%s", v)
	}
	if strings.Contains(v, "\n\n") {
		t.Errorf("empty console shows a stray blank line:\n%s", v)
	}
}
