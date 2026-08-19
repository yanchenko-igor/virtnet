package checksum

import (
	"net/netip"
	"testing"
)

func TestSumWorkedExample(t *testing.T) {
	// Worked example: the one's-complement checksum of this byte sequence is
	// 0x3015 (independently verified; odd length is right-padded with zero).
	data := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff}
	if got := Sum(data); got != 0x3015 {
		t.Errorf("Sum = 0x%04x, want 0x3015", got)
	}
}

func TestSumEmpty(t *testing.T) {
	if got := Sum(nil); got != 0xffff {
		t.Errorf("Sum(nil) = 0x%04x, want 0xffff (one's complement of zero)", got)
	}
}

func TestSumVerification(t *testing.T) {
	// A message plus its own checksum must sum to zero: re-computing the
	// checksum over data that already includes the checksum field yields 0.
	data := []byte{0x45, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x00}
	chk := Sum(data)
	withChecksum := append(append([]byte(nil), data...), byte(chk>>8), byte(chk))
	if got := Sum(withChecksum); got != 0 {
		t.Errorf("checksum over data+checksum = 0x%04x, want 0x0000", got)
	}
}

func TestPseudoSumUDPVector(t *testing.T) {
	// Independent vector: src 10.0.0.1:1000 -> dst 10.0.0.2:2000, payload
	// "hello" (UDP header + payload, checksum field zeroed). Verified against
	// a from-scratch implementation.
	src := mustAddr(t, "10.0.0.1")
	dst := mustAddr(t, "10.0.0.2")
	data := []byte{0x03, 0xe8, 0x07, 0xd0, 0x00, 0x0d, 0x00, 0x00}
	data = append(data, "hello"...)
	if got := PseudoSum(src, dst, 17, 13, data); got != 0x9c47 {
		t.Errorf("PseudoSum(UDP) = 0x%04x, want 0x9c47", got)
	}
}

func TestPseudoSumTCPVector(t *testing.T) {
	// Independent vector: src 10.0.0.1:1234 -> dst 10.0.0.2:80, seq=1, ack=2,
	// SYN|ACK, window 65535, payload "ping".
	src := mustAddr(t, "10.0.0.1")
	dst := mustAddr(t, "10.0.0.2")
	data := []byte{0x04, 0xd2, 0x00, 0x50, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, 0x50, 0x12, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00}
	data = append(data, "ping"...)
	if got := PseudoSum(src, dst, 6, 24, data); got != 0xb7d6 {
		t.Errorf("PseudoSum(TCP) = 0x%04x, want 0xb7d6", got)
	}
}

func TestPseudoSumVerification(t *testing.T) {
	// Re-computing with the checksum field filled in must yield 0.
	src := mustAddr(t, "10.0.0.1")
	dst := mustAddr(t, "10.0.0.2")
	data := []byte{0x03, 0xe8, 0x07, 0xd0, 0x00, 0x0d, 0x00, 0x00}
	data = append(data, "hello"...)
	chk := PseudoSum(src, dst, 17, 13, data)
	data[6], data[7] = byte(chk>>8), byte(chk)
	if got := PseudoSum(src, dst, 17, 13, data); got != 0 {
		t.Errorf("PseudoSum over data+checksum = 0x%04x, want 0x0000", got)
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}
