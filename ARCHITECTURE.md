# VirtNet Architecture

Single-process, deterministic, userspace virtual computer and network simulator.

## 1. Purpose

VirtNet is a network laboratory: a terminal application where each tab is a virtual
machine connected to a configurable virtual network. Machines ping each other, run
ordinary networking tools, exchange arbitrary packets, and can be organized into
separate physical/logical networks. A machine can be a minimal Linux-like host, a
router with a Cisco-inspired CLI, a switch, or a server.

The **entire virtual world exists inside one host process**. No kernel networking,
no host interfaces, no per-machine OS processes, no real-time delays.

```
                     Virtual Lab
        ┌──── PC1 ────┬──── PC2 ────┬──── R1 ────┬──── Server ────┐
        │ $ ping ...  │ $ ip addr   │ Router#    │ $ nc ...       │
        └─────────────┴─────────────┴────────────┴────────────────┘
```

## 2. Executive Summary

VirtNet is **not** a discrete-event simulator. It is a **synchronous, deterministic
state-transition system with a manipulable virtual clock**.

- All machines, network devices, protocols, applications, and packets exist as
  userspace state in one process.
- Virtual time is an explicit **value in the state**, not a scheduling mechanism.
- When an operation has a simulated temporal cost, it **calculates** the resulting
  virtual timestamp, **advances the clock immediately**, and continues executing.
- **Nothing in the virtual world waits for time to pass.** No host timer, sleep,
  async delay, or event queue represents simulated time.
- The only thing the virtual world may genuinely wait for is **external user input**.

## 3. Guiding Principles

These invariants must be preserved by every part of the implementation.

| # | Invariant |
|---|-----------|
| 1 | **Single process** — all machines and networks live in one host process. |
| 2 | **No host networking** — virtual packets never enter the host network stack. |
| 3 | **No real-time waiting** — virtual delays never cause host delays. |
| 4 | **Timers are state** — a simulated timer is a timestamp, not an async mechanism. |
| 5 | **No required event queue** — causal operations execute synchronously and immediately. |
| 6 | **Virtual time is explicit** — time is part of simulation state. |
| 7 | **Time advancement is computation** — operations calculate temporal consequences and advance the clock immediately. |
| 8 | **Deterministic causal ordering** — same state + same inputs ⇒ same transitions. |
| 9 | **User input is external** — the simulation may pause for user input, never for virtual time. |

## 4. Design Decisions & Rejected Alternatives

### 4.1 Decision: pure userspace simulation, not real Linux networking

The initial proposal (early conversation) used Linux network namespaces, veth pairs,
and bridges to get "real networking." This was **rejected**. It would require
privileged operations, tie the product to Linux kernel features, and make the
simulator a wrapper around host resources rather than a self-contained model.

**Rejected dependencies** (never use for virtual-machine networking):
Linux network namespaces, virtual Ethernet devices, host bridges, TAP/TUN
interfaces, host routing, host sockets, kernel network stacks, separate VM
processes, real network latency.

### 4.2 Decision: no discrete-event scheduler

