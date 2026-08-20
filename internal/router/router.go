// Package router implements the multi-interface IPv4 router (ARCHITECTURE.md
// §9.8): a network device that forwards packets between its interfaces, honors
// TTL, and answers ARP and ICMP for its own addresses. Forwarding is fully
// synchronous — the router re-transmits inside the ingress Send call — and
// reuses the shared protocol layers (arp, ipv4, icmp, udp, tcp, route).
//
// The router is a device, not a host: it carries no sockets and runs no
// applications. Traffic to its own addresses is answered minimally (echo
// reply, TCP RST on SYN, ICMP port unreachable for UDP).
package router

import (
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/netstack/icmp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/netstack/route"
	"github.com/yanchenko-igor/virtnet/internal/netstack/tcp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/udp"
)

// Port is one router interface: its link-layer identity, its IPv4 address,
// and its own ARP cache. ARP is per-interface because each segment resolves
// addresses independently.
type Port struct {
	iface *fabric.Interface
	addr  netip.Prefix
	arp   *arp.Cache
}

// Router is a multi-interface IPv4 forwarding device.
type Router struct {
	clock      *clock.VirtualClock
	ports      map[string]*Port
	routes     *route.Table
	arpTimeout time.Duration
}

// New returns a router with no interfaces. An arpTimeout of zero uses the
// netstack default.
func New(c *clock.VirtualClock, arpTimeout time.Duration) *Router {
	if arpTimeout == 0 {
		arpTimeout = netstack.DefaultARPTimeout
	}
	return &Router{
		clock:      c,
		ports:      make(map[string]*Port),
		routes:     route.NewTable(),
		arpTimeout: arpTimeout,
	}
}

// AddInterface configures a router interface with its IPv4 prefix and installs
// the directly-connected route. The interface is wired as the frame sink for
// that segment.
func (r *Router) AddInterface(name string, iface *fabric.Interface, pfx netip.Prefix) error {
	if iface == nil {
		return fmt.Errorf("router: interface %q has no fabric interface", name)
	}
	if !pfx.IsValid() || !pfx.Addr().Is4() {
		return fmt.Errorf("router: interface %q needs an IPv4 prefix, got %v", name, pfx)
	}
	if _, dup := r.ports[name]; dup {
		return fmt.Errorf("router: interface %q already configured", name)
	}
	p := &Port{iface: iface, addr: pfx, arp: arp.NewCache(r.arpTimeout)}
	r.ports[name] = p
	iface.Attach(portSink{r: r, name: name})
	return r.routes.Add(route.Route{Prefix: pfx.Masked(), Interface: name})
}

// PortInfo describes one router interface for inspection.
type PortInfo struct {
	Name string
	Addr netip.Addr
	MAC  ethernet.MAC
}

// Interfaces returns the configured interfaces, sorted by name.
func (r *Router) Interfaces() []PortInfo {
	names := make([]string, 0, len(r.ports))
	for name := range r.ports {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]PortInfo, 0, len(names))
	for _, name := range names {
		p := r.ports[name]
		out = append(out, PortInfo{Name: name, Addr: p.addr.Addr(), MAC: p.iface.MAC})
	}
	return out
}

// ARPEntries returns the non-expired ARP cache of one interface, sorted by IP.
func (r *Router) ARPEntries(port string) []arp.KeyedEntry {
	if p, ok := r.ports[port]; ok {
		return p.arp.Entries(r.clock.Now())
	}
	return nil
}

// Routes returns the routing table entries.
func (r *Router) Routes() []route.Route {
	return r.routes.Routes()
}

// receive is the ingress path for one interface.
func (r *Router) receive(name string, f ethernet.Frame) error {
	port, ok := r.ports[name]
	if !ok {
		return nil
	}
	if !f.Dst.IsBroadcast() && f.Dst != port.iface.MAC {
		return nil
	}
	switch f.Type {
	case ethernet.EtherTypeARP:
		return r.handleARP(port, f)
	case ethernet.EtherTypeIPv4:
		return r.handleIPv4(port, f)
	default:
		return nil
	}
}

