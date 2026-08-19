package netstack

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/netstack/icmp"
)

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

// setupPair builds PC1 and PC2 connected by a 10ms point-to-point link, the
// acceptance-test topology from ARCHITECTURE.md §15 (same subnet).
func setupPair(t *testing.T, delay time.Duration) (*clock.VirtualClock, *Stack, *Stack) {
	t.Helper()
	c := clock.New()
	pc1if := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	pc2if := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	pc1, err := New(c, pc1if, Config{Addr: mustPrefix(t, "10.0.0.10/24")})
	if err != nil {
		t.Fatalf("New(pc1): %v", err)
	}
	pc2, err := New(c, pc2if, Config{Addr: mustPrefix(t, "10.0.0.20/24")})
	if err != nil {
		t.Fatalf("New(pc2): %v", err)
	}
	if _, err := fabric.NewLink(c, pc1if, pc2if, delay); err != nil {
		t.Fatal(err)
	}
	return c, pc1, pc2
}

func TestAcceptanceSameSubnetPing(t *testing.T) {
	c, pc1, _ := setupPair(t, 10*time.Millisecond)

	res, err := pc1.Ping(mustAddr(t, "10.0.0.20"))
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if res.Reply.Type != icmp.TypeEchoReply {
		t.Errorf("Reply.Type = %d, want echo reply", res.Reply.Type)
	}
	// Cold ARP: request + reply + echo request + echo reply = 4 link traversals.
	if want := 40 * time.Millisecond; res.RTT != want {
		t.Errorf("cold RTT = %v, want %v", res.RTT, want)
	}
	if got := c.Now(); got != 40*time.Millisecond {
		t.Errorf("virtual clock = %v, want 40ms", got)
	}
}

func TestAcceptanceWarmARP(t *testing.T) {
	c, pc1, _ := setupPair(t, 10*time.Millisecond)
	dst := mustAddr(t, "10.0.0.20")

	if _, err := pc1.Ping(dst); err != nil {
		t.Fatalf("cold ping: %v", err)
	}
	if got := c.Now(); got != 40*time.Millisecond {
		t.Fatalf("clock after cold ping = %v, want 40ms", got)
	}

	// Warm ARP: only the echo round trip traverses the link.
	res, err := pc1.Ping(dst)
	if err != nil {
		t.Fatalf("warm ping: %v", err)
	}
	if want := 20 * time.Millisecond; res.RTT != want {
		t.Errorf("warm RTT = %v, want %v", res.RTT, want)
	}
	if got := c.Now(); got != 60*time.Millisecond {
		t.Errorf("virtual clock = %v, want 60ms", got)
	}
}

func TestPingBothDirections(t *testing.T) {
	_, pc1, pc2 := setupPair(t, 5*time.Millisecond)

	if _, err := pc2.Ping(pc1.Addr()); err != nil {
		t.Fatalf("pc2 → pc1: %v", err)
	}
	if _, err := pc1.Ping(pc2.Addr()); err != nil {
		t.Fatalf("pc1 → pc2: %v", err)
	}
}

func TestPingNoSuchHostOnSubnet(t *testing.T) {
	_, pc1, _ := setupPair(t, 10*time.Millisecond)
	// 10.0.0.99 is on the subnet but nothing answers ARP for it.
	_, err := pc1.Ping(mustAddr(t, "10.0.0.99"))
	if err == nil {
		t.Fatal("expected error pinging a non-existent host")
	}
}

func TestPingNoRoute(t *testing.T) {
	_, pc1, _ := setupPair(t, 10*time.Millisecond)
	if _, err := pc1.Ping(mustAddr(t, "192.168.5.5")); err == nil {
		t.Fatal("expected no-route error")
	}
}

func TestNewRejectsNonIPv4(t *testing.T) {
	c := clock.New()
	iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	if _, err := New(c, iface, Config{Addr: mustPrefix(t, "fe80::1/64")}); err == nil {
		t.Fatal("expected error for IPv6 address")
	}
}

func TestNewRejectsInvalidPrefix(t *testing.T) {
	c := clock.New()
	iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	if _, err := New(c, iface, Config{Addr: netip.Prefix{}}); err == nil {
		t.Fatal("expected error for invalid prefix")
	}
}

func TestMalformedFramesDroppedSilently(t *testing.T) {
	c, pc1, _ := setupPair(t, 10*time.Millisecond)
	// A frame with a garbage ARP payload must not crash the peer or error the
	// sender; the stack drops malformed packets (ARCHITECTURE.md §40).
	f := ethernet.Frame{
		Dst:     mustMAC(t, "02:00:00:00:00:02"),
		Src:     mustMAC(t, "02:00:00:00:00:01"),
		Type:    ethernet.EtherTypeARP,
		Payload: []byte{0xde, 0xad, 0xbe, 0xef},
	}
	if err := pc1.iface.Send(f); err != nil {
		t.Fatalf("malformed frame caused error: %v", err)
	}
	if got := c.Now(); got != 10*time.Millisecond {
		t.Errorf("clock = %v, want 10ms (frame still traversed the link)", got)
	}
}

func TestUnknownEtherTypeDropped(t *testing.T) {
	_, pc1, _ := setupPair(t, 10*time.Millisecond)
	f := ethernet.Frame{
		Dst:     mustMAC(t, "02:00:00:00:00:02"),
		Src:     mustMAC(t, "02:00:00:00:00:01"),
		Type:    ethernet.EtherType(0x88B5),
		Payload: []byte{1, 2, 3},
	}
	if err := pc1.iface.Send(f); err != nil {
		t.Fatalf("unknown EtherType caused error: %v", err)
	}
}

func TestDeterministicPingScenario(t *testing.T) {
	run := func() (time.Duration, error) {
		c, pc1, _ := setupPair(t, 10*time.Millisecond)
		if _, err := pc1.Ping(mustAddr(t, "10.0.0.20")); err != nil {
			return 0, err
		}
		// A non-existent host on the subnet must fail deterministically too.
		if _, err := pc1.Ping(mustAddr(t, "10.0.0.99")); err == nil {
			return 0, fmt.Errorf("expected no-ARP-reply error")
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
