// State snapshots for checkpoint/restore (ARCHITECTURE.md §12.2). A Machine
// owns its filesystem, console transcript, and process table; its network
// stack is serialized by the netstack package and carried alongside by the
// lab's world snapshot.
package machine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
)

// MachineState is a serializable snapshot of everything a machine owns except
// its network stack.
type MachineState struct {
	ID         string
	Hostname   string
	Files      []fileSnapshot
	Transcript string
	Procs      []procState
	NextPID    int
}

// procState is one process in serializable form. Its app-specific Data is
// carried as a tagged payload so the restore path can rebuild both the value
// and the process's step hook.
type procState struct {
	Pid      int
	Name     string
	Args     []string
	State    ProcessState
	Stdout   string
	Stderr   string
	Stdin    string
	WakeupAt time.Duration
	ExitCode int
	Data     *procData
}

// procData is an app-specific process payload, tagged by kind.
type procData struct {
	Kind    string
	Payload json.RawMessage
}

// dataCodec knows how to serialize and rebuild one app's Process.Data.
type dataCodec struct {
	Kind        string
	Serialize   func(m *Machine, p *Process) (json.RawMessage, error)
	Deserialize func(m *Machine, p *Process, raw json.RawMessage) error
}

// dataCodecs registers the apps whose background processes hold state that
// must survive a checkpoint. A background nc -l is the only stateful one today.
var dataCodecs = map[string]dataCodec{
	"nc": {Kind: "tcp-listener", Serialize: ncDataSerialize, Deserialize: ncDataDeserialize},
}

// NewWithStack builds a machine around an existing network stack. Used by the
// restore path, which rebuilds the stack from its own snapshot.
func NewWithStack(id, hostname string, c *clock.VirtualClock, st *netstack.Stack) *Machine {
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
	m.procs[1] = &Process{
		Pid: 1, Name: "sh", State: Running,
		Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		m: m,
	}
	return m
}

// State captures the machine's serializable state. Processes are emitted in
// pid order; the shell is recreated by NewWithStack, so it is not included.
func (m *Machine) State() MachineState {
	st := MachineState{
		ID:         m.ID,
		Hostname:   m.Hostname,
		Files:      m.FS.Snapshot(),
		Transcript: m.Console.Transcript(),
		NextPID:    m.nextPID,
	}
	for _, p := range m.Processes() {
		if p.Pid == 1 {
			continue
		}
		ps := procState{
			Pid:      p.Pid,
			Name:     p.Name,
			Args:     p.Args,
			State:    p.State,
			Stdout:   p.Stdout.String(),
			Stderr:   p.Stderr.String(),
			Stdin:    p.Stdin.String(),
			WakeupAt: p.WakeupAt,
			ExitCode: p.ExitCode,
		}
		if p.Data != nil {
			codec, ok := dataCodecs[p.Name]
			if !ok {
				continue
			}
			raw, err := codec.Serialize(m, p)
			if err != nil {
				continue
			}
			ps.Data = &procData{Kind: codec.Kind, Payload: raw}
		}
		st.Procs = append(st.Procs, ps)
	}
	return st
}

// Restore replaces the machine's filesystem, console transcript, and process
// table with a previously captured snapshot. The network stack must already
// be rebuilt and attached.
func (m *Machine) Restore(st MachineState) error {
	m.FS = NewFS()
	for _, f := range st.Files {
		if err := m.FS.WriteFile(f.Path, f.Data); err != nil {
			return fmt.Errorf("machine %s: restore file %s: %w", m.ID, f.Path, err)
		}
	}
	m.Console = NewConsole(m.Hostname + "$ ")
	m.Console.Write(st.Transcript)
	m.nextPID = st.NextPID
	m.procs = map[int]*Process{
		1: {Pid: 1, Name: "sh", State: Running, Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, m: m},
	}
	for _, ps := range st.Procs {
		p := &Process{
			Pid:      ps.Pid,
			Name:     ps.Name,
			Args:     ps.Args,
			State:    ps.State,
			Stdin:    &bytes.Buffer{},
			Stdout:   &bytes.Buffer{},
			Stderr:   &bytes.Buffer{},
			WakeupAt: ps.WakeupAt,
			ExitCode: ps.ExitCode,
			m:        m,
		}
		_, _ = p.Stdin.WriteString(ps.Stdin)
		_, _ = p.Stdout.WriteString(ps.Stdout)
		_, _ = p.Stderr.WriteString(ps.Stderr)
		if ps.Data != nil {
			codec, ok := dataCodecs[p.Name]
			if !ok {
				return fmt.Errorf("machine %s: no data codec for %q", m.ID, p.Name)
			}
			if err := codec.Deserialize(m, p, ps.Data.Payload); err != nil {
				return fmt.Errorf("machine %s: restore %s data: %w", m.ID, p.Name, err)
			}
		}
		m.procs[ps.Pid] = p
	}
	return nil
}

// ncDataSerialize serializes a background nc listener's state: just the bound
// port, which identifies the restored listener in the rebuilt stack.
func ncDataSerialize(m *Machine, p *Process) (json.RawMessage, error) {
	l, ok := p.Data.(*netstack.TCPConn)
	if !ok {
		return nil, fmt.Errorf("machine: nc process %d data is not a TCP listener", p.Pid)
	}
	return json.Marshal(l.LocalPort())
}

// ncDataDeserialize re-attaches the restored listener to the process and
// reinstates its step hook.
func ncDataDeserialize(m *Machine, p *Process, raw json.RawMessage) error {
	var port uint16
	if err := json.Unmarshal(raw, &port); err != nil {
		return err
	}
	l := m.Stack.Listener(port)
	if l == nil {
		return fmt.Errorf("machine: no restored listener on port %d", port)
	}
	p.Data = l
	p.step = ncListenStep
	return nil
}
