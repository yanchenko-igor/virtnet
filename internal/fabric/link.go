package fabric

import (
	"fmt"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// Link is a point-to-point virtual link between exactly two interfaces
// (ARCHITECTURE.md §7.6).
//
// A link's delay is a mathematical transformation of simulation time: it
// calculates the delivery timestamp, advances the shared virtual clock, and
// delivers immediately. No host timer or wait is involved (ARCHITECTURE.md
// §5.3). Bandwidth, loss, jitter, and corruption are added in phase 9.
type Link struct {
	clock *clock.VirtualClock
	a, b  *Interface
	delay time.Duration
	taps  []Tap
}

// Tap observes a frame crossing a link. It is called after the virtual clock
// has advanced to the delivery timestamp and before the peer receives the
// frame. Taps are pure observation: they must not mutate the frame or advance
// the clock (ARCHITECTURE.md §11.3).
type Tap func(now time.Duration, src, dst *Interface, f ethernet.Frame)

// AddTap registers an observer on the link. Multiple taps are allowed and run
// in registration order.
func (l *Link) AddTap(t Tap) {
	l.taps = append(l.taps, t)
}

// NewLink connects two interfaces with a simulated propagation delay.
// Both interfaces must be distinct; the delay must be non-negative.
func NewLink(c *clock.VirtualClock, a, b *Interface, delay time.Duration) (*Link, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("fabric: link requires two interfaces")
	}
	if a == b {
		return nil, fmt.Errorf("fabric: link cannot connect an interface to itself")
	}
	if delay < 0 {
		return nil, fmt.Errorf("fabric: negative link delay %v", delay)
	}
	l := &Link{clock: c, a: a, b: b, delay: delay}
	a.link = l
	b.link = l
	return l, nil
}

// Delay returns the configured propagation delay.
func (l *Link) Delay() time.Duration {
	return l.delay
}

// Transmit carries a frame from src to its peer, synchronously.
//
// The delivery timestamp is computed, the virtual clock is advanced, and the
// peer's Receive runs within this same call — there is no waiting and no
// queued delivery.
func (l *Link) Transmit(src *Interface, f ethernet.Frame) error {
	var peer *Interface
	switch src {
	case l.a:
		peer = l.b
	case l.b:
		peer = l.a
	default:
		return fmt.Errorf("fabric: interface %q is not attached to this link", src.Name)
	}
	if !peer.AdminUp {
		return fmt.Errorf("fabric: peer interface %q is down", peer.Name)
	}
	if err := l.clock.AdvanceBy(l.delay); err != nil {
		return fmt.Errorf("fabric: link transmit: %w", err)
	}
	for _, t := range l.taps {
		t(l.clock.Now(), src, peer, f)
	}
	return peer.Receive(f)
}

// Endpoints returns the two connected interfaces.
func (l *Link) Endpoints() (*Interface, *Interface) {
	return l.a, l.b
}
