// Package clock implements the virtual clock.
//
// Virtual time is an explicit value of the simulation state and is completely
// independent of host wall-clock time (ARCHITECTURE.md §5.2). The clock only
// ever moves forward during causal execution; Set is reserved for checkpoint
// restore, which may jump in either direction.
package clock

import (
	"fmt"
	"time"
)

// VirtualClock tracks simulation time as a time.Duration.
//
// time.Duration provides nanosecond resolution and serializes as an integer,
// which keeps determinism and checkpointing trivial.
type VirtualClock struct {
	current time.Duration
}

// New returns a clock starting at virtual time zero.
func New() *VirtualClock {
	return &VirtualClock{}
}

// Now returns the current virtual time.
func (c *VirtualClock) Now() time.Duration {
	return c.current
}

// AdvanceBy moves the clock forward by d. It returns an error if d is negative,
// since time never flows backwards during causal execution.
func (c *VirtualClock) AdvanceBy(d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("clock: cannot advance by negative duration %v", d)
	}
	c.current += d
	return nil
}

// AdvanceTo moves the clock to an absolute timestamp t. It returns an error if
// t is before the current virtual time.
func (c *VirtualClock) AdvanceTo(t time.Duration) error {
	if t < c.current {
		return fmt.Errorf("clock: cannot advance backwards from %v to %v", c.current, t)
	}
	c.current = t
	return nil
}

// Set unconditionally sets the current virtual time. It is used only for
// checkpoint restore, where jumping in either direction is legitimate.
func (c *VirtualClock) Set(t time.Duration) {
	if t < 0 {
		panic("clock: negative virtual time")
	}
	c.current = t
}
