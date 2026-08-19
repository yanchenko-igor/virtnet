package udp

import (
	"bytes"
	"net/netip"
	"reflect"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

func TestRoundTrip(t *testing.T) {
	tt := []struct {
		name    string
		src     string
		dst     string
		pkt     Packet
		wantLen int
	}{
		{name: "empty payload", src: "10.0.0.1", dst: "10.0.0.2", pkt: Packet{SrcPort: 1000, DstPort: 2000}, wantLen: 8},
		{name: "hello", src: "10.0.0.1", dst: "10.0.0.2", pkt: Packet{SrcPort: 1000, DstPort: 2000, Payload: []byte("hello")}, wantLen: 13},
		{name: "odd payload", src: "192.168.1.1", dst: "192.168.1.2", pkt: Packet{SrcPort: 53, DstPort: 53000, Payload: []byte("abc")}, wantLen: 11},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.pkt.Marshal(mustAddr(t, tc.src), mustAddr(t, tc.dst))
			if len(raw) != tc.wantLen {
				t.Fatalf("Marshal length = %d, want %d", len(raw), tc.wantLen)
			}
			got, err := Unmarshal(raw, mustAddr(t, tc.src), mustAddr(t, tc.dst))
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.pkt) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, tc.pkt)
			}
		})
	}
}

func TestKnownChecksumVector(t *testing.T) {
	// Independent vector: src 10.0.0.1:1000 -> dst 10.0.0.2:2000, "hello".
	p := Packet{SrcPort: 1000, DstPort: 2000, Payload: []byte("hello")}
	raw := p.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	if want := []byte{0x03, 0xe8, 0x07, 0xd0, 0x00, 0x0d, 0x9c, 0x47}; !bytes.Equal(raw[:8], want) {
		t.Errorf("header = % x, want % x", raw[:8], want)
	}
}

func TestWrongPseudoHeaderRejected(t *testing.T) {
	p := Packet{SrcPort: 1000, DstPort: 2000, Payload: []byte("hello")}
	raw := p.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	// Verifying with a different address pair must fail the checksum.
	if _, err := Unmarshal(raw, mustAddr(t, "10.0.0.3"), mustAddr(t, "10.0.0.4")); err == nil {
		t.Fatal("expected checksum failure with a different pseudo-header")
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tt := []struct {
		name string
		data []byte
	}{
		{name: "too short", data: []byte{0x01, 0x02}},
		{name: "length exceeds data", data: make([]byte, 8)},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Unmarshal(tc.data, mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2")); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCorruptedPayloadRejected(t *testing.T) {
	p := Packet{SrcPort: 1000, DstPort: 2000, Payload: []byte("hello")}
	raw := p.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	raw[len(raw)-1] ^= 0xff
	if _, err := Unmarshal(raw, mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2")); err == nil {
		t.Fatal("expected checksum failure on corrupted payload")
	}
}
