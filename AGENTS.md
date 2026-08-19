# AGENTS.md

Project: VirtNet — single-process, deterministic, userspace virtual network simulator.
Authoritative design doc: `ARCHITECTURE.md`.

## Commit policy

- **Commit every time tests pass.** After any phase/gate completes and `go test ./...`
  is green, commit. Use conventional commits (e.g. `feat:`, `fix:`, `refactor:`).
- Never add AI attribution or "Co-Authored-By" lines to commits.
- Do not build after changes (no `go build`); rely on `go test` / `go vet` / `gofmt`.

## Verification

- `gofmt -w . && go vet ./... && go test ./...` before every commit.
- Keep coverage high; run `go test -cover ./...` to check.

## Core invariants

- **Synchronous execution model**: time is state, advancement is computation. No
  event queue, no scheduler, no host timers, no goroutines for protocol logic.
  Frames traverse links synchronously; a full ARP/TCP exchange happens inside one
  Send call. Virtual time is advanced by link traversal only.
- **Determinism**: never iterate Go maps in order-sensitive paths (collect + sort
  keys). Seeded sim-owned PRNG. Never consult the host clock or host randomness in
  protocol code. Every behavior must be reproducible from the same script.
- **Lazy timeouts**: timers are deadlines (virtual-clock offsets) owned by the
  initiating process/connection, checked via `Stack.Tick()` when the machine
  drives the stack. No registered timer list.
- **Silent drops**: malformed frames/packets, unknown EtherTypes, unknown ports
  (except TCP SYN → RST), out-of-order data → dropped with nil error.
- Userspace only: virtual sockets, not host syscalls. IPv4 first; IPv6 deferred.

## Structure

- `internal/clock` — `VirtualClock` (Now/AdvanceBy/AdvanceTo/Set)
- `internal/sim` — `Simulation` root (seeded PRNG + clock + Digest)
- `internal/fabric` — `Interface`/`Link` (shared-clock synchronous transmit)
- `internal/netstack` — ethernet, arp, ipv4, icmp, route, checksum, udp, tcp,
  `Stack` + socket layer (UDPSocket, TCPConn)
- `cmd/virtnet` — seed/digest demo CLI

## Phases (ARCHITECTURE.md §14)

1-4 done (state engine, Ethernet, IPv4/ARP/ICMP+ping, UDP/TCP+sockets).
Next: 5 Machine/Process/Shell/VFS, 6 Router/Switch, 7 UI, 8 Serialization, 9 Advanced.

## Test conventions

- Table-driven tests; acceptance tests assert exact virtual-clock totals.
- White-box tests (`package netstack`) may access unexported state.
- Use `reflect.DeepEqual` for frames/packets (contain `[]byte`).
- `setupPair` in `internal/netstack/stack_test.go` builds PC1↔PC2.
