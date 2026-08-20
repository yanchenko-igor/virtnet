package topology

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBuildRefTopology(t *testing.T) {
	data, err := os.ReadFile("testdata/topology_ref.json")
	if err != nil {
		t.Fatal(err)
	}
	var topo Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		t.Fatal(err)
	}
	// Debug: print parsed topology
	t.Logf("Machines: %d", len(topo.Machines))
	for _, m := range topo.Machines {
		t.Logf("  Machine: id=%s host=%s ip=%v mac=%v", m.ID, m.Host, m.IP, m.MAC)
	}
	t.Logf("Switches: %d", len(topo.Switches))
	t.Logf("Routers: %d", len(topo.Routers))
	for _, r := range topo.Routers {
		t.Logf("  Router: id=%s host=%s interfaces=%d", r.ID, r.Host, len(r.Interfaces))
	}
	t.Logf("Links: %d", len(topo.Links))
	for _, l := range topo.Links {
		t.Logf("  Link: %s <-> %s delay=%v", l.A, l.B, l.Delay)
	}

	l, sw, rt, err := topo.BuildLab()
	if err != nil {
		t.Fatal(err)
	}

	// Verify switch and router created
	if sw == nil {
		t.Fatal("switch not created")
	}
	if rt == nil {
		t.Fatal("router not created")
	}

	// Verify 3 machines
	if len(l.Machines) != 3 {
		t.Fatalf("expected 3 machines, got %d", len(l.Machines))
	}

	// Verify machines have correct IPs
	pc1 := l.Machine("pc1")
	if pc1 == nil {
		t.Fatal("pc1 not found")
	}
	if pc1.Stack.Addr().String() != "10.0.0.10" {
		t.Errorf("pc1 IP = %s, want 10.0.0.10", pc1.Stack.Addr())
	}

	pc3 := l.Machine("pc3")
	if pc3 == nil {
		t.Fatal("pc3 not found")
	}
	if pc3.Stack.Addr().String() != "10.0.1.10" {
		t.Errorf("pc3 IP = %s, want 10.0.1.10", pc3.Stack.Addr())
	}

	// Verify switch and router exist (returned from BuildLab)
	if sw == nil {
		t.Fatal("switch not created")
	}
	if rt == nil {
		t.Fatal("router not created")
	}

	// Test ping pc1 -> pc2 (same subnet, cold)
	if _, err := pc1.RunCommand("ping 10.0.0.20"); err != nil {
		t.Fatalf("pc1->pc2 ping failed: %v", err)
	}
	clock1 := l.Clock.Now()

	// Test ping pc1 -> pc3 (cross subnet)
	if _, err := pc1.RunCommand("ping 10.0.1.10"); err != nil {
		t.Fatalf("pc1->pc3 ping failed: %v", err)
	}
	clock2 := l.Clock.Now()

	// Verify clock advanced (cold ping 90ms + cross subnet 130ms + command costs)
	if clock2 <= clock1 {
		t.Errorf("clock did not advance: %v -> %v", clock1, clock2)
	}

	// Verify default route on pc1
	if out, err := pc1.RunCommand("ip route"); err != nil || !contains(out, "default via 10.0.0.1") {
		t.Errorf("pc1 missing default route: %v", err)
	}
}

func TestBuildDNSTopology(t *testing.T) {
	data, err := os.ReadFile("testdata/dns_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var topo Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		t.Fatal(err)
	}

	l, _, _, err := topo.BuildLab()
	if err != nil {
		t.Fatal(err)
	}

	// Verify 2 machines
	if len(l.Machines) != 2 {
		t.Fatalf("expected 2 machines, got %d", len(l.Machines))
	}

	dns1 := l.Machine("dns1")
	if dns1 == nil {
		t.Fatal("dns1 not found")
	}
	pc1 := l.Machine("pc1")
	if pc1 == nil {
		t.Fatal("pc1 not found")
	}

	// Test nslookup from pc1 to dns1 (authoritative zone)
	out, err := pc1.RunCommand("nslookup www.example.com 10.0.0.53")
	if err != nil {
		t.Fatalf("nslookup failed: %v", err)
	}
	if !strings.Contains(out, "Address: 10.0.0.10") {
		t.Errorf("nslookup output missing expected address:\n%s", out)
	}
	if !strings.Contains(out, "www.example.com") {
		t.Errorf("nslookup output missing queried name:\n%s", out)
	}

	// Test dig from pc1 to dns1
	out, err = pc1.RunCommand("dig @10.0.0.53 www.example.com A")
	if err != nil {
		t.Fatalf("dig failed: %v", err)
	}
	if !strings.Contains(out, "10.0.0.10") {
		t.Errorf("dig output missing expected address:\n%s", out)
	}

	// Test nslookup for @ record
	out, err = pc1.RunCommand("nslookup example.com 10.0.0.53")
	if err != nil {
		t.Fatalf("nslookup @ failed: %v", err)
	}
	if !strings.Contains(out, "10.0.0.10") {
		t.Errorf("nslookup @ missing address:\n%s", out)
	}

	// Test dig for MX record
	out, err = pc1.RunCommand("dig @10.0.0.53 example.com MX")
	if err != nil {
		t.Fatalf("dig MX failed: %v", err)
	}
	if !strings.Contains(out, "MX") {
		t.Errorf("dig MX missing record type:\n%s", out)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
