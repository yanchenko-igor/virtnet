package lab

import (
	"strings"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/machine"
)

// step is one recorded external input: a command issued on a machine at a
// specific virtual time. Replay feeds the same steps to a fresh world and must
// reproduce it exactly (ARCHITECTURE.md §12.2).
type step struct {
	Time time.Duration
	Host string
	Line string
}

// labScript drives the reference lab through a deterministic workload that
// exercises every serializable surface: ARP caches, switch learning, TCP
// listeners with backlog children, a live Waiting nc process, UDP-free
// routing, and transcripts. Scripted is returns true after the checkpoint.
func labScript(l *Lab, steps *[]step) {
	pc1 := l.Machine("pc1")
	pc3 := l.Machine("pc3")

	run := func(m *machine.Machine, line string) {
		if steps != nil {
			*steps = append(*steps, step{Time: l.Clock.Now(), Host: m.Hostname, Line: line})
		}
		m.HandleInput(line)
	}

	// Segment 1 (before the checkpoint): caches warm up and an in-flight TCP
	// exchange is left mid-way.
	run(pc1, "ping 10.0.0.20")
	run(pc3, "ping 10.0.1.10")
	run(pc1, "nc -l 7000")
	run(pc3, "nc 10.0.0.10 7000 hello")
	// At this point pc1's nc is Waiting with a backlog child carrying "hello".
}

// continueScript is the workload applied after the checkpoint on both the
// original and the restored world.
func continueScript(l *Lab) {
	pc1 := l.Machine("pc1")
	pc2 := l.Machine("pc2")
	pc3 := l.Machine("pc3")

	// Drain the listener parked before the checkpoint.
	pc1.Step()

	pc3.HandleInput("ping -c 2 10.0.0.20")
	pc2.HandleInput("nc -l 8000")
	pc1.HandleInput("nc 10.0.0.20 8000 data2")
	pc2.Step()
}

// TestSnapshotRestoreRoundTrip verifies that a restored world is byte-for-byte
// equivalent to the original at the checkpoint, and that both worlds continue
// identically from there.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	a, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	labScript(a, nil)
	mid := a.Snapshot()

	b, err := RestoreWorld(mid)
	if err != nil {
		t.Fatalf("RestoreWorld: %v", err)
	}

	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("restored world diverged at checkpoint:\n  a = %x\n  b = %x", da, db)
	}
	if b.Clock.Now() != a.Clock.Now() {
		t.Fatalf("restored clock = %v, want %v", b.Clock.Now(), a.Clock.Now())
	}

	continueScript(a)
	continueScript(b)

	da, _ = a.Digest()
	db, _ = b.Digest()
	if da != db {
		t.Fatalf("restored world diverged after continuing:\n  a = %x\n  b = %x", da, db)
	}
	for _, name := range []string{"pc1", "pc2", "pc3"} {
		ta := a.Machine(name).Console.Transcript()
		tb := b.Machine(name).Console.Transcript()
		if ta != tb {
			t.Fatalf("%s transcript diverged after restore:\n--- a ---\n%s\n--- b ---\n%s", name, ta, tb)
		}
	}
	if a.Capture.Len() != b.Capture.Len() {
		t.Fatalf("capture length diverged: a=%d b=%d", a.Capture.Len(), b.Capture.Len())
	}
}

// TestReplayReproducesIdenticalWorld records the external inputs of a full run
// and replays them against a fresh lab, asserting the same digest: the world
// is fully reproducible from its (virtual time, machine, command) trace.
func TestReplayReproducesIdenticalWorld(t *testing.T) {
	var steps []step
	a, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	labScript(a, &steps)
	continueScript(a)
	digestA, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}

	b, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if b.Clock.Now() != s.Time {
			t.Fatalf("replay clock = %v at %q, want %v", b.Clock.Now(), s.Line, s.Time)
		}
		m := b.Machine(s.Host)
		if m == nil {
			t.Fatalf("replay: no machine %q", s.Host)
		}
		m.HandleInput(s.Line)
	}
	continueScript(b)
	digestB, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digestA != digestB {
		t.Fatalf("replay diverged:\n  original = %x\n  replay   = %x", digestA, digestB)
	}
}

// TestDigestDeterministic asserts that a quiescent lab's digest is stable
// across repeated captures.
func TestDigestDeterministic(t *testing.T) {
	l, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	l.Machine("pc1").HandleInput("ping 10.0.0.20")
	d1, _ := l.Digest()
	d2, _ := l.Digest()
	if d1 != d2 {
		t.Fatalf("digest not deterministic:\n  %x\n  %x", d1, d2)
	}
}

// TestNew15AtDateCommand anchors the clock at a start timestamp and verifies
// the date command reads it, and that the anchor survives a checkpoint.
func TestNew15AtDateCommand(t *testing.T) {
	start := time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC)
	l, err := New15At(start)
	if err != nil {
		t.Fatal(err)
	}

	pc1 := l.Machine("pc1")
	pc1.HandleInput("date")
	if tr := pc1.Console.Transcript(); !strings.Contains(tr, "Thu Jan 15 08:00:00.000 UTC 2026 (t=0s)\n") {
		t.Fatalf("date at start = %q", tr)
	}

	pc1.HandleInput("ping 10.0.0.20") // advances the clock by 90ms (cold)
	pc1.HandleInput("date")
	if tr := pc1.Console.Transcript(); !strings.Contains(tr, "Thu Jan 15 08:00:00.090 UTC 2026 (t=90ms)\n") {
		t.Fatalf("date after ping = %q", tr)
	}

	// The anchor is part of the world snapshot: a restored lab reads the same
	// date.
	restored, err := RestoreWorld(l.Snapshot())
	if err != nil {
		t.Fatalf("RestoreWorld: %v", err)
	}
	out, err := restored.Machine("pc1").RunCommand("date")
	if err != nil {
		t.Fatalf("date after restore: %v", err)
	}
	if out != "Thu Jan 15 08:00:00.090 UTC 2026 (t=90ms)\n" {
		t.Fatalf("date after restore = %q", out)
	}
}

// TestDefaultLabDateAtEpoch asserts the unanchored lab reads the Unix epoch.
func TestDefaultLabDateAtEpoch(t *testing.T) {
	l, err := New15()
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Machine("pc1").RunCommand("date")
	if err != nil {
		t.Fatalf("date: %v", err)
	}
	if out != "Thu Jan  1 00:00:00.000 UTC 1970 (t=0s)\n" {
		t.Fatalf("default date = %q", out)
	}
}
