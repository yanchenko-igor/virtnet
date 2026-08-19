// Package ethernet defines the L2 frame model of the virtual world.
//
// Frames are ordinary in-memory objects (ARCHITECTURE.md §7.4). Unknown
// EtherTypes are preserved, not rejected, so the simulator supports arbitrary
// network packets.
package ethernet

import (
	"fmt"
	"net"
	"strings"
)

// HeaderSize is the fixed length of an Ethernet frame header (6+6+2 bytes).
const HeaderSize = 14

// BroadcastMAC is the L2 broadcast address (ff:ff:ff:ff:ff:ff).
var BroadcastMAC = MAC{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// MAC is a 48-bit hardware address.
type MAC [6]byte

// IsBroadcast reports whether the address is the L2 broadcast address.
func (m MAC) IsBroadcast() bool {
	return m == BroadcastMAC
}

// IsZero reports whether the address is all zeroes (unset).
func (m MAC) IsZero() bool {
	return m == MAC{}
}

// String returns the canonical xx:xx:xx:xx:xx:xx form.
func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// ParseMAC parses an xx:xx:xx:xx:xx:xx address.
func ParseMAC(s string) (MAC, error) {
	hw, err := net.ParseMAC(s)
	if err != nil {
		return MAC{}, fmt.Errorf("ethernet: invalid MAC %q: %w", s, err)
	}
	if len(hw) != 6 {
		return MAC{}, fmt.Errorf("ethernet: MAC %q must be 6 bytes", s)
	}
	var m MAC
	copy(m[:], hw)
	return m, nil
}

// EtherType identifies the protocol carried in a frame payload.
type EtherType uint16

// Well-known EtherTypes used by the simulator.
const (
	EtherTypeIPv4 EtherType = 0x0800
	EtherTypeARP  EtherType = 0x0806
	EtherTypeIPv6 EtherType = 0x86DD
)

// String returns the canonical hexadecimal EtherType.
func (t EtherType) String() string {
	return fmt.Sprintf("0x%04x", uint16(t))
}

// Frame is a single Ethernet frame flowing through the virtual fabric.
//
// Payload is interpreted by higher layers (ARP, IPv4, ...); the fabric treats
// it as opaque bytes and must preserve it untouched.
type Frame struct {
	Dst     MAC
	Src     MAC
	Type    EtherType
	Payload []byte
}

// Size returns the total frame length in bytes.
func (f Frame) Size() int {
	return HeaderSize + len(f.Payload)
}

// Marshal serializes the frame to wire format. Unmarshal is its inverse.
func (f Frame) Marshal() []byte {
	b := make([]byte, 0, f.Size())
	b = append(b, f.Dst[:]...)
	b = append(b, f.Src[:]...)
	b = append(b, byte(f.Type>>8), byte(f.Type))
	b = append(b, f.Payload...)
	return b
}

// Unmarshal parses a wire-format frame, preserving the payload verbatim
// regardless of EtherType.
func Unmarshal(b []byte) (Frame, error) {
	if len(b) < HeaderSize {
		return Frame{}, fmt.Errorf("ethernet: frame too short: %d bytes", len(b))
	}
	var f Frame
	copy(f.Dst[:], b[0:6])
	copy(f.Src[:], b[6:12])
	f.Type = EtherType(uint16(b[12])<<8 | uint16(b[13]))
	f.Payload = append([]byte(nil), b[HeaderSize:]...)
	return f, nil
}

// String renders a short human-readable description.
func (f Frame) String() string {
	kind := f.Type.String()
	if s, ok := knownTypes[f.Type]; ok {
		kind = s
	}
	return strings.Join([]string{
		f.Dst.String(),
		"<-",
		f.Src.String(),
		kind,
		fmt.Sprintf("%d bytes", f.Size()),
	}, " ")
}

var knownTypes = map[EtherType]string{
	EtherTypeIPv4: "IPv4",
	EtherTypeARP:  "ARP",
	EtherTypeIPv6: "IPv6",
}
