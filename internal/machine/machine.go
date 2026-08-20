package machine

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
)

// Machine is a virtual machine (ARCHITECTURE.md §7.2): identity, hostname,
// network stack, filesystem, processes, and console. It is a plain object
// with no OS process, thread, kernel, or host network interface behind it.
type Machine struct {
	ID           string
	Hostname     string
	Stack        *netstack.Stack
	FS           *FS
	Console      *Console
	clock        *clock.VirtualClock
	procs        map[int]*Process
	nextPID      int
	lastExitCode int // $? - exit code of last foreground command
}

// New builds a machine around a fresh network stack on iface.
func New(id, hostname string, c *clock.VirtualClock, iface *fabric.Interface, cfg netstack.Config) (*Machine, error) {
	st, err := netstack.New(c, iface, cfg)
	if err != nil {
		return nil, err
	}
	m := &Machine{
		ID:       id,
		Hostname: hostname,
		Stack:    st,
		FS:       NewFS(),
		Console:  NewConsole(hostname + "$ "),
		clock:    c,
		procs:    make(map[int]*Process),
		nextPID:  2, // pid 1 is reserved for the shell
	}
	_ = m.FS.WriteFile("/etc/hostname", []byte(hostname+"\n"))
	// pid 1: the interactive shell, the machine's only always-running process.
	m.procs[1] = &Process{
		Pid: 1, Name: "sh", State: Running,
		Stdin: &bytes.Buffer{}, Stdout: NewBoundedBuffer(MaxTranscriptBytes), Stderr: NewBoundedBuffer(MaxTranscriptBytes),
		m: m,
	}
	return m, nil
}

// Step drives the machine's non-shell processes that are waiting. Each waiting
// process gets one step; the process itself decides whether its data arrived or
// its wakeup deadline passed, and either progresses or parks again. This is the
// lazy trigger the UI uses after advancing virtual time (ARCHITECTURE.md §5.4).
func (m *Machine) Step() {
	for _, pid := range sortedPID(m.procs) {
		p := m.procs[pid]
		if p == nil || p == m.shell() || p.State != Waiting || p.step == nil {
			continue
		}
		p.State = Running
		p.step(m, p)
	}
	m.reap()
}

// RunCommand executes one shell command line and returns its stdout. The
// command runs synchronously, advancing virtual time as frames cross links.
func (m *Machine) RunCommand(line string) (string, error) {
	out := m.execute(line)
	if out == nil {
		return "", nil
	}
	if out.State == Waiting && out.Foreground {
		m.RunForegroundToCompletion(out)
	}
	return out.Stdout.String(), cmdErr(out)
}

// HandleInput feeds a command line to the console: it echoes the prompt and
// command, then executes and drives foreground processes to completion,
// appending stdout/stderr to the transcript.
func (m *Machine) HandleInput(line string) {
	m.Console.WritePrompt()
	m.Console.Write(line + "\n")
	p := m.execute(line)
	if p != nil {
		if p.State == Waiting && p.Foreground {
			m.RunForegroundToCompletion(p)
		}
		if s := p.Stdout.String(); s != "" {
			m.Console.Write(s)
		}
		if s := p.Stderr.String(); s != "" {
			m.Console.Write(s)
		}
	}
	m.Console.WritePrompt()
}

