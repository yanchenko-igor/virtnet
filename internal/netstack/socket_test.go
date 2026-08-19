package netstack

import (
	"bytes"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
	"github.com/yanchenko-igor/virtnet/internal/netstack/tcp"
)

// ---- UDP ----

func TestUDPSendReceive(t *testing.T) {
	c, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	u1 := pc1.NewUDPSocket()
	if err := u1.Bind(11111); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	u2 := pc2.NewUDPSocket()
	if err := u2.Bind(5000); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if err := u1.SendTo(mustAddr(t, "10.0.0.20"), 5000, []byte("hello")); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	// Cold ARP (20ms) + datagram (10ms).
	if got := c.Now(); got != 30*time.Millisecond {
		t.Errorf("clock after send = %v, want 30ms", got)
	}

	src, sport, data, ok := u2.RecvFrom()
	if !ok {
		t.Fatal("PC2 did not receive the datagram")
	}
	if src != mustAddr(t, "10.0.0.10") || sport != 11111 {
		t.Errorf("src = %s:%d, want 10.0.0.10:11111", src, sport)
	}
	if !bytes.Equal(data, []byte("hello")) {
		t.Errorf("payload = %q, want hello", data)
	}

	// Reply: PC2's ARP cache is warm.
	if err := u2.SendTo(mustAddr(t, "10.0.0.10"), 11111, []byte("hi")); err != nil {
		t.Fatalf("SendTo reply: %v", err)
	}
	if got := c.Now(); got != 40*time.Millisecond {
		t.Errorf("clock after reply = %v, want 40ms", got)
	}
	_, dport, data, ok := u1.RecvFrom()
	if !ok || !bytes.Equal(data, []byte("hi")) || dport != 5000 {
		t.Errorf("PC1 reply = (%v, %q), want (5000, hi)", dport, data)
	}
}

func TestUDPConnectedSocketFilters(t *testing.T) {
	_, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	u1 := pc1.NewUDPSocket()
	if err := u1.Bind(11111); err != nil {
		t.Fatal(err)
	}
	u2 := pc2.NewUDPSocket()
	if err := u2.Bind(5000); err != nil {
		t.Fatal(err)
	}
	u2.Connect(mustAddr(t, "10.0.0.10"), 11111)

	if err := u1.SendTo(mustAddr(t, "10.0.0.20"), 5000, []byte("allowed")); err != nil {
		t.Fatal(err)
	}
	// A different source socket must be filtered out.
	u1b := pc1.NewUDPSocket()
	if err := u1b.Bind(22222); err != nil {
		t.Fatal(err)
	}
	if err := u1b.SendTo(mustAddr(t, "10.0.0.20"), 5000, []byte("blocked")); err != nil {
		t.Fatal(err)
	}

	_, _, data, ok := u2.RecvFrom()
	if !ok || !bytes.Equal(data, []byte("allowed")) {
		t.Errorf("got (%v, %q), want allowed", ok, data)
	}
	if _, _, _, ok := u2.RecvFrom(); ok {
		t.Error("filtered datagram was delivered")
	}
}

