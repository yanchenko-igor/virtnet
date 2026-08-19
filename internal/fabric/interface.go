// Package fabric implements the NetworkFabric: the L2/L3 infrastructure that
// machines plug into (ARCHITECTURE.md §6).
//
// Phase 2 covers virtual network interfaces and point-to-point links.
// Switches, routers, and multi-segment networks arrive in phase 6.
package fabric

import (
	"fmt"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// DefaultMTU is the standard Ethernet payload MTU in bytes.
const DefaultMTU = 1500

// FrameSink is the delivery endpoint of an interface: whatever object handles
// incoming frames. For a machine this is its Ethernet layer; in tests it is a
// collector.
type FrameSink interface {
	ReceiveFrame(f ethernet.Frame) error
}

// Interface is a virtual network interface attached to a machine or device
// (ARCHITECTURE.md §7.3).
//
// An interface knows its link-level identity and current administrative state.
// RX/TX queues are deferred until bandwidth modeling (phase 9): in the
// synchronous model, delivery is immediate, not queued.
type Interface struct {
	Name    string
	MAC     ethernet.MAC
	MTU     int
	AdminUp bool

	sink FrameSink
	link *Link
}

// NewInterface returns an interface in the administratively UP state with the
// standard MTU.
func NewInterface(name string, mac ethernet.MAC) *Interface {
	return &Interface{
		Name:    name,
		MAC:     mac,
		MTU:     DefaultMTU,
		AdminUp: true,
	}
}

// Attach registers the frame sink that receives frames delivered to this
// interface. It is set once, by the owning machine/device, when the interface
// is plugged into its network stack.
func (i *Interface) Attach(sink FrameSink) {
	i.sink = sink
}

// Send transmits f through the attached link. It fails loudly if the interface
// is administratively down; a link delivers to the peer in the same call,
// advancing virtual time as part of the causal chain.
func (i *Interface) Send(f ethernet.Frame) error {
	if !i.AdminUp {
		return fmt.Errorf("fabric: interface %q is down", i.Name)
	}
	if i.link == nil {
		return fmt.Errorf("fabric: interface %q has no link attached", i.Name)
	}
	return i.link.Transmit(i, f)
}

// Receive delivers an incoming frame from the link. Frames arriving on a down
// interface are dropped silently, matching real NIC behavior.
func (i *Interface) Receive(f ethernet.Frame) error {
	if !i.AdminUp {
		return nil
	}
	if i.sink == nil {
		return fmt.Errorf("fabric: interface %q has no frame sink attached", i.Name)
	}
	return i.sink.ReceiveFrame(f)
}
