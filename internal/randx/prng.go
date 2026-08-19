// Package randx provides the simulation-owned deterministic PRNG.
//
// The entire virtual world draws randomness from this generator only.
// It is seeded by the Simulation and its state is serializable, so that
// identical seeds reproduce identical behavior (ARCHITECTURE.md §12.1).
package randx

// PRNG is a deterministic xorshift64* generator.
//
// State is a single uint64, which makes snapshots and checkpoints trivial:
// no hidden allocation or OS-provided randomness is involved.
type PRNG struct {
	state uint64
}

const multiplier = 0x2545F4914F6CDD1D // 2685821657736338717

// New returns a PRNG seeded with the given seed. A zero seed is remapped
// to a non-zero state so the generator never gets stuck.
func New(seed uint64) *PRNG {
	p := &PRNG{}
	p.Seed(seed)
	return p
}

// Seed resets the generator to a known state.
func (p *PRNG) Seed(seed uint64) {
	p.state = seed
	if p.state == 0 {
		p.state = 1
	}
}

// Uint64 returns the next 64-bit value.
func (p *PRNG) Uint64() uint64 {
	x := p.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	p.state = x
	return x * multiplier
}

// Uint64n returns a uniformly distributed value in [0, n) without modulo bias.
// It panics if n == 0.
func (p *PRNG) Uint64n(n uint64) uint64 {
	if n == 0 {
		panic("randx: Uint64n called with n == 0")
	}
	if n&(n-1) == 0 {
		return p.Uint64() & (n - 1)
	}
	threshold := ^uint64(0) - ^uint64(0)%n
	for {
		if v := p.Uint64(); v < threshold {
			return v % n
		}
	}
}

// Float64 returns a uniformly distributed value in [0, 1).
func (p *PRNG) Float64() float64 {
	return float64(p.Uint64()>>11) / (1 << 53)
}

// State returns the current internal state, usable with Restore.
func (p *PRNG) State() uint64 {
	return p.state
}

// Restore resets the generator to a previously captured state.
func (p *PRNG) Restore(state uint64) {
	if state == 0 {
		panic("randx: Restore called with zero state")
	}
	p.state = state
}

// Copy returns an independent generator with identical state.
func (p *PRNG) Copy() *PRNG {
	return &PRNG{state: p.state}
}
