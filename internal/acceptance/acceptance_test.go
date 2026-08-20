// Package acceptance holds the end-to-end acceptance scenarios from
// ARCHITECTURE.md §15: PC1/PC2/R1/PC3 talking through a switch and a router,
// entirely inside one process with exact virtual-clock totals.
package acceptance

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/machine"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/router"
)

// topology is the §15 acceptance network:
//
//	PC1 10.0.0.10 ─┐
//	               ├─SW─ R1 eth0 10.0.0.1 ── R1 eth1 10.0.1.1 ── PC3 10.0.1.10
//	PC2 10.0.0.20 ─┘
//
// Every segment link has a 10ms propagation delay; the switch adds none.
type topology struct {
	c  *clock.VirtualClock
	sw *fabric.Switch
	r  *router.Router
}

// setupTopology builds the §15 network and returns the clock plus PC1, PC2,
// and PC3.
func setupTopology(t *testing.T) (*topology, *machine.Machine, *machine.Machine, *machine.Machine) {
	t.Helper()
	c := clock.New()
	sw := fabric.NewSwitch(c, 0)
	r := router.New(c, 0)

	pc1Iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	pc2Iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	pc3Iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:03"))
	pc1 := newMachine(t, c, "pc1", "10.0.0.10/24", pc1Iface)
	pc2 := newMachine(t, c, "pc2", "10.0.0.20/24", pc2Iface)
	pc3 := newMachine(t, c, "pc3", "10.0.1.10/24", pc3Iface)

	// LAN-A: PC1, PC2, and R1 eth0 behind the switch.
	attachToSwitch(t, c, sw, "p1", pc1Iface)
	attachToSwitch(t, c, sw, "p2", pc2Iface)
	rEth0 := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:0a"))
	attachToSwitch(t, c, sw, "p3", rEth0)
	if err := r.AddInterface("eth0", rEth0, netip.MustParsePrefix("10.0.0.1/24")); err != nil {
		t.Fatal(err)
	}

	// LAN-B: R1 eth1 point-to-point to PC3.
	rEth1 := fabric.NewInterface("eth1", mustMAC(t, "02:00:00:00:00:0b"))
	if err := r.AddInterface("eth1", rEth1, netip.MustParsePrefix("10.0.1.1/24")); err != nil {
		t.Fatal(err)
	}
	if _, err := fabric.NewLink(c, rEth1, pc3Iface, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Gateways: the only way out of each LAN is through R1.
	if err := pc1.Stack.AddRoute(netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("10.0.0.1"), "eth0", 0); err != nil {
		t.Fatal(err)
	}
	if err := pc3.Stack.AddRoute(netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("10.0.1.1"), "eth0", 0); err != nil {
		t.Fatal(err)
	}

	return &topology{c: c, sw: sw, r: r}, pc1, pc2, pc3
}

func newMachine(t *testing.T, c *clock.VirtualClock, name, prefix string, iface *fabric.Interface) *machine.Machine {
	t.Helper()
	m, err := machine.New(name, name, c, iface, netstack.Config{Addr: netip.MustParsePrefix(prefix)})
	if err != nil {
		t.Fatalf("machine %s: %v", name, err)
	}
	return m
}

// attachToSwitch links an interface to a switch port.
func attachToSwitch(t *testing.T, c *clock.VirtualClock, sw *fabric.Switch, portName string, iface *fabric.Interface) {
	t.Helper()
	port := fabric.NewInterface(portName, mustMAC(t, "02:00:00:00:00:00"))
	if _, err := fabric.NewLink(c, iface, port, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := sw.AddPort(portName, port); err != nil {
		t.Fatal(err)
	}
}

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func assertPing(t *testing.T, c *clock.VirtualClock, pc *machine.Machine, dst string, wantRTT string, wantClock time.Duration) {
	t.Helper()
	out, err := pc.RunCommand("ping " + dst)
	if err != nil {
		t.Fatalf("ping %s failed: %v\n%s", dst, err, out)
	}
	want := "64 bytes from " + dst + ": icmp_seq=1 ttl=64 time=" + wantRTT + " ms"
	if !strings.Contains(out, want) {
		t.Errorf("ping %s output missing %q:\n%s", dst, want, out)
	}
	if got := c.Now(); got != wantClock {
		t.Errorf("clock after ping %s = %v, want %v", dst, got, wantClock)
	}
}

func TestSameSubnetPing(t *testing.T) {
	tp, pc1, _, _ := setupTopology(t)

	// Cold 90ms: PC1's ARP for PC2 floods both switch ports; PC2's reply
	// (40ms) plus ICMP round trip (40ms) plus the flood's p3 leg (10ms)
	// that the ARP request Send waits on. Warm 40ms: no ARP.
	assertPing(t, tp.c, pc1, "10.0.0.20", "90.000", 90*time.Millisecond)
	assertPing(t, tp.c, pc1, "10.0.0.20", "40.000", 130*time.Millisecond)
}

func TestCrossSubnetPing(t *testing.T) {
	tp, pc1, _, _ := setupTopology(t)

	// Cold 130ms: gateway ARP 50 + ICMP 80 across R1 (req 20 + R1→PC3 20 +
	// reply 20 + R1→PC1 20) with R1's eth1 ARP 20 in between. Warm 60ms:
	// no ARP at all.
	assertPing(t, tp.c, pc1, "10.0.1.10", "130.000", 130*time.Millisecond)
	assertPing(t, tp.c, pc1, "10.0.1.10", "60.000", 190*time.Millisecond)
}

func TestRouterSelfPing(t *testing.T) {
	tp, pc1, _, _ := setupTopology(t)

	// Cold 90ms: gateway ARP 50 + ICMP to the router (40ms round trip).
	assertPing(t, tp.c, pc1, "10.0.0.1", "90.000", 90*time.Millisecond)
	assertPing(t, tp.c, pc1, "10.0.0.1", "40.000", 130*time.Millisecond)
}

func TestReversePing(t *testing.T) {
	tp, _, _, pc3 := setupTopology(t)

	// Cold 130ms: PC3↔R1 ARP 20 + ICMP req 10 + R1's eth0 ARP for PC1 50
	// (request floods PC2 first; PC1's reply + the p2 leg = 40ms, plus the
	// p1 delivery) + req 20 + reply 20 + forward 10. Warm 60ms: no ARP.
	assertPing(t, tp.c, pc3, "10.0.0.10", "130.000", 130*time.Millisecond)
	assertPing(t, tp.c, pc3, "10.0.0.10", "60.000", 190*time.Millisecond)
}

func TestPingUnreachableHost(t *testing.T) {
	_, pc1, _, _ := setupTopology(t)

	out, err := pc1.RunCommand("ping 10.0.0.99")
	if err == nil {
		t.Fatal("ping to a silent host should fail")
	}
	if !strings.Contains(out, "Request timeout for icmp_seq 1") {
		t.Errorf("missing timeout line:\n%s", out)
	}
	if !strings.Contains(out, "1 packets transmitted, 0 received, 100.0% packet loss") {
		t.Errorf("missing loss stats:\n%s", out)
	}
}

func TestRouterAndSwitchState(t *testing.T) {
	tp, pc1, pc2, _ := setupTopology(t)

	if _, err := pc1.RunCommand("ping 10.0.0.20"); err != nil {
		t.Fatal(err)
	}
	if _, err := pc1.RunCommand("ping 10.0.1.10"); err != nil {
		t.Fatal(err)
	}
	// PC2 must originate traffic for R1 to learn it: pinging the router
	// forces an ARP request for 10.0.0.1 that broadcasts to the segment.
	if _, err := pc2.RunCommand("ping 10.0.0.1"); err != nil {
		t.Fatal(err)
	}

	// R1 learned PC1 and PC2 on eth0, PC3 on eth1.
	eth0 := tp.r.ARPEntries("eth0")
	if !hasARP(eth0, "10.0.0.10") || !hasARP(eth0, "10.0.0.20") {
		t.Errorf("router eth0 ARP missing PC1/PC2:\n%v", eth0)
	}
	eth1 := tp.r.ARPEntries("eth1")
	if !hasARP(eth1, "10.0.1.10") {
		t.Errorf("router eth1 ARP missing PC3:\n%v", eth1)
	}

	// The switch learned PC1, PC2, and R1's eth0 MAC.
	if got := len(tp.sw.ForwardingTable()); got < 3 {
		t.Errorf("switch forwarding table has %d entries, want >= 3", got)
	}

	// PC1's shell sees the default route through R1.
	out, err := pc1.RunCommand("ip route")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "default via 10.0.0.1 dev eth0") {
		t.Errorf("pc1 ip route missing default route:\n%s", out)
	}

	// PC2 never configured a gateway: only its connected route exists.
	if out, err := pc2.RunCommand("ip route"); err != nil {
		t.Fatal(err)
	} else if strings.Contains(out, "default") {
		t.Errorf("pc2 unexpectedly has a default route:\n%s", out)
	}
}

func hasARP(entries []arp.KeyedEntry, ip string) bool {
	for _, e := range entries {
		if e.IP == netip.MustParseAddr(ip) {
			return true
		}
	}
	return false
}

func TestAcceptanceDeterminism(t *testing.T) {
	run := func() (time.Duration, string) {
		tp, pc1, _, pc3 := setupTopology(t)
		var sb strings.Builder
		for _, dst := range []string{"10.0.0.20", "10.0.1.10", "10.0.0.1"} {
			out, err := pc1.RunCommand("ping " + dst)
			if err != nil {
				t.Fatal(err)
			}
			sb.WriteString(out)
		}
		out, err := pc3.RunCommand("ping 10.0.0.10")
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString(out)
		return tp.c.Now(), sb.String()
	}

	t1, o1 := run()
	t2, o2 := run()
	if t1 != t2 || o1 != o2 {
		t.Errorf("non-deterministic acceptance run:\n%v vs %v\n%s\n---\n%s", t1, t2, o1, o2)
	}
}
