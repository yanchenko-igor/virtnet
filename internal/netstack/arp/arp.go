// Package arp implements the Address Resolution Protocol for the virtual
// fabric: ARP messages (RFC 826) and the per-machine IPv4→MAC cache.
package arp

import (
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// Operation codes (RFC 826).
const (
	OpRequest uint16 = 1
	OpReply   uint16 = 2
)

// MessageLen is the fixed length of an ARP message for Ethernet/IPv4.
const MessageLen = 28

// Message is a single ARP message for Ethernet (6-byte hlen) over IPv4
// (4-byte plen), matching the virtual fabric.
type Message struct {
	Op        uint16
	SenderMAC ethernet.MAC
	SenderIP  netip.Addr
	TargetMAC ethernet.MAC
	TargetIP  netip.Addr
}

// Marshal serializes the message to its 28-byte wire format.
func (m Message) Marshal() []byte {
	b := make([]byte, MessageLen)
	b[0], b[1] = 0x00, 0x01 // htype: Ethernet
	b[2], b[3] = 0x08, 0x00 // ptype: IPv4
	b[4] = 6                // hlen
	b[5] = 4                // plen
	b[6], b[7] = byte(m.Op>>8), byte(m.Op)
	copy(b[8:14], m.SenderMAC[:])
	sp := m.SenderIP.As4()
	copy(b[14:18], sp[:])
	copy(b[18:24], m.TargetMAC[:])
	tp := m.TargetIP.As4()
	copy(b[24:28], tp[:])
	return b
}

// Unmarshal parses an ARP message and validates that it is Ethernet/IPv4.
func Unmarshal(b []byte) (Message, error) {
	if len(b) < MessageLen {
		return Message{}, fmt.Errorf("arp: message too short: %d bytes", len(b))
	}
	if b[0] != 0x00 || b[1] != 0x01 {
		return Message{}, fmt.Errorf("arp: unsupported hardware type")
	}
	if b[2] != 0x08 || b[3] != 0x00 {
		return Message{}, fmt.Errorf("arp: unsupported protocol type")
	}
	if b[4] != 6 || b[5] != 4 {
		return Message{}, fmt.Errorf("arp: unexpected address lengths")
	}
	var m Message
	m.Op = uint16(b[6])<<8 | uint16(b[7])
	copy(m.SenderMAC[:], b[8:14])
	copy(m.TargetMAC[:], b[18:24])
	m.SenderIP = netip.AddrFrom4([4]byte{b[14], b[15], b[16], b[17]})
	m.TargetIP = netip.AddrFrom4([4]byte{b[24], b[25], b[26], b[27]})
	return m, nil
}

// Cache is a virtual machine's IPv4→MAC mapping.
//
// Entries carry an expiry timestamp in virtual time. Expiry is evaluated on
// lookup by comparing against the current virtual clock — no timer or
// scheduled event is involved (ARCHITECTURE.md §9.2).
type Cache struct {
	entries map[netip.Addr]Entry
	timeout time.Duration
}

// Entry is one cached mapping.
type Entry struct {
	MAC     ethernet.MAC
	Expires time.Duration // virtual timestamp at which the entry expires
}

// NewCache returns an empty cache with the given timeout.
func NewCache(timeout time.Duration) *Cache {
	return &Cache{
		entries: make(map[netip.Addr]Entry),
		timeout: timeout,
	}
}

// Get returns the MAC for ip, honoring expiry. now is the current virtual time.
func (c *Cache) Get(ip netip.Addr, now time.Duration) (ethernet.MAC, bool) {
	e, ok := c.entries[ip]
	if !ok {
		return ethernet.MAC{}, false
	}
	if now >= e.Expires {
		delete(c.entries, ip)
		return ethernet.MAC{}, false
	}
	return e.MAC, true
}

// Put records ip→mac, expiring timeout after now (virtual time).
func (c *Cache) Put(ip netip.Addr, mac ethernet.MAC, now time.Duration) {
	c.entries[ip] = Entry{MAC: mac, Expires: now + c.timeout}
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	return len(c.entries)
}

// KeyedEntry is a cached mapping together with its IP address.
type KeyedEntry struct {
	IP  netip.Addr
	MAC ethernet.MAC
}

// Entries returns the non-expired cache entries, sorted by IP and evaluated
// at virtual time now. Deterministic: never depends on Go map order.
func (c *Cache) Entries(now time.Duration) []KeyedEntry {
	ips := make([]netip.Addr, 0, len(c.entries))
	for ip := range c.entries {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].Less(ips[j]) })
	var out []KeyedEntry
	for _, ip := range ips {
		if mac, ok := c.Get(ip, now); ok {
			out = append(out, KeyedEntry{IP: ip, MAC: mac})
		}
	}
	return out
}