func TestUDPPortConflictAndClose(t *testing.T) {
	_, pc1, _ := setupPair(t, 10*time.Millisecond)
	a := pc1.NewUDPSocket()
	if err := a.Bind(5000); err != nil {
		t.Fatal(err)
	}
	b := pc1.NewUDPSocket()
	if err := b.Bind(5000); err == nil {
		t.Fatal("expected port conflict")
	}
	// Ephemeral bind is fine.
	if err := b.Bind(0); err != nil {
		t.Fatalf("ephemeral bind: %v", err)
	}
	if b.localPort < firstEphemeral {
		t.Errorf("ephemeral port %d below %d", b.localPort, firstEphemeral)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pc1.NewUDPSocket().Bind(5000); err != nil {
		t.Fatalf("port not released after Close: %v", err)
	}
}

// ---- TCP ----

func TestTCPHandshakeDataClose(t *testing.T) {
	c, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	if _, err := pc2.Listen(8080); err != nil {
		t.Fatal(err)
	}

	client, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 8080)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if client.State() != tcp.StateEstablished {
		t.Errorf("client state = %s, want ESTABLISHED", client.State())
	}
	// ARP (20ms) + SYN (10ms) + SYN-ACK (10ms) + ACK (10ms).
	if got := c.Now(); got != 50*time.Millisecond {
		t.Errorf("clock after handshake = %v, want 50ms", got)
	}

	server, err := pc2.tcpListeners[8080].Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if server.State() != tcp.StateEstablished {
		t.Errorf("server state = %s, want ESTABLISHED", server.State())
	}

	n, err := client.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := c.Now(); got != 70*time.Millisecond {
		t.Errorf("clock after write = %v, want 70ms", got)
	}
	buf := make([]byte, 64)
	n, err = server.Read(buf)
	if err != nil || n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("Read = %q, %v (n=%d)", buf[:n], err, n)
	}
	if got := c.Now(); got != 70*time.Millisecond {
		t.Errorf("clock changed by read: %v", got)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("client Close: %v", err)
	}
	if got := c.Now(); got != 90*time.Millisecond {
		t.Errorf("clock after client close = %v, want 90ms", got)
	}
	if client.State() != tcp.StateFinWait2 {
		t.Errorf("client state = %s, want FIN-WAIT-2", client.State())
	}
	if server.State() != tcp.StateCloseWait {
		t.Errorf("server state = %s, want CLOSE-WAIT", server.State())
	}

	if err := server.Close(); err != nil {
		t.Fatalf("server Close: %v", err)
	}
	if got := c.Now(); got != 110*time.Millisecond {
		t.Errorf("clock after server close = %v, want 110ms", got)
	}
	if server.State() != tcp.StateClosed {
		t.Errorf("server state = %s, want CLOSED", server.State())
	}
	if client.State() != tcp.StateTimeWait {
		t.Errorf("client state = %s, want TIME-WAIT", client.State())
	}
}

func TestTCPBidirectionalData(t *testing.T) {
	c, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	if _, err := pc2.Listen(8080); err != nil {
		t.Fatal(err)
	}
	client, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 8080)
	if err != nil {
		t.Fatal(err)
	}
	server, err := pc2.tcpListeners[8080].Accept()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	// Data + ACK.
	if got := c.Now(); got != 70*time.Millisecond {
		t.Errorf("clock after client write = %v, want 70ms", got)
	}
	buf := make([]byte, 64)
	n, _ := server.Read(buf)
	if n != 4 || string(buf[:n]) != "ping" {
		t.Errorf("server read = %q, want ping", buf[:n])
	}

	if _, err := server.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if got := c.Now(); got != 90*time.Millisecond {
		t.Errorf("clock after server write = %v, want 90ms", got)
	}
	n, _ = client.Read(buf)
	if n != 4 || string(buf[:n]) != "pong" {
		t.Errorf("client read = %q, want pong", buf[:n])
	}

	// Active close by client, then server.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.Now(); got != 110*time.Millisecond {
		t.Errorf("clock after client close = %v, want 110ms", got)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.Now(); got != 130*time.Millisecond {
		t.Errorf("clock after server close = %v, want 130ms", got)
	}
}

func TestTCPEOFAfterPeerClose(t *testing.T) {
	_, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	if _, err := pc2.Listen(8080); err != nil {
		t.Fatal(err)
	}
	client, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 8080)
	if err != nil {
		t.Fatal(err)
	}
	server, err := pc2.tcpListeners[8080].Accept()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("bye")); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := server.Read(buf)
	if n != 3 || string(buf[:n]) != "bye" || err != nil {
		t.Fatalf("first Read = %q, %v", buf[:n], err)
	}
	n, err = server.Read(buf)
	if n != 0 || err == nil {
		t.Fatalf("second Read = %d, %v, want io.EOF", n, err)
	}
}

func TestTCPRSTOnUnopenedPort(t *testing.T) {
	c, pc1, _ := setupPair(t, 10*time.Millisecond)
	_, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 9999)
	if err == nil {
		t.Fatal("expected RST refusal")
	}
	// ARP (20ms) + SYN (10ms) + RST (10ms).
	if got := c.Now(); got != 40*time.Millisecond {
		t.Errorf("clock = %v, want 40ms", got)
	}
	if len(pc1.tcpConns) != 0 {
		t.Error("failed connection left behind in the connection table")
	}
}

