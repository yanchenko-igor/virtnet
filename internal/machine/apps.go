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
	"curl":     cmdCurl,
	"date":     cmdDate,
	"dig":      cmdDig,
	"echo":     cmdEcho,
	"help":     cmdHelp,
	"hostname": cmdHostname,
	"ifconfig": cmdIfconfig,
	"ip":       cmdIP,
	"ls":       cmdLS,
	"nc":       cmdNC,
	"netstat":  cmdNetstat,
	"nslookup": cmdNSLookup,
	"ping":     cmdPing,
	"route":    cmdRoute,
}

func cmdHelp(m *Machine, args []string) *Process {
	p := newProcess(m, "help", args)
	p.writeOut(`Available commands:
  arp        show the ARP cache
  cat FILE   print a file
  curl URL   HTTP client
  date       print the virtual date and time
  dig [@SERVER] NAME [TYPE]   DNS lookup
  echo TEXT  print text
  help       this list
  hostname   print the hostname
  ifconfig   show interface configuration
  ip addr|route|link   show interfaces, routes, links
  ls [DIR]   list a directory
  nc -l PORT | nc HOST PORT [MSG]   connect or listen
  netstat    show sockets
  nslookup NAME [SERVER]   DNS lookup
  ping [-c N] DST   ping a host
  route      show the routing table
`)
	return p
}

// cmdDate prints the virtual wall time: the lab's start timestamp plus the
// accumulated virtual-clock offset. It never consults the host clock
// (ARCHITECTURE.md §5.2).
func cmdDate(m *Machine, args []string) *Process {
	p := newProcess(m, "date", args)
	t := m.clock.WallTime()
	p.writeOut(t.Format("Mon Jan _2 15:04:05.000 UTC 2006"))
	p.writeOut(fmt.Sprintf(" (t=%s)\n", m.clock.Now()))
	return p
}

func cmdHostname(m *Machine, args []string) *Process {
	p := newProcess(m, "hostname", args)
	p.writeOut(m.Hostname + "\n")
	return p
}

func cmdEcho(m *Machine, args []string) *Process {
	p := newProcess(m, "echo", args)
	out := strings.Join(args, " ")
	// Expand $? to last exit code
	out = strings.ReplaceAll(out, "$?", strconv.Itoa(m.lastExitCode))
	p.writeOut(out + "\n")
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
	// Ensure output ends with a single newline
	out := string(data)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	p.writeOut(out)
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
	addr, err := m.resolveHost(dst)
	if err != nil {
		p.writeErr(fmt.Sprintf("ping: unknown host %s\n", dst))
		p.exit(1)
		return p
	}

	// Ping runs as a multi-step process: each Step() sends one ping, prints
	// the result, and parks. Foreground=true makes execute() drive it to
	// completion for RunCommand/HandleInput compatibility; the UI can
	// override for incremental rendering.
	p.Foreground = true
	st := &pingState{
		addr:      addr,
		count:     count,
		sent:      0,
		recv:      0,
		started:   false,
		startTime: m.clock.Now(),
	}
	p.Data = st
	p.step = pingStep
	p.interrupt = pingInterrupt // print summary on Ctrl+C
	p.writeOut(fmt.Sprintf("PING %s (%s) 56 bytes of data.\n", addr, addr))
	p.waitForData() // park; execute() will drive to completion
	return p
}

type pingState struct {
	addr      netip.Addr
	count     int
	sent      int
	recv      int
	started   bool
	startTime time.Duration // virtual time when ping started
}

func pingStep(m *Machine, p *Process) {
	st := p.Data.(*pingState)
	if !st.started {
		st.started = true
	} else {
		// Subsequent steps: just continue (virtual time already advanced by Ping)
	}
	if st.sent >= st.count {
		// All sent: print summary and exit
		loss := 100.0
		if st.count > 0 {
			loss = 100.0 * float64(st.count-st.recv) / float64(st.count)
		}
		p.writeOut(fmt.Sprintf("--- %s ping statistics ---\n", st.addr))
		p.writeOut(fmt.Sprintf("%d packets transmitted, %d received, %.1f%% packet loss, time %.0fms\n",
			st.count, st.recv, loss, float64(m.clock.Now()-st.startTime)/float64(time.Millisecond)))
		if st.recv < st.count {
			p.exit(1)
		} else {
			p.exit(0)
		}
		return
	}
	st.sent++
	res, err := m.Stack.Ping(st.addr)
	if err != nil {
		p.writeOut(fmt.Sprintf("Request timeout for icmp_seq %d\n", st.sent))
	} else {
		st.recv++
		p.writeOut(fmt.Sprintf("64 bytes from %s: icmp_seq=%d ttl=%d time=%.3f ms\n",
			st.addr, st.sent, 64, float64(res.RTT)/float64(time.Millisecond)))
	}
	// Park for next Step (virtual time already advanced by Ping's RTT)
	p.waitForData()
}

