// World snapshot and restore (ARCHITECTURE.md §12.2). A WorldSnapshot is a
// fully serializable image of the laboratory: the interface/link/port graph,
// every device's state, and the capture. RestoreWorld rebuilds a fresh lab
// from it — new objects, re-wired taps, re-populated capture — so a restored
// world is byte-for-byte equivalent to the one that produced the snapshot.
package lab

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/capture"
	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/machine"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/router"
)

// WorldSnapshot is a full serializable snapshot of the laboratory.
type WorldSnapshot struct {
	Clock time.Duration
	// Start is the wall-time anchor of the virtual clock (the `date` command's
	// epoch), normalized to UTC.
	Start time.Time
	// RouterARPTimeout is the router's ARP cache lifetime (new entries added
	// after restore use it).
	RouterARPTimeout time.Duration

	Ifaces      []ifaceState
	Links       []linkState
	SwitchPorts []switchPortState
	RouterPorts []routerPortState
	Machines    []machineState
	Switch      fabric.SwitchState
	RouterARP   map[string][]arp.SnapshotEntry
	Capture     []capture.SerializedRecord
}

// ifaceState is one fabric interface, identified by a stable label.
type ifaceState struct {
	ID      string
	Name    string
	MAC     ethernet.MAC
	MTU     int
	AdminUp bool
}

// linkState is one link in the graph, referencing its endpoints by iface ID.
type linkState struct {
	A, B  string
	Delay time.Duration
}

// switchPortState maps a switch port name to its interface ID.
type switchPortState struct {
	Name  string
	Iface string
}

// routerPortState maps a router port name to its interface ID and address.
type routerPortState struct {
	Name  string
	Iface string
	Addr  netip.Prefix
}

// machineState pairs a machine's own state with its network stack's state and
// the interface it is bound to.
type machineState struct {
	State machine.MachineState
	Iface string
	Stack netstack.StackState
}

// Snapshot captures the entire world in serializable form. Output order is
// deterministic: machines in creation order, device ports sorted by name,
// links in creation order, capture in chronological order.
func (l *Lab) Snapshot() WorldSnapshot {
	ws := WorldSnapshot{
		Clock:            l.Clock.Now(),
		Start:            l.Clock.Base(),
		RouterARPTimeout: l.Router.ARPTimeout(),
		Switch:           l.Switch.State(),
		RouterARP:        l.Router.ARPSnapshot(),
		Capture:          l.Capture.Snapshot(l.Label),
	}

	ifaceIDs := make(map[*fabric.Interface]string)
	emit := func(iface *fabric.Interface, id string) {
		ifaceIDs[iface] = id
		ws.Ifaces = append(ws.Ifaces, ifaceState{
			ID: id, Name: iface.Name, MAC: iface.MAC, MTU: iface.MTU, AdminUp: iface.AdminUp,
		})
	}

	for _, m := range l.Machines {
		iface := m.Stack.Iface()
		id := m.Hostname + "." + iface.Name
		emit(iface, id)
		ws.Machines = append(ws.Machines, machineState{
			State: m.State(),
			Iface: id,
			Stack: m.Stack.State(),
		})
	}

	for _, name := range sortedNames(l.Router.Interfaces()) {
		iface := l.Router.PortIface(name)
		if iface == nil {
			continue
		}
		id := "r1." + name
		emit(iface, id)
		ws.RouterPorts = append(ws.RouterPorts, routerPortState{Name: name, Iface: id, Addr: l.Router.PortAddr(name)})
	}

	for _, name := range l.Switch.Ports() {
		iface := l.Switch.Port(name)
		if iface == nil {
			continue
		}
		id := name
		emit(iface, id)
		ws.SwitchPorts = append(ws.SwitchPorts, switchPortState{Name: name, Iface: id})
	}

	for _, ln := range l.links {
		a, b := ln.Endpoints()
		ws.Links = append(ws.Links, linkState{A: ifaceIDs[a], B: ifaceIDs[b], Delay: ln.Delay()})
	}
	return ws
}

