package clock

import (
	"testing"
	"time"
)

func TestAdvanceBy(t *testing.T) {
	tests := []struct {
		name   string
		from   time.Duration
		step   time.Duration
		want   time.Duration
		wantOK bool
	}{
		{name: "zero", from: 0, step: 0, want: 0, wantOK: true},
		{name: "positive step", from: 100 * time.Millisecond, step: 50 * time.Millisecond, want: 150 * time.Millisecond, wantOK: true},
		{name: "from non-zero", from: 1 * time.Second, step: 2 * time.Second, want: 3 * time.Second, wantOK: true},
		{name: "negative step rejected", from: 1 * time.Second, step: -1 * time.Millisecond, want: 1 * time.Second, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.Set(tt.from)
			err := c.AdvanceBy(tt.step)
			if (err == nil) != tt.wantOK {
				t.Fatalf("AdvanceBy(%v) err = %v, wantOK %v", tt.step, err, tt.wantOK)
			}
			if got := c.Now(); got != tt.want {
				t.Errorf("Now() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdvanceTo(t *testing.T) {
	tests := []struct {
		name   string
		from   time.Duration
		target time.Duration
		want   time.Duration
		wantOK bool
	}{
		{name: "forward jump", from: 10 * time.Millisecond, target: 100 * time.Millisecond, want: 100 * time.Millisecond, wantOK: true},
		{name: "same timestamp", from: 5 * time.Second, target: 5 * time.Second, want: 5 * time.Second, wantOK: true},
		{name: "backwards rejected", from: 100 * time.Millisecond, target: 50 * time.Millisecond, want: 100 * time.Millisecond, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.Set(tt.from)
			err := c.AdvanceTo(tt.target)
			if (err == nil) != tt.wantOK {
				t.Fatalf("AdvanceTo(%v) err = %v, wantOK %v", tt.target, err, tt.wantOK)
			}
			if got := c.Now(); got != tt.want {
				t.Errorf("Now() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetRestoresState(t *testing.T) {
	c := New()
	c.Set(10 * time.Second)
	if c.Now() != 10*time.Second {
		t.Fatalf("Set: got %v, want 10s", c.Now())
	}
	// Restore may jump backwards (checkpoint semantics).
	c.Set(1 * time.Millisecond)
	if c.Now() != 1*time.Millisecond {
		t.Fatalf("Set backwards: got %v, want 1ms", c.Now())
	}
}

func TestSetPanicsOnNegative(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for negative virtual time")
		}
	}()
	New().Set(-1)
}

func TestAdvanceOrdering(t *testing.T) {
	// A causal chain accumulates time: 10ms link + 20ms link = 30ms.
	c := New()
	if err := c.AdvanceBy(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.AdvanceBy(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if c.Now() != 30*time.Millisecond {
		t.Fatalf("Now() = %v, want 30ms", c.Now())
	}
}

func TestNewAtAnchorsWallTime(t *testing.T) {
	base := time.Date(2026, time.January, 15, 8, 0, 0, 0, time.FixedZone("X", 3600))
	c := NewAt(base)
	if got := c.Base(); !got.Equal(time.Date(2026, time.January, 15, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("Base() = %v, want the input normalized to UTC", got)
	}
	if got := c.WallTime(); !got.Equal(c.Base()) {
		t.Fatalf("WallTime() at t=0 = %v, want %v", got, c.Base())
	}
	if err := c.AdvanceBy(10*time.Millisecond + 250*time.Microsecond); err != nil {
		t.Fatal(err)
	}
	want := c.Base().Add(10*time.Millisecond + 250*time.Microsecond)
	if got := c.WallTime(); !got.Equal(want) {
		t.Fatalf("WallTime() = %v, want %v", got, want)
	}
	// The offset is independent of the anchor.
	if c.Now() != 10*time.Millisecond+250*time.Microsecond {
		t.Fatalf("Now() = %v, want the accumulated offset", c.Now())
	}
}

func TestDefaultClockAnchoredAtEpoch(t *testing.T) {
	c := New()
	if !c.WallTime().Equal(time.Unix(0, 0)) {
		t.Fatalf("default WallTime() = %v, want the Unix epoch", c.WallTime())
	}
}