func TestTCPRetransmission(t *testing.T) {
	c, pc1, _ := setupPair(t, 10*time.Millisecond)
	// Warm the ARP cache so the SYN is the only link traversal we count.
	if _, err := pc1.Ping(mustAddr(t, "10.0.0.20")); err != nil {
		t.Fatal(err)
	}
	start := c.Now()

	conn := &TCPConn{
		stack:      pc1,
		state:      tcp.StateSynSent,
		localAddr:  pc1.addr.Addr(),
		localPort:  5555,
		remoteAddr: mustAddr(t, "10.0.0.20"),
		remotePort: 9999,
		sndNxt:     pc1.allocISN(),
		rto:        DefaultRTO,
	}
	syn := &tcp.Segment{SrcPort: 5555, DstPort: 9999, Seq: conn.sndNxt, Flags: tcp.FlagSYN, Window: tcpWindow}
	conn.sndNxt++
	conn.setRetx(syn)
	pc1.tcpConns[conn.key()] = conn

	// Simulate a silent peer: advance well past the RTO, then drive time.
	c.AdvanceBy(2 * time.Second)
	conn.tick()
	// The retransmitted SYN hits the unopened port and draws an RST.
	if conn.state != tcp.StateClosed {
		t.Errorf("state = %s, want CLOSED after RST", conn.state)
	}
	// SYN (10ms) + RST (10ms) from the resend.
	if got := c.Now(); got != start+2*time.Second+20*time.Millisecond {
		t.Errorf("clock = %v, want %v", got, start+2*time.Second+20*time.Millisecond)
	}
}

func TestTCPTimeWaitCleanup(t *testing.T) {
	c, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	if _, err := pc2.Listen(8080); err != nil {
		t.Fatal(err)
	}
	client, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 8080)
	if err != nil {
		t.Fatal(err)
	}
	server, err := pc2.tcpListeners[8080].Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if client.State() != tcp.StateTimeWait {
		t.Fatalf("client state = %s, want TIME-WAIT", client.State())
	}
	if _, ok := pc1.tcpConns[client.key()]; !ok {
		t.Fatal("TIME-WAIT connection not in table")
	}

	c.AdvanceBy(2 * tcpTimeWait)
	pc1.Tick()
	if _, ok := pc1.tcpConns[client.key()]; ok {
		t.Error("TIME-WAIT connection not cleaned up")
	}
}

func TestMalformedTransportDropped(t *testing.T) {
	_, pc1, _ := setupPair(t, 10*time.Millisecond)
	// An IPv4 packet claiming to be UDP but carrying garbage must be dropped
	// silently by the peer, not crash the stack or error the sender.
	f := ethernet.Frame{
		Dst:     mustMAC(t, "02:00:00:00:00:02"),
		Src:     mustMAC(t, "02:00:00:00:00:01"),
		Type:    ethernet.EtherTypeIPv4,
		Payload: ipv4.Packet{Src: mustAddr(t, "10.0.0.10"), Dst: mustAddr(t, "10.0.0.20"), TTL: 64, Protocol: ipv4.ProtoUDP, Payload: []byte{0xde, 0xad}}.Marshal(),
	}
	if err := pc1.iface.Send(f); err != nil {
		t.Fatalf("malformed transport caused error: %v", err)
	}
}

func TestDeterministicTCPScenario(t *testing.T) {
	run := func() (time.Duration, error) {
		c, pc1, pc2 := setupPair(t, 10*time.Millisecond)
		if _, err := pc2.Listen(8080); err != nil {
			return 0, err
		}
		client, err := pc1.Dial(mustAddr(t, "10.0.0.20"), 8080)
		if err != nil {
			return 0, err
		}
		server, err := pc2.tcpListeners[8080].Accept()
		if err != nil {
			return 0, err
		}
		if _, err := client.Write([]byte("hello")); err != nil {
			return 0, err
		}
		if err := client.Close(); err != nil {
			return 0, err
		}
		if err := server.Close(); err != nil {
			return 0, err
		}
		return c.Now(), nil
	}

	ta, err := run()
	if err != nil {
		t.Fatal(err)
	}
	tb, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if ta != tb {
		t.Errorf("non-deterministic: %v vs %v", ta, tb)
	}
}
