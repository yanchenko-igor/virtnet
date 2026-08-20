// Socket layer for the virtual network stack (ARCHITECTURE.md §9.5-9.6).
//
// UDP and TCP sockets are plain objects owned by the simulation — not host
// syscalls. Delivery is synchronous: a frame that traverses a link is handled
// by the peer inside the sender's own call. There are no blocking waits;
// retransmission timers are deadlines on the virtual clock, checked lazily
// when the machine drives the stack (Stack.Tick).
package netstack

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/netstack/tcp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/udp"
	"github.com/yanchenko-igor/virtnet/internal/services"
)

const (
	tcpWindow      = 65535
	tcpBacklog     = 16
	DefaultRTO     = time.Second
	maxRetransmits = 8
	maxRTO         = 64 * time.Second
	tcpTimeWait    = 60 * time.Second
	firstEphemeral = 49152
)

var errNoPending = errors.New("netstack: no pending connection")

// ---- UDP ----

// UDPSocket is a virtual UDP socket (RFC 768). Bind assigns the local port;
// Connect optionally filters incoming datagrams to a single peer.
type UDPSocket struct {
	stack     *Stack
	localAddr netip.Addr
	localPort uint16
	bound     bool
	connected bool
	peerAddr  netip.Addr
	peerPort  uint16
	rxq       []udpDatagram
	closed    bool
}

type udpDatagram struct {
	src     netip.Addr
	srcPort uint16
	data    []byte
}

// NewUDPSocket creates an unbound UDP socket on this stack.
func (s *Stack) NewUDPSocket() *UDPSocket {
	return &UDPSocket{stack: s, localAddr: s.addr.Addr()}
}

// Bind binds the socket to a local port. Port 0 picks an ephemeral port.
func (u *UDPSocket) Bind(port uint16) error {
	if u.closed {
		return fmt.Errorf("netstack: UDP socket closed")
	}
	if u.bound {
		return fmt.Errorf("netstack: UDP socket already bound to port %d", u.localPort)
	}
	if port == 0 {
		port = u.stack.ephemeralPort()
	} else if _, taken := u.stack.udpSockets[port]; taken {
		return fmt.Errorf("netstack: UDP port %d already in use", port)
	}
	u.localPort = port
	u.bound = true
	u.stack.udpSockets[port] = u
	return nil
}

// LocalPort returns the bound local port (0 if unbound).
func (u *UDPSocket) LocalPort() uint16 {
	return u.localPort
}

// Connect filters incoming datagrams to a single peer. Outgoing SendTo calls
// without an address use this peer. A zero port clears the filter.
func (u *UDPSocket) Connect(addr netip.Addr, port uint16) {
	u.stack.Tick()
	if port == 0 {
		u.connected = false
		return
	}
	u.connected = true
	u.peerAddr = addr
	u.peerPort = port
}

// SendTo sends a datagram to addr:port. An unbound socket binds to an
// ephemeral local port first.
func (u *UDPSocket) SendTo(addr netip.Addr, port uint16, data []byte) error {
	u.stack.Tick()
	if u.closed {
		return fmt.Errorf("netstack: UDP socket closed")
	}
	if !u.bound {
		if err := u.Bind(0); err != nil {
			return err
		}
	}
	pkt := udp.Packet{SrcPort: u.localPort, DstPort: port, Payload: data}
	return u.stack.sendIP(addr, ipv4.ProtoUDP, pkt.Marshal(u.localAddr, addr))
}

// RecvFrom pops the oldest received datagram. ok is false when the queue is
// empty. Delivery is non-blocking: in the synchronous model the data is
// already here by the time the sender's call returned.
func (u *UDPSocket) RecvFrom() (netip.Addr, uint16, []byte, bool) {
	u.stack.Tick()
	if len(u.rxq) == 0 {
		return netip.Addr{}, 0, nil, false
	}
	d := u.rxq[0]
	u.rxq = u.rxq[1:]
	return d.src, d.srcPort, d.data, true
}

// Close unbinds the socket and discards its queue.
func (u *UDPSocket) Close() error {
	if u.closed {
		return nil
	}
	u.closed = true
	if u.bound {
		delete(u.stack.udpSockets, u.localPort)
	}
	u.rxq = nil
	return nil
}

