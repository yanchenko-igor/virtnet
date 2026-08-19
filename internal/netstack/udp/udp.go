// Package udp implements the userspace UDP datagram model (RFC 768) with the
// IPv4 pseudo-header checksum. Deterministic: Marshal/Unmarshal never consult
// the host or the real clock.
package udp

import (
	"fmt"
	"net/netip"

	"github.com/yanchenko-igor/virtnet/internal/netstack/checksum"
)

// HeaderLen is the fixed UDP header length (8 bytes).
const HeaderLen = 8

// Proto is the IPv4 protocol number for UDP.
const Proto = 17

// Packet is a single UDP datagram: header plus payload.
type Packet struct {
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

// Marshal serializes the datagram to wire format. The checksum covers the
// IPv4 pseudo-header, the UDP header, and the payload (RFC 768).
func (p Packet) Marshal(src, dst netip.Addr) []byte {
	total := HeaderLen + len(p.Payload)
	b := make([]byte, 0, total)
	b = append(b, byte(p.SrcPort>>8), byte(p.SrcPort))
	b = append(b, byte(p.DstPort>>8), byte(p.DstPort))
	b = append(b, byte(total>>8), byte(total))
	b = append(b, 0, 0) // checksum field, computed below
	b = append(b, p.Payload...)
	chk := checksum.PseudoSum(src, dst, Proto, total, b)
	b[6], b[7] = byte(chk>>8), byte(chk)
	return b
}

// Unmarshal parses a wire-format datagram and verifies its checksum. src and
// dst are the addresses carried in the encapsulating IPv4 header, needed for
// the pseudo-header.
func Unmarshal(b []byte, src, dst netip.Addr) (Packet, error) {
	if len(b) < HeaderLen {
		return Packet{}, fmt.Errorf("udp: datagram too short: %d bytes", len(b))
	}
	total := int(b[4])<<8 | int(b[5])
	if total < HeaderLen || total > len(b) {
		return Packet{}, fmt.Errorf("udp: invalid length %d", total)
	}
	if checksum.PseudoSum(src, dst, Proto, total, b[:total]) != 0 {
		return Packet{}, fmt.Errorf("udp: bad checksum")
	}
	return Packet{
		SrcPort: uint16(b[0])<<8 | uint16(b[1]),
		DstPort: uint16(b[2])<<8 | uint16(b[3]),
		Payload: append([]byte(nil), b[HeaderLen:total]...),
	}, nil
}
