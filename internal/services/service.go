package services

import (
	"net/netip"
)

type ServiceKey struct {
	Port  uint16
	Proto uint8 // ipv4.Protocol value
}

type ServicePort struct {
	Port  uint16
	Proto uint8
}

type ServiceContext struct {
	Machine interface{} // *machine.Machine
	Stack   interface{} // *netstack.Stack
	SrcAddr netip.Addr
	SrcPort uint16
	DstAddr netip.Addr
	DstPort uint16
	Proto   uint8
	Clock   interface{} // *clock.VirtualClock
}

type ServiceRequest struct {
	Payload []byte
}

type Service interface {
	Ports() []ServicePort
	HandleRequest(ctx ServiceContext, req ServiceRequest) ([]byte, error)
}

type ServiceFactory func(config map[string]interface{}) Service

var factories = make(map[string]ServiceFactory)

func Register(name string, factory ServiceFactory) {
	if _, exists := factories[name]; exists {
		panic("service already registered: " + name)
	}
	factories[name] = factory
}

func GetFactory(name string) (ServiceFactory, bool) {
	f, ok := factories[name]
	return f, ok
}