The first full specification modeled the system around a **virtual clock + event
queue + scheduler** (a classic discrete-event simulator: "schedule delivery at
T+50ms, wait, process events in order"). This was **rejected** in favor of a
synchronous model.

The distinction that matters:

> ❌ Time passage drives execution. — wrong
> ✅ Execution computes time passage. — right

| | Discrete-event (rejected) | Synchronous (adopted) |
|---|---|---|
| Delivery | `send` → schedule → wait → T becomes 150 → deliver | `send` → calculate T+50 → advance clock → deliver immediately |
| Components | Clock, EventQueue, EventScheduler | VirtualClock, Machines, NetworkFabric, State |
| Waiting | events wait for their timestamp | nothing waits |
| Time | external mechanism | part of the state |

There is no `EventQueue`, `Scheduler`, or `TimerQueue` as a fundamental subsystem.

### 4.3 Decision: time advancement is computation

```
send packet at T=100            send packet at T=100
        ↓                               ↓
schedule delivery at T=150      calculate delivery at T=150
        ↓                               ↓
wait / process other events     advance virtual clock to T=150
        ↓                               ↓
T becomes 150                   deliver packet immediately
        ↓                               ↓
deliver packet                  continue processing
```

Latency, bandwidth, packet loss, corruption, ARP/TCP/DNS timeouts are all
**mathematical transformations of simulation time**, applied synchronously:

```
transmit(packet):
    start = clock.now()
    end = start + transmission_time + propagation_delay
    clock.set(end)
    receiver.receive(packet)
```

A "50 ms link delay" means "the receiver observes this packet at virtual time
T+50ms" — it does **not** mean "wait 50 ms then invoke the receiver."

## 5. Execution Model

### 5.1 The core loop

```
                 INPUT
                   │
                   ▼
            ┌─────────────┐
            │ State       │
            │ transition  │
            └──────┬──────┘
                   │
            calculate time
                   │
                   ▼
            advance virtual
                 clock
                   │
                   ▼
            immediately
            apply result
                   │
                   ▼
               NEW STATE
```

### 5.2 Virtual clock

The clock is independent of system time, monotonic host time, wall-clock time, and
CPU time.

```
Virtual time: 10.000 seconds
Host time:    0.002 seconds
```

```
VirtualClock {
    current_time
}
```

API: `now()`, `advance_by()`, `advance_to()`.

### 5.3 Causal ordering

No event queue means ordering is defined by **causality**. If `PC1.send(A)` causes
`Switch.receive(A)`, then `Switch.receive(A)` completes before any next independent
operation runs. The entire chain can execute during a single host function call:

```
PC1.send(packet)
  ├── calculate transmission time
  ├── advance virtual clock
  ├── deliver packet to link
  ├── switch receives + processes packet
  ├── calculate next link delay
  ├── advance virtual clock
  └── destination receives packet
```

Where multiple simultaneous outcomes have no intrinsic causal order, the
implementation defines a deterministic ordering rule.

### 5.4 Timers are timestamps, not mechanisms

The simulation core is **completely lazy**: it holds no registry, scheduler, or
wakeup mechanism for timers. Timeouts are owned by **the process that initiated
the event**. That process holds the deadline as its own state and decides what to
do when it is next evaluated:

```
TCP:  retransmit_at = 105.0        → owned by the TCP connection/process
ARP:  expires_at   = 130.0         → evaluated on ARP cache use
DNS:  expires_at   = 160.0         → owned by the DNS client process
```

A protocol timer follows one model:

```
deadline = current_virtual_time + duration
```

Nothing is scheduled to happen at `deadline`. When the owning component is next
evaluated, it compares the deadline against the current virtual clock and decides
the outcome itself (retransmit, give up, print "Request timeout").

```
ARP cache lookup
  ↓
compare expiration timestamp vs. current virtual clock
  ↓
entry expired / valid
```

The one practical trigger: when the user advances virtual time (`Run`/`Advance` in
the UI), the per-machine process scheduler may be **queried** for the earliest
pending process `wakeup_at`, advance to it, and step that one process. This is a
lazy query, not a queue — the process still owns the deadline and the decision.

This keeps **time jumps** free. `clock.advance_by(1 hour)` is an immediate
operation; state that depends on time is evaluated lazily on next use. The
simulator can represent years of virtual time in seconds of host time.

### 5.5 User interaction

The one place the model pauses is the user:

```
pc1$ nc 10.0.0.2 80        → virtual process waits for input
```

This is not simulated waiting — the simulation is **paused**. When the user types,
the input becomes a new causal input processed immediately.

> Virtual world: never waits.
> Real UI: may wait for user input.

## 6. System Architecture

```
Simulation
│
├── VirtualClock
│
├── Machines
│   ├── Machine
│   │   ├── Console
│   │   ├── Processes
│   │   ├── Filesystem
│   │   ├── NetworkInterfaces
│   │   └── NetworkStack
│   └── ...
│
├── NetworkFabric
│   ├── Networks
│   ├── Links
│   ├── Switches
│   └── Routers
│
└── UI/API
    ├── Terminals
    ├── Topology
    ├── Packet capture
    └── Simulation controls
```

The `Simulation` object owns all virtual resources:

```
simulation.clock()
simulation.machines()
simulation.networks()
simulation.create_machine(...)
simulation.destroy_machine(...)
simulation.create_network(...)
simulation.connect(...)
simulation.disconnect(...)
```

## 7. Domain Model

All entities are ordinary in-memory objects: a packet is an object, an interface is
an object, a TCP connection is an object, a router is an object.

### 7.1 Simulation state

The entire virtual world is one serializable state object:

```
SimulationState {
    virtual_clock
    machines
    networks
    links
    switches
    routers
    packets
    sockets
    processes
    routing_tables
    arp_caches
    filesystems
    random_state
}
```

It contains everything required to reproduce the simulation.

### 7.2 Machine

```
Machine
├── identity
├── hostname
├── interfaces
├── network stack
├── routing table
├── processes
├── filesystem
└── console
```

```
Machine {
    id: "pc1",
    hostname: "pc1",
    interfaces: [
        eth0 {
            mac: "02:00:00:00:00:01",
            ipv4: "10.0.0.10/24"
        }
    ]
}
```

No machine requires an OS process, a thread, a kernel, or a host network interface.

### 7.3 Network interface

```
NetworkInterface {
    name
    mac_address
    mtu
    administrative_state      # UP | DOWN
    network_connection
    rx_queue
    tx_queue
}
```

### 7.4 Ethernet frame

```
EthernetFrame {
    destination_mac
    source_mac
    ether_type
    payload
}
```

The complete frame must be preserved where possible. **Unknown EtherTypes are
allowed**, not discarded — the requirement is arbitrary network packets, not just
ICMP/TCP/UDP.

### 7.5 Network

A virtual network is independent of the physical host network. LAN-A, LAN-B, WAN-A
are connected only when the topology explicitly connects them:

```
PC1 ─ LAN-A ─ R1 ─ WAN-A ─ R2 ─ LAN-B ─ PC2
```

### 7.6 Link

```
Link {
    id
    endpoints
    propagation_delay
    bandwidth
    packet_loss
    jitter
    corruption
    enabled
}
```

All properties affect **virtual state transitions only**, computed synchronously.

### 7.7 Process

Virtual processes are cooperative/event-driven objects, never host processes:

```
Process {
    pid
    name
    state
    stdin
    stdout
    stderr
    file_descriptors
}
```

Examples: shell, ping, netcat, DNS client, HTTP client, SSH client.

Executing any command has a simulated CPU cost: the machine advances the
virtual clock by a fixed per-command cost before the command runs. A
command that also sends traffic advances further as its frames cross
links, so `date`, `echo`, and a `ping` all consume virtual time.

### 7.8 Virtual filesystem

Per-machine in-memory filesystem:

```
/
├── bin/
├── etc/
├── home/
├── tmp/
└── var/
```

Persistence may be added later.

## 8. Layered Network Stack

```
Ethernet
   ↓
ARP / IPv4 / IPv6
   ↓
ICMP / UDP / TCP
   ↓
Socket API
   ↓
Applications
```

Clean interfaces per layer; applications must not touch lower-level internals:

```
NetworkInterface.receive(frame)
Ethernet.receive(frame)
IPv4.receive(packet)
UDP.receive(segment)
Socket.deliver(data)
Process.on_readable()
```

### Core data flow (end to end, fully synchronous)

```
Application → Socket → TCP/UDP → IP → ARP → Ethernet
  → Virtual Interface → Virtual Link
      ├── calculate virtual time transformation
      ├── advance clock
  → Virtual Interface → Ethernet → IP → TCP/UDP → Socket → Application
```

## 9. Protocol Implementations

### 9.1 Ethernet switching

A userspace virtual switch:

```
Switch
├── ports
├── MAC forwarding table (MAC → port)
└── forwarding logic
```

Required: MAC learning, unicast forwarding, broadcast, unknown-unicast flooding,
port enable/disable, MAC table aging (using virtual time).

### 9.2 ARP

Each IPv4 machine has an ARP cache:

```
ARPEntry {
    ip_address
    mac_address
    expires_at
}
```

Implement request, reply, cache, and expiration. Expiration is a timestamp
comparison against the virtual clock — no expiration timer.

### 9.3 IPv4

Userspace IPv4 stack: address configuration, subnet masks, packet parsing and
generation, routing, TTL, forwarding, ICMP integration, fragmentation/reassembly
where appropriate. IPv6 support is architected for from the start, implementation
deferred until IPv4 is stable.

### 9.4 ICMP

Minimum: Echo Request, Echo Reply, Destination Unreachable, Time Exceeded.
Enables `ping` and lays the foundation for `traceroute`.

### 9.5 UDP

Virtual sockets, not host syscalls:

```
VirtualUDPSocket {
    local_address
    local_port
    remote_address
    remote_port
    receive_buffer
}
```

### 9.6 TCP

A userspace state machine with the standard states (CLOSED, LISTEN, SYN-SENT,
SYN-RECEIVED, ESTABLISHED, FIN-WAIT-1/2, CLOSE-WAIT, LAST-ACK, TIME-WAIT, CLOSING).
Timers are deadlines:

```
tcp.retransmission_deadline = current_time + RTO
```

No host timer is created.

### 9.7 Routing

```
Route {
    destination
    prefix_length
    next_hop
    interface
    metric
}
```

Longest-prefix match. Static routing first; dynamic routing (RIP, OSPF, BGP) later
as virtual protocols.

### 9.8 Router machine

A router is a specialized machine with multiple interfaces and forwarding enabled.
Forwarding is fully synchronous:

```
receive frame → parse Ethernet → parse IPv4 → route lookup
  → decrement TTL → ARP lookup → construct Ethernet frame → transmit
```

A Cisco-like personality exposes an IOS-style CLI (`enable`, `show interfaces`,
`show ip route`, `configure terminal`, `interface eth0`, `ip address ... no
shutdown`). The CLI is a presentation/config layer over the same router state — not
emulation of actual Cisco firmware.

## 10. Machine Types

All machine types share one common interface and the same packet/network
abstractions.

Initial:
- `LinuxLikeMachine`
- `RouterMachine`
- `Switch`
- `ServerMachine`

Eventually: Firewall, LoadBalancer, DNS Server, DHCP Server, NAT Gateway, Wireless
AP, Custom Appliance.

The architecture leaves room for a future emulated-machine backend (CPU/RAM/devices/
kernel) connected to the **same** `VirtualInterface → VirtualNetwork →
VirtualInterface` fabric, so an emulated RISC-V Linux machine could communicate
with a lightweight virtual router without changing the network fabric.

## 11. UI/API

The UI consumes the topology model; it does not define it. Topology state is
separate from presentation.

### 11.1 Terminal

Tabs, each a console session attached to a machine (a tab is a console session, not
necessarily a machine — multiple consoles per machine allowed later):

```
┌──── PC1 ────┬──── PC2 ────┬──── R1 ────┬──── Server ────┐
│ pc1$        │ pc2$        │ Router#    │ server$        │
└─────────────┴─────────────┴────────────┴────────────────┘
```

The machine must not depend on the UI.

### 11.2 Topology

Visual editor: add machines, drag to connect, click a node to open its console.
Topology is a first-class object (machines + networks + interfaces with addresses),
independent of any particular UI.

### 11.3 Packet inspection

Because every packet is already an in-memory object, packet capture is native.
Record timestamp, source, destination, interface, and the frame/packet/segment.
Optionally retain packet history:

```
T=10.030  Ethernet: 02:00:00:00:00:01 → 02:00:00:00:00:02
          IPv4:     10.0.0.1 → 10.0.0.2  TTL 64  ICMP
```

### 11.4 Simulation controls

`Run`, `Pause`, `Step`, `Advance time`. **Step means "execute the next deterministic
causal operation"** — it must NOT mean "wait for the next scheduled event"; there
may be no event queue. Display virtual time and pending state.

Typing into a terminal creates simulation input events; the UI stays responsive
while the state is manipulated.

## 12. Determinism, Checkpointing, Replay

### 12.1 Determinism

Identical initial state + identical external input ⇒ identical results. Determinism
must not depend on host timing, thread scheduling, host random generators,
wall-clock time, or OS timers. Randomness uses a **simulation-owned seeded PRNG**:

```
Simulation(seed = 12345)
```

### 12.2 Checkpoint / restore / replay / rewind

Because the world is one serializable state, checkpointing is natural. A checkpoint
contains: virtual clock, machines, network topology/state, TCP/UDP/ARP/routing
state, process state, filesystem state, PRNG state.

External inputs are recorded for replay:

```
T=0.000   create PC1
T=0.000   connect PC1 to LAN1
T=2.000   terminal input: ping 10.0.0.2
```

Replay reconstructs the same state transitions. Rewind = restore a checkpoint.

## 13. Non-Goals

The initial implementation must NOT:

- Emulate physical hardware or an entire x86/ARM computer.
- Use host networking to implement virtual networking.
- Run one OS process per machine.
- Achieve real-time network behavior.
- Reproduce a specific commercial router's firmware.
- Depend on internet connectivity or privileged host operations.

## 14. Implementation Phases

| Phase | Scope |
|-------|-------|
| 1 — State engine | `Simulation`, `VirtualClock`, deterministic state. **No scheduler.** |
| 2 — Ethernet | `EthernetFrame`, `NetworkInterface`, `VirtualLink`; raw PC1→PC2 frames. |
| 3 — IPv4/ARP/ICMP | IPv4, ARP, ICMP, routing; `ping 10.0.0.2` works. |
| 4 — UDP/TCP | UDP, TCP, `VirtualSocket`; connection, data, close. |
| 5 — Machine | Machine, Process, Shell, Filesystem, Console; `ip`, `ping`, `arp`, `route`, `netstat`, `nc`. |
| 6 — Router/Switch | VirtualSwitch (MAC learning), Router (multi-interface, forwarding, TTL, ICMP errors). |
| 7 — UI | Terminal tabs, topology editor, packet inspection, clock display. |
| 8 — State management | Serialization, checkpoint, restore, replay. |
| 9 — Advanced networking | IPv6, DNS, DHCP, NAT, firewalls, OSPF, BGP, bandwidth, loss/corruption/jitter, link failures, capture. |

Performance target: thousands of virtual machines per process. Optimize for low
memory overhead, no thread/process per machine, event-driven (synchronous)
execution, low-copy packet handling — but do not optimize prematurely at the
expense of clean protocol abstractions.

## 15. Acceptance Criteria

The following must work entirely inside one process:

```
             LAN-A                LAN-B
       ┌───────┼───────┐           │
      PC1     PC2      R1 ─────── PC3
```

- PC1 = 10.0.0.10/24, PC2 = 10.0.0.20/24, R1 eth0 = 10.0.0.1/24, R1 eth1 = 10.0.1.1/24, PC3 = 10.0.1.10/24
- `PC1 ping 10.0.0.20` succeeds (same subnet).
- `PC1 ping 10.0.1.10` succeeds after configuring the appropriate gateway/routes.
- `PC3 ping 10.0.0.10` succeeds.

All of it without creating a host network interface, sending a packet through the
host kernel network stack, starting another OS process, or waiting for real time.

## 16. Guiding Definition

> This is a synchronous, deterministic virtual world containing multiple networked
> machines. All machines, network devices, protocols, applications, and packets
> exist as userspace state within a single process. Virtual time is an explicit
> value in that state. When an operation has a simulated temporal cost, the
> operation calculates the resulting virtual timestamp, advances the virtual clock
> immediately, and continues execution. Nothing in the virtual world waits for time
> to pass, and no host timer, sleep, asynchronous delay, or event queue is required
> to represent simulated time.

---

*Source: extracted conversation log (`log-*.txt`). The architecture reflects the
final agreed model (turns 4–7); the earlier real-networking (turn 2) and
discrete-event (turn 3) approaches are documented in §4 as rejected alternatives.*
