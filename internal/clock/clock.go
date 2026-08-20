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

// VirtualClock tracks simulation time as a time.Duration offset from a fixed
// wall-time epoch.
//
// time.Duration provides nanosecond resolution and serializes as an integer,
// which keeps determinism and checkpointing trivial. The epoch anchor is what
// the `date` command reads: virtual time itself never consults the host clock.
type VirtualClock struct {
	base    time.Time
	current time.Duration
}

// New returns a clock anchored at the Unix epoch, starting at virtual time
// zero.
func New() *VirtualClock {
	return NewAt(time.Unix(0, 0))
}

// NewAt returns a clock whose wall time reads as base plus the elapsed virtual
// time: WallTime() = base.Add(current). The base is normalized to UTC so a
// snapshot serializes deterministically regardless of the input's zone.
func NewAt(base time.Time) *VirtualClock {
	return &VirtualClock{base: base.UTC()}
}

// Base returns the wall-time epoch the clock is anchored to.
func (c *VirtualClock) Base() time.Time {
	return c.base
}

// WallTime returns the wall time corresponding to the current virtual time.
func (c *VirtualClock) WallTime() time.Time {
	return c.base.Add(c.current)
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
