package icmp

import (
	"bytes"
	"reflect"
	"testing"
)

func TestEchoRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		m    Message
	}{
		{name: "request", m: NewEchoRequest(1, 1, []byte("virtnet"))},
		{name: "reply", m: NewEchoReply(0xabcd, 7, nil)},
		{name: "empty echo", m: NewEchoRequest(0, 0, nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.m.Marshal()
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

func TestEchoHelpers(t *testing.T) {
	req := NewEchoRequest(7, 42, []byte("hello"))
	if id, ok := req.EchoID(); !ok || id != 7 {
		t.Errorf("EchoID = %d, %v; want 7, true", id, ok)
	}
	if seq, ok := req.EchoSeq(); !ok || seq != 42 {
		t.Errorf("EchoSeq = %d, %v; want 42, true", seq, ok)
	}
	if data, ok := req.EchoData(); !ok || !bytes.Equal(data, []byte("hello")) {
		t.Errorf("EchoData = %q, %v", data, ok)
	}
}

func TestNonEchoHasNoEchoHelpers(t *testing.T) {
	m := Message{Type: TypeDestUnreach, Code: 1}
	if _, ok := m.EchoID(); ok {
		t.Error("non-echo message reported an echo ID")
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	ok := NewEchoRequest(1, 1, []byte{0xde, 0xad})
	raw := ok.Marshal()

	badChecksum := append([]byte(nil), raw...)
	badChecksum[2] ^= 0xff

	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "too short", in: raw[:HeaderLen-1]},
		{name: "bad checksum", in: badChecksum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Unmarshal(tt.in); err == nil {
				t.Errorf("Unmarshal(%s) expected error", tt.name)
			}
		})
	}
}
