package capture

import (
	"net/netip"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("bad MAC %q: %v", s, err)
	}
	return m
}

// sink collects received frames; it implements fabric.FrameSink.
type sink struct{ frames []ethernet.Frame }

func (s *sink) ReceiveFrame(f ethernet.Frame) error {
	s.frames = append(s.frames, f)
	return nil
}

func TestTapRecordsTraversal(t *testing.T) {
	c := clock.New()
	a := fabric.NewInterface("a", mustMAC(t, "02:00:00:00:00:01"))
	b := fabric.NewInterface("b", mustMAC(t, "02:00:00:00:00:02"))
	sa, sb := &sink{}, &sink{}
	a.Attach(sa)
	b.Attach(sb)
	l, err := fabric.NewLink(c, a, b, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	cap := New(0)
	l.AddTap(cap.Tap())

	f := ethernet.Frame{Src: a.MAC, Dst: b.MAC, Type: ethernet.EtherTypeARP,
		Payload: (arp.Message{Op: arp.OpRequest, SenderMAC: a.MAC, SenderIP: mustAddr(t, "10.0.0.1"), TargetIP: mustAddr(t, "10.0.0.2")}).Marshal()}
	if err := a.Send(f); err != nil {
		t.Fatal(err)
	}

	recs := cap.Records()
	if len(recs) != 1 {
		t.Fatalf("captured %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Time != 10*time.Millisecond {
		t.Errorf("record time = %v, want 10ms", rec.Time)
	}
	if rec.Src != a || rec.Dst != b {
		t.Errorf("record endpoints = %v/%v, want a/b", rec.Src, rec.Dst)
	}
	if rec.Frame.Type != ethernet.EtherTypeARP {
		t.Errorf("record frame type = %v, want ARP", rec.Frame.Type)
	}
	if got := cap.SrcIPs(); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("SrcIPs = %v, want [10.0.0.1]", got)
	}
}

func TestTapDoesNotAffectDelivery(t *testing.T) {
	c := clock.New()
	a := fabric.NewInterface("a", mustMAC(t, "02:00:00:00:00:01"))
	b := fabric.NewInterface("b", mustMAC(t, "02:00:00:00:00:02"))
	sb := &sink{}
	b.Attach(sb)
	l, err := fabric.NewLink(c, a, b, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	cap := New(0)
	l.AddTap(func(now time.Duration, src, dst *fabric.Interface, f ethernet.Frame) {
		// A misbehaving tap records a wildly wrong timestamp; delivery
		// itself must be unaffected.
		cap.Add(Record{Time: now + 999, Src: src, Dst: dst, Frame: ethernet.Frame{}})
	})

	f := ethernet.Frame{Src: a.MAC, Dst: b.MAC, Type: ethernet.EtherTypeARP, Payload: []byte{1, 2, 3}}
	if err := a.Send(f); err != nil {
		t.Fatal(err)
	}
	if len(sb.frames) != 1 {
		t.Fatalf("peer received %d frames, want 1", len(sb.frames))
	}
	if got := sb.frames[0].Payload; string(got) != string([]byte{1, 2, 3}) {
		t.Errorf("peer payload corrupted: %v", got)
	}
	if got := c.Now(); got != 5*time.Millisecond {
		t.Errorf("clock after tap = %v, want 5ms (tap must not advance it)", got)
	}
}

func TestCaptureBound(t *testing.T) {
	c := clock.New()
	a := fabric.NewInterface("a", mustMAC(t, "02:00:00:00:00:01"))
	b := fabric.NewInterface("b", mustMAC(t, "02:00:00:00:00:02"))
	sa, sb := &sink{}, &sink{}
	a.Attach(sa)
	b.Attach(sb)
	l, err := fabric.NewLink(c, a, b, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	cap := New(3)
	l.AddTap(cap.Tap())

	for i := 0; i < 5; i++ {
		if err := a.Send(ethernet.Frame{Src: a.MAC, Dst: b.MAC, Type: ethernet.EtherTypeARP}); err != nil {
			t.Fatal(err)
		}
	}
	if got := cap.Len(); got != 3 {
		t.Fatalf("capture holds %d records, want 3 (newest)", got)
	}
	recs := cap.Records()
	if recs[0].Time != 3*time.Millisecond {
		t.Errorf("oldest kept record time = %v, want 3ms", recs[0].Time)
	}
	if recs[2].Time != 5*time.Millisecond {
		t.Errorf("newest record time = %v, want 5ms", recs[2].Time)
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad addr %q: %v", s, err)
	}
	return a
}
