package fabric

import (
	"fmt"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// testSwitch wires a switch with n ports, each connected to a host interface
// whose frames land in a collector. Returns the switch, the host interfaces,
// and the collectors.
func testSwitch(t *testing.T, c *clock.VirtualClock, delay time.Duration, n int) (*Switch, []*Interface, []*collector) {
	t.Helper()
	sw := NewSwitch(c, 30*time.Second)
	hosts := make([]*Interface, n)
	cols := make([]*collector, n)
	for i := 0; i < n; i++ {
		hosts[i] = NewInterface(fmt.Sprintf("eth%d", i), macAddr(i+1))
		port := NewInterface(fmt.Sprintf("p%d", i), macAddr(0x100+i))
		if _, err := NewLink(c, hosts[i], port, delay); err != nil {
			t.Fatal(err)
		}
		if err := sw.AddPort(port.Name, port); err != nil {
			t.Fatal(err)
		}
		cols[i] = &collector{}
		hosts[i].Attach(cols[i])
	}
	return sw, hosts, cols
}

// macAddr builds a deterministic MAC from a small int.
func macAddr(n int) ethernet.MAC {
	return ethernet.MAC{0x02, 0x00, 0x00, 0x00, byte(n >> 8), byte(n)}
}

func sendFrame(from *Interface, dst ethernet.MAC, src ethernet.MAC) error {
	return from.Send(ethernet.Frame{Dst: dst, Src: src, Type: ethernet.EtherTypeIPv4, Payload: []byte{0xde, 0xad}})
}

func TestSwitchBroadcast(t *testing.T) {
	c := clock.New()
	_, hosts, cols := testSwitch(t, c, 10*time.Millisecond, 3)

	if err := sendFrame(hosts[0], ethernet.BroadcastMAC, macAddr(1)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		if len(cols[i].frames) != 1 {
			t.Errorf("port %d received %d frames, want 1", i, len(cols[i].frames))
		}
	}
	if len(cols[0].frames) != 0 {
		t.Errorf("broadcast looped back to the sender: %d frames", len(cols[0].frames))
	}
}

func TestSwitchUnicastLearned(t *testing.T) {
	c := clock.New()
	sw, hosts, cols := testSwitch(t, c, 10*time.Millisecond, 3)

	// A→B: B's MAC is unknown, so the frame floods — but now the switch
	// learned A (port 0) and B (port 1).
	if err := sendFrame(hosts[0], macAddr(2), macAddr(1)); err != nil {
		t.Fatal(err)
	}
	if len(cols[1].frames) != 1 {
		t.Fatalf("B received %d frames, want 1", len(cols[1].frames))
	}
	beforeA, beforeC := len(cols[0].frames), len(cols[2].frames)

	// B→A: A's MAC is known, so the frame goes only to port 0.
	if err := sendFrame(hosts[1], macAddr(1), macAddr(2)); err != nil {
		t.Fatal(err)
	}
	if got := len(cols[0].frames) - beforeA; got != 1 {
		t.Errorf("A received %d frames, want 1", got)
	}
	if got := len(cols[2].frames) - beforeC; got != 0 {
		t.Errorf("C received %d frames on a learned unicast, want 0", got)
	}

	// Table: A→p0, B→p1.
	table := sw.ForwardingTable()
	if len(table) != 2 {
		t.Errorf("forwarding table has %d entries, want 2:\n%+v", len(table), table)
	}
}

func TestSwitchUnknownFlood(t *testing.T) {
	c := clock.New()
	_, hosts, cols := testSwitch(t, c, 10*time.Millisecond, 3)

	unknown := ethernet.MAC{0x02, 0xff, 0xff, 0xff, 0xff, 0xff}
	if err := sendFrame(hosts[0], unknown, macAddr(1)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		if len(cols[i].frames) != 1 {
			t.Errorf("unknown destination not flooded to port %d", i)
		}
	}
}

func TestSwitchAging(t *testing.T) {
	c := clock.New()
	sw, hosts, cols := testSwitch(t, c, 10*time.Millisecond, 2)

	if err := sendFrame(hosts[0], ethernet.BroadcastMAC, macAddr(1)); err != nil {
		t.Fatal(err)
	}
	if len(cols[1].frames) != 1 {
		t.Fatal("initial flood failed")
	}

	// After aging, the entry must be forgotten.
	if err := c.AdvanceBy(31 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(hosts[1], macAddr(1), macAddr(2)); err != nil {
		t.Fatal(err)
	}
	if len(cols[0].frames) != 1 {
		t.Errorf("expired entry still forwarded unicast (got %d frames, want flood/1)", len(cols[0].frames))
	}
	_ = sw
}

func TestSwitchDeterministicFlood(t *testing.T) {
	run := func() []int {
		c := clock.New()
		_, hosts, cols := testSwitch(t, c, 10*time.Millisecond, 3)
		if err := sendFrame(hosts[0], ethernet.BroadcastMAC, macAddr(1)); err != nil {
			t.Fatal(err)
		}
		return []int{len(cols[0].frames), len(cols[1].frames), len(cols[2].frames)}
	}
	a, b := run(), run()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic flood: %v vs %v", a, b)
		}
	}
}
