package machine

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

func mustMAC(t *testing.T, s string) ethernet.MAC {
	t.Helper()
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func setupMachines(t *testing.T, delay time.Duration) (*clock.VirtualClock, *Machine, *Machine) {
	t.Helper()
	c := clock.New()
	pc1if := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	pc2if := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:02"))
	pc1, err := New("pc1", "pc1", c, pc1if, netstack.Config{Addr: netip.MustParsePrefix("10.0.0.10/24")})
	if err != nil {
		t.Fatalf("New(pc1): %v", err)
	}
	pc2, err := New("pc2", "pc2", c, pc2if, netstack.Config{Addr: netip.MustParsePrefix("10.0.0.20/24")})
	if err != nil {
		t.Fatalf("New(pc2): %v", err)
	}
	if _, err := fabric.NewLink(c, pc1if, pc2if, delay); err != nil {
		t.Fatal(err)
	}
	return c, pc1, pc2
}

func TestShellBasics(t *testing.T) {
	_, pc1, _ := setupMachines(t, 10*time.Millisecond)

	out, err := pc1.RunCommand("hostname")
	if err != nil || out != "pc1\n" {
		t.Errorf("hostname = %q, %v", out, err)
	}

	out, err = pc1.RunCommand("cat /etc/hostname")
	if err != nil || out != "pc1\n" {
		t.Errorf("cat /etc/hostname = %q, %v", out, err)
	}

	out, _ = pc1.RunCommand("ls /")
	for _, want := range []string{"bin/", "etc/", "home/", "tmp/", "var/"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls / missing %q:\n%s", want, out)
		}
	}

	if out, err := pc1.RunCommand("ip addr"); err != nil {
		t.Errorf("ip addr: %v", err)
	} else if !strings.Contains(out, "inet 10.0.0.10/24 scope global eth0") {
		t.Errorf("ip addr missing address:\n%s", out)
	}

	if out, err := pc1.RunCommand("ifconfig"); err != nil {
		t.Errorf("ifconfig: %v", err)
	} else if !strings.Contains(out, "inet 10.0.0.10  netmask 255.255.255.0  broadcast 10.0.0.255") {
		t.Errorf("ifconfig missing address:\n%s", out)
	}

	if out, err := pc1.RunCommand("ip route"); err != nil {
		t.Errorf("ip route: %v", err)
	} else if !strings.Contains(out, "10.0.0.0/24 dev eth0 scope link") {
		t.Errorf("ip route missing direct route:\n%s", out)
	}

	if out, err := pc1.RunCommand("route"); err != nil {
		t.Errorf("route: %v", err)
	} else {
		want := fmt.Sprintf("%-16s %-16s %-16s %-5s", "10.0.0.0", "0.0.0.0", "255.255.255.0", "U")
		if !strings.Contains(out, want) {
			t.Errorf("route missing row %q:\n%s", want, out)
		}
	}

	if _, err := pc1.RunCommand("nosuchcmd"); err == nil {
		t.Error("unknown command should fail")
	}
}

func TestShellPing(t *testing.T) {
	c, pc1, _ := setupMachines(t, 10*time.Millisecond)

	out, err := pc1.RunCommand("ping 10.0.0.20")
	if err != nil {
		t.Fatalf("ping failed: %v\n%s", err, out)
	}
	want := "64 bytes from 10.0.0.20: icmp_seq=1 ttl=64 time=40.000 ms"
	if !strings.Contains(out, want) {
		t.Errorf("ping output missing %q:\n%s", want, out)
	}
	if !strings.Contains(out, "1 packets transmitted, 1 received, 0.0% packet loss") {
		t.Errorf("ping stats wrong:\n%s", out)
	}
	if got := c.Now(); got != 40*time.Millisecond {
		t.Errorf("clock = %v, want 40ms", got)
	}
}

func TestShellPingFailure(t *testing.T) {
	_, pc1, _ := setupMachines(t, 10*time.Millisecond)
	out, err := pc1.RunCommand("ping 10.0.0.99")
	if err == nil {
		t.Fatal("ping to a silent host should fail")
	}
	if !strings.Contains(out, "Request timeout for icmp_seq 1") {
		t.Errorf("missing timeout line:\n%s", out)
	}
	if !strings.Contains(out, "1 packets transmitted, 0 received, 100.0% packet loss") {
		t.Errorf("missing loss stats:\n%s", out)
	}
}

func TestShellPingCount(t *testing.T) {
	c, pc1, _ := setupMachines(t, 10*time.Millisecond)
	out, err := pc1.RunCommand("ping -c 3 10.0.0.20")
	if err != nil {
		t.Fatalf("ping -c 3 failed: %v\n%s", err, out)
	}
	for _, seq := range []string{"icmp_seq=1", "icmp_seq=2", "icmp_seq=3"} {
		if !strings.Contains(out, seq) {
			t.Errorf("missing %s:\n%s", seq, out)
		}
	}
	// First packet cold (40ms: ARP 20 + echo 10 + reply 10), then warm 20ms.
	if got := c.Now(); got != 80*time.Millisecond {
		t.Errorf("clock = %v, want 80ms (40 + 20 + 20)", got)
	}
}