// ---- TCP ----

// TCPConn is a virtual TCP connection (RFC 793 state machine). Timers are
// deadlines on the virtual clock; Tick (called by the stack's public methods)
// drives retransmission lazily.
type TCPConn struct {
	stack *Stack
	state tcp.State

	localAddr  netip.Addr
	localPort  uint16
	remoteAddr netip.Addr
	remotePort uint16

	sndNxt uint32
	sndUna uint32
	rcvNxt uint32

	rxq []byte

	rto          time.Duration
	retxDeadline time.Duration // zero = no pending retransmission
	retxSeg      *tcp.Segment
	retxCount    int

	finSeq     uint32
	finAcked   bool
	peerClosed bool

	backlog []*TCPConn // LISTEN: children awaiting Accept

	timeWaitUntil time.Duration
}

type tcpKey struct {
	localAddr  netip.Addr
	localPort  uint16
	remoteAddr netip.Addr
	remotePort uint16
}

func (c *TCPConn) key() tcpKey {
	return tcpKey{localAddr: c.localAddr, localPort: c.localPort, remoteAddr: c.remoteAddr, remotePort: c.remotePort}
}

// Listen creates a listening socket bound to port.
func (s *Stack) Listen(port uint16) (*TCPConn, error) {
	s.Tick()
	if _, taken := s.tcpListeners[port]; taken {
		return nil, fmt.Errorf("netstack: TCP port %d already in use", port)
	}
	l := &TCPConn{stack: s, state: tcp.StateListen, localAddr: s.addr.Addr(), localPort: port, rto: DefaultRTO}
	s.tcpListeners[port] = l
	return l, nil
}

// Dial opens a connection to addr:port and completes the three-way handshake
// synchronously: SYN, SYN-ACK, and ACK all cross the links within this call.
func (s *Stack) Dial(dst netip.Addr, port uint16) (*TCPConn, error) {
	s.Tick()
	c := &TCPConn{
		stack:      s,
		state:      tcp.StateSynSent,
		localAddr:  s.addr.Addr(),
		localPort:  s.ephemeralPort(),
		remoteAddr: dst,
		remotePort: port,
		sndNxt:     s.allocISN(),
		rto:        DefaultRTO,
	}
	key := c.key()
	s.tcpConns[key] = c
	syn := &tcp.Segment{SrcPort: c.localPort, DstPort: c.remotePort, Seq: c.sndNxt, Flags: tcp.FlagSYN, Window: tcpWindow}
	c.sndNxt++
	c.setRetx(syn)
	if err := c.send(syn); err != nil {
		delete(s.tcpConns, key)
		return nil, err
	}
	if c.state != tcp.StateEstablished {
		delete(s.tcpConns, key)
		return nil, fmt.Errorf("netstack: connect to %s:%d failed", dst, port)
	}
	return c, nil
}

// Accept returns the oldest established connection in the backlog. It errors
// when no connection is pending. The handshake completes during the client's
// Dial, so a connection is ready as soon as the server resumes. A connection
// whose peer already closed (CLOSE-WAIT) is still accepted: accept(2) returns
// the socket once established, regardless of a later peer FIN.
func (l *TCPConn) Accept() (*TCPConn, error) {
	l.stack.Tick()
	if l.state != tcp.StateListen {
		return nil, fmt.Errorf("netstack: not a listening socket")
	}
	for i, ch := range l.backlog {
		if ch.state == tcp.StateEstablished || ch.state == tcp.StateCloseWait {
			l.backlog = append(l.backlog[:i], l.backlog[i+1:]...)
			return ch, nil
		}
	}
	return nil, errNoPending
}

// Read copies received bytes into buf. It returns io.EOF once the peer has
// closed and all data has been consumed; 0 with nil error when nothing is
// available yet (non-blocking).
func (c *TCPConn) Read(buf []byte) (int, error) {
	c.stack.Tick()
	if c.state == tcp.StateClosed {
		return 0, fmt.Errorf("netstack: connection closed")
	}
	if len(c.rxq) == 0 {
		if c.peerClosed {
			return 0, io.EOF
		}
		return 0, nil
	}
	n := copy(buf, c.rxq)
	c.rxq = c.rxq[n:]
	return n, nil
}

