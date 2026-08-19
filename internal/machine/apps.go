package machine

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack"
)

// appFunc builds a process that implements one shell command. Foreground
// commands write to the process's streams and exit; background commands (nc -l)
// return a process parked in Waiting with a step hook.
type appFunc func(m *Machine, args []string) *Process

var apps = map[string]appFunc{
	"arp":      cmdARP,
	"cat":      cmdCat,
	"echo":     cmdEcho,
	"help":     cmdHelp,
	"hostname": cmdHostname,
	"ifconfig": cmdIfconfig,
	"ip":       cmdIP,
	"ls":       cmdLS,
	"nc":       cmdNC,
	"netstat":  cmdNetstat,
	"ping":     cmdPing,
	"route":    cmdRoute,
}

func cmdHelp(m *Machine, args []string) *Process {
	p := newProcess(m, "help", args)
	p.writeOut(`Available commands:
  arp        show the ARP cache
  cat FILE   print a file
  echo TEXT  print text
  help       this list
  hostname   print the hostname
  ifconfig   show interface configuration
  ip addr|route|link   show interfaces, routes, links
  ls [DIR]   list a directory
  nc -l PORT | nc HOST PORT [MSG]   connect or listen
  netstat    show sockets
  ping [-c N] DST   ping a host
  route      show the routing table
`)
	return p
}

func cmdHostname(m *Machine, args []string) *Process {
	p := newProcess(m, "hostname", args)
	p.writeOut(m.Hostname + "\n")
	return p
}

func cmdEcho(m *Machine, args []string) *Process {
	p := newProcess(m, "echo", args)
	p.writeOut(strings.Join(args, " ") + "\n")
	return p
}

func cmdLS(m *Machine, args []string) *Process {
	p := newProcess(m, "ls", args)
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	entries, err := m.FS.ListDir(path)
	if err != nil {
		p.writeErr("ls: " + err.Error() + "\n")
		p.exit(1)
		return p
	}
	for _, e := range entries {
		p.writeOut(e + "\n")
	}
	return p
}

func cmdCat(m *Machine, args []string) *Process {
	p := newProcess(m, "cat", args)
	if len(args) < 1 {
		p.writeErr("Usage: cat FILE\n")
		p.exit(1)
		return p
	}
	data, err := m.FS.ReadFile(args[0])
	if err != nil {
		p.writeErr("cat: " + err.Error() + "\n")
		p.exit(1)
		return p
	}
	p.writeOut(string(data))
	return p
}

func cmdIP(m *Machine, args []string) *Process {
	p := newProcess(m, "ip", args)
	if len(args) < 1 {
		p.writeErr("Usage: ip addr|route|link\n")
		p.exit(1)
		return p
	}
	switch args[0] {
	case "addr":
		p.writeOut(ipAddrText(m))
	case "link":
		p.writeOut(ipLinkText(m))
	case "route":
		p.writeOut(ipRouteText(m))
	default:
		p.writeErr(fmt.Sprintf("ip: unknown object %q\n", args[0]))
		p.exit(1)
	}
	return p
}

func ipLinkText(m *Machine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "1: %s: <BROADCAST,UP> mtu 1500 qdisc noqueue\n", m.Stack.InterfaceName())
	fmt.Fprintf(&b, "    link/ether %s\n", m.Stack.MAC())
	return b.String()
}

func ipAddrText(m *Machine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "1: %s: <BROADCAST,UP> mtu 1500\n", m.Stack.InterfaceName())
	fmt.Fprintf(&b, "    link/ether %s\n", m.Stack.MAC())
	fmt.Fprintf(&b, "    inet %s scope global %s\n", m.Stack.Prefix(), m.Stack.InterfaceName())
	return b.String()
}

