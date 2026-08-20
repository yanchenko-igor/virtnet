// Package netstack ties the protocol layers into a machine's network stack
// (ARCHITECTURE.md §8). It implements fabric.FrameSink and drives the
// synchronous causal chain: a full ARP+ICMP exchange happens inside one Send.
package netstack

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/netstack/icmp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/netstack/route"
)

// DefaultARPTimeout is how long ARP cache entries live, in virtual time.
const DefaultARPTimeout = 30 * time.Second

// Config configures a NetworkStack.
type Config struct {
	Addr       netip.Prefix  // IPv4 address + prefix, e.g. 10.0.0.10/24
	ARPTimeout time.Duration // ARP cache lifetime; zero uses DefaultARPTimeout
}

// PingResult is the outcome of a Ping.
type PingResult struct {
	Reply icmp.Message
	RTT   time.Duration
}

type pendingEcho struct {
	dst   netip.Addr
	reply *icmp.Message
}

// Stack is a machine's network stack.
//
// Outbound traffic resolves the next hop with ARP and transmits synchronously;
// the peer's reply is delivered back into this stack within the same Send call.
// No timers, no event queue — virtual time is advanced by each link traversal.
type Stack struct {
	clock        *clock.VirtualClock
	iface        *fabric.Interface
	addr         netip.Prefix
	routes       *route.Table
	arp          *arp.Cache
	forward      bool
	nextEcho     uint16
	pending      map[uint16]*pendingEcho
	udpSockets   map[uint16]*UDPSocket
	tcpListeners map[uint16]*TCPConn
	tcpConns     map[tcpKey]*TCPConn
	nextPort     uint16
	nextISN      uint32
}

// New creates a stack bound to iface, attaches it as the interface's frame
// sink, and installs a directly-connected route for its subnet.
func New(c *clock.VirtualClock, iface *fabric.Interface, cfg Config) (*Stack, error) {
	if !cfg.Addr.IsValid() {
		return nil, fmt.Errorf("netstack: invalid address %v", cfg.Addr)
	}
	if !cfg.Addr.Addr().Is4() {
		return nil, fmt.Errorf("netstack: IPv4 address required, got %v", cfg.Addr)
	}
	timeout := cfg.ARPTimeout
	if timeout == 0 {
		timeout = DefaultARPTimeout
	}
	s := &Stack{
		clock:        c,
		iface:        iface,
		addr:         cfg.Addr,
		routes:       route.NewTable(),
		arp:          arp.NewCache(timeout),
		pending:      make(map[uint16]*pendingEcho),
		udpSockets:   make(map[uint16]*UDPSocket),
		tcpListeners: make(map[uint16]*TCPConn),
		tcpConns:     make(map[tcpKey]*TCPConn),
		nextPort:     firstEphemeral,
	}
	if err := s.routes.Add(route.Route{Prefix: cfg.Addr.Masked(), Interface: iface.Name}); err != nil {
		return nil, err
	}
	iface.Attach(s)
	return s, nil
}

// Addr returns the stack's IPv4 address.
func (s *Stack) Addr() netip.Addr {
	return s.addr.Addr()
}

// Prefix returns the stack's configured IPv4 prefix.
func (s *Stack) Prefix() netip.Prefix {
	return s.addr
}

// InterfaceName returns the bound interface's name.
func (s *Stack) InterfaceName() string {
	return s.iface.Name
}

// MAC returns the bound interface's MAC address.
func (s *Stack) MAC() ethernet.MAC {
	return s.iface.MAC
}

// Iface returns the bound fabric interface. The lab and UI use it to map
// captured frames back to machines; the stack itself never depends on them.
func (s *Stack) Iface() *fabric.Interface {
	return s.iface
}

// ARPEntries returns the stack's current ARP cache (expired entries removed),
// sorted by IP.
func (s *Stack) ARPEntries() []arp.KeyedEntry {
	return s.arp.Entries(s.clock.Now())
}

// Routes returns a copy of the routing table entries.
func (s *Stack) Routes() []route.Route {
	return s.routes.Routes()
}

// EnableForwarding turns IP forwarding on (used by routers, phase 6).
func (s *Stack) EnableForwarding() {
	s.forward = true
}

// AddRoute installs a static route (gateways, phase 6). nextHop may be a zero
// address for directly-connected networks. Routes are matched by
// longest-prefix, then lowest metric, then insertion order.
func (s *Stack) AddRoute(pfx netip.Prefix, nextHop netip.Addr, iface string, metric int) error {
	if !pfx.IsValid() || !pfx.Addr().Is4() {
		return fmt.Errorf("netstack: invalid route prefix %v", pfx)
	}
	if nextHop.IsValid() && !nextHop.Is4() {
		return fmt.Errorf("netstack: invalid route next hop %v", nextHop)
	}
	return s.routes.Add(route.Route{Prefix: pfx, NextHop: nextHop, Interface: iface, Metric: metric})
}