// Digest returns a deterministic hash of the full world state. Two worlds with
// equal digests are behaviorally identical.
func (l *Lab) Digest() ([32]byte, error) {
	b, err := json.Marshal(l.Snapshot())
	if err != nil {
		return [32]byte{}, fmt.Errorf("lab: marshal snapshot: %w", err)
	}
	return sha256.Sum256(b), nil
}

// RestoreWorld rebuilds a laboratory from a snapshot: fresh interfaces, links
// and taps, machines with restored stacks and processes, the switch's learning
// table, the router's ARP caches, and the re-populated capture.
func RestoreWorld(ws WorldSnapshot) (*Lab, error) {
	if ws.Clock < 0 {
		return nil, fmt.Errorf("lab: restore: negative virtual clock %v", ws.Clock)
	}
	c := clock.NewAt(ws.Start)
	c.Set(ws.Clock)
	l := &Lab{
		Clock:      c,
		Capture:    capture.New(0),
		ifaceOwner: make(map[*fabric.Interface]*machine.Machine),
	}

	ifaces := make(map[string]*fabric.Interface, len(ws.Ifaces))
	for _, is := range ws.Ifaces {
		iface := fabric.NewInterface(is.Name, is.MAC)
		iface.MTU = is.MTU
		iface.AdminUp = is.AdminUp
		ifaces[is.ID] = iface
	}

	for _, ls := range ws.Links {
		a, okA := ifaces[ls.A]
		b, okB := ifaces[ls.B]
		if !okA || !okB {
			return nil, fmt.Errorf("lab: restore: link references unknown interface (%q, %q)", ls.A, ls.B)
		}
		ln, err := fabric.NewLink(c, a, b, ls.Delay)
		if err != nil {
			return nil, err
		}
		ln.AddTap(l.Capture.Tap())
		l.links = append(l.links, ln)
	}

	for _, ms := range ws.Machines {
		iface, ok := ifaces[ms.Iface]
		if !ok {
			return nil, fmt.Errorf("lab: restore: machine %s iface %q missing", ms.State.ID, ms.Iface)
		}
		st, err := netstack.RestoreStack(c, iface, ms.Stack)
		if err != nil {
			return nil, fmt.Errorf("lab: restore: machine %s stack: %w", ms.State.ID, err)
		}
		m := machine.NewWithStack(ms.State.ID, ms.State.Hostname, c, st)
		if err := m.Restore(ms.State); err != nil {
			return nil, err
		}
		l.Machines = append(l.Machines, m)
		l.ifaceOwner[iface] = m
	}

	sw := fabric.NewSwitch(c, ws.Switch.Aging)
	for _, ps := range ws.SwitchPorts {
		iface, ok := ifaces[ps.Iface]
		if !ok {
			return nil, fmt.Errorf("lab: restore: switch port %q iface %q missing", ps.Name, ps.Iface)
		}
		if err := sw.AddPort(ps.Name, iface); err != nil {
			return nil, err
		}
	}
	sw.Restore(ws.Switch)
	l.Switch = sw

	r := router.New(c, ws.RouterARPTimeout)
	for _, ps := range ws.RouterPorts {
		iface, ok := ifaces[ps.Iface]
		if !ok {
			return nil, fmt.Errorf("lab: restore: router port %q iface %q missing", ps.Name, ps.Iface)
		}
		if err := r.AddInterface(ps.Name, iface, ps.Addr); err != nil {
			return nil, err
		}
	}
	r.RestoreARPCaches(ws.RouterARP)
	l.Router = r

	byLabel := make(map[string]*fabric.Interface, len(ifaces))
	for id, iface := range ifaces {
		byLabel[id] = iface
	}
	l.Capture.Restore(ws.Capture, func(label string) *fabric.Interface { return byLabel[label] })

	return l, nil
}

// sortedNames returns the names of the router interfaces, sorted. The router
// exposes them through its typed accessor; this keeps the lab independent of
// that slice's internal order.
func sortedNames(infos []router.PortInfo) []string {
	names := make([]string, 0, len(infos))
	for _, pi := range infos {
		names = append(names, pi.Name)
	}
	sort.Strings(names)
	return names
}
