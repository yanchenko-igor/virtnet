// Package ipv4 implements the userspace IPv4 packet model.
//
// The packet is an in-memory object; Marshal/Unmarshal produce the wire format
// used by the virtual Ethernet frames. Fragmentation is deferred until the
// IPv4 stack is stable (ARCHITECTURE.md §9.3).
package ipv4

import (
	"fmt"
	"net/netip"

	"github.com/yanchenko-igor/virtnet/internal/netstack/checksum"
)

// HeaderLen is the minimum IPv4 header length (20 bytes, no options).
const HeaderLen = 20

// Protocol is an IP protocol number carried in the header.
type Protocol uint8

// Well-known protocol numbers used by the simulator.
const (
	ProtoICMP Protocol = 1
	ProtoTCP  Protocol = 6
	ProtoUDP  Protocol = 17
)

// String returns the protocol name.
func (p Protocol) String() string {
	switch p {
	case ProtoICMP:
		return "ICMP"
	case ProtoTCP:
		return "TCP"
	case ProtoUDP:
		return "UDP"
	default:
		return fmt.Sprintf("%d", uint8(p))
	}
}

// Packet is a single IPv4 datagram.
type Packet struct {
	Src      netip.Addr
	Dst      netip.Addr
	TTL      uint8
	Protocol Protocol
	Payload  []byte
}

// Marshal serializes the packet to wire format, computing the header checksum.
func (p Packet) Marshal() []byte {
	hdr := make([]byte, HeaderLen)
	hdr[0] = 0x45 // version 4, header length 5 words
	total := HeaderLen + len(p.Payload)
	hdr[2] = byte(total >> 8)
	hdr[3] = byte(total)
	hdr[8] = p.TTL
	hdr[9] = byte(p.Protocol)
	src := p.Src.As4()
	dst := p.Dst.As4()
	copy(hdr[12:16], src[:])
	copy(hdr[16:20], dst[:])
	chk := checksum.Sum(hdr) // checksum field (bytes 10-11) is still zero
	hdr[10] = byte(chk >> 8)
	hdr[11] = byte(chk)
	return append(hdr, p.Payload...)
}

// Unmarshal parses a wire-format IPv4 datagram and verifies its header checksum.
func Unmarshal(b []byte) (Packet, error) {
	if len(b) < HeaderLen {
		return Packet{}, fmt.Errorf("ipv4: datagram too short: %d bytes", len(b))
	}
	if b[0]>>4 != 4 {
		return Packet{}, fmt.Errorf("ipv4: not IPv4 (version %d)", b[0]>>4)
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < HeaderLen {
		return Packet{}, fmt.Errorf("ipv4: invalid header length %d", ihl)
	}
	if len(b) < ihl {
		return Packet{}, fmt.Errorf("ipv4: truncated header")
	}
	if checksum.Sum(b[:ihl]) != 0 {
		return Packet{}, fmt.Errorf("ipv4: bad header checksum")
	}
	total := int(b[2])<<8 | int(b[3])
	if total < ihl || total > len(b) {
		return Packet{}, fmt.Errorf("ipv4: invalid total length %d", total)
	}

	var p Packet
	p.TTL = b[8]
	p.Protocol = Protocol(b[9])
	p.Src = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	p.Dst = netip.AddrFrom4([4]byte{b[16], b[17], b[18], b[19]})
	p.Payload = append([]byte(nil), b[ihl:total]...)
	return p, nil
}

// Size returns the total datagram length in bytes.
func (p Packet) Size() int {
	return HeaderLen + len(p.Payload)
}
