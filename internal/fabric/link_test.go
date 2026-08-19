package fabric

import (
	"reflect"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// collector records every frame it receives.
type collector struct {
	frames []ethernet.Frame
}

func (c *collector) ReceiveFrame(f ethernet.Frame) error {
	c.frames = append(c.frames, f)
	return nil
}

func (c *collector) count() int {
	return len(c.frames)
}

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func testFrame(t *testing.T, payload ...byte) ethernet.Frame {
	t.Helper()
	return ethernet.Frame{
		Dst:     mustMAC(t, "02:00:00:00:00:02"),
		Src:     mustMAC(t, "02:00:00:00:00:01"),
		Type:    ethernet.EtherTypeIPv4,
		Payload: payload,
	}
}

func TestDeliveryAtoB(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	ca, cb := &collector{}, &collector{}
	a.Attach(ca)
	b.Attach(cb)

	if _, err := NewLink(c, a, b, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	f := ethernet.Frame{Dst: b.MAC, Src: a.MAC, Type: ethernet.EtherTypeIPv4, Payload: []byte{0xde, 0xad}}
	if err := a.Send(f); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if cb.count() != 1 {
		t.Fatalf("peer received %d frames, want 1", cb.count())
	}
	if got := cb.frames[0]; !reflect.DeepEqual(got, f) {
		t.Errorf("peer frame mismatch:\n got %+v\nwant %+v", got, f)
	}
	if ca.count() != 0 {
		t.Errorf("source received %d frames, want 0", ca.count())
	}
	if got := c.Now(); got != 10*time.Millisecond {
		t.Errorf("clock = %v, want 10ms (delay applied, not waited)", got)
	}
}

func TestDeliveryBtoA(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	ca, cb := &collector{}, &collector{}
	a.Attach(ca)
	b.Attach(cb)
	if _, err := NewLink(c, a, b, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if err := b.Send(testFrame(t)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ca.count() != 1 {
		t.Fatalf("peer received %d frames, want 1", ca.count())
	}
	if got := c.Now(); got != 5*time.Millisecond {
		t.Errorf("clock = %v, want 5ms", got)
	}
}

func TestClockAccumulatesAcrossHops(t *testing.T) {
	// A ── 10ms ── R ── 20ms ── B : the full chain advances the shared clock.
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	r0 := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:03"))
	r1 := NewInterface("eth1", mustMAC(t, "02:00:00:00:00:04"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))

	ca, cb, cr := &collector{}, &collector{}, &collector{}
	a.Attach(ca)
	b.Attach(cb)
	r0.Attach(cr)

	l1, err := NewLink(c, a, r0, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := NewLink(c, r1, b, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = l1
	_ = l2

	if err := a.Send(testFrame(t)); err != nil {
		t.Fatal(err)
	}
	if cr.count() != 1 {
		t.Fatalf("middle node received %d frames, want 1", cr.count())
	}
	// The middle node (router-like) forwards synchronously out the egress port.
	if err := r1.Send(testFrame(t)); err != nil {
		t.Fatal(err)
	}

	if got := c.Now(); got != 30*time.Millisecond {
		t.Errorf("clock = %v, want 30ms", got)
	}
	if cb.count() != 1 {
		t.Errorf("final hop received %d frames, want 1", cb.count())
	}
}

func TestSendDownInterface(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	a.AdminUp = false
	b.Attach(&collector{})
	if _, err := NewLink(c, a, b, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(testFrame(t)); err == nil {
		t.Fatal("expected error sending from down interface")
	}
}

func TestSendToDownPeer(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	b.AdminUp = false
	b.Attach(&collector{})
	if _, err := NewLink(c, a, b, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(testFrame(t)); err == nil {
		t.Fatal("expected error sending to down peer")
	}
	if c.Now() != 0 {
		t.Errorf("clock advanced despite failed delivery: %v", c.Now())
	}
}

func TestReceiveDropsOnDown(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	cb := &collector{}
	b.Attach(cb)
	if _, err := NewLink(c, a, b, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	b.AdminUp = false

	if err := a.Send(testFrame(t)); err == nil {
		t.Fatal("expected error: peer down drops the frame")
	}
	if cb.count() != 0 {
		t.Errorf("down interface received %d frames, want 0", cb.count())
	}
}

func TestSendWithoutLink(t *testing.T) {
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	if err := a.Send(testFrame(t)); err == nil {
		t.Fatal("expected error sending without attached link")
	}
}

func TestLinkConstructorErrors(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))

	tests := []struct {
		name  string
		a, b  *Interface
		delay time.Duration
	}{
		{name: "nil endpoint", a: a, b: nil, delay: time.Millisecond},
		{name: "self connection", a: a, b: a, delay: time.Millisecond},
		{name: "negative delay", a: a, b: b, delay: -time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewLink(c, tt.a, tt.b, tt.delay); err == nil {
				t.Errorf("NewLink(%s) expected error", tt.name)
			}
		})
	}
}

func TestTransmitRejectsForeignInterface(t *testing.T) {
	c := clock.New()
	a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	x := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:09"))
	l, err := NewLink(c, a, b, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Transmit(x, testFrame(t)); err == nil {
		t.Fatal("expected error transmitting from foreign interface")
	}
}

func TestDeterminism(t *testing.T) {
	run := func() (time.Duration, int) {
		c := clock.New()
		a := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
		b := NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
		cb := &collector{}
		a.Attach(&collector{})
		b.Attach(cb)
		if _, err := NewLink(c, a, b, 7*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			if err := a.Send(testFrame(t, byte(i))); err != nil {
				t.Fatal(err)
			}
		}
		return c.Now(), cb.count()
	}

	ta, na := run()
	tb, nb := run()
	if ta != tb || na != nb {
		t.Errorf("non-deterministic run: (%v,%d) vs (%v,%d)", ta, na, tb, nb)
	}
}
