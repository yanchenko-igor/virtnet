package router

import (
	"net/netip"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/netstack/icmp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/netstack/tcp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/udp"
)

// collector records every frame delivered to it.
type collector struct {
	frames []ethernet.Frame
}

func (c *collector) ReceiveFrame(f ethernet.Frame) error {
	c.frames = append(c.frames, f)
	return nil
}

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func addr(s string) netip.Addr {
	return netip.MustParseAddr(s)
}

// rig is a two-segment router test bed:
//
//	hostA 10.0.0.10 —[eth0 10.0.0.1] R [eth1 10.0.1.1]— hostB 10.0.1.10
//
// hostA and hostB are plain interfaces whose received frames land in a
// collector; ARP caches are pre-seeded so the tests focus on router logic.
// The end-to-end ARP dance is exercised by the acceptance tests.
type rig struct {
	c     *clock.VirtualClock
	r     *Router
	hostA *fabric.Interface
	eth0  *fabric.Interface
	hostB *fabric.Interface
	eth1  *fabric.Interface
	colA  *collector
	colB  *collector
}

func setup(t *testing.T) *rig {
	t.Helper()
	c := clock.New()
	rg := &rig{
		c:     c,
		r:     New(c, 0),
		hostA: fabric.NewInterface("a", mustMAC(t, "02:00:00:00:00:01")),
		eth0:  fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:0a")),
		hostB: fabric.NewInterface("b", mustMAC(t, "02:00:00:00:00:02")),
		eth1:  fabric.NewInterface("eth1", mustMAC(t, "02:00:00:00:00:0b")),
		colA:  &collector{},
		colB:  &collector{},
	}
	if _, err := fabric.NewLink(rg.c, rg.hostA, rg.eth0, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := rg.r.AddInterface("eth0", rg.eth0, netip.MustParsePrefix("10.0.0.1/24")); err != nil {
		t.Fatal(err)
	}
	if _, err := fabric.NewLink(rg.c, rg.hostB, rg.eth1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := rg.r.AddInterface("eth1", rg.eth1, netip.MustParsePrefix("10.0.1.1/24")); err != nil {
		t.Fatal(err)
	}
	rg.hostA.Attach(rg.colA)
	rg.hostB.Attach(rg.colB)

	// Pre-seed the ARP caches with the hosts' addresses.
	seedARP(t, rg.hostA, rg.eth0, addr("10.0.0.1"), addr("10.0.0.10"))
	seedARP(t, rg.hostB, rg.eth1, addr("10.0.1.1"), addr("10.0.1.10"))
	return rg
}

// seedARP teaches the router that hostIP is at host's MAC on host's segment.
func seedARP(t *testing.T, host, routerPort *fabric.Interface, routerIP, hostIP netip.Addr) {
	t.Helper()
	msg := arp.Message{
		Op:        arp.OpReply,
		SenderMAC: host.MAC,
		SenderIP:  hostIP,
		TargetMAC: routerPort.MAC,
		TargetIP:  routerIP,
	}
	frame := ethernet.Frame{Dst: routerPort.MAC, Src: host.MAC, Type: ethernet.EtherTypeARP, Payload: msg.Marshal()}
	if err := host.Send(frame); err != nil {
		t.Fatal(err)
	}
}

// inject sends an IPv4 packet from a host toward the router.
func inject(t *testing.T, host, routerPort *fabric.Interface, pkt ipv4.Packet) {
	t.Helper()
	frame := ethernet.Frame{Dst: routerPort.MAC, Src: host.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt.Marshal()}
	if err := host.Send(frame); err != nil {
		t.Fatal(err)
	}
}

func last(col *collector) ethernet.Frame {
	return col.frames[len(col.frames)-1]
}

func TestRouterARPReply(t *testing.T) {
	rg := setup(t)

	// hostA asks "who has 10.0.0.1?" — the router must answer with eth0's MAC.
	req := arp.Message{
		Op:        arp.OpRequest,
		SenderMAC: rg.hostA.MAC,
		SenderIP:  addr("10.0.0.10"),
		TargetIP:  addr("10.0.0.1"),
	}
	frame := ethernet.Frame{Dst: ethernet.BroadcastMAC, Src: rg.hostA.MAC, Type: ethernet.EtherTypeARP, Payload: req.Marshal()}
	if err := rg.hostA.Send(frame); err != nil {
		t.Fatal(err)
	}
	got := last(rg.colA)
	if got.Dst != rg.hostA.MAC || got.Src != rg.eth0.MAC {
		t.Errorf("reply addressed %v<-%v, want %v<-%v", got.Dst, got.Src, rg.hostA.MAC, rg.eth0.MAC)
	}
	m, err := arp.Unmarshal(got.Payload)
	if err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if m.Op != arp.OpReply || m.SenderIP != addr("10.0.0.1") || m.TargetIP != addr("10.0.0.10") {
		t.Errorf("unexpected ARP reply: %+v", m)
	}
	// Router learned the requester.
	if len(rg.r.ARPEntries("eth0")) != 1 {
		t.Errorf("router did not learn requester")
	}
}

func TestRouterEchoReply(t *testing.T) {
	rg := setup(t)

	req := icmp.NewEchoRequest(7, 42, []byte("ping"))
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.0.1"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	inject(t, rg.hostA, rg.eth0, pkt)

	reply := last(rg.colA)
	if reply.Dst != rg.hostA.MAC || reply.Src != rg.eth0.MAC {
		t.Errorf("echo reply addressed %v<-%v", reply.Dst, reply.Src)
	}
	rp, err := ipv4.Unmarshal(reply.Payload)
	if err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if rp.Src != addr("10.0.0.1") || rp.Dst != addr("10.0.0.10") {
		t.Errorf("reply src/dst = %v/%v", rp.Src, rp.Dst)
	}
	m, err := icmp.Unmarshal(rp.Payload)
	if err != nil {
		t.Fatalf("unmarshal icmp: %v", err)
	}
	if m.Type != icmp.TypeEchoReply {
		t.Errorf("type = %d, want echo reply", m.Type)
	}
	if id, _ := m.EchoID(); id != 7 {
		t.Errorf("echo id = %d, want 7", id)
	}
	if data, _ := m.EchoData(); string(data) != "ping" {
		t.Errorf("echo data = %q, want %q", data, "ping")
	}
}

func TestRouterForward(t *testing.T) {
	rg := setup(t)

	// hostA pings 10.0.1.10 (hostB). TTL 64 must reach hostB as 63.
	req := icmp.NewEchoRequest(1, 1, []byte("virtnet"))
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.1.10"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	inject(t, rg.hostA, rg.eth0, pkt)

	if len(rg.colB.frames) != 1 {
		t.Fatalf("hostB received %d frames, want 1", len(rg.colB.frames))
	}
	f := last(rg.colB)
	rp, err := ipv4.Unmarshal(f.Payload)
	if err != nil {
		t.Fatalf("unmarshal forwarded packet: %v", err)
	}
	if rp.TTL != 63 {
		t.Errorf("forwarded TTL = %d, want 63", rp.TTL)
	}
	if rp.Src != addr("10.0.0.10") || rp.Dst != addr("10.0.1.10") {
		t.Errorf("forwarded src/dst = %v/%v", rp.Src, rp.Dst)
	}
	m, err := icmp.Unmarshal(rp.Payload)
	if err != nil {
		t.Fatalf("unmarshal icmp: %v", err)
	}
	if m.Type != icmp.TypeEchoRequest {
		t.Errorf("forwarded type = %d, want echo request", m.Type)
	}
	if len(rg.colA.frames) != 0 {
		t.Errorf("forwarded frame echoed back to the ingress segment")
	}
}

func TestRouterTTLExceeded(t *testing.T) {
	rg := setup(t)

	req := icmp.NewEchoRequest(1, 1, []byte("virtnet"))
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.1.10"), TTL: 1, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	inject(t, rg.hostA, rg.eth0, pkt)

	if len(rg.colA.frames) != 1 {
		t.Fatalf("no ICMP error emitted (got %d frames)", len(rg.colA.frames))
	}
	f := last(rg.colA)
	rp, err := ipv4.Unmarshal(f.Payload)
	if err != nil {
		t.Fatalf("unmarshal error packet: %v", err)
	}
	if rp.Src != addr("10.0.0.1") || rp.Dst != addr("10.0.0.10") {
		t.Errorf("error src/dst = %v/%v", rp.Src, rp.Dst)
	}
	m, err := icmp.Unmarshal(rp.Payload)
	if err != nil {
		t.Fatalf("unmarshal icmp: %v", err)
	}
	if m.Type != icmp.TypeTimeExceeded || m.Code != 0 {
		t.Errorf("icmp = type %d code %d, want time exceeded (11,0)", m.Type, m.Code)
	}
	// The error payload must embed the original datagram prefix.
	if len(m.Payload) < ipv4.HeaderLen+8 {
		t.Errorf("error payload too short: %d bytes", len(m.Payload))
	}
}

func TestRouterDestUnreachable(t *testing.T) {
	rg := setup(t)

	req := icmp.NewEchoRequest(1, 1, []byte("virtnet"))
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("192.168.99.1"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	inject(t, rg.hostA, rg.eth0, pkt)

	if len(rg.colA.frames) != 1 {
		t.Fatalf("no ICMP error emitted (got %d frames)", len(rg.colA.frames))
	}
	rp, err := ipv4.Unmarshal(last(rg.colA).Payload)
	if err != nil {
		t.Fatalf("unmarshal error packet: %v", err)
	}
	m, err := icmp.Unmarshal(rp.Payload)
	if err != nil {
		t.Fatalf("unmarshal icmp: %v", err)
	}
	if m.Type != icmp.TypeDestUnreach || m.Code != 0 {
		t.Errorf("icmp = type %d code %d, want network unreachable (3,0)", m.Type, m.Code)
	}
}

func TestRouterTCPReset(t *testing.T) {
	rg := setup(t)

	seg := tcp.Segment{SrcPort: 49152, DstPort: 80, Seq: 100, Flags: tcp.FlagSYN, Window: 65535}
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.0.1"), TTL: 64, Protocol: ipv4.ProtoTCP, Payload: seg.Marshal(addr("10.0.0.10"), addr("10.0.0.1"))}
	inject(t, rg.hostA, rg.eth0, pkt)

	if len(rg.colA.frames) != 1 {
		t.Fatalf("no RST emitted (got %d frames)", len(rg.colA.frames))
	}
	rp, err := ipv4.Unmarshal(last(rg.colA).Payload)
	if err != nil {
		t.Fatalf("unmarshal RST: %v", err)
	}
	rst, err := tcp.Unmarshal(rp.Payload, rp.Src, rp.Dst)
	if err != nil {
		t.Fatalf("unmarshal tcp: %v", err)
	}
	if !rst.Has(tcp.FlagRST) || !rst.Has(tcp.FlagACK) {
		t.Errorf("reply flags = %v, want RST|ACK", rst.Flags)
	}
	if rst.SrcPort != 80 || rst.DstPort != 49152 {
		t.Errorf("reply ports = %d:%d, want 80:49152", rst.SrcPort, rst.DstPort)
	}
	if rst.Seq != 0 || rst.Ack != 101 {
		t.Errorf("reply seq/ack = %d/%d, want 0/101", rst.Seq, rst.Ack)
	}
}

func TestRouterUDPPortUnreachable(t *testing.T) {
	rg := setup(t)

	ud := udp.Packet{SrcPort: 49152, DstPort: 9999, Payload: []byte("hello")}
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.0.1"), TTL: 64, Protocol: ipv4.ProtoUDP, Payload: ud.Marshal(addr("10.0.0.10"), addr("10.0.0.1"))}
	inject(t, rg.hostA, rg.eth0, pkt)

	if len(rg.colA.frames) != 1 {
		t.Fatalf("no ICMP error emitted (got %d frames)", len(rg.colA.frames))
	}
	rp, err := ipv4.Unmarshal(last(rg.colA).Payload)
	if err != nil {
		t.Fatalf("unmarshal error packet: %v", err)
	}
	m, err := icmp.Unmarshal(rp.Payload)
	if err != nil {
		t.Fatalf("unmarshal icmp: %v", err)
	}
	if m.Type != icmp.TypeDestUnreach || m.Code != 3 {
		t.Errorf("icmp = type %d code %d, want port unreachable (3,3)", m.Type, m.Code)
	}
}

func TestRouterDropsUnrelated(t *testing.T) {
	rg := setup(t)

	// Not our MAC, not broadcast.
	other := ethernet.MAC{0x02, 0xff, 0xff, 0xff, 0xff, 0xff}
	req := icmp.NewEchoRequest(1, 1, []byte("x"))
	pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.0.1"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	frame := ethernet.Frame{Dst: other, Src: rg.hostA.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt.Marshal()}
	if err := rg.hostA.Send(frame); err != nil {
		t.Fatal(err)
	}
	// Unknown EtherType.
	frame2 := ethernet.Frame{Dst: rg.eth0.MAC, Src: rg.hostA.MAC, Type: ethernet.EtherTypeIPv6, Payload: []byte{1, 2, 3}}
	if err := rg.hostA.Send(frame2); err != nil {
		t.Fatal(err)
	}
	// Loop of our own address arriving on the wrong interface: dst = eth1
	// address but received on eth0 → drop.
	pkt2 := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.1.1"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
	frame3 := ethernet.Frame{Dst: rg.eth0.MAC, Src: rg.hostA.MAC, Type: ethernet.EtherTypeIPv4, Payload: pkt2.Marshal()}
	if err := rg.hostA.Send(frame3); err != nil {
		t.Fatal(err)
	}

	if len(rg.colA.frames) != 0 || len(rg.colB.frames) != 0 {
		t.Errorf("router answered unrelated traffic: A=%d B=%d", len(rg.colA.frames), len(rg.colB.frames))
	}
}

func TestRouterDeterminism(t *testing.T) {
	run := func() (time.Duration, int, int) {
		rg := setup(t)
		req := icmp.NewEchoRequest(1, 1, []byte("virtnet"))
		pkt := ipv4.Packet{Src: addr("10.0.0.10"), Dst: addr("10.0.1.10"), TTL: 64, Protocol: ipv4.ProtoICMP, Payload: req.Marshal()}
		inject(t, rg.hostA, rg.eth0, pkt)
		return rg.c.Now(), len(rg.colA.frames), len(rg.colB.frames)
	}
	ta, aa, ba := run()
	tb, ab, bb := run()
	if ta != tb || aa != ab || ba != bb {
		t.Errorf("non-deterministic: (%v,%d,%d) vs (%v,%d,%d)", ta, aa, ba, tb, ab, bb)
	}
}
