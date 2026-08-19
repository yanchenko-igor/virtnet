// Package icmp implements the ICMP message model (echo, destination
// unreachable, time exceeded) for the virtual network stack.
package icmp

import (
	"fmt"

	"github.com/yanchenko-igor/virtnet/internal/netstack/checksum"
)

// Message types.
const (
	TypeEchoReply    uint8 = 0
	TypeDestUnreach  uint8 = 3
	TypeTimeExceeded uint8 = 11
	TypeEchoRequest  uint8 = 8
)

// HeaderLen is the fixed ICMP header length.
const HeaderLen = 4

// Message is a single ICMP message. Payload is the message body after the
// checksum field; for echo messages it begins with identifier/sequence.
type Message struct {
	Type    uint8
	Code    uint8
	Payload []byte
}

// Marshal serializes the message, computing the checksum over the whole body.
func (m Message) Marshal() []byte {
	b := make([]byte, 0, HeaderLen+len(m.Payload))
	b = append(b, m.Type, m.Code, 0, 0)
	b = append(b, m.Payload...)
	chk := checksum.Sum(b)
	b[2], b[3] = byte(chk>>8), byte(chk)
	return b
}

// Unmarshal parses an ICMP message and verifies its checksum.
func Unmarshal(b []byte) (Message, error) {
	if len(b) < HeaderLen {
		return Message{}, fmt.Errorf("icmp: message too short: %d bytes", len(b))
	}
	if checksum.Sum(b) != 0 {
		return Message{}, fmt.Errorf("icmp: bad checksum")
	}
	return Message{
		Type:    b[0],
		Code:    b[1],
		Payload: append([]byte(nil), b[HeaderLen:]...),
	}, nil
}

// NewEchoRequest builds an echo request carrying id, seq, and data.
func NewEchoRequest(id, seq uint16, data []byte) Message {
	return Message{
		Type:    TypeEchoRequest,
		Payload: echoBody(id, seq, data),
	}
}

// NewEchoReply builds an echo reply echoing the request's id, seq, and data.
func NewEchoReply(id, seq uint16, data []byte) Message {
	return Message{
		Type:    TypeEchoReply,
		Payload: echoBody(id, seq, data),
	}
}

func echoBody(id, seq uint16, data []byte) []byte {
	b := make([]byte, 0, 4+len(data))
	b = append(b, byte(id>>8), byte(id), byte(seq>>8), byte(seq))
	return append(b, data...)
}

// EchoID returns the identifier of an echo message.
func (m Message) EchoID() (uint16, bool) {
	if m.Type != TypeEchoRequest && m.Type != TypeEchoReply {
		return 0, false
	}
	if len(m.Payload) < 2 {
		return 0, false
	}
	return uint16(m.Payload[0])<<8 | uint16(m.Payload[1]), true
}

// EchoSeq returns the sequence number of an echo message.
func (m Message) EchoSeq() (uint16, bool) {
	if m.Type != TypeEchoRequest && m.Type != TypeEchoReply {
		return 0, false
	}
	if len(m.Payload) < 4 {
		return 0, false
	}
	return uint16(m.Payload[2])<<8 | uint16(m.Payload[3]), true
}

// EchoData returns the data portion of an echo message.
func (m Message) EchoData() ([]byte, bool) {
	if m.Type != TypeEchoRequest && m.Type != TypeEchoReply {
		return nil, false
	}
	if len(m.Payload) < 4 {
		return nil, false
	}
	return m.Payload[4:], true
}
