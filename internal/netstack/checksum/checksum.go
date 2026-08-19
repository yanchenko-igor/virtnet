// Package checksum implements the Internet checksum (RFC 1071).
//
// Used by the IPv4 header and ICMP message serialization. The algorithm is
// pure and deterministic — no host randomness or timing involved.
package checksum

// Sum computes the one's complement of the one's complement 16-bit sum of b.
// Words are interpreted in network byte order; odd-length input is padded with
// a zero byte on the right, as RFC 1071 prescribes.
func Sum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
