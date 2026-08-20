package fabric

import (
	"fmt"
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// DefaultSwitchAging is how long the MAC forwarding table keeps an entry,
// in virtual time.
const DefaultSwitchAging = 5 * time.Minute

// Switch is a learning L2 switch (ARCHITECTURE.md §9.1): it learns which MAC
// address is reachable on which port, forwards unicast frames to the exact
// port, and floods broadcasts and unknown destinations to every other port.
//
// Forwarding is synchronous: a frame received on one port is re-transmitted
// out the target port inside the same ingress Send call, so a full end-to-end
// exchange still happens within one host Send. Switching adds no virtual delay;
// only link traversal advances the clock.
type Switch struct {
	clock   *clock.VirtualClock
	ports   map[string]*Interface
	macPort map[ethernet.MAC]string
	macExp  map[ethernet.MAC]time.Duration
	aging   time.Duration
}

// NewSwitch returns a switch with the given MAC-table aging. An aging of zero
// uses DefaultSwitchAging.
func NewSwitch(c *clock.VirtualClock, aging time.Duration) *Switch {
	if aging == 0 {
		aging = DefaultSwitchAging
	}
	return &Switch{
		clock:   c,
		ports:   make(map[string]*Interface),
		macPort: make(map[ethernet.MAC]string),
		macExp:  make(map[ethernet.MAC]time.Duration),
		aging:   aging,
	}
}

// AddPort registers a switch port and wires it to the switch. The port
// interface must already be linked to the segment it serves.
func (sw *Switch) AddPort(name string, iface *Interface) error {
	if iface == nil {
		return fmt.Errorf("fabric: switch port %q has no interface", name)
	}
	if _, dup := sw.ports[name]; dup {
		return fmt.Errorf("fabric: switch port %q already registered", name)
	}
	sw.ports[name] = iface
	iface.Attach(portSink{sw: sw, name: name})
	return nil
}

// Ports returns the registered port names, sorted. Deterministic: callers may
// iterate the result in order without depending on Go map order.
func (sw *Switch) Ports() []string {
	return sw.sortedPorts()
}

// ForwardingTable returns the learned MAC→port entries that have not yet aged
// out, sorted by MAC. Deterministic for tests and the UI.
func (sw *Switch) ForwardingTable() []macEntry {
	macs := make([]ethernet.MAC, 0, len(sw.macPort))
	for mac := range sw.macPort {
		macs = append(macs, mac)
	}
	sort.Slice(macs, func(i, j int) bool {
		return string(macs[i][:]) < string(macs[j][:])
	})
	var out []macEntry
	for _, mac := range macs {
		if port, ok := sw.lookup(mac); ok {
			out = append(out, macEntry{MAC: mac, Port: port})
		}
	}
	return out
}

// macEntry is one row of the forwarding table.
type macEntry struct {
	MAC  ethernet.MAC
	Port string
}

// receive is the ingress path: learn the source, then forward or flood.
func (sw *Switch) receive(ingress string, f ethernet.Frame) error {
	sw.learn(f.Src, ingress)
	if f.Dst.IsBroadcast() {
		sw.flood(ingress, f)
		return nil
	}
	if port, ok := sw.lookup(f.Dst); ok {
		sw.forward(port, f)
		return nil
	}
	sw.flood(ingress, f)
	return nil
}

// learn records that mac is reachable on port, with a lazy expiry in virtual
// time (no timer; the entry is dropped on the next lookup after expiry).
func (sw *Switch) learn(mac ethernet.MAC, port string) {
	if mac.IsZero() {
		return
	}
	now := sw.clock.Now()
	sw.macPort[mac] = port
	sw.macExp[mac] = now + sw.aging
}

// lookup returns the port for mac, honoring expiry. An expired entry is
// dropped lazily and reported as unknown.
func (sw *Switch) lookup(mac ethernet.MAC) (string, bool) {
	exp, ok := sw.macExp[mac]
	if !ok {
		return "", false
	}
	if sw.clock.Now() >= exp {
		delete(sw.macPort, mac)
		delete(sw.macExp, mac)
		return "", false
	}
	return sw.macPort[mac], true
}

// flood delivers f to every port except the ingress. Ports that fail to
// transmit (down, unlinked) are skipped silently, matching switch behavior.
func (sw *Switch) flood(ingress string, f ethernet.Frame) {
	for _, name := range sw.sortedPorts() {
		if name == ingress {
			continue
		}
		if p, ok := sw.ports[name]; ok {
			_ = p.Send(f)
		}
	}
}

// forward delivers f out of the given port. Transmit errors are dropped
// silently.
func (sw *Switch) forward(port string, f ethernet.Frame) {
	if p, ok := sw.ports[port]; ok {
		_ = p.Send(f)
	}
}

func (sw *Switch) sortedPorts() []string {
	names := make([]string, 0, len(sw.ports))
	for name := range sw.ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// portSink routes frames arriving on one switch port to the switch with the
// ingress port name.
type portSink struct {
	sw   *Switch
	name string
}

// ReceiveFrame implements fabric.FrameSink.
func (p portSink) ReceiveFrame(f ethernet.Frame) error {
	return p.sw.receive(p.name, f)
}