func ipRouteText(m *Machine) string {
	var b strings.Builder
	for _, r := range m.Stack.Routes() {
		dest := r.Prefix.String()
		if r.Prefix.Bits() == 0 {
			dest = "default"
		}
		if r.NextHop.IsValid() {
			fmt.Fprintf(&b, "%s via %s dev %s\n", dest, r.NextHop, r.Interface)
		} else {
			fmt.Fprintf(&b, "%s dev %s scope link\n", dest, r.Interface)
		}
	}
	return b.String()
}

func cmdIfconfig(m *Machine, args []string) *Process {
	p := newProcess(m, "ifconfig", args)
	st := m.Stack
	mask := prefixMask(st.Prefix())
	bc := broadcast(st.Prefix())
	var b strings.Builder
	fmt.Fprintf(&b, "%s: flags=4163<UP,BROADCAST,RUNNING,MULTICAST>  mtu 1500\n", st.InterfaceName())
	fmt.Fprintf(&b, "        inet %s  netmask %s  broadcast %s\n", st.Addr(), mask, bc)
	fmt.Fprintf(&b, "        ether %s\n", st.MAC())
	p.writeOut(b.String())
	return p
}

func cmdRoute(m *Machine, args []string) *Process {
	p := newProcess(m, "route", args)
	var b strings.Builder
	b.WriteString("Kernel IP routing table\n")
	b.WriteString("Destination     Gateway         Genmask         Flags Metric Ref    Use Iface\n")
	for _, r := range m.Stack.Routes() {
		dest := r.Prefix.Masked().Addr().String()
		gw := "0.0.0.0"
		flags := "U"
		if r.NextHop.IsValid() {
			gw = r.NextHop.String()
			flags = "UG"
		}
		genmask := netMaskString(r.Prefix.Bits())
		fmt.Fprintf(&b, "%-16s %-16s %-16s %-5s %-6d %-6d %-6d %s\n", dest, gw, genmask, flags, r.Metric, 0, 0, r.Interface)
	}
	p.writeOut(b.String())
	return p
}

func cmdARP(m *Machine, args []string) *Process {
	p := newProcess(m, "arp", args)
	entries := m.Stack.ARPEntries()
	var b strings.Builder
	if len(args) > 0 && args[0] == "-a" {
		for _, e := range entries {
			fmt.Fprintf(&b, "? (%s) at %s [ether] on %s\n", e.IP, e.MAC, m.Stack.InterfaceName())
		}
	} else {
		b.WriteString("Address                  HWtype  HWaddress           Flags Mask\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "%-24s ether   %-18s C\n", e.IP, e.MAC)
		}
	}
	p.writeOut(b.String())
	return p
}

func cmdNetstat(m *Machine, args []string) *Process {
	p := newProcess(m, "netstat", args)
	var b strings.Builder
	b.WriteString("Proto Recv-Q Send-Q Local           Foreign         State\n")
	for _, c := range m.Stack.Netstat() {
		fmt.Fprintf(&b, "%-5s %-6d %-6d %-15s %-15s %s\n", c.Proto, 0, 0, c.Local, c.Remote, c.State)
	}
	p.writeOut(b.String())
	return p
}

func cmdPing(m *Machine, args []string) *Process {
	p := newProcess(m, "ping", args)
	count := 1
	var dst string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					count = n
					i++
					continue
				}
			}
			p.writeErr("ping: invalid count\n")
			p.exit(1)
			return p
		default:
			dst = args[i]
		}
	}
	if dst == "" {
		p.writeErr("Usage: ping [-c N] DST\n")
		p.exit(1)
		return p
	}
	addr, err := netip.ParseAddr(dst)
	if err != nil {
		p.writeErr(fmt.Sprintf("ping: unknown host %s\n", dst))
		p.exit(1)
		return p
	}

	var b strings.Builder
	fmt.Fprintf(&b, "PING %s (%s) 56 bytes of data.\n", addr, addr)
	received := 0
	for i := 1; i <= count; i++ {
		res, err := m.Stack.Ping(addr)
		if err != nil {
			fmt.Fprintf(&b, "Request timeout for icmp_seq %d\n", i)
			continue
		}
		received++
		fmt.Fprintf(&b, "64 bytes from %s: icmp_seq=%d ttl=%d time=%.3f ms\n", addr, i, 64, float64(res.RTT)/float64(time.Millisecond))
	}
	fmt.Fprintf(&b, "--- %s ping statistics ---\n", addr)
	loss := 100.0
	if count > 0 {
		loss = 100.0 * float64(count-received) / float64(count)
	}
	fmt.Fprintf(&b, "%d packets transmitted, %d received, %.1f%% packet loss\n", count, received, loss)
	p.writeOut(b.String())
	if received < count {
		p.exit(1)
	}
	return p
}

