package machine

import (
	"bytes"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/services"
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
	lastExitCode int                         // $? - exit code of last foreground command
	services     map[uint16]services.Service // registered services by port
	dnsServers   []netip.Addr                // DNS server addresses
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
		services: make(map[uint16]services.Service),
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

// RegisterService registers a service on this machine. The service will handle
// incoming packets on its registered ports.
func (m *Machine) RegisterService(name string, config map[string]interface{}) error {
	factory, ok := services.GetFactory(name)
	if !ok {
		return fmt.Errorf("unknown service: %s", name)
	}
	svc := factory(config)
	// Inject filesystem for HTTP service
	if httpSvc, ok := svc.(interface{ SetFS(services.FS) }); ok {
		httpSvc.SetFS(m.FS)
	}
	for _, sp := range svc.Ports() {
		m.services[sp.Port] = svc
		m.Stack.RegisterService(sp.Port, ipv4.Protocol(sp.Proto), svc)
		// For TCP services, create a listener to accept connections
		if sp.Proto == uint8(ipv4.ProtoTCP) {
			_, err := m.Stack.Listen(sp.Port)
			if err != nil {
				return fmt.Errorf("failed to create listener for service %s on port %d: %w", name, sp.Port, err)
			}
		}
	}
	return nil
}



// SetDNSServer adds a DNS server address to the machine's DNS server list.

// SetDNSServer adds a DNS server address to the machine's DNS server list.
func (m *Machine) SetDNSServer(ipStr string) error {
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return fmt.Errorf("invalid DNS server IP %q: %w", ipStr, err)
	}
	m.dnsServers = append(m.dnsServers, addr)
	return nil
}

// resolveHost resolves a hostname to an IP address using the configured DNS servers
// First checks local DNS service if available, then tries configured DNS servers
func (m *Machine) resolveHost(name string) (netip.Addr, error) {
	// Try parsing as IP first
	if addr, err := netip.ParseAddr(name); err == nil {
		return addr, nil
	}

	// First, check if there's a local DNS service running on port 53
	if dnsSvc, ok := m.services[53]; ok {
		if dnsSvc, ok := dnsSvc.(*services.DNSService); ok {
			recs := dnsSvc.ResolveLocal(name, services.TypeA)
			if len(recs) > 0 {
				if len(recs[0].Data) == 4 {
					return netip.AddrFrom4([4]byte{recs[0].Data[0], recs[0].Data[1], recs[0].Data[2], recs[0].Data[3]}), nil
				}
			}
		}
	}

	// If no local DNS service or no answer, try configured DNS servers
	if len(m.dnsServers) == 0 {
		return netip.Addr{}, fmt.Errorf("no DNS servers configured")
	}

	// Create a DNS query message
	query := services.DNSMessage{
		ID:      uint16(m.clock.Now().Nanoseconds() & 0xFFFF),
		Flags:   0x0100, // RD=1 (recursion desired)
		QDCount: 1,
		Questions: []services.DNSQuestion{
			{Name: name, Type: services.TypeA, Class: services.ClassIN},
		},
	}

	// Try each DNS server until one responds
	for _, dnsServer := range m.dnsServers {
		// Send DNS query via UDP to DNS service
		sock, err := m.Stack.ListenUDP(0)
		if err != nil {
			continue // Try next DNS server
		}
		defer sock.Close()

		if err := sock.SendTo(dnsServer, 53, query.Pack()); err != nil {
			continue // Try next DNS server
		}

		// Wait for response
		_, _, data, ok := sock.RecvFrom()
		if !ok {
			continue // Try next DNS server
		}

		// Parse response
		msg, err := services.ParseDNSMessage(data)
		if err != nil {
			continue // Try next DNS server
		}

		// Extract A record from answer
		for _, ans := range msg.Answers {
			if ans.Type == services.TypeA {
				if len(ans.Data) == 4 {
					return netip.AddrFrom4([4]byte{ans.Data[0], ans.Data[1], ans.Data[2], ans.Data[3]}), nil
				}
			}
		}
	}

	return netip.Addr{}, fmt.Errorf("no A record found for %s", name)
}

// RequestDHCP requests an IP address via DHCP from the configured DHCP server.
// Returns the assigned IP, gateway, DNS servers, and lease time.
// NOTE: This is a placeholder - DHCP client is not yet implemented.
func (m *Machine) RequestDHCP() (netip.Addr, netip.Addr, []netip.Addr, time.Duration, error) {
	return netip.Addr{}, netip.Addr{}, nil, 0, fmt.Errorf("DHCP client not implemented")
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