// Ping sends an ICMP echo request to dst and returns the reply synchronously.
// The entire exchange — including ARP resolution — completes within this call.
func (s *Stack) Ping(dst netip.Addr) (PingResult, error) {
	id := s.nextEcho
	s.nextEcho++
	pe := &pendingEcho{dst: dst}
	s.pending[id] = pe
	defer delete(s.pending, id)

	req := icmp.NewEchoRequest(id, 1, []byte("virtnet"))
	t0 := s.clock.Now()
	if err := s.sendIP(dst, ipv4.ProtoICMP, req.Marshal()); err != nil {
		return PingResult{}, err
	}
	if pe.reply == nil {
		return PingResult{}, fmt.Errorf("netstack: no echo reply from %s", dst)
	}
	return PingResult{Reply: *pe.reply, RTT: s.clock.Now() - t0}, nil
}

// ReceiveFrame implements fabric.FrameSink: Ethernet frames addressed to this
// stack are dispatched to ARP or IPv4. Frames for other destinations, malformed
// packets, and unknown EtherTypes are dropped silently.
func (s *Stack) ReceiveFrame(f ethernet.Frame) error {
	if !f.Dst.IsBroadcast() && f.Dst != s.iface.MAC {
		return nil
	}
	switch f.Type {
	case ethernet.EtherTypeARP:
		return s.handleARP(f)
	case ethernet.EtherTypeIPv4:
		return s.handleIPv4(f)
	default:
		return nil
	}
}

func (s *Stack) handleARP(f ethernet.Frame) error {
	m, err := arp.Unmarshal(f.Payload)
	if err != nil {
		return nil
	}
	switch m.Op {
	case arp.OpRequest:
		// Learn the sender regardless of whether the request targets us.
		s.arp.Put(m.SenderIP, m.SenderMAC, s.clock.Now())
		if m.TargetIP != s.addr.Addr() {
			return nil
		}
		reply := arp.Message{
			Op:        arp.OpReply,
			SenderMAC: s.iface.MAC,
			SenderIP:  s.addr.Addr(),
			TargetMAC: m.SenderMAC,
			TargetIP:  m.SenderIP,
		}
		frame := ethernet.Frame{Dst: m.SenderMAC, Src: s.iface.MAC, Type: ethernet.EtherTypeARP, Payload: reply.Marshal()}
		return s.iface.Send(frame)
	case arp.OpReply:
		s.arp.Put(m.SenderIP, m.SenderMAC, s.clock.Now())
		return nil
	default:
		return nil
	}
}

func (s *Stack) handleIPv4(f ethernet.Frame) error {
	pkt, err := ipv4.Unmarshal(f.Payload)
	if err != nil {
		return nil
	}
	if pkt.TTL == 0 || pkt.Dst != s.addr.Addr() {
		// Forwarding (routers) is wired in phase 6; a non-forwarding host
		// silently drops traffic not addressed to it.
		return nil
	}
	switch pkt.Protocol {
	case ipv4.ProtoICMP:
		return s.handleICMP(pkt)
	case ipv4.ProtoUDP:
		return s.handleUDP(pkt)
	case ipv4.ProtoTCP:
		return s.handleTCP(pkt)
	default:
		return nil
	}
}

func (s *Stack) handleICMP(pkt ipv4.Packet) error {
	m, err := icmp.Unmarshal(pkt.Payload)
	if err != nil {
		return nil
	}
	switch m.Type {
	case icmp.TypeEchoRequest:
		id, _ := m.EchoID()
		seq, _ := m.EchoSeq()
		data, _ := m.EchoData()
		reply := icmp.NewEchoReply(id, seq, data)
		return s.sendICMP(pkt.Src, reply)
	case icmp.TypeEchoReply:
		if id, ok := m.EchoID(); ok {
			if pe, ok := s.pending[id]; ok {
				rep := m
				pe.reply = &rep
			}
		}
		return nil
	default:
		return nil
	}
}

func (s *Stack) sendICMP(dst netip.Addr, m icmp.Message) error {
	return s.sendIP(dst, ipv4.ProtoICMP, m.Marshal())
}

func (s *Stack) sendIP(dst netip.Addr, proto ipv4.Protocol, payload []byte) error {
	r, ok := s.routes.Lookup(dst)
	if !ok {
		return fmt.Errorf("netstack: no route to %s", dst)
	}
	nextHop := dst
	if r.NextHop.IsValid() {
		nextHop = r.NextHop
	}
	mac, err := s.resolveMAC(nextHop)
	if err != nil {
		return err
	}
	pkt := ipv4.Packet{Src: s.addr.Addr(), Dst: dst, TTL: 64, Protocol: proto, Payload: payload}
	frame := ethernet.Frame{Dst: mac, Src: s.iface.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt.Marshal()}
	return s.iface.Send(frame)
}

func (s *Stack) resolveMAC(ip netip.Addr) (ethernet.MAC, error) {
	if mac, ok := s.arp.Get(ip, s.clock.Now()); ok {
		return mac, nil
	}
	req := arp.Message{
		Op:        arp.OpRequest,
		SenderMAC: s.iface.MAC,
		SenderIP:  s.addr.Addr(),
		TargetIP:  ip,
	}
	frame := ethernet.Frame{Dst: ethernet.BroadcastMAC, Src: s.iface.MAC, Type: ethernet.EtherTypeARP, Payload: req.Marshal()}
	if err := s.iface.Send(frame); err != nil {
		return ethernet.MAC{}, err
	}
	if mac, ok := s.arp.Get(ip, s.clock.Now()); ok {
		return mac, nil
	}
	return ethernet.MAC{}, fmt.Errorf("netstack: no ARP reply from %s", ip)
}