// pingInterrupt prints ping statistics when Ctrl+C is pressed.
func pingInterrupt(m *Machine, p *Process) {
	st := p.Data.(*pingState)
	loss := 100.0
	if st.sent > 0 {
		loss = 100.0 * float64(st.sent-st.recv) / float64(st.sent)
	}
	p.writeOut(fmt.Sprintf("\n--- %s ping statistics ---\n", st.addr))
	p.writeOut(fmt.Sprintf("%d packets transmitted, %d received, %.1f%% packet loss, time %.0fms\n",
		st.sent, st.recv, loss, float64(m.clock.Now()-st.startTime)/float64(time.Millisecond)))
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

func cmdNSLookup(m *Machine, args []string) *Process {
	p := newProcess(m, "nslookup", args)
	if len(args) < 1 {
		p.writeErr("Usage: nslookup NAME [SERVER]\n")
		p.exit(1)
		return p
	}
	name := args[0]
	server := "127.0.0.1"
	if len(args) > 1 {
		server = args[1]
	}
	p.writeOut(fmt.Sprintf("Server:		%s\n", server))
	p.writeOut(fmt.Sprintf("Address:	%s#53\n\n", server))
	p.writeOut(fmt.Sprintf("Name:	%s\n", name))
	p.writeOut("Address: 10.0.0.10\n")
	return p
}

func cmdDig(m *Machine, args []string) *Process {
	p := newProcess(m, "dig", args)
	if len(args) < 1 {
		p.writeErr("Usage: dig [@SERVER] NAME [TYPE]\n")
		p.exit(1)
		return p
	}
	server := ""
	name := ""
	qtype := "A"
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") {
			server = arg[1:]
		} else if name == "" {
			name = arg
		} else {
			qtype = strings.ToUpper(arg)
		}
	}
	if server == "" {
		server = "127.0.0.1"
	}
	p.writeOut(fmt.Sprintf("; <<>> DiG 9.16.1 <<>> %s %s\n", name, qtype))
	p.writeOut(";; global options: +cmd\n")
	p.writeOut(";; Got answer:\n")
	p.writeOut(";; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 12345\n")
	p.writeOut(";; flags: qr aa rd ra; QUERY: 1, ANSWER: 1, AUTHORITY: 0, ADDITIONAL: 1\n\n")
	p.writeOut(";; QUESTION SECTION:\n")
	p.writeOut(fmt.Sprintf(";%s.\t\tIN\t%s\n\n", name, qtype))
	p.writeOut(";; ANSWER SECTION:\n")
	p.writeOut(fmt.Sprintf("%s.\t\t3600\tIN\t%s\t10.0.0.10\n\n", name, qtype))
	p.writeOut(";; Query time: 1 msec\n")
	p.writeOut(fmt.Sprintf(";; SERVER: %s#53\n", server))
	p.writeOut(";; WHEN: Thu Jan  1 00:00:00 UTC 1970\n")
	p.writeOut(";; MSG SIZE  rcvd: 56\n")
	return p
}

func prefixMask(pfx netip.Prefix) string {
	return netMaskString(pfx.Bits())
}

func cmdCurl(m *Machine, args []string) *Process {
	p := newProcess(m, "curl", args)
	if len(args) < 1 {
		p.writeErr("Usage: curl [-i] [-I] [-v] URL\n")
		p.exit(1)
		return p
	}

	// Parse flags
	includeHeaders := false
	headOnly := false
	verbose := false
	url := ""
	for _, arg := range args {
		switch arg {
		case "-i", "--include":
			includeHeaders = true
		case "-I", "--head":
			headOnly = true
		case "-v", "--verbose":
			verbose = true
		default:
			if strings.HasPrefix(arg, "http://") {
				url = arg
			}
		}
	}
	if url == "" {
		p.writeErr("Usage: curl [-i] [-I] [-v] URL\n")
		p.exit(1)
		return p
	}

	// Simple URL parsing: http://host:port/path
	if !strings.HasPrefix(url, "http://") {
		p.writeErr("curl: only http:// URLs supported\n")
		p.exit(1)
		return p
	}
	url = url[7:] // strip http://

	parts := strings.SplitN(url, "/", 2)
	hostPort := parts[0]
	path := "/"
	if len(parts) > 1 {
		path = "/" + parts[1]
	}

	hostParts := strings.Split(hostPort, ":")
	host := hostParts[0]
	port := 80
	if len(hostParts) > 1 {
		fmt.Sscanf(hostParts[1], "%d", &port)
	}

	// Resolve host via DNS if it's not an IP address
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// Try DNS resolution
		addr, err = m.resolveHost(host)
		if err != nil {
			p.writeErr(fmt.Sprintf("curl: could not resolve host: %s\n", host))
			p.exit(1)
			return p
		}
	}

	// Connect via TCP
	conn, err := m.Stack.Dial(addr, uint16(port))
	if err != nil {
		p.writeErr(fmt.Sprintf("curl: connect failed: %v\n", err))
		p.exit(1)
		return p
	}

	// Send HTTP request
	method := "GET"
	if headOnly {
		method = "HEAD"
	}
	request := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, path, host)
	if verbose {
		p.writeOut(fmt.Sprintf("* Trying %s:%d...\n", host, port))
		p.writeOut(fmt.Sprintf("* Connected to %s (%s) port %d\n", host, addr, port))
		p.writeOut(fmt.Sprintf("> %s %s HTTP/1.1\n", method, path))
		p.writeOut(fmt.Sprintf("> Host: %s\n", host))
		p.writeOut("> Connection: close\n")
		p.writeOut(">\n")
	}
	if _, err := conn.Write([]byte(request)); err != nil {
		p.writeErr(fmt.Sprintf("curl: write failed: %v\n", err))
		_ = conn.Close()
		p.exit(1)
		return p
	}

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if n > 0 {
		response := string(buf[:n])
		if verbose {
			// Print response headers
			if idx := strings.Index(response, "\r\n\r\n"); idx >= 0 {
				headers := response[:idx]
				for _, line := range strings.Split(headers, "\r\n") {
					p.writeOut(fmt.Sprintf("< %s\n", line))
				}
			}
		}
		// Extract body from HTTP response (skip headers)
		if idx := strings.Index(response, "\r\n\r\n"); idx >= 0 {
			headers := response[:idx+4]
			body := response[idx+4:]
			if includeHeaders {
				p.writeOut(headers)
			}
			p.writeOut(body)
		} else {
			p.writeOut(response)
		}
	}
	_ = err
	_ = conn.Close()
	return p
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
