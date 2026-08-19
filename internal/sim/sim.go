// Package sim owns the single-process virtual world.
//
// The Simulation is the root object: it owns the virtual clock and the seeded
// PRNG, and will own all machines and network devices added in later phases.
// It contains no scheduler and no event queue — causal operations execute
// synchronously and advance the virtual clock themselves (ARCHITECTURE.md §5).
package sim

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/randx"
)

// Simulation is the root of the virtual world.
//
// A Simulation is not safe for concurrent use. Determinism requires single-
// threaded execution (ARCHITECTURE.md §10); the UI serializes input.
type Simulation struct {
	clock *clock.VirtualClock
	rng   *randx.PRNG
}

// New returns a Simulation with the given randomness seed. The same seed and
// the same external inputs must always produce the same behavior.
func New(seed uint64) *Simulation {
	return &Simulation{
		clock: clock.New(),
		rng:   randx.New(seed),
	}
}

// Clock returns the virtual clock. Operations advance it to reflect simulated
// temporal costs; nothing ever waits for it.
func (s *Simulation) Clock() *clock.VirtualClock {
	return s.clock
}

// RNG returns the simulation-owned deterministic generator. All randomness in
// the virtual world must come from this generator.
func (s *Simulation) RNG() *randx.PRNG {
	return s.rng
}

// SimulationState is a serializable snapshot of the entire virtual world.
//
// Phase 1 holds the clock and PRNG state; later phases extend it with machines,
// networks, sockets, filesystems, and so on. It is the unit of determinism
// testing (this phase), and of checkpointing and replay (phase 8).
type SimulationState struct {
	VirtualClock time.Duration `json:"virtual_clock"`
	RandomState  uint64        `json:"random_state"`
}

// State captures the current simulation state.
func (s *Simulation) State() SimulationState {
	return SimulationState{
		VirtualClock: s.clock.Now(),
		RandomState:  s.rng.State(),
	}
}

// Restore replaces the simulation state with a previously captured snapshot.
// It is the building block for checkpoint restore and rewind (ARCHITECTURE.md
// §12.2). Only the state is replaced; the owning Simulation remains intact.
func (s *Simulation) Restore(st SimulationState) error {
	if st.VirtualClock < 0 {
		return fmt.Errorf("sim: cannot restore negative virtual clock %v", st.VirtualClock)
	}
	s.clock.Set(st.VirtualClock)
	s.rng.Restore(st.RandomState)
	return nil
}

// Digest returns a deterministic hash of the full simulation state. Two states
// with equal digests are behaviorally identical; the digest is the assertion
// primitive for determinism tests.
func (s *Simulation) Digest() ([32]byte, error) {
	b, err := json.Marshal(s.State())
	if err != nil {
		return [32]byte{}, fmt.Errorf("sim: serialize state: %w", err)
	}
	return sha256.Sum256(b), nil
}