// Write sends data reliably to the peer. The peer's ACK arrives synchronously
// within this call; retransmission handles a silent peer.
func (c *TCPConn) Write(data []byte) (int, error) {
	c.stack.Tick()
	switch c.state {
	case tcp.StateEstablished, tcp.StateCloseWait, tcp.StateFinWait2:
	default:
		return 0, fmt.Errorf("netstack: cannot write in state %s", c.state)
	}
	if len(data) == 0 {
		return 0, nil
	}
	seg := &tcp.Segment{SrcPort: c.localPort, DstPort: c.remotePort, Seq: c.sndNxt, Ack: c.rcvNxt, Flags: tcp.FlagACK, Window: tcpWindow, Payload: data}
	c.sndNxt += uint32(len(data))
	c.setRetx(seg)
	if err := c.send(seg); err != nil {
		c.sndNxt -= uint32(len(data))
		c.clearRetx()
		return 0, err
	}
	return len(data), nil
}

// Close initiates connection teardown. On a listening socket it stops
// listening. The FIN exchange completes synchronously on the active close.
func (c *TCPConn) Close() error {
	c.stack.Tick()
	switch c.state {
	case tcp.StateListen:
		delete(c.stack.tcpListeners, c.localPort)
		c.state = tcp.StateClosed
		return nil
	case tcp.StateEstablished, tcp.StateCloseWait:
		c.finSeq = c.sndNxt
		fin := &tcp.Segment{SrcPort: c.localPort, DstPort: c.remotePort, Seq: c.sndNxt, Ack: c.rcvNxt, Flags: tcp.FlagFIN | tcp.FlagACK, Window: tcpWindow}
		c.sndNxt++
		if c.state == tcp.StateEstablished {
			c.state = tcp.StateFinWait1
		} else {
			c.state = tcp.StateLastAck
		}
		c.setRetx(fin)
		return c.send(fin)
	default:
		return fmt.Errorf("netstack: cannot close in state %s", c.state)
	}
}

// State returns the connection state.
func (c *TCPConn) State() tcp.State {
	return c.state
}

// LocalPort returns the local port of the connection or listener.
func (c *TCPConn) LocalPort() uint16 {
	return c.localPort
}

// Remote returns the peer address and port.
func (c *TCPConn) Remote() (netip.Addr, uint16) {
	return c.remoteAddr, c.remotePort
}

// Tick drives lazy timeouts: a segment past its retransmission deadline is
// resent with exponential backoff; TIME-WAIT connections expire.
func (c *TCPConn) tick() {
	if c.state == tcp.StateClosed || c.retxSeg == nil || c.retxDeadline == 0 {
		return
	}
	now := c.stack.clock.Now()
	if now < c.retxDeadline {
		return
	}
	if c.retxCount >= maxRetransmits {
		c.state = tcp.StateClosed
		c.clearRetx()
		return
	}
	// Reschedule FIRST: a synchronous reply during the resend re-enters tick
	// from handleTCP, and the deadline must already be in the future or the
	// resend would loop forever.
	c.retxCount++
	backoff := c.rto
	for i := 0; i < c.retxCount; i++ {
		backoff *= 2
		if backoff > maxRTO {
			backoff = maxRTO
			break
		}
	}
	c.retxDeadline = now + backoff
	_ = c.send(c.retxSeg)
	if c.retxSeg == nil {
		c.retxDeadline = 0
	}
}

func (c *TCPConn) setRetx(seg *tcp.Segment) {
	c.retxSeg = seg
	c.retxCount = 0
	c.retxDeadline = c.stack.clock.Now() + c.rto
}

func (c *TCPConn) clearRetx() {
	c.retxSeg = nil
	c.retxCount = 0
	c.retxDeadline = 0
}

func (c *TCPConn) send(seg *tcp.Segment) error {
	return c.stack.sendSegment(c.remoteAddr, seg)
}

