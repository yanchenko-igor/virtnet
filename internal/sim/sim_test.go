package sim

import (
	"bytes"
	"testing"
	"time"
)

// runScript exercises a deterministic sequence of clock advances and RNG draws,
// mimicking a causal chain (link delays, loss rolls, ARP timeouts).
func runScript(seed uint64) ([32]byte, error) {
	s := New(seed)
	c := s.Clock()
	rng := s.RNG()

	// Link 1: 10 ms propagation delay, one loss roll.
	if err := c.AdvanceBy(10 * time.Millisecond); err != nil {
		return [32]byte{}, err
	}
	_ = rng.Uint64n(100)

	// Link 2: 20 ms propagation delay, one corruption roll.
	if err := c.AdvanceBy(20 * time.Millisecond); err != nil {
		return [32]byte{}, err
	}
	_ = rng.Float64()

	// Explicit jump to a future timestamp (ARP expiration style).
	if err := c.AdvanceTo(130 * time.Millisecond); err != nil {
		return [32]byte{}, err
	}

	return s.Digest()
}

func TestSameSeedProducesIdenticalState(t *testing.T) {
	tests := []struct {
		name string
		seed uint64
	}{
		{name: "zero seed", seed: 0},
		{name: "typical seed", seed: 12345},
		{name: "large seed", seed: 0xCAFEBABEDEADBEEF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, errA := runScript(tt.seed)
			b, errB := runScript(tt.seed)
			if errA != nil {
				t.Fatalf("first run: %v", errA)
			}
			if errB != nil {
				t.Fatalf("second run: %v", errB)
			}
			if a != b {
				t.Errorf("same seed produced different digests:\n  a = %x\n  b = %x", a, b)
			}
		})
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a, _ := runScript(1)
	b, _ := runScript(2)
	if bytes.Equal(a[:], b[:]) {
		t.Fatal("different seeds produced identical digests")
	}
}

func TestDeterministicCausalOrdering(t *testing.T) {
	// Order of RNG draws matters: advancing the clock between draws must not
	// change the sequence of outcomes relative to the same script.
	a, _ := runScript(42)
	b, _ := runScript(42)
	if a != b {
		t.Fatal("causal ordering not deterministic")
	}
}

func TestStateRoundTripReproduces(t *testing.T) {
	// Capture state mid-script, continue, restore, and continue again: the
	// restored simulation must reproduce the exact same subsequent state.
	seed := uint64(777)

	run := func(s *Simulation, phase int) error {
		c := s.Clock()
		rng := s.RNG()
		for i := 0; i < phase; i++ {
			if err := c.AdvanceBy(5 * time.Millisecond); err != nil {
				return err
			}
			_ = rng.Uint64n(1000)
		}
		return nil
	}

	// Reference: run to phase 3 and capture.
	ref := New(seed)
	if err := run(ref, 3); err != nil {
		t.Fatal(err)
	}
	captured := ref.State()

	// Restore into a fresh sim, run to phase 3, then continue to phase 6.
	restored := New(seed)
	if err := run(restored, 3); err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(captured); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := run(restored, 3); err != nil {
		t.Fatal(err)
	}

	// Continue the reference to phase 6 without restore.
	if err := run(ref, 3); err != nil {
		t.Fatal(err)
	}

	ra, err := ref.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rb, err := restored.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if ra != rb {
		t.Errorf("restored simulation diverged:\n  ref      = %x\n  restored = %x", ra, rb)
	}
}

func TestRestoreRejectsNegativeClock(t *testing.T) {
	s := New(1)
	err := s.Restore(SimulationState{VirtualClock: -time.Second, RandomState: 1})
	if err == nil {
		t.Fatal("expected error for negative virtual clock")
	}
}

func TestStateCapturesClock(t *testing.T) {
	s := New(1)
	if err := s.Clock().AdvanceBy(250 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.VirtualClock != 250*time.Millisecond {
		t.Errorf("State.VirtualClock = %v, want 250ms", st.VirtualClock)
	}
	if st.RandomState == 0 {
		t.Error("State.RandomState should be non-zero after construction")
	}
}