func cmdNC(m *Machine, args []string) *Process {
	p := newProcess(m, "nc", args)
	if len(args) >= 2 && args[0] == "-l" {
		return ncListen(m, p, args[1])
	}
	if len(args) < 2 {
		p.writeErr("Usage: nc -l PORT | nc HOST PORT [MSG]\n")
		p.exit(1)
		return p
	}
	addr, err := netip.ParseAddr(args[0])
	if err != nil {
		p.writeErr(fmt.Sprintf("nc: unknown host %s\n", args[0]))
		p.exit(1)
		return p
	}
	port, err := strconv.Atoi(args[1])
	if err != nil || port < 1 || port > 65535 {
		p.writeErr("nc: invalid port\n")
		p.exit(1)
		return p
	}
	conn, err := m.Stack.Dial(addr, uint16(port))
	if err != nil {
		p.writeErr(fmt.Sprintf("nc: connect to %s:%d failed: %v\n", addr, port, err))
		p.exit(1)
		return p
	}
	if len(args) > 2 {
		if _, err := conn.Write([]byte(strings.Join(args[2:], " "))); err != nil {
			p.writeErr("nc: write: " + err.Error() + "\n")
			p.exit(1)
			return p
		}
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if n > 0 {
		p.writeOut(string(buf[:n]))
	}
	_ = err
	_ = conn.Close()
	return p
}

func ncListen(m *Machine, p *Process, portStr string) *Process {
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		p.writeErr("nc: invalid port\n")
		p.exit(1)
		return p
	}
	l, err := m.Stack.Listen(uint16(port))
	if err != nil {
		p.writeErr("nc: listen: " + err.Error() + "\n")
		p.exit(1)
		return p
	}
	p.Data = l
	p.writeOut(fmt.Sprintf("Listening on 0.0.0.0:%d\n", port))
	p.step = ncListenStep
	p.waitForData()
	return p
}

// ncListenStep parks the nc listener until a connection arrives, then drains
// and prints whatever the client sent.
func ncListenStep(m *Machine, p *Process) {
	l := p.Data.(*netstack.TCPConn)
	conn, err := l.Accept()
	if err != nil {
		p.waitForData() // no connection yet: park again (lazy, §5.4)
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			p.writeOut(string(buf[:n]))
		}
		if n == 0 || err != nil {
			break
		}
	}
	_ = conn.Close()
	_ = l.Close()
	p.exit(0)
}

func prefixMask(pfx netip.Prefix) string {
	return netMaskString(pfx.Bits())
}

func netMaskString(bits int) string {
	mask := make([]byte, 4)
	for i := 0; i < 4; i++ {
		rem := bits - 8*i
		switch {
		case rem >= 8:
			mask[i] = 0xff
		case rem > 0:
			mask[i] = 0xff << (8 - rem)
		}
	}
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}

func broadcast(pfx netip.Prefix) string {
	addr := pfx.Masked().Addr().As4()
	hostBits := 32 - pfx.Bits()
	for i := 0; i < 4; i++ {
		n := hostBits - (3-i)*8 // host bits in this byte (right-aligned)
		switch {
		case n >= 8:
			addr[i] = 0xff
		case n > 0:
			addr[i] |= 0xff >> (8 - n)
		}
	}
	return fmt.Sprintf("%d.%d.%d.%d", addr[0], addr[1], addr[2], addr[3])
}