func (c *TCPConn) sendAck() {
	_ = c.send(&tcp.Segment{SrcPort: c.localPort, DstPort: c.remotePort, Seq: c.sndNxt, Ack: c.rcvNxt, Flags: tcp.FlagACK, Window: tcpWindow})
}

func (c *TCPConn) enterTimeWait() {
	c.timeWaitUntil = c.stack.clock.Now() + tcpTimeWait
}

// handleSegment processes one inbound segment according to the state machine.
func (c *TCPConn) handleSegment(src netip.Addr, seg tcp.Segment) {
	switch c.state {
	case tcp.StateListen:
		if seg.Has(tcp.FlagSYN) && !seg.Has(tcp.FlagACK) {
			child := &TCPConn{
				stack:      c.stack,
				state:      tcp.StateSynReceived,
				localAddr:  c.localAddr,
				localPort:  c.localPort,
				remoteAddr: src,
				remotePort: seg.SrcPort,
				sndNxt:     c.stack.allocISN(),
				rcvNxt:     seg.Seq + 1,
				rto:        DefaultRTO,
			}
			synack := &tcp.Segment{SrcPort: c.localPort, DstPort: seg.SrcPort, Seq: child.sndNxt, Ack: child.rcvNxt, Flags: tcp.FlagSYN | tcp.FlagACK, Window: tcpWindow}
			child.sndNxt++
			child.setRetx(synack)
			if len(c.backlog) < tcpBacklog {
				c.backlog = append(c.backlog, child)
				c.stack.tcpConns[child.key()] = child
				_ = child.send(synack)
			}
		}
	case tcp.StateSynSent:
		if seg.Has(tcp.FlagSYN) && seg.Has(tcp.FlagACK) {
			c.rcvNxt = seg.Seq + 1
			c.sndUna = seg.Ack
			c.state = tcp.StateEstablished
			c.clearRetx()
			c.sendAck()
		} else if seg.Has(tcp.FlagRST) {
			c.state = tcp.StateClosed
			c.clearRetx()
		}
	case tcp.StateSynReceived:
		if seg.Has(tcp.FlagRST) {
			c.state = tcp.StateClosed
			c.clearRetx()
		} else if seg.Has(tcp.FlagACK) {
			c.sndUna = seg.Ack
			c.state = tcp.StateEstablished
			c.clearRetx()
		}
	case tcp.StateEstablished, tcp.StateFinWait1, tcp.StateFinWait2, tcp.StateCloseWait, tcp.StateClosing, tcp.StateLastAck, tcp.StateTimeWait:
		c.processData(seg)
		c.processAck(seg)
		c.processFin(seg)
	default:
		// CLOSED: ignore.
	}
}

func (c *TCPConn) processData(seg tcp.Segment) {
	if len(seg.Payload) == 0 {
		return
	}
	if seg.Seq != c.rcvNxt {
		return // out of order; the sender retransmits
	}
	c.rxq = append(c.rxq, seg.Payload...)
	c.rcvNxt += uint32(len(seg.Payload))
	c.sendAck()
}

func (c *TCPConn) processAck(seg tcp.Segment) {
	if !seg.Has(tcp.FlagACK) {
		return
	}
	if seqGE(seg.Ack, c.sndUna) {
		c.sndUna = seg.Ack
		c.clearRetx()
	}
	if c.finSeq != 0 && !c.finAcked && seqGE(seg.Ack, c.finSeq+1) {
		c.finAcked = true
		switch c.state {
		case tcp.StateFinWait1:
			if c.peerClosed {
				c.state = tcp.StateTimeWait
				c.enterTimeWait()
			} else {
				c.state = tcp.StateFinWait2
			}
		case tcp.StateClosing:
			c.state = tcp.StateTimeWait
			c.enterTimeWait()
		case tcp.StateLastAck:
			c.state = tcp.StateClosed
		}
	}
}

