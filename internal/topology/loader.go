package topology

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
	"github.com/yanchenko-igor/virtnet/internal/lab"
	"github.com/yanchenko-igor/virtnet/internal/machine"
	"github.com/yanchenko-igor/virtnet/internal/netstack"
	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
	"github.com/yanchenko-igor/virtnet/internal/router"
)

func mustMAC(s string) ethernet.MAC {
	m, err := ethernet.ParseMAC(s)
	if err != nil {
		panic(fmt.Sprintf("topology: bad MAC %q: %v", s, err))
	}
	return m
}

// BuildLab constructs a Lab from the topology definition.
func (t *Topology) BuildLab() (*lab.Lab, *fabric.Switch, *router.Router, error) {
	c := clock.New()

	// Create switches
	switches := make(map[string]*fabric.Switch)
	switchPorts := make(map[string]*fabric.Interface)
	for _, sw := range t.Switches {
		switches[sw.ID] = fabric.NewSwitch(c, 0)
	}
	// Pre-create switch ports referenced in links
	for _, link := range t.Links {
		for _, ep := range []string{link.A, link.B} {
			parts := strings.Split(ep, ":")
			if len(parts) == 3 && parts[0] == "switch" {
				swID, portName := parts[1], parts[2]
				key := fmt.Sprintf("switch:%s:%s", swID, portName)
				if _, exists := switchPorts[key]; !exists {
					port := fabric.NewInterface(portName, mustMAC("02:00:00:00:00:00"))
					switchPorts[key] = port
					if sw, ok := switches[swID]; ok {
						if err := sw.AddPort(portName, port); err != nil {
							return nil, nil, nil, fmt.Errorf("switch %s port %s: %w", swID, portName, err)
						}
					}
				}
			}
		}
	}

	// Create routers
	routers := make(map[string]*router.Router)
	routerInterfaces := make(map[string]*fabric.Interface)
	for _, r := range t.Routers {
		rt := router.New(c, 0)
		routers[r.ID] = rt
		for _, ifaceDef := range r.Interfaces {
			mac, err := ifaceDef.MAC.MAC()
			if err != nil {
				return nil, nil, nil, fmt.Errorf("router %s iface %s: %w", r.ID, ifaceDef.Name, err)
			}
			iface := fabric.NewInterface(ifaceDef.Name, mac)
			if err := rt.AddInterface(ifaceDef.Name, iface, ifaceDef.IP.Prefix()); err != nil {
				return nil, nil, nil, fmt.Errorf("router %s iface %s: %w", r.ID, ifaceDef.Name, err)
			}
			routerInterfaces[fmt.Sprintf("router:%s:%s", r.ID, ifaceDef.Name)] = iface
		}
	}

	// Create machines
	machines := make(map[string]*machine.Machine)
	machineInterfaces := make(map[string]*fabric.Interface)
	for _, m := range t.Machines {
		mac, err := m.MAC.MAC()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("machine %s: %w", m.ID, err)
		}
		iface := fabric.NewInterface("eth0", mac)
		mach, err := machine.New(m.ID, m.Host, c, iface, netstack.Config{Addr: m.IP.Prefix()})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("machine %s: %w", m.ID, err)
		}
		if m.Gateway != "" {
			gw := netip.MustParseAddr(m.Gateway)
			if err := mach.Stack.AddRoute(netip.MustParsePrefix("0.0.0.0/0"), gw, "eth0", 0); err != nil {
				return nil, nil, nil, fmt.Errorf("machine %s gateway: %w", m.ID, err)
			}
		}
		machines[m.ID] = mach
		machineInterfaces[fmt.Sprintf("machine:%s:eth0", m.ID)] = iface
	}

	// Connect links
	for _, link := range t.Links {
		a := getEndpoint(link.A, machineInterfaces, routerInterfaces, switchPorts)
		b := getEndpoint(link.B, machineInterfaces, routerInterfaces, switchPorts)
		if a == nil || b == nil {
			return nil, nil, nil, fmt.Errorf("link endpoint not found: %s <-> %s", link.A, link.B)
		}
		if _, err := fabric.NewLink(c, a, b, link.Delay.Duration()); err != nil {
			return nil, nil, nil, fmt.Errorf("link %s-%s: %w", link.A, link.B, err)
		}
	}

	// Collect machines in order
	var machineList []*machine.Machine
	for _, m := range t.Machines {
		machineList = append(machineList, machines[m.ID])
	}

	l, err := lab.NewWithMachines(c, machineList)
	if err != nil {
		return nil, nil, nil, err
	}
	// Return first switch and router (if any)
	var sw *fabric.Switch
	if len(t.Switches) > 0 {
		sw = switches[t.Switches[0].ID]
	}
	var rt *router.Router
	if len(t.Routers) > 0 {
		rt = routers[t.Routers[0].ID]
	}
	return l, sw, rt, nil
}

func getEndpoint(key string, machines, routerIfaces, switchPorts map[string]*fabric.Interface) *fabric.Interface {
	if iface, ok := machines[key]; ok {
		return iface
	}
	if iface, ok := routerIfaces[key]; ok {
		return iface
	}
	// switch:port -> switch port interface
	parts := strings.Split(key, ":")
	if len(parts) == 3 && parts[0] == "switch" {
		return switchPorts[fmt.Sprintf("switch:%s:%s", parts[1], parts[2])]
	}
	return nil
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
