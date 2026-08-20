// Package lab builds the reference laboratory from ARCHITECTURE.md §15: two
// LAN segments bridged by a switch and a router, with four machines and a
// deterministic frame capture on every link.
//
// A Lab is a first-class topology object (ARCHITECTURE.md §11.2): it owns the
// shared clock, the devices, the machines, and the capture, and can be
// manipulated headlessly (tests, CLI) or presented in a UI. The machine and
// device packages never depend on it.
package lab

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/capture"
	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/machine"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/router"
)

// LinkDelay is the propagation delay of every segment in the reference lab.
const LinkDelay = 10 * time.Millisecond

// Lab is the §15 topology.
type Lab struct {
	Clock    *clock.VirtualClock
	Switch   *fabric.Switch
	Router   *router.Router
	Capture  *capture.Capture
	Machines []*machine.Machine

	links      []*fabric.Link
	ifaceOwner map[*fabric.Interface]*machine.Machine
}

// New15 builds the reference topology with the clock anchored at the Unix
// epoch (the `date` command prints 1970-01-01T00:00:00Z at t=0). Use New15At
// to anchor the simulation at a specific start timestamp.
func New15() (*Lab, error) {
	return New15At(time.Unix(0, 0))
}

// New15At builds the reference topology with the virtual clock anchored at the
// given start timestamp. Elapsed virtual time advances it: `date` reads
// start.Add(clock.Now()). The anchor is normalized to UTC so snapshots and
// digests stay deterministic.
//
//	PC1 10.0.0.10 ─┐
//	               ├─SW─ R1 eth0 10.0.0.1 ── R1 eth1 10.0.1.1 ── PC3 10.0.1.10
//	PC2 10.0.0.20 ─┘
//
// Every link is tapped into the lab's capture. The capture retains every
// traversal.
func New15At(start time.Time) (*Lab, error) {
	l := &Lab{
		Clock:      clock.NewAt(start),
		Capture:    capture.New(0),
		ifaceOwner: make(map[*fabric.Interface]*machine.Machine),
	}
	sw := fabric.NewSwitch(l.Clock, 0)
	r := router.New(l.Clock, 0)
	l.Switch, l.Router = sw, r

	pc1, err := l.addMachine("pc1", "10.0.0.10/24", "02:00:00:00:00:01")
	if err != nil {
		return nil, err
	}
	pc2, err := l.addMachine("pc2", "10.0.0.20/24", "02:00:00:00:00:02")
	if err != nil {
		return nil, err
	}
	pc3, err := l.addMachine("pc3", "10.0.1.10/24", "02:00:00:00:00:03")
	if err != nil {
		return nil, err
	}

	// LAN-A: PC1, PC2, and R1 eth0 behind the switch.
	if err := l.attachToSwitch(pc1, "p1"); err != nil {
		return nil, err
	}
	if err := l.attachToSwitch(pc2, "p2"); err != nil {
		return nil, err
	}
	rEth0 := fabric.NewInterface("eth0", mustMAC("02:00:00:00:00:0a"))
	if err := l.attachToSwitchIface("p3", rEth0); err != nil {
		return nil, err
	}
	if err := r.AddInterface("eth0", rEth0, netip.MustParsePrefix("10.0.0.1/24")); err != nil {
		return nil, fmt.Errorf("router eth0: %w", err)
	}

	// LAN-B: R1 eth1 point-to-point to PC3.
	rEth1 := fabric.NewInterface("eth1", mustMAC("02:00:00:00:00:0b"))
	if err := r.AddInterface("eth1", rEth1, netip.MustParsePrefix("10.0.1.1/24")); err != nil {
		return nil, fmt.Errorf("router eth1: %w", err)
	}
	pc3Iface := pc3.Stack.Iface()
	link, err := fabric.NewLink(l.Clock, rEth1, pc3Iface, LinkDelay)
	if err != nil {
		return nil, err
	}
	l.link(link)

	// Gateways: the only way out of each LAN is through R1.
	if err := pc1.Stack.AddRoute(netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("10.0.0.1"), "eth0", 0); err != nil {
		return nil, fmt.Errorf("pc1 default route: %w", err)
	}
	if err := pc3.Stack.AddRoute(netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("10.0.1.1"), "eth0", 0); err != nil {
		return nil, fmt.Errorf("pc3 default route: %w", err)
	}

	return l, nil
}

