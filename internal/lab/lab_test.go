package lab

import (
	"testing"
	"time"
)

// runPings drives the same script twice and returns the final clock and the
// captured ARP sources, so determinism is asserted end to end.
func runPings(t *testing.T) (time.Duration, []string) {
	t.Helper()
	l, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	pc1 := l.Machine("pc1")
	pc3 := l.Machine("pc3")
	if pc1 == nil || pc3 == nil {
		t.Fatal("lab missing machines")
	}
	if _, err := pc1.RunCommand("ping 10.0.0.20"); err != nil {
		t.Fatalf("pc1->pc2: %v", err)
	}
	if _, err := pc1.RunCommand("ping 10.0.1.10"); err != nil {
		t.Fatalf("pc1->pc3: %v", err)
	}
	if _, err := pc3.RunCommand("ping 10.0.0.10"); err != nil {
		t.Fatalf("pc3->pc1: %v", err)
	}
	return l.Clock.Now(), l.Capture.SrcIPs()
}

// TestRunQuiescent advances time through the machine scheduler and steps the
// machines without deadlock or error.
func TestRunQuiescent(t *testing.T) {
	l, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	// Launch a background listener on PC2, then have PC1 connect to it. The
	// listener only progresses when the lab is run quiescent.
	if _, err := l.Machine("pc2").RunCommand("nc -l 9000"); err != nil {
		t.Fatalf("nc -l: %v", err)
	}
	if _, err := l.Machine("pc1").RunCommand("nc 10.0.0.20 9000 hello"); err != nil {
		t.Fatalf("nc: %v", err)
	}
	l.RunQuiescent()
	if l.Clock.Now() == 0 {
		t.Fatal("RunQuiescent did not advance the clock")
	}
}

// Test15ClockTotals locks the exact virtual-clock totals for the §15 topology.
// These match the acceptance suite: the flood-leg semantics of a broadcast ARP
// make the cold pings 90/130/130ms. Each ping command also costs 1ms.
func Test15ClockTotals(t *testing.T) {
	t1, srcs1 := runPings(t)
	t2, srcs2 := runPings(t)
	if t1 != t2 {
		t.Errorf("non-deterministic lab run: %v vs %v", t1, t2)
	}
	want := []string{"10.0.0.1", "10.0.0.10", "10.0.0.20", "10.0.1.1", "10.0.1.10"}
	if len(srcs1) != len(want) || len(srcs2) != len(want) {
		t.Fatalf("capture saw %v and %v ARP senders, want %v", srcs1, srcs2, want)
	}
	for i := range want {
		if srcs1[i] != want[i] || srcs2[i] != want[i] {
			t.Fatalf("capture ARP senders = %v / %v, want %v", srcs1, srcs2, want)
		}
	}
	if t1 != 283*time.Millisecond {
		t.Errorf("final clock = %v, want 283ms (90+1 + 130+1 + 60+1)", t1)
	}
}
