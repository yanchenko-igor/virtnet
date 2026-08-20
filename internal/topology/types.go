package topology

import (
	"encoding/json"
	"net/netip"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ethernet"
)

// MACAddr wraps ethernet.MAC for JSON marshaling.
type MACAddr string

func (m MACAddr) MAC() (ethernet.MAC, error) {
	return ethernet.ParseMAC(string(m))
}

// Prefix wraps netip.Prefix for JSON marshaling.
type Prefix netip.Prefix

func (p Prefix) Prefix() netip.Prefix {
	return netip.Prefix(p)
}

func (p *Prefix) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return err
	}
	*p = Prefix(prefix)
	return nil
}

func (p Prefix) MarshalJSON() ([]byte, error) {
	return json.Marshal(netip.Prefix(p).String())
}

// Delay wraps time.Duration for JSON marshaling (ms integer).
type Delay time.Duration

func (d Delay) Duration() time.Duration {
	return time.Duration(d)
}

func (d *Delay) UnmarshalJSON(b []byte) error {
	var ms int64
	if err := json.Unmarshal(b, &ms); err != nil {
		return err
	}
	*d = Delay(ms * int64(time.Millisecond))
	return nil
}

func (d Delay) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(time.Duration(d) / time.Millisecond))
}

// MachineDef defines a virtual machine.
type MachineDef struct {
	ID      string  `json:"id"`
	Host    string  `json:"host"`
	IP      Prefix  `json:"ip"`
	MAC     MACAddr `json:"mac"`
	Gateway string  `json:"gateway,omitempty"` // IP of default gateway
}

// SwitchDef defines a virtual switch.
type SwitchDef struct {
	ID string `json:"id"`
}

// RouterDef defines a virtual router.
type RouterDef struct {
	ID         string               `json:"id"`
	Host       string               `json:"host"`
	Interfaces []RouterInterfaceDef `json:"interfaces"`
}

// RouterInterfaceDef defines a router interface.
type RouterInterfaceDef struct {
	Name string  `json:"name"`
	IP   Prefix  `json:"ip"`
	MAC  MACAddr `json:"mac"`
}

// LinkDef defines a link between two endpoints.
type LinkDef struct {
	A     string `json:"a"` // "machine:id:iface" or "switch:id:port" or "router:id:iface"
	B     string `json:"b"`
	Delay Delay  `json:"delay_ms,omitempty"`
}

// Topology is the complete network topology.
type Topology struct {
	Machines []MachineDef `json:"machines"`
	Switches []SwitchDef  `json:"switches"`
	Routers  []RouterDef  `json:"routers"`
	Links    []LinkDef    `json:"links"`
}

// ParseEndpoint parses "type:id:name" into components.
func ParseEndpoint(s string) (kind, id, name string, err error) {
	// format: machine:id:iface / switch:id:port / router:id:iface
	parts := []string{}
	for _, p := range []rune(s) {
		if p == ':' {
			parts = append(parts, "")
		} else {
			if len(parts) == 0 {
				parts = append(parts, string(p))
			} else {
				parts[len(parts)-1] += string(p)
			}
		}
	}
	if len(parts) != 3 {
		return "", "", "", err
	}
	return parts[0], parts[1], parts[2], nil
}

type ErrInvalidEndpoint string

func (e ErrInvalidEndpoint) Error() string {
	return "topology: invalid endpoint " + string(e)
}

var _ error = ErrInvalidEndpoint("")
