package ethernet

import (
	"bytes"
	"reflect"
	"testing"
)

func TestMACStringRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		hex  string
	}{
		{name: "local admin", hex: "02:00:00:00:00:01"},
		{name: "all zeros", hex: "00:00:00:00:00:00"},
		{name: "random", hex: "3a:6c:9f:10:2d:55"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseMAC(tt.hex)
			if err != nil {
				t.Fatalf("ParseMAC: %v", err)
			}
			if got := m.String(); got != tt.hex {
				t.Errorf("String() = %q, want %q", got, tt.hex)
			}
		})
	}
}

func TestParseMACAcceptsStandardForms(t *testing.T) {
	// net.ParseMAC is intentionally lenient: it accepts colon, dash, and dot
	// separators. All normalize to the same 6-byte address.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "colons", in: "02:00:00:00:00:01", want: "02:00:00:00:00:01"},
		{name: "dashes", in: "02-00-00-00-00-01", want: "02:00:00:00:00:01"},
		{name: "dotted", in: "0200.0000.0001", want: "02:00:00:00:00:01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseMAC(tt.in)
			if err != nil {
				t.Fatalf("ParseMAC(%q): %v", tt.in, err)
			}
			if got := m.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMACErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "too short", in: "02:00:00"},
		{name: "invalid hex", in: "zz:00:00:00:00:00"},
		{name: "extra byte", in: "02:00:00:00:00:00:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseMAC(tt.in); err == nil {
				t.Errorf("ParseMAC(%q) expected error", tt.in)
			}
		})
	}
}

func TestBroadcast(t *testing.T) {
	if !BroadcastMAC.IsBroadcast() {
		t.Error("BroadcastMAC should report IsBroadcast")
	}
	var zero MAC
	if zero.IsBroadcast() {
		t.Error("zero MAC should not be broadcast")
	}
	if !zero.IsZero() {
		t.Error("zero MAC should report IsZero")
	}
}

func TestFrameMarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		f    Frame
	}{
		{
			name: "empty payload",
			f:    Frame{Dst: BroadcastMAC, Src: mustMAC(t, "02:00:00:00:00:01"), Type: EtherTypeIPv4},
		},
		{
			name: "ipv4 payload",
			f:    Frame{Dst: mustMAC(t, "02:00:00:00:00:02"), Src: mustMAC(t, "02:00:00:00:00:01"), Type: EtherTypeIPv4, Payload: []byte{0x45, 0x00, 0x00, 0x14}},
		},
		{
			name: "unknown ethertype preserved",
			f:    Frame{Dst: mustMAC(t, "02:00:00:00:00:02"), Src: mustMAC(t, "02:00:00:00:00:01"), Type: EtherType(0x88B5), Payload: []byte{0xde, 0xad}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.f.Marshal()
			if len(raw) != tt.f.Size() {
				t.Errorf("Marshal length = %d, want Size %d", len(raw), tt.f.Size())
			}
			got, err := Unmarshal(raw)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tt.f) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, tt.f)
			}
			if !bytes.Equal(got.Payload, tt.f.Payload) {
				t.Errorf("payload not preserved verbatim")
			}
		})
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "too short", in: make([]byte, HeaderSize-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Unmarshal(tt.in); err == nil {
				t.Errorf("Unmarshal(%v) expected error", tt.in)
			}
		})
	}
}

func mustMAC(t *testing.T, s string) MAC {
	t.Helper()
	m, err := ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}
