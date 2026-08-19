// Package checksum implements the Internet checksum (RFC 1071).
//
// Used by the IPv4 header, ICMP, and the UDP/TCP transport checksums. The
// algorithm is pure and deterministic — no host randomness or timing involved.
package checksum

import "net/netip"

// Sum computes the one's complement of the one's complement 16-bit sum of b.
// Words are interpreted in network byte order; odd-length input is padded with
// a zero byte on the right, as RFC 1071 prescribes.
func Sum(b []byte) uint16 {
	return ^fold(rawSum(b))
}

// PseudoSum computes the transport checksum over the IPv4 pseudo-header (RFC
// 768/793) followed by the transport segment. src/dst are the IPv4 addresses
// in the datagram's header, proto the transport protocol number, length the
// transport segment length (UDP: header+payload; TCP: header+options+payload),
// and data the wire-format segment.
func PseudoSum(src, dst netip.Addr, proto uint8, length int, data []byte) uint16 {
	a := src.As4()
	b := dst.As4()
	var sum uint32
	for i := 0; i < 4; i += 2 {
		sum += uint32(a[i])<<8 | uint32(a[i+1])
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	sum += uint32(proto)
	sum += uint32(length)
	return ^fold(sum + rawSum(data))
}

func rawSum(b []byte) uint32 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	return sum
}

func fold(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return uint16(sum)
}
