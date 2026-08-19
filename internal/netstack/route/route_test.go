package route

import (
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

func TestLongestPrefixMatch(t *testing.T) {
	tab := NewTable()
	if err := tab.Add(Route{Prefix: mustPrefix(t, "0.0.0.0/0"), NextHop: mustAddr(t, "10.0.0.254"), Interface: "eth0", Metric: 10}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/16"), Interface: "eth0"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/24"), Interface: "eth0"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		dst       string
		wantBits  int
		wantRoute bool
	}{
		{name: "most specific /24", dst: "10.0.0.5", wantBits: 24, wantRoute: true},
		{name: "falls back to /16", dst: "10.0.1.5", wantBits: 16, wantRoute: true},
		{name: "default route", dst: "8.8.8.8", wantBits: 0, wantRoute: true},
		{name: "default route via next hop", dst: "192.168.1.1", wantBits: 0, wantRoute: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := tab.Lookup(mustAddr(t, tt.dst))
			if ok != tt.wantRoute {
				t.Fatalf("Lookup(%s) ok = %v, want %v", tt.dst, ok, tt.wantRoute)
			}
			if ok && r.Prefix.Bits() != tt.wantBits {
				t.Errorf("Lookup(%s) prefix bits = %d, want %d", tt.dst, r.Prefix.Bits(), tt.wantBits)
			}
		})
	}
}

func TestMetricTiebreak(t *testing.T) {
	tab := NewTable()
	tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/24"), NextHop: mustAddr(t, "10.0.0.1"), Interface: "eth0", Metric: 100})
	tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/24"), NextHop: mustAddr(t, "10.0.0.2"), Interface: "eth1", Metric: 50})

	r, ok := tab.Lookup(mustAddr(t, "10.0.0.9"))
	if !ok {
		t.Fatal("no route")
	}
	if r.NextHop != mustAddr(t, "10.0.0.2") {
		t.Errorf("NextHop = %v, want lower metric 10.0.0.2", r.NextHop)
	}
}

func TestNoRoute(t *testing.T) {
	tab := NewTable()
	tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/24"), Interface: "eth0"})
	if _, ok := tab.Lookup(mustAddr(t, "10.1.0.1")); ok {
		t.Fatal("expected no route")
	}
}

func TestAddDel(t *testing.T) {
	tab := NewTable()
	tab.Add(Route{Prefix: mustPrefix(t, "10.0.0.0/24"), Interface: "eth0"})
	tab.Add(Route{Prefix: mustPrefix(t, "10.0.1.0/24"), Interface: "eth1"})
	if tab.Len() != 2 {
		t.Fatalf("Len = %d, want 2", tab.Len())
	}
	tab.Del(mustPrefix(t, "10.0.0.0/24"))
	if tab.Len() != 1 {
		t.Fatalf("Len after Del = %d, want 1", tab.Len())
	}
	if _, ok := tab.Lookup(mustAddr(t, "10.0.0.1")); ok {
		t.Fatal("deleted route still matches")
	}
}

func TestAddRejectsInvalid(t *testing.T) {
	tab := NewTable()
	if err := tab.Add(Route{Prefix: netip.Prefix{}}); err == nil {
		t.Fatal("expected error for invalid prefix")
	}
}