// Machine returns the machine with the given name, or nil.
func (l *Lab) Machine(name string) *machine.Machine {
	for _, m := range l.Machines {
		if m.Hostname == name {
			return m
		}
	}
	return nil
}

// Label returns a display name for an interface, e.g. "pc1.eth0" for a
// machine interface or "r1.eth0" for a router interface. The UI and the
// packet panel use it to render capture records.
func (l *Lab) Label(iface *fabric.Interface) string {
	if m, ok := l.ifaceOwner[iface]; ok {
		return m.Hostname + "." + iface.Name
	}
	for _, pi := range l.Router.Interfaces() {
		if pi.MAC == iface.MAC {
			return "r1." + iface.Name
		}
	}
	return iface.Name
}

// RunQuiescent advances virtual time until no machine has a pending process
// wakeup, stepping every machine as time passes. It is the UI's "run" action:
// background processes (e.g. nc -l) progress deterministically without host
// timers.
func (l *Lab) RunQuiescent() {
	for {
		var earliest time.Duration
		for _, m := range l.Machines {
			if w := m.WakeupAt(); w != 0 && (earliest == 0 || w < earliest) {
				earliest = w
			}
		}
		if earliest == 0 {
			return
		}
		if err := l.Clock.AdvanceTo(earliest); err != nil {
			return
		}
		for _, m := range l.Machines {
			m.Step()
		}
	}
}

func (l *Lab) addMachine(name, prefix, mac string) (*machine.Machine, error) {
	iface := fabric.NewInterface("eth0", mustMAC(mac))
	m, err := machine.New(name, name, l.Clock, iface, netstack.Config{Addr: netip.MustParsePrefix(prefix)})
	if err != nil {
		return nil, fmt.Errorf("machine %s: %w", name, err)
	}
	l.Machines = append(l.Machines, m)
	l.ifaceOwner[iface] = m
	return m, nil
}

// attachToSwitch links one of the lab's machines to a switch port.
func (l *Lab) attachToSwitch(m *machine.Machine, portName string) error {
	return l.attachToSwitchIface(portName, m.Stack.Iface())
}

func (l *Lab) attachToSwitchIface(portName string, iface *fabric.Interface) error {
	port := fabric.NewInterface(portName, mustMAC("02:00:00:00:00:00"))
	link, err := fabric.NewLink(l.Clock, iface, port, LinkDelay)
	if err != nil {
		return err
	}
	l.link(link)
	return l.Switch.AddPort(portName, port)
}

// link wires a new link into the lab's capture.
func (l *Lab) link(link *fabric.Link) {
	link.AddTap(l.Capture.Tap())
	l.links = append(l.links, link)
}

// mustMAC parses a MAC address or panics (configuration error).
func mustMAC(s string) ethernet.MAC {
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		panic(fmt.Sprintf("lab: bad MAC %q: %v", s, err))
	}
	return m
}

// NewWithMachines creates a Lab with the given clock and machines.
// Unlike New15/New15At, it does not add any links or routes.
// The caller is responsible for wiring machines, switches, routers, and capture.
func NewWithMachines(c *clock.VirtualClock, machines []*machine.Machine) (*Lab, error) {
	l := &Lab{
		Clock:      c,
		Capture:    capture.New(0),
		Machines:   machines,
		ifaceOwner: make(map[*fabric.Interface]*machine.Machine),
	}
	for _, m := range machines {
		l.ifaceOwner[m.Stack.Iface()] = m
	}
	return l, nil
}