func (c *TCPConn) processFin(seg tcp.Segment) {
	if !seg.Has(tcp.FlagFIN) {
		return
	}
	if c.peerClosed {
		c.sendAck() // duplicate FIN: re-ACK
		return
	}
	if seg.Seq != c.rcvNxt {
		return // FIN not in order
	}
	c.peerClosed = true
	c.rcvNxt++
	c.sendAck()
	switch c.state {
	case tcp.StateEstablished:
		c.state = tcp.StateCloseWait
	case tcp.StateFinWait1:
		if c.finAcked {
			c.state = tcp.StateTimeWait
			c.enterTimeWait()
		} else {
			c.state = tcp.StateClosing
		}
	case tcp.StateFinWait2:
		c.state = tcp.StateTimeWait
		c.enterTimeWait()
	}
}

func seqGE(a, b uint32) bool {
	return int32(a-b) >= 0
}

// ---- Stack integration ----

// Tick advances the stack's lazy timers. It must be called whenever the
// machine's virtual clock may have moved past a connection's deadline; the
// public socket methods call it automatically.
func (s *Stack) Tick() {
	now := s.clock.Now()
	for _, k := range sortedTCPKeys(s.tcpConns) {
		c := s.tcpConns[k]
		c.tick()
		if c.state == tcp.StateTimeWait && c.timeWaitUntil != 0 && now >= c.timeWaitUntil {
			delete(s.tcpConns, k)
		}
	}
}

func (s *Stack) handleUDP(pkt ipv4.Packet) error {
	d, err := udp.Unmarshal(pkt.Payload, pkt.Src, pkt.Dst)
	if err != nil {
		return nil
	}

	key := services.ServiceKey{Port: d.DstPort, Proto: uint8(ipv4.ProtoUDP)}
	if svc, ok := s.services[key]; ok {
		ctx := services.ServiceContext{
			Machine: nil,
			Stack:   s,
			SrcAddr: pkt.Src,
			SrcPort: d.SrcPort,
			DstAddr: pkt.Dst,
			DstPort: d.DstPort,
			Proto:   uint8(ipv4.ProtoUDP),
			Clock:   s.clock,
		}
		resp, err := svc.HandleRequest(ctx, services.ServiceRequest{Payload: d.Payload})
		if err == nil && len(resp) > 0 {
			_ = s.sendIP(pkt.Src, ipv4.ProtoUDP, resp)
		}
		return err
	}

	sock := s.udpSockets[d.DstPort]
	if sock == nil || sock.closed {
		return nil
	}
	if sock.connected && (pkt.Src != sock.peerAddr || d.SrcPort != sock.peerPort) {
		return nil
	}
	sock.rxq = append(sock.rxq, udpDatagram{src: pkt.Src, srcPort: d.SrcPort, data: append([]byte(nil), d.Payload...)})
	return nil
}

func (s *Stack) handleTCP(pkt ipv4.Packet) error {
	seg, err := tcp.Unmarshal(pkt.Payload, pkt.Src, pkt.Dst)
	if err != nil {
		return nil
	}

	key := services.ServiceKey{Port: seg.DstPort, Proto: uint8(ipv4.ProtoTCP)}
	if svc, ok := s.services[key]; ok {
		// Only call service for established connections with data (PSH flag) or data payload
		tcpKey := tcpKey{localAddr: pkt.Dst, localPort: seg.DstPort, remoteAddr: pkt.Src, remotePort: seg.SrcPort}
		if conn, ok := s.tcpConns[tcpKey]; ok && conn.State() == tcp.StateEstablished {
			// Only process data segments (with PSH flag or non-empty payload that's not just ACK)
			if len(seg.Payload) > 0 && (seg.Flags&tcp.FlagPSH != 0 || len(seg.Payload) > 0) {
				ctx := services.ServiceContext{
					Machine: nil,
					Stack:   s,
					SrcAddr: pkt.Src,
					SrcPort: seg.SrcPort,
					DstAddr: pkt.Dst,
					DstPort: seg.DstPort,
					Proto:   uint8(ipv4.ProtoTCP),
					Clock:   s.clock,
				}
				resp, err := svc.HandleRequest(ctx, services.ServiceRequest{Payload: seg.Payload})
				if err == nil && len(resp) > 0 {
					rstSeg := &tcp.Segment{
						SrcPort: seg.DstPort,
						DstPort: seg.SrcPort,
						Seq:     seg.Ack,
						Ack:     seg.Seq + uint32(len(seg.Payload)),
						Flags:   tcp.FlagACK | tcp.FlagPSH,
						Window:  65535,
						Payload: resp,
					}
					_ = s.sendSegment(pkt.Src, rstSeg)
				}
				return err
			}
		}
	}

	tcpKey := tcpKey{localAddr: pkt.Dst, localPort: seg.DstPort, remoteAddr: pkt.Src, remotePort: seg.SrcPort}
	if c := s.tcpConns[tcpKey]; c != nil {
		c.tick()
		c.handleSegment(pkt.Src, seg)
		return nil
	}
	if l := s.tcpListeners[seg.DstPort]; l != nil {
		l.handleSegment(pkt.Src, seg)
		return nil
	}
	if seg.Has(tcp.FlagSYN) {
		// No listener: refuse the connection (RFC 793 §3.4).
		rst := &tcp.Segment{SrcPort: seg.DstPort, DstPort: seg.SrcPort, Seq: 0, Ack: seg.Seq + 1, Flags: tcp.FlagRST | tcp.FlagACK, Window: 0}
		_ = s.sendSegment(pkt.Src, rst)
	}
	return nil
}