func (r *Router) handleARP(port *Port, f ethernet.Frame) error {
	m, err := arp.Unmarshal(f.Payload)
	if err != nil {
		return nil
	}
	switch m.Op {
	case arp.OpRequest:
		// Learn the sender regardless of whether the request targets us.
		port.arp.Put(m.SenderIP, m.SenderMAC, r.clock.Now())
		if m.TargetIP != port.addr.Addr() {
			return nil
		}
		reply := arp.Message{
			Op:        arp.OpReply,
			SenderMAC: port.iface.MAC,
			SenderIP:  port.addr.Addr(),
			TargetMAC: m.SenderMAC,
			TargetIP:  m.SenderIP,
		}
		frame := ethernet.Frame{Dst: m.SenderMAC, Src: port.iface.MAC, Type: ethernet.EtherTypeARP, Payload: reply.Marshal()}
		return port.iface.Send(frame)
	case arp.OpReply:
		port.arp.Put(m.SenderIP, m.SenderMAC, r.clock.Now())
		return nil
	default:
		return nil
	}
}

func (r *Router) handleIPv4(port *Port, f ethernet.Frame) error {
	pkt, err := ipv4.Unmarshal(f.Payload)
	if err != nil {
		return nil
	}
	// Passive learning: any received IPv4 frame reveals its sender on this
	// segment.
	if !pkt.Src.IsUnspecified() {
		port.arp.Put(pkt.Src, f.Src, r.clock.Now())
	}
	if r.isOwnAddr(pkt.Dst) {
		if pkt.Dst == port.addr.Addr() {
			return r.handleLocal(port, f, pkt)
		}
		return nil // another interface's address on the wrong segment: drop
	}
	return r.forward(port, f, pkt)
}

// handleLocal answers traffic addressed to the router itself.
func (r *Router) handleLocal(port *Port, f ethernet.Frame, pkt ipv4.Packet) error {
	switch pkt.Protocol {
	case ipv4.ProtoICMP:
		m, err := icmp.Unmarshal(pkt.Payload)
		if err != nil {
			return nil
		}
		if m.Type != icmp.TypeEchoRequest {
			return nil
		}
		id, _ := m.EchoID()
		seq, _ := m.EchoSeq()
		data, _ := m.EchoData()
		return r.sendICMP(port, pkt.Src, icmp.NewEchoReply(id, seq, data))
	case ipv4.ProtoTCP:
		seg, err := tcp.Unmarshal(pkt.Payload, pkt.Src, pkt.Dst)
		if err != nil {
			return nil
		}
		if seg.Has(tcp.FlagSYN) && !seg.Has(tcp.FlagACK) {
			// Closed port: refuse with RST, matching the host stacks.
			rst := tcp.Segment{
				SrcPort: seg.DstPort,
				DstPort: seg.SrcPort,
				Seq:     seg.Ack,
				Ack:     seg.Seq + 1,
				Flags:   tcp.FlagRST | tcp.FlagACK,
			}
			return r.sendIP(port, pkt.Src, ipv4.ProtoTCP, rst.Marshal(port.addr.Addr(), pkt.Src))
		}
		return nil
	case ipv4.ProtoUDP:
		if _, err := udp.Unmarshal(pkt.Payload, pkt.Src, pkt.Dst); err != nil {
			return nil
		}
		// Closed port: ICMP port unreachable.
		return r.sendICMP(port, pkt.Src, icmp.Message{
			Type:    icmp.TypeDestUnreach,
			Code:    3,
			Payload: icmpErrPayload(f.Payload),
		})
	default:
		return nil
	}
}