func TestShellARP(t *testing.T) {
	_, pc1, _ := setupMachines(t, 10*time.Millisecond)
	if _, err := pc1.RunCommand("ping 10.0.0.20"); err != nil {
		t.Fatal(err)
	}

	out, err := pc1.RunCommand("arp")
	if err != nil {
		t.Fatalf("arp: %v", err)
	}
	if !strings.Contains(out, "10.0.0.20") || !strings.Contains(out, "02:00:00:00:00:02") {
		t.Errorf("arp missing entry:\n%s", out)
	}

	out, _ = pc1.RunCommand("arp -a")
	if !strings.Contains(out, "? (10.0.0.20) at 02:00:00:00:00:02 [ether] on eth0") {
		t.Errorf("arp -a missing entry:\n%s", out)
	}
}

func TestShellNetstat(t *testing.T) {
	_, pc1, pc2 := setupMachines(t, 10*time.Millisecond)

	if _, err := pc2.RunCommand("nc -l 8080"); err != nil {
		t.Fatal(err)
	}
	out, err := pc2.RunCommand("netstat")
	if err != nil {
		t.Fatalf("netstat: %v", err)
	}
	if !strings.Contains(out, "10.0.0.20:8080") || !strings.Contains(out, "LISTEN") {
		t.Errorf("netstat missing nc listener:\n%s", out)
	}

	if _, err := pc1.RunCommand("nc 10.0.0.20 8080 hello"); err != nil {
		t.Fatal(err)
	}
	out, _ = pc1.RunCommand("netstat")
	if !strings.Contains(out, "10.0.0.10:") {
		t.Errorf("pc1 netstat missing client socket:\n%s", out)
	}
}

func TestNCExchange(t *testing.T) {
	c, pc1, pc2 := setupMachines(t, 10*time.Millisecond)

	out, err := pc2.RunCommand("nc -l 8080")
	if err != nil {
		t.Fatalf("nc -l: %v", err)
	}
	if out != "Listening on 0.0.0.0:8080\n" {
		t.Errorf("nc -l output = %q", out)
	}

	var nc *Process
	for _, p := range pc2.Processes() {
		if p.Name == "nc" {
			nc = p
		}
	}
	if nc == nil {
		t.Fatal("nc listener process not registered")
	}
	if got := len(pc2.Processes()); got != 2 { // shell + nc
		t.Errorf("pc2 processes = %d, want 2", got)
	}

	if _, err := pc1.RunCommand("nc 10.0.0.20 8080 hello"); err != nil {
		t.Fatalf("nc client: %v", err)
	}

	// The listener is still parked; stepping the machine drains the data.
	pc2.Step()
	if got := nc.Stdout.String(); !strings.Contains(got, "hello") {
		t.Errorf("server never received 'hello', got %q", got)
	}
	if nc.State != Exited {
		t.Errorf("nc state = %v, want EXITED after receiving data", nc.State)
	}
	if got := len(pc2.Processes()); got != 1 {
		t.Errorf("pc2 processes = %d, want 1 after reap", got)
	}
	// Client: handshake 50 + data-ACK 20 + FIN 10 + FIN-ACK 10 = 90ms; the
	// server drains on Step, then its own close adds FIN 10 + ACK 10 = 110ms.
	if got := c.Now(); got != 110*time.Millisecond {
		t.Errorf("clock = %v, want 110ms", got)
	}
}

func TestConsoleTranscript(t *testing.T) {
	_, pc1, _ := setupMachines(t, 10*time.Millisecond)
	pc1.HandleInput("hostname")
	tr := pc1.Console.Transcript()
	if !strings.Contains(tr, "pc1$ hostname\n") || !strings.Contains(tr, "pc1\n") || !strings.HasSuffix(tr, "pc1$ ") {
		t.Errorf("transcript malformed:\n%q", tr)
	}
}

func TestDeterministicShellScenario(t *testing.T) {
	run := func() (time.Duration, string, error) {
		c, pc1, pc2 := setupMachines(t, 10*time.Millisecond)
		if _, err := pc1.RunCommand("ping 10.0.0.20"); err != nil {
			return 0, "", err
		}
		if _, err := pc2.RunCommand("nc -l 8080"); err != nil {
			return 0, "", err
		}
		var nc *Process
		for _, p := range pc2.Processes() {
			if p.Name == "nc" {
				nc = p
			}
		}
		if _, err := pc1.RunCommand("nc 10.0.0.20 8080 hello"); err != nil {
			return 0, "", err
		}
		pc2.Step()
		return c.Now(), nc.Stdout.String(), nil
	}

	ta, oa, err := run()
	if err != nil {
		t.Fatal(err)
	}
	tb, ob, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if ta != tb || oa != ob {
		t.Errorf("non-deterministic: (%v, %q) vs (%v, %q)", ta, oa, tb, ob)
	}
}