func (s *Stack) sendSegment(dst netip.Addr, seg *tcp.Segment) error {
	b := seg.Marshal(s.addr.Addr(), dst)
	return s.sendIP(dst, ipv4.ProtoTCP, b)
}

func (s *Stack) ephemeralPort() uint16 {
	p := s.nextPort
	s.nextPort++
	if s.nextPort == 0 {
		s.nextPort = firstEphemeral
	}
	return p
}

func (s *Stack) allocISN() uint32 {
	s.nextISN += 1000
	return s.nextISN
}

// ConnInfo is one socket in the stack's connection table.
type ConnInfo struct {
	Proto  string // tcp | udp
	Local  string // addr:port
	Remote string // addr:port; "-" for unconnected UDP
	State  string
}

// Netstat returns a snapshot of the socket table, sorted for determinism
// (Proto, Local, Remote, State).
func (s *Stack) Netstat() []ConnInfo {
	out := make([]ConnInfo, 0, len(s.udpSockets)+len(s.tcpListeners)+len(s.tcpConns))
	for _, port := range sortedUint16Keys(s.udpSockets) {
		u := s.udpSockets[port]
		if u.closed {
			continue
		}
		remote := "-"
		if u.connected {
			remote = net.JoinHostPort(u.peerAddr.String(), fmt.Sprint(u.peerPort))
		}
		out = append(out, ConnInfo{Proto: "udp", Local: net.JoinHostPort(u.localAddr.String(), fmt.Sprint(port)), Remote: remote, State: "UNCONNECTED"})
	}
	for _, port := range sortedUint16Keys(s.tcpListeners) {
		l := s.tcpListeners[port]
		out = append(out, ConnInfo{Proto: "tcp", Local: net.JoinHostPort(l.localAddr.String(), fmt.Sprint(port)), Remote: "0.0.0.0:*", State: tcp.StateListen.String()})
	}
	for _, k := range sortedTCPKeys(s.tcpConns) {
		c := s.tcpConns[k]
		out = append(out, ConnInfo{Proto: "tcp", Local: net.JoinHostPort(c.localAddr.String(), fmt.Sprint(c.localPort)), Remote: net.JoinHostPort(c.remoteAddr.String(), fmt.Sprint(c.remotePort)), State: c.state.String()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		if out[i].Local != out[j].Local {
			return out[i].Local < out[j].Local
		}
		if out[i].Remote != out[j].Remote {
			return out[i].Remote < out[j].Remote
		}
		return out[i].State < out[j].State
	})
	return out
}

func sortedUint16Keys[V any](m map[uint16]V) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortedTCPKeys(m map[tcpKey]*TCPConn) []tcpKey {
	keys := make([]tcpKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if cmp := a.localAddr.Compare(b.localAddr); cmp != 0 {
			return cmp < 0
		}
		if a.localPort != b.localPort {
			return a.localPort < b.localPort
		}
		if cmp := a.remoteAddr.Compare(b.remoteAddr); cmp != 0 {
			return cmp < 0
		}
		return a.remotePort < b.remotePort
	})
	return keys
}
