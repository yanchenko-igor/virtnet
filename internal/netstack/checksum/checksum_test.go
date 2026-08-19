package checksum

import "testing"

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
