// State snapshots for checkpoint/restore (ARCHITECTURE.md §12.2). A Stack's
// serializable state covers its address, routing table, ARP cache, counters,
// and every open socket. Everything is emitted in sorted order so snapshots
// never depend on Go map iteration order.
package netstack

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/route"
	"github.com/yanchenko-igor/virtnet/internal/netstack/tcp"
)

// StackState is a serializable snapshot of a machine's network stack.
type StackState struct {
	Addr       netip.Prefix
	ARPTimeout time.Duration
	ARP        []arp.SnapshotEntry
	Routes     []route.Route
	Forward    bool
	NextEcho   uint16
	NextPort   uint16
	NextISN    uint32

	UDPSockets []udpSocketState
	TCPListen  []tcpConnState
	TCPConns   []tcpConnState
}

// udpSocketState is one UDP socket in serializable form.
type udpSocketState struct {
	LocalPort uint16
	Bound     bool
	Connected bool
	PeerAddr  netip.Addr
	PeerPort  uint16
	RXQ       []udpDatagramState
	Closed    bool
}

// udpDatagramState is one queued datagram in serializable form.
type udpDatagramState struct {
	Src     netip.Addr
	SrcPort uint16
	Data    []byte
}

// tcpConnState is one TCP connection in serializable form. Backlog references
// (listen) children by their snapshot ID.
type tcpConnState struct {
	ID            int
	State         tcp.State
	LocalAddr     netip.Addr
	LocalPort     uint16
	RemoteAddr    netip.Addr
	RemotePort    uint16
	SndNxt        uint32
	SndUna        uint32
	RcvNxt        uint32
	RXQ           []byte
	RTO           time.Duration
	RetxDeadline  time.Duration
	RetxCount     int
	RetxSeg       *tcp.Segment
	FinSeq        uint32
	FinAcked      bool
	PeerClosed    bool
	Backlog       []int
	TimeWaitUntil time.Duration
}

// State captures the stack's full serializable state.
func (s *Stack) State() StackState {
	st := StackState{
		Addr:       s.addr,
		ARPTimeout: s.arp.Timeout(),
		ARP:        s.arp.Snapshot(),
		Routes:     s.routes.Routes(),
		Forward:    s.forward,
		NextEcho:   s.nextEcho,
		NextPort:   s.nextPort,
		NextISN:    s.nextISN,
	}

	for _, port := range sortedUint16Keys(s.udpSockets) {
		u := s.udpSockets[port]
		us := udpSocketState{
			LocalPort: port,
			Bound:     u.bound,
			Connected: u.connected,
			PeerAddr:  u.peerAddr,
			PeerPort:  u.peerPort,
			Closed:    u.closed,
		}
		for _, d := range u.rxq {
			us.RXQ = append(us.RXQ, udpDatagramState{Src: d.src, SrcPort: d.srcPort, Data: append([]byte(nil), d.data...)})
		}
		st.UDPSockets = append(st.UDPSockets, us)
	}

	nextID := 1
	ids := map[*TCPConn]int{}
	idFor := func(c *TCPConn) int {
		if id, ok := ids[c]; ok {
			return id
		}
		ids[c] = nextID
		nextID++
		return ids[c]
	}
	snapConn := func(c *TCPConn) tcpConnState {
		cs := tcpConnState{
			ID:            idFor(c),
			State:         c.state,
			LocalAddr:     c.localAddr,
			LocalPort:     c.localPort,
			RemoteAddr:    c.remoteAddr,
			RemotePort:    c.remotePort,
			SndNxt:        c.sndNxt,
			SndUna:        c.sndUna,
			RcvNxt:        c.rcvNxt,
			RXQ:           append([]byte(nil), c.rxq...),
			RTO:           c.rto,
			RetxDeadline:  c.retxDeadline,
			RetxCount:     c.retxCount,
			RetxSeg:       c.retxSeg,
			FinSeq:        c.finSeq,
			FinAcked:      c.finAcked,
			PeerClosed:    c.peerClosed,
			TimeWaitUntil: c.timeWaitUntil,
		}
		for _, ch := range c.backlog {
			cs.Backlog = append(cs.Backlog, idFor(ch))
		}
		return cs
	}

	for _, port := range sortedUint16Keys(s.tcpListeners) {
		st.TCPListen = append(st.TCPListen, snapConn(s.tcpListeners[port]))
	}
	for _, k := range sortedTCPKeys(s.tcpConns) {
		st.TCPConns = append(st.TCPConns, snapConn(s.tcpConns[k]))
	}
	return st
}