// forward routes transit traffic: TTL check, decrement, lookup, ARP, transmit.
func (r *Router) forward(port *Port, f ethernet.Frame, pkt ipv4.Packet) error {
	if pkt.TTL <= 1 {
		return r.sendICMP(port, pkt.Src, icmp.Message{
			Type:    icmp.TypeTimeExceeded,
			Code:    0, // TTL expired in transit
			Payload: icmpErrPayload(f.Payload),
		})
	}
	pkt.TTL--
	rte, ok := r.routes.Lookup(pkt.Dst)
	if !ok {
		return r.sendICMP(port, pkt.Src, icmp.Message{
			Type:    icmp.TypeDestUnreach,
			Code:    0, // network unreachable
			Payload: icmpErrPayload(f.Payload),
		})
	}
	egress, ok := r.ports[rte.Interface]
	if !ok {
		return nil
	}
	nextHop := pkt.Dst
	if rte.NextHop.IsValid() {
		nextHop = rte.NextHop
	}
	mac, err := r.resolveMAC(egress, nextHop)
	if err != nil {
		return nil // ARP failed: drop silently
	}
	frame := ethernet.Frame{Dst: mac, Src: egress.iface.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt.Marshal()}
	return egress.iface.Send(frame)
}

// sendICMP sends an ICMP message from one of the router's addresses.
func (r *Router) sendICMP(port *Port, dst netip.Addr, m icmp.Message) error {
	return r.sendIP(port, dst, ipv4.ProtoICMP, m.Marshal())
}

// sendIP transmits an IPv4 packet out of port, resolving dst with ARP
// synchronously.
func (r *Router) sendIP(port *Port, dst netip.Addr, proto ipv4.Protocol, payload []byte) error {
	mac, err := r.resolveMAC(port, dst)
	if err != nil {
		return err
	}
	pkt := ipv4.Packet{Src: port.addr.Addr(), Dst: dst, TTL: 64, Protocol: proto, Payload: payload}
	frame := ethernet.Frame{Dst: mac, Src: port.iface.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt.Marshal()}
	return port.iface.Send(frame)
}

// resolveMAC returns the MAC for ip on port's segment, sending an ARP request
// if needed. The peer's reply is delivered within this same call.
func (r *Router) resolveMAC(port *Port, ip netip.Addr) (ethernet.MAC, error) {
	if mac, ok := port.arp.Get(ip, r.clock.Now()); ok {
		return mac, nil
	}
	req := arp.Message{
		Op:        arp.OpRequest,
		SenderMAC: port.iface.MAC,
		SenderIP:  port.addr.Addr(),
		TargetIP:  ip,
	}
	frame := ethernet.Frame{Dst: ethernet.BroadcastMAC, Src: port.iface.MAC, Type: ethernet.EtherTypeARP, Payload: req.Marshal()}
	if err := port.iface.Send(frame); err != nil {
		return ethernet.MAC{}, err
	}
	if mac, ok := port.arp.Get(ip, r.clock.Now()); ok {
		return mac, nil
	}
	return ethernet.MAC{}, fmt.Errorf("router: no ARP reply from %s", ip)
}

// isOwnAddr reports whether addr belongs to any router interface.
func (r *Router) isOwnAddr(addr netip.Addr) bool {
	for _, p := range r.ports {
		if p.addr.Addr() == addr {
			return true
		}
	}
	return false
}

// icmpErrPayload returns the original datagram prefix embedded in ICMP error
// messages: the full IP header plus the first 8 bytes of the transport header
// (RFC 792). The original header bytes are preserved verbatim, checksum and
// all.
func icmpErrPayload(wire []byte) []byte {
	n := ipv4.HeaderLen + 8
	if len(wire) < n {
		n = len(wire)
	}
	return append([]byte(nil), wire[:n]...)
}

// portSink routes frames arriving on one router interface to the router with
// the interface name.
type portSink struct {
	r    *Router
	name string
}

// ReceiveFrame implements fabric.FrameSink.
func (p portSink) ReceiveFrame(f ethernet.Frame) error {
	return p.r.receive(p.name, f)
}