// Processes returns the registered processes sorted by pid.
func (m *Machine) Processes() []*Process {
	out := make([]*Process, 0, len(m.procs))
	for _, pid := range sortedPID(m.procs) {
		if p := m.procs[pid]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

// WakeupAt returns the earliest pending process wakeup deadline (0 when none).
// The UI advances virtual time to this before stepping the machine.
func (m *Machine) WakeupAt() time.Duration {
	var earliest time.Duration
	for _, p := range m.Processes() {
		if p.WakeupAt != 0 && (earliest == 0 || p.WakeupAt < earliest) {
			earliest = p.WakeupAt
		}
	}
	return earliest
}

// commandCost is the simulated CPU cost of executing one shell command:
// every command advances the virtual clock by this much before it runs, so
// even a command that sends no traffic consumes virtual time (ARCHITECTURE.md
// §7.7). Networking costs are added on top as frames cross links.
const commandCost = time.Millisecond

// execute parses a command line and runs it. Foreground commands run to
// completion synchronously; background commands (e.g. nc -l) register as
// waiting processes and are stepped by Step. Executing a command advances the
// virtual clock by commandCost, then the command's own temporal costs.
func (m *Machine) execute(line string) *Process {
	argv := tokenize(line)
	if len(argv) == 0 {
		return nil
	}
	m.clock.AdvanceBy(commandCost) // CPU cost of running the command
	name := argv[0]
	app, ok := apps[name]
	if !ok {
		p := newProcess(m, name, argv[1:])
		p.writeErr(fmt.Sprintf("%s: command not found\n", name))
		p.exit(127)
		return p
	}
	p := app(m, argv[1:])
	if p.State == Waiting {
		m.procs[p.Pid] = p
	}
	return p
}

// Execute runs a command line without driving foreground processes to
// completion. Used by the UI for incremental rendering. Returns the
// process (which may be Waiting with a step hook).
func (m *Machine) Execute(line string) *Process {
	return m.execute(line)
}

// RunForegroundToCompletion drives a foreground waiting process to exit.
// Used by RunCommand for sync behavior (tests, CLI).
func (m *Machine) RunForegroundToCompletion(p *Process) {
	for p.State == Waiting && p.step != nil {
		p.State = Running
		p.step(m, p)
	}
	m.lastExitCode = p.ExitCode
	m.reap()
}

// StepForeground steps all foreground waiting processes once.
// Returns true if any foreground process was stepped (UI can re-render).
// Does NOT reap; caller (CopyForegroundOutput) reaps after copying output.
func (m *Machine) StepForeground() bool {
	stepped := false
	for _, pid := range sortedPID(m.procs) {
		p := m.procs[pid]
		if p == nil || p == m.shell() || p.State != Waiting || p.step == nil || !p.Foreground {
			continue
		}
		p.State = Running
		p.step(m, p)
		stepped = true
	}
	return stepped
}

// InterruptForeground terminates any running foreground process (e.g. ping)
// by calling its interrupt hook (which can print statistics) and copying output.
func (m *Machine) InterruptForeground() {
	for _, pid := range sortedPID(m.procs) {
		p := m.procs[pid]
		if p == nil || p == m.shell() || p.step == nil || !p.Foreground {
			continue
		}
		if p.State == Waiting || p.State == Running {
			if p.interrupt != nil {
				p.interrupt(m, p) // let process print summary
			}
			p.exit(130) // SIGINT
		}
	}
	m.CopyForegroundOutput()
}

// CopyForegroundOutput copies stdout/stderr from foreground waiting
// processes to the machine's console for rendering. If a foreground
// process has exited, writes the shell prompt. Reaps exited processes.
func (m *Machine) CopyForegroundOutput() {
	for _, p := range m.procs {
		if p == nil || p.Pid == 1 || p.step == nil || !p.Foreground {
			continue
		}
		if p.State == Exited {
			// Process finished: copy any remaining output, then write prompt.
			if s := p.Stdout.String(); s != "" {
				m.Console.Write(s)
				p.Stdout.Reset()
			}
			if s := p.Stderr.String(); s != "" {
				m.Console.Write(s)
				p.Stderr.Reset()
			}
			m.lastExitCode = p.ExitCode
			m.Console.WritePrompt()
			continue
		}
		if p.State != Waiting {
			continue
		}
		if s := p.Stdout.String(); s != "" {
			m.Console.Write(s)
			p.Stdout.Reset()
		}
		if s := p.Stderr.String(); s != "" {
			m.Console.Write(s)
			p.Stderr.Reset()
		}
	}
	// Reap exited foreground processes.
	m.reap()
}

// HasForeground reports whether this machine has any foreground
// processes (waiting or running) that need continued stepping.
func (m *Machine) HasForeground() bool {
	for _, p := range m.procs {
		if p == nil || p.Pid == 1 || p.step == nil || !p.Foreground {
			continue
		}
		if p.State == Waiting || p.State == Running {
			return true
		}
	}
	return false
}

func (m *Machine) shell() *Process {
	return m.procs[1]
}

func (m *Machine) reap() {
	for _, pid := range sortedPID(m.procs) {
		p := m.procs[pid]
		if p != nil && p != m.shell() && p.State == Exited {
			delete(m.procs, pid)
		}
	}
}

func sortedPID(m map[int]*Process) []int {
	out := make([]int, 0, len(m))
	for pid := range m {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

func tokenize(line string) []string {
	return strings.Fields(line)
}

func cmdErr(p *Process) error {
	if p.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", p.Name, strings.TrimSpace(p.Stderr.String()))
}
