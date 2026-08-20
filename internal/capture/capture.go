// Package capture records frames as they cross virtual links, with virtual
// timestamps (ARCHITECTURE.md §11.3). Capture is pure observation: it never
// mutates frames or advances the clock, so it cannot affect determinism.
package capture

import (
	"sort"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack/arp"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// Record is one observed link traversal.
type Record struct {
	Time  time.Duration
	Src   *fabric.Interface
	Dst   *fabric.Interface
	Frame ethernet.Frame
}

// SerializedRecord is an interface-free traversal: endpoints are named by
// label instead of pointer, so a snapshot survives a graph rebuild.
type SerializedRecord struct {
	Time  time.Duration
	Src   string
	Dst   string
	Frame ethernet.Frame
}

// Capture is an ordered, bounded record of observed link traversals. A bound
// of zero keeps every record; otherwise the oldest records are dropped once
// the bound is exceeded, keeping the newest.
type Capture struct {
	records []Record
	max     int
}

// New returns an empty capture. A max of zero retains all records; a positive
// max keeps only the newest max records.
func New(max int) *Capture {
	return &Capture{max: max}
}

// Tap returns a fabric.Tap that records every traversal into c.
func (c *Capture) Tap() fabric.Tap {
	return func(now time.Duration, src, dst *fabric.Interface, f ethernet.Frame) {
		c.Add(Record{Time: now, Src: src, Dst: dst, Frame: f})
	}
}

// Add records one traversal, honoring the bound.
func (c *Capture) Add(r Record) {
	if c.max > 0 && len(c.records) == c.max {
		c.records = append(c.records[1:], r)
		return
	}
	c.records = append(c.records, r)
}

// Records returns a copy of the captured traversals in chronological order.
func (c *Capture) Records() []Record {
	out := make([]Record, len(c.records))
	copy(out, c.records)
	return out
}

// Len returns the number of captured traversals.
func (c *Capture) Len() int {
	return len(c.records)
}

// Reset discards all captured traversals.
func (c *Capture) Reset() {
	c.records = nil
}

// Snapshot converts the recorded traversals to interface-free form. label
// names each endpoint (e.g. "pc1.eth0", "sw.p1").
func (c *Capture) Snapshot(label func(*fabric.Interface) string) []SerializedRecord {
	out := make([]SerializedRecord, 0, len(c.records))
	for _, r := range c.records {
		out = append(out, SerializedRecord{Time: r.Time, Src: label(r.Src), Dst: label(r.Dst), Frame: r.Frame})
	}
	return out
}

// Restore replaces the records, resolving labels back to interfaces. Records
// whose endpoints cannot be resolved are dropped.
func (c *Capture) Restore(recs []SerializedRecord, iface func(string) *fabric.Interface) {
	c.records = c.records[:0]
	for _, r := range recs {
		src, dst := iface(r.Src), iface(r.Dst)
		if src == nil || dst == nil {
			continue
		}
		c.records = append(c.records, Record{Time: r.Time, Src: src, Dst: dst, Frame: r.Frame})
	}
}

// CountByType returns the number of records whose frame has the given EtherType,
// sorted by time. It is a convenience for tests and the UI's packet panel.
func (c *Capture) CountByType(typ ethernet.EtherType) int {
	n := 0
	for _, r := range c.records {
		if r.Frame.Type == typ {
			n++
		}
	}
	return n
}

// SrcIPs returns the distinct source IP addresses observed in ARP requests,
// sorted. Used to assert which hosts the capture saw without depending on
// frame ordering.
func (c *Capture) SrcIPs() []string {
	seen := map[string]struct{}{}
	for _, r := range c.records {
		if r.Frame.Type == ethernet.EtherTypeARP {
			if m, err := arp.Unmarshal(r.Frame.Payload); err == nil {
				seen[m.SenderIP.String()] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}
