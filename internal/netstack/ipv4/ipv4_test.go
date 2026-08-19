package ipv4

import (
	"bytes"
	"net/netip"
	"reflect"
	"testing"

	"github.com/yanchenko-igor/virtnet/internal/netstack/checksum"
)

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		p    Packet
	}{
		{
			name: "icmp",
			p:    Packet{Src: addr(t, "10.0.0.1"), Dst: addr(t, "10.0.0.2"), TTL: 64, Protocol: ProtoICMP, Payload: []byte{0x08, 0x00, 0x00, 0x00}},
		},
		{
			name: "empty payload",
			p:    Packet{Src: addr(t, "10.0.0.1"), Dst: addr(t, "10.0.0.2"), TTL: 1, Protocol: ProtoTCP},
		},
		{
			name: "ttl one",
			p:    Packet{Src: addr(t, "172.16.0.1"), Dst: addr(t, "8.8.8.8"), TTL: 1, Protocol: ProtoUDP, Payload: []byte{0xde, 0xad, 0xbe, 0xef}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.p.Marshal()
			if len(raw) != tt.p.Size() {
				t.Errorf("Marshal length = %d, want %d", len(raw), tt.p.Size())
			}
			got, err := Unmarshal(raw)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tt.p) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, tt.p)
			}
			if !bytes.Equal(got.Payload, tt.p.Payload) {
				t.Error("payload not preserved")
			}
		})
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	ok := Packet{Src: addr(t, "10.0.0.1"), Dst: addr(t, "10.0.0.2"), TTL: 64, Protocol: ProtoICMP, Payload: []byte{1, 2, 3, 4}}
	raw := ok.Marshal()

	badVersion := append([]byte(nil), raw...)
	badVersion[0] = 0x65 // version 6

	truncated := raw[:HeaderLen-1]

	badChecksum := append([]byte(nil), raw...)
	badChecksum[10] ^= 0xff

	badTotal := append([]byte(nil), raw...)
	badTotal[2] = 0xff // total length > actual

	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "truncated", in: truncated},
		{name: "wrong version", in: badVersion},
		{name: "bad checksum", in: badChecksum},
		{name: "bad total length", in: badTotal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Unmarshal(tt.in); err == nil {
				t.Errorf("Unmarshal(%s) expected error", tt.name)
			}
		})
	}
}

func TestChecksumStoredInHeader(t *testing.T) {
	p := Packet{Src: addr(t, "10.0.0.1"), Dst: addr(t, "10.0.0.2"), TTL: 64, Protocol: ProtoICMP}
	raw := p.Marshal()
	// The header checksum field itself must be the checksum of the zeroed header.
	zeroed := append([]byte(nil), raw[:HeaderLen]...)
	zeroed[10], zeroed[11] = 0, 0
	if got := checksum.Sum(zeroed); got != uint16(raw[10])<<8|uint16(raw[11]) {
		t.Errorf("stored checksum 0x%02x%02x does not match computed 0x%04x", raw[10], raw[11], got)
	}
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}
