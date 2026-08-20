// Package route implements the per-machine routing table with longest-prefix
// matching (ARCHITECTURE.md §9.7). Static routes only for now; dynamic routing
// arrives later as virtual protocols.
package route

import (
	"fmt"
	"net/netip"
)

// Route is a single routing table entry.
type Route struct {
	Prefix    netip.Prefix
	NextHop   netip.Addr // invalid (zero) for directly-connected networks
	Interface string
	Metric    int
}

// Table is an ordered collection of routes. Insertion order is preserved so
// lookups are deterministic; iteration never depends on Go map order.
type Table struct {
	routes []Route
}

// NewTable returns an empty routing table.
func NewTable() *Table {
	return &Table{}
}

// Add inserts a route. The prefix is normalized (bits past the prefix length
// are zeroed).
func (t *Table) Add(r Route) error {
	if !r.Prefix.IsValid() {
		return fmt.Errorf("route: invalid prefix")
	}
	r.Prefix = r.Prefix.Masked()
	t.routes = append(t.routes, r)
	return nil
}

// Del removes all routes matching the given prefix.
func (t *Table) Del(pfx netip.Prefix) {
	out := t.routes[:0]
	for _, r := range t.routes {
		if r.Prefix != pfx.Masked() {
			out = append(out, r)
		}
	}
	t.routes = out
}

// Lookup finds the best route for dst using longest-prefix matching. Ties are
// broken by lower metric, then by insertion order — all deterministic.
func (t *Table) Lookup(dst netip.Addr) (Route, bool) {
	var best Route
	found := false
	for _, r := range t.routes {
		if !r.Prefix.Contains(dst) {
			continue
		}
		if !found ||
			r.Prefix.Bits() > best.Prefix.Bits() ||
			(r.Prefix.Bits() == best.Prefix.Bits() && r.Metric < best.Metric) {
			best = r
			found = true
		}
	}
	return best, found
}

// Routes returns a copy of the routes in insertion order.
func (t *Table) Routes() []Route {
	return append([]Route(nil), t.routes...)
}

// Len returns the number of routes.
func (t *Table) Len() int {
	return len(t.routes)
}

// Restore replaces the table contents with a previously captured snapshot.
// Route order is preserved, so longest-prefix lookups stay deterministic.
func (t *Table) Restore(routes []Route) {
	t.routes = append(t.routes[:0], routes...)
}
