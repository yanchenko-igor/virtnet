package arp

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func TestMessageRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		m    Message
	}{
		{
			name: "request",
			m:    Message{Op: OpRequest, SenderMAC: mustMAC(t, "02:00:00:00:00:01"), SenderIP: addr(t, "10.0.0.10"), TargetIP: addr(t, "10.0.0.20")},
		},
		{
			name: "reply",
			m:    Message{Op: OpReply, SenderMAC: mustMAC(t, "02:00:00:00:00:02"), SenderIP: addr(t, "10.0.0.20"), TargetMAC: mustMAC(t, "02:00:00:00:00:01"), TargetIP: addr(t, "10.0.0.10")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.m.Marshal()
			if len(raw) != MessageLen {
				t.Errorf("Marshal length = %d, want %d", len(raw), MessageLen)
			}
			got, err := Unmarshal(raw)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tt.m) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, tt.m)
			}
		})
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	ok := Message{Op: OpRequest, SenderMAC: mustMAC(t, "02:00:00:00:00:01"), SenderIP: addr(t, "10.0.0.10"), TargetIP: addr(t, "10.0.0.20")}
	raw := ok.Marshal()

	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "too short", in: raw[:MessageLen-1]},
		{name: "wrong htype", in: mutate(raw, 0, 0x02)},
		{name: "wrong ptype", in: mutate(raw, 2, 0x86)},
		{name: "wrong hlen", in: mutate(raw, 4, 0x05)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Unmarshal(tt.in); err == nil {
				t.Errorf("Unmarshal(%s) expected error", tt.name)
			}
		})
	}
}

func TestCacheGetPutExpiry(t *testing.T) {
	c := NewCache(30 * time.Second)
	ip := addr(t, "10.0.0.20")
	mac := mustMAC(t, "02:00:00:00:00:02")

	if _, ok := c.Get(ip, 0); ok {
		t.Fatal("cache should be empty initially")
	}

	c.Put(ip, mac, 100*time.Second)

	if m, ok := c.Get(ip, 129*time.Second); !ok {
		t.Fatal("entry valid before expiry")
	} else if m != mac {
		t.Errorf("got MAC %v, want %v", m, mac)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}

	// At exactly the expiry timestamp the entry is gone.
	if _, ok := c.Get(ip, 130*time.Second); ok {
		t.Fatal("entry should be expired at expires_at")
	}
	if c.Len() != 0 {
		t.Error("expired entry not removed")
	}
}

func mutate(b []byte, i int, v byte) []byte {
	out := append([]byte(nil), b...)
	out[i] = v
	return out
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}
