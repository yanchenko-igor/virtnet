// Package tcp implements the userspace TCP segment model (RFC 793) with the
// IPv4 pseudo-header checksum. The transport is a pure state machine driven by
// virtual time — no host sockets or timers. Deterministic: Marshal/Unmarshal
// never consult the host or the real clock.
package tcp

import (
	"fmt"
	"net/netip"

	"github.com/yanchenko-igor/virtnet/internal/netstack/checksum"
)

// HeaderLen is the minimum TCP header length (20 bytes, no options). The
// simulator emits option-free segments, so dataOffset is always 5 words.
const HeaderLen = 20

// Proto is the IPv4 protocol number for TCP.
const Proto = 6

// Flags is the set of TCP control bits in byte 13 of the header.
type Flags uint8

// Control bits (RFC 793).
const (
	FlagFIN Flags = 1 << 0
	FlagSYN Flags = 1 << 1
	FlagRST Flags = 1 << 2
	FlagPSH Flags = 1 << 3
	FlagACK Flags = 1 << 4
	FlagURG Flags = 1 << 5
	FlagECE Flags = 1 << 6
	FlagCWR Flags = 1 << 7
)

// State is a TCP connection state (RFC 793, figure 6).
type State uint8

// Connection states.
const (
	StateClosed State = iota
	StateListen
	StateSynSent
	StateSynReceived
	StateEstablished
	StateFinWait1
	StateFinWait2
	StateCloseWait
	StateLastAck
	StateTimeWait
	StateClosing
)

// String returns the state name.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateListen:
		return "LISTEN"
	case StateSynSent:
		return "SYN-SENT"
	case StateSynReceived:
		return "SYN-RECEIVED"
	case StateEstablished:
		return "ESTABLISHED"
	case StateFinWait1:
		return "FIN-WAIT-1"
	case StateFinWait2:
		return "FIN-WAIT-2"
	case StateCloseWait:
		return "CLOSE-WAIT"
	case StateLastAck:
		return "LAST-ACK"
	case StateTimeWait:
		return "TIME-WAIT"
	case StateClosing:
		return "CLOSING"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", uint8(s))
	}
}

// Segment is a single TCP segment: header fields plus payload.
type Segment struct {
	SrcPort uint16
	DstPort uint16
	Seq     uint32
	Ack     uint32
	Flags   Flags
	Window  uint16
	Payload []byte
}

// Has reports whether all the given flags are set.
func (s Segment) Has(flags Flags) bool {
	return s.Flags&flags == flags
}

// Marshal serializes the segment to wire format. The checksum covers the IPv4
// pseudo-header, the TCP header, and the payload (RFC 793).
func (s Segment) Marshal(src, dst netip.Addr) []byte {
	total := HeaderLen + len(s.Payload)
	b := make([]byte, 0, total)
	b = append(b, byte(s.SrcPort>>8), byte(s.SrcPort))
	b = append(b, byte(s.DstPort>>8), byte(s.DstPort))
	b = append(b, byte(s.Seq>>24), byte(s.Seq>>16), byte(s.Seq>>8), byte(s.Seq))
	b = append(b, byte(s.Ack>>24), byte(s.Ack>>16), byte(s.Ack>>8), byte(s.Ack))
	b = append(b, 0x50, byte(s.Flags)) // data offset 5 words, no options
	b = append(b, byte(s.Window>>8), byte(s.Window))
	b = append(b, 0, 0) // checksum field, computed below
	b = append(b, 0, 0) // urgent pointer
	b = append(b, s.Payload...)
	chk := checksum.PseudoSum(src, dst, Proto, total, b)
	b[16], b[17] = byte(chk>>8), byte(chk)
	return b
}

// Unmarshal parses a wire-format segment and verifies its checksum. src and dst
// are the addresses carried in the encapsulating IPv4 header, needed for the
// pseudo-header. Segments with options are rejected: the simulator never emits
// them.
func Unmarshal(b []byte, src, dst netip.Addr) (Segment, error) {
	if len(b) < HeaderLen {
		return Segment{}, fmt.Errorf("tcp: segment too short: %d bytes", len(b))
	}
	dataOffset := int(b[12]>>4) * 4
	if dataOffset < HeaderLen {
		return Segment{}, fmt.Errorf("tcp: invalid data offset %d", dataOffset)
	}
	if len(b) < dataOffset {
		return Segment{}, fmt.Errorf("tcp: truncated header")
	}
	if dataOffset > HeaderLen {
		return Segment{}, fmt.Errorf("tcp: options unsupported (%d-byte header)", dataOffset)
	}
	if checksum.PseudoSum(src, dst, Proto, len(b), b) != 0 {
		return Segment{}, fmt.Errorf("tcp: bad checksum")
	}
	return Segment{
		SrcPort: uint16(b[0])<<8 | uint16(b[1]),
		DstPort: uint16(b[2])<<8 | uint16(b[3]),
		Seq:     uint32(b[4])<<24 | uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7]),
		Ack:     uint32(b[8])<<24 | uint32(b[9])<<16 | uint32(b[10])<<8 | uint32(b[11]),
		Flags:   Flags(b[13]),
		Window:  uint16(b[14])<<8 | uint16(b[15]),
		Payload: append([]byte(nil), b[dataOffset:]...),
	}, nil
}