// RestoreStack builds a fresh stack from a captured state, bound to iface.
// The directly-connected route and the ARP cache come from the snapshot, so a
// restored stack is byte-for-byte equivalent to the one that produced it.
func RestoreStack(c *clock.VirtualClock, iface *fabric.Interface, st StackState) (*Stack, error) {
	if !st.Addr.IsValid() || !st.Addr.Addr().Is4() {
		return nil, fmt.Errorf("netstack: restore: invalid address %v", st.Addr)
	}
	s := &Stack{
		clock:        c,
		iface:        iface,
		addr:         st.Addr,
		routes:       route.NewTable(),
		arp:          arp.NewCache(st.ARPTimeout),
		pending:      make(map[uint16]*pendingEcho),
		udpSockets:   make(map[uint16]*UDPSocket),
		tcpListeners: make(map[uint16]*TCPConn),
		tcpConns:     make(map[tcpKey]*TCPConn),
		forward:      st.Forward,
		nextEcho:     st.NextEcho,
		nextPort:     st.NextPort,
		nextISN:      st.NextISN,
	}
	s.routes.Restore(st.Routes)
	s.arp.Restore(st.ARP)

	for _, us := range st.UDPSockets {
		u := &UDPSocket{
			stack:     s,
			localAddr: s.addr.Addr(),
			localPort: us.LocalPort,
			bound:     us.Bound,
			connected: us.Connected,
			peerAddr:  us.PeerAddr,
			peerPort:  us.PeerPort,
			closed:    us.Closed,
		}
		for _, d := range us.RXQ {
			u.rxq = append(u.rxq, udpDatagram{src: d.Src, srcPort: d.SrcPort, data: append([]byte(nil), d.Data...)})
		}
		s.udpSockets[us.LocalPort] = u
	}

	byID := make(map[int]*TCPConn, len(st.TCPListen)+len(st.TCPConns))
	for _, cs := range st.TCPListen {
		c := restoreTCPConn(s, cs)
		byID[cs.ID] = c
		s.tcpListeners[cs.LocalPort] = c
	}
	for _, cs := range st.TCPConns {
		c := restoreTCPConn(s, cs)
		byID[cs.ID] = c
		s.tcpConns[c.key()] = c
	}
	// Backlogs reference children by ID; wire them once every connection exists.
	wire := func(cs tcpConnState) {
		if len(cs.Backlog) == 0 {
			return
		}
		c := byID[cs.ID]
		if c == nil {
			return
		}
		for _, id := range cs.Backlog {
			if ch, ok := byID[id]; ok {
				c.backlog = append(c.backlog, ch)
			}
		}
	}
	for _, cs := range st.TCPListen {
		wire(cs)
	}
	for _, cs := range st.TCPConns {
		wire(cs)
	}

	iface.Attach(s)
	return s, nil
}

// restoreTCPConn rebuilds one connection from its snapshot. Backlog wiring and
// registration in the stack's tables happen in RestoreStack.
func restoreTCPConn(s *Stack, cs tcpConnState) *TCPConn {
	return &TCPConn{
		stack:         s,
		state:         cs.State,
		localAddr:     cs.LocalAddr,
		localPort:     cs.LocalPort,
		remoteAddr:    cs.RemoteAddr,
		remotePort:    cs.RemotePort,
		sndNxt:        cs.SndNxt,
		sndUna:        cs.SndUna,
		rcvNxt:        cs.RcvNxt,
		rxq:           append([]byte(nil), cs.RXQ...),
		rto:           cs.RTO,
		retxDeadline:  cs.RetxDeadline,
		retxSeg:       cs.RetxSeg,
		retxCount:     cs.RetxCount,
		finSeq:        cs.FinSeq,
		finAcked:      cs.FinAcked,
		peerClosed:    cs.PeerClosed,
		timeWaitUntil: cs.TimeWaitUntil,
	}
}
