package tcp

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
		name string
		src  string
		dst  string
		seg  Segment
	}{
		{name: "empty", src: "10.0.0.1", dst: "10.0.0.2", seg: Segment{}},
		{name: "syn", src: "10.0.0.1", dst: "10.0.0.2", seg: Segment{SrcPort: 49152, DstPort: 8080, Seq: 1001, Flags: FlagSYN, Window: 65535}},
		{name: "syn-ack", src: "10.0.0.2", dst: "10.0.0.1", seg: Segment{SrcPort: 8080, DstPort: 49152, Seq: 2001, Ack: 1002, Flags: FlagSYN | FlagACK, Window: 65535}},
		{name: "data", src: "10.0.0.1", dst: "10.0.0.2", seg: Segment{SrcPort: 49152, DstPort: 8080, Seq: 1002, Ack: 2002, Flags: FlagACK, Window: 65535, Payload: []byte("hello")}},
		{name: "fin", src: "10.0.0.1", dst: "10.0.0.2", seg: Segment{SrcPort: 49152, DstPort: 8080, Seq: 1007, Ack: 2002, Flags: FlagFIN | FlagACK, Window: 65535}},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.seg.Marshal(mustAddr(t, tc.src), mustAddr(t, tc.dst))
			got, err := Unmarshal(raw, mustAddr(t, tc.src), mustAddr(t, tc.dst))
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.seg) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, tc.seg)
			}
		})
	}
}

func TestKnownChecksumVector(t *testing.T) {
	// Independent vector: src 10.0.0.1:1234 -> dst 10.0.0.2:80, seq=1, ack=2,
	// SYN|ACK, window 65535, payload "ping".
	seg := Segment{SrcPort: 1234, DstPort: 80, Seq: 1, Ack: 2, Flags: FlagSYN | FlagACK, Window: 65535, Payload: []byte("ping")}
	raw := seg.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	if want := []byte{0x04, 0xd2, 0x00, 0x50}; !bytes.Equal(raw[:4], want) {
		t.Errorf("ports = % x, want % x", raw[:4], want)
	}
	if got := uint16(raw[16])<<8 | uint16(raw[17]); got != 0xb7d6 {
		t.Errorf("checksum = 0x%04x, want 0xb7d6", got)
	}
}

func TestWrongPseudoHeaderRejected(t *testing.T) {
	seg := Segment{SrcPort: 1234, DstPort: 80, Seq: 1, Flags: FlagSYN, Window: 65535}
	raw := seg.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	// Verifying with a different address pair must fail the checksum.
	if _, err := Unmarshal(raw, mustAddr(t, "10.0.0.3"), mustAddr(t, "10.0.0.4")); err == nil {
		t.Fatal("expected checksum failure with a different pseudo-header")
	}
}

func TestCorruptedPayloadRejected(t *testing.T) {
	seg := Segment{SrcPort: 1234, DstPort: 80, Seq: 1, Flags: FlagSYN, Window: 65535, Payload: []byte("data")}
	raw := seg.Marshal(mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2"))
	raw[len(raw)-1] ^= 0xff
	if _, err := Unmarshal(raw, mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2")); err == nil {
		t.Fatal("expected checksum failure on corrupted payload")
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tt := []struct {
		name string
		data []byte
	}{
		{name: "too short", data: make([]byte, 10)},
		{name: "options unsupported", data: func() []byte {
			b := make([]byte, 24)
			b[12] = 0x60 // data offset 6 words = options present
			return b
		}()},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Unmarshal(tc.data, mustAddr(t, "10.0.0.1"), mustAddr(t, "10.0.0.2")); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestStateString(t *testing.T) {
	states := map[State]string{
		StateClosed:      "CLOSED",
		StateListen:      "LISTEN",
		StateSynSent:     "SYN-SENT",
		StateSynReceived: "SYN-RECEIVED",
		StateEstablished: "ESTABLISHED",
		StateFinWait1:    "FIN-WAIT-1",
		StateFinWait2:    "FIN-WAIT-2",
		StateCloseWait:   "CLOSE-WAIT",
		StateLastAck:     "LAST-ACK",
		StateTimeWait:    "TIME-WAIT",
		StateClosing:     "CLOSING",
	}
	for s, want := range states {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", uint8(s), got, want)
		}
	}
}

func TestHas(t *testing.T) {
	seg := Segment{Flags: FlagSYN | FlagACK}
	if !seg.Has(FlagACK) || !seg.Has(FlagSYN) || !seg.Has(FlagSYN|FlagACK) {
		t.Error("Has failed for set flags")
	}
	if seg.Has(FlagFIN) || seg.Has(FlagRST) {
		t.Error("Has reported an unset flag")
	}
}
