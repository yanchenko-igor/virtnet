package services

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
)

func init() {
	Register("dns", NewDNSService)
}

type DNSService struct {
	zones     map[string]Zone
	cache     *DNSCache
	roots     []RootHint
	recursive bool
}

type Zone struct {
	Name    string
	TTL     uint32
	Records []Record
}

type Record struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32
	Data  []byte
}

type RootHint struct {
	Name string
	IP   netip.Addr
}

type DNSCache struct {
	mu    sync.RWMutex
	items map[string]cacheEntry
	max   int
}

type cacheEntry struct {
	records []Record
	expiry  time.Duration
}

var RootHints = []RootHint{
	{Name: "a.root-servers.net", IP: netip.MustParseAddr("198.41.0.4")},
	{Name: "b.root-servers.net", IP: netip.MustParseAddr("199.9.14.201")},
	{Name: "c.root-servers.net", IP: netip.MustParseAddr("192.33.4.12")},
	{Name: "d.root-servers.net", IP: netip.MustParseAddr("199.7.91.13")},
	{Name: "e.root-servers.net", IP: netip.MustParseAddr("192.203.230.10")},
	{Name: "f.root-servers.net", IP: netip.MustParseAddr("192.5.5.241")},
	{Name: "g.root-servers.net", IP: netip.MustParseAddr("192.112.36.4")},
	{Name: "h.root-servers.net", IP: netip.MustParseAddr("198.97.190.53")},
	{Name: "i.root-servers.net", IP: netip.MustParseAddr("192.36.148.17")},
	{Name: "j.root-servers.net", IP: netip.MustParseAddr("192.58.128.30")},
	{Name: "k.root-servers.net", IP: netip.MustParseAddr("193.0.14.129")},
	{Name: "l.root-servers.net", IP: netip.MustParseAddr("199.7.83.42")},
	{Name: "m.root-servers.net", IP: netip.MustParseAddr("202.12.27.33")},
}

func NewDNSCache(maxSize int) *DNSCache {
	return &DNSCache{
		items: make(map[string]cacheEntry),
		max:   maxSize,
	}
}

func (c *DNSCache) Get(key string, now time.Duration) ([]Record, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || e.expiry <= now {
		return nil, false
	}
	return e.records, true
}

func (c *DNSCache) Set(key string, records []Record, ttl time.Duration, now time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max && c.max > 0 {
		for k := range c.items {
			delete(c.items, k)
			break
		}
	}
	c.items[key] = cacheEntry{records: records, expiry: now + ttl}
}

const (
	TypeA     uint16 = 1
	TypeNS    uint16 = 2
	TypeCNAME uint16 = 5
	TypeSOA   uint16 = 6
	TypePTR   uint16 = 12
	TypeMX    uint16 = 15
	TypeAAAA  uint16 = 28
	ClassIN   uint16 = 1
)

func NewDNSService(config map[string]interface{}) Service {
	svc := &DNSService{
		zones:     make(map[string]Zone),
		cache:     NewDNSCache(10000),
		roots:     RootHints,
		recursive: true,
	}

	if zones, ok := config["zones"].(map[string]interface{}); ok {
		for name, z := range zones {
			zm := z.(map[string]interface{})
			zone := Zone{Name: name}
			if ttl, ok := zm["ttl"].(float64); ok {
				zone.TTL = uint32(ttl)
			} else {
				zone.TTL = 3600
			}
			if recs, ok := zm["records"].([]interface{}); ok {
				for _, r := range recs {
					rm := r.(map[string]interface{})
					rec := Record{}
					if n, ok := rm["name"].(string); ok {
						rec.Name = n
					}
					if t, ok := rm["type"].(string); ok {
						rec.Type = dnsTypeFromString(t)
					}
					if v, ok := rm["value"].(string); ok {
						rec.Data = dnsDataFromString(rec.Type, v)
					}
					rec.Class = ClassIN
					rec.TTL = zone.TTL
					zone.Records = append(zone.Records, rec)
				}
			}
			svc.zones[name] = zone
		}
	}

	if rec, ok := config["recursive"].(bool); ok {
		svc.recursive = rec
	}
	if size, ok := config["cache_size"].(float64); ok {
		svc.cache = NewDNSCache(int(size))
	}
	if roots, ok := config["roots"].([]interface{}); ok {
		svc.roots = nil
		for _, r := range roots {
			rm := r.(map[string]interface{})
			svc.roots = append(svc.roots, RootHint{
				Name: rm["name"].(string),
				IP:   netip.MustParseAddr(rm["ip"].(string)),
			})
		}
	}

	return svc
}

func dnsTypeFromString(s string) uint16 {
	switch strings.ToUpper(s) {
	case "A":
		return TypeA
	case "NS":
		return TypeNS
	case "CNAME":
		return TypeCNAME
	case "SOA":
		return TypeSOA
	case "PTR":
		return TypePTR
	case "MX":
		return TypeMX
	case "AAAA":
		return TypeAAAA
	default:
		return 0
	}
}

func dnsDataFromString(t uint16, s string) []byte {
	switch t {
	case TypeA:
		return netip.MustParseAddr(s).AsSlice()
	case TypeAAAA:
		return netip.MustParseAddr(s).AsSlice()
	case TypeNS, TypeCNAME, TypePTR:
		return encodeName(s)
	case TypeMX:
		parts := strings.Fields(s)
		pref := uint16(10)
		if len(parts) > 1 {
			fmt.Sscanf(parts[0], "%d", &pref)
			return append(encodeUint16(pref), encodeName(parts[1])...)
		}
		return append(encodeUint16(pref), encodeName(s)...)
	case TypeSOA:
		return encodeName(s)
	default:
		return []byte(s)
	}
}

func encodeName(s string) []byte {
	if s == "@" || s == "." {
		return []byte{0}
	}
	var b []byte
	for _, part := range strings.Split(strings.Trim(s, "."), ".") {
		b = append(b, byte(len(part)))
		b = append(b, part...)
	}
	b = append(b, 0)
	return b
}

func encodeUint16(v uint16) []byte {
	return []byte{byte(v >> 8), byte(v)}
}

func (d *DNSService) Ports() []ServicePort {
	return []ServicePort{
		{Port: 53, Proto: uint8(ipv4.ProtoUDP)},
		{Port: 53, Proto: uint8(ipv4.ProtoTCP)},
	}
}

func (d *DNSService) HandleRequest(ctx ServiceContext, req ServiceRequest) ([]byte, error) {
	if ctx.Proto == uint8(ipv4.ProtoUDP) {
		return d.handleUDP(ctx, req.Payload)
	}
	return d.handleTCP(ctx, req.Payload)
}

func (d *DNSService) handleUDP(ctx ServiceContext, payload []byte) ([]byte, error) {
	msg, err := ParseDNSMessage(payload)
	if err != nil {
		return nil, err
	}
	resp := d.processQuery(ctx, msg)
	return resp.Pack(), nil
}

func (d *DNSService) handleTCP(ctx ServiceContext, payload []byte) ([]byte, error) {
	if len(payload) < 2 {
		return nil, fmt.Errorf("dns: TCP message too short")
	}
	length := binary.BigEndian.Uint16(payload[:2])
	if len(payload)-2 < int(length) {
		return nil, fmt.Errorf("dns: TCP message truncated")
	}
	msg, err := ParseDNSMessage(payload[2 : 2+length])
	if err != nil {
		return nil, err
	}
	resp := d.processQuery(ctx, msg)
	data := resp.Pack()
	out := make([]byte, 2+len(data))
	binary.BigEndian.PutUint16(out[:2], uint16(len(data)))
	copy(out[2:], data)
	return out, nil
}

type DNSMessage struct {
	ID         uint16
	Flags      uint16
	QDCount    uint16
	ANCount    uint16
	NSCount    uint16
	ARCount    uint16
	Questions  []DNSQuestion
	Answers    []Record
	Authority  []Record
	Additional []Record
}

type DNSQuestion struct {
	Name  string
	Type  uint16
	Class uint16
}

func ParseDNSMessage(data []byte) (DNSMessage, error) {
	if len(data) < 12 {
		return DNSMessage{}, fmt.Errorf("dns: message too short")
	}
	msg := DNSMessage{
		ID:      binary.BigEndian.Uint16(data[0:2]),
		Flags:   binary.BigEndian.Uint16(data[2:4]),
		QDCount: binary.BigEndian.Uint16(data[4:6]),
		ANCount: binary.BigEndian.Uint16(data[6:8]),
		NSCount: binary.BigEndian.Uint16(data[8:10]),
		ARCount: binary.BigEndian.Uint16(data[10:12]),
	}
	offset := 12
	var err error
	for i := uint16(0); i < msg.QDCount; i++ {
		q, n, e := parseQuestion(data, offset)
		if e != nil {
			return DNSMessage{}, e
		}
		msg.Questions = append(msg.Questions, q)
		offset += n
	}
	return msg, err
}

func parseQuestion(data []byte, offset int) (DNSQuestion, int, error) {
	name, n, err := parseName(data, offset)
	if err != nil {
		return DNSQuestion{}, 0, err
	}
	offset += n
	if offset+4 > len(data) {
		return DNSQuestion{}, 0, fmt.Errorf("dns: question truncated")
	}
	qtype := binary.BigEndian.Uint16(data[offset : offset+2])
	qclass := binary.BigEndian.Uint16(data[offset+2 : offset+4])
	return DNSQuestion{Name: name, Type: qtype, Class: qclass}, offset + 4, nil
}

func parseName(data []byte, offset int) (string, int, error) {
	var labels []string
	origOffset := offset
	for {
		if offset >= len(data) {
			return "", 0, fmt.Errorf("dns: name truncated")
		}
		length := data[offset]
		offset++
		if length == 0 {
			break
		}
		if length&0xC0 == 0xC0 {
			if offset >= len(data) {
				return "", 0, fmt.Errorf("dns: pointer truncated")
			}
			ptr := binary.BigEndian.Uint16(data[offset-1:offset+1]) & 0x3FFF
			offset++
			name, _, err := parseName(data, int(ptr))
			if err != nil {
				return "", 0, err
			}
			return strings.Join(labels, ".") + "." + name, offset - origOffset, nil
		}
		if offset+int(length) > len(data) {
			return "", 0, fmt.Errorf("dns: label truncated")
		}
		labels = append(labels, string(data[offset:offset+int(length)]))
		offset += int(length)
	}
	return strings.Join(labels, "."), offset - origOffset, nil
}

func (msg *DNSMessage) Pack() []byte {
	var buf []byte
	buf = append(buf, encodeUint16(msg.ID)...)
	buf = append(buf, encodeUint16(msg.Flags)...)
	buf = append(buf, encodeUint16(uint16(len(msg.Questions)))...)
	buf = append(buf, encodeUint16(uint16(len(msg.Answers)))...)
	buf = append(buf, encodeUint16(uint16(len(msg.Authority)))...)
	buf = append(buf, encodeUint16(uint16(len(msg.Additional)))...)
	for _, q := range msg.Questions {
		buf = append(buf, encodeName(q.Name)...)
		buf = append(buf, encodeUint16(q.Type)...)
		buf = append(buf, encodeUint16(q.Class)...)
	}
	for _, r := range msg.Answers {
		buf = append(buf, r.Pack()...)
	}
	for _, r := range msg.Authority {
		buf = append(buf, r.Pack()...)
	}
	for _, r := range msg.Additional {
		buf = append(buf, r.Pack()...)
	}
	return buf
}

func (r Record) Pack() []byte {
	var buf []byte
	buf = append(buf, encodeName(r.Name)...)
	buf = append(buf, encodeUint16(r.Type)...)
	buf = append(buf, encodeUint16(r.Class)...)
	buf = append(buf, encodeUint32(r.TTL)...)
	buf = append(buf, encodeUint16(uint16(len(r.Data)))...)
	buf = append(buf, r.Data...)
	return buf
}

func encodeUint32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func (d *DNSService) processQuery(ctx ServiceContext, query DNSMessage) DNSMessage {
	resp := DNSMessage{
		ID:        query.ID,
		Flags:     0x8000, // QR=1
		Questions: query.Questions,
	}
	for _, q := range query.Questions {
		if q.Class != ClassIN {
			continue
		}
		records := d.lookup(ctx, q.Name, q.Type)
		if len(records) > 0 {
			resp.Answers = append(resp.Answers, records...)
			resp.Flags |= 0x8000
		} else {
			resp.Flags |= 0x0003 // NXDOMAIN
		}
	}
	return resp
}

func (d *DNSService) lookup(ctx ServiceContext, name string, qtype uint16) []Record {
	key := fmt.Sprintf("%s|%d", name, qtype)
	clk := ctx.Clock.(interface{ Now() time.Duration })
	if recs, ok := d.cache.Get(key, clk.Now()); ok {
		return recs
	}

	for zoneName, zone := range d.zones {
		if strings.HasSuffix(name, zoneName) {
			for _, r := range zone.Records {
				if r.Type == qtype && d.nameMatch(name, zoneName, r.Name) {
					d.cache.Set(key, []Record{r}, time.Duration(r.TTL)*time.Second, clk.Now())
					return []Record{r}
				}
			}
		}
	}

	if d.recursive && qtype == TypeA {
		recs := d.resolveRecursive(ctx, name)
		if len(recs) > 0 {
			return recs
		}
	}

	return nil
}

// ResolveLocal resolves a name using only authoritative zones (no recursion)
// This is used by the local machine when it hosts the DNS service
func (d *DNSService) ResolveLocal(name string, qtype uint16) []Record {
	key := fmt.Sprintf("%s|%d", name, qtype)
	now := time.Duration(time.Now().UnixNano())
	if recs, ok := d.cache.Get(key, now); ok {
		return recs
	}

	for zoneName, zone := range d.zones {
		if strings.HasSuffix(name, zoneName) {
			for _, r := range zone.Records {
				if r.Type == qtype && d.nameMatch(name, zoneName, r.Name) {
					d.cache.Set(key, []Record{r}, time.Duration(r.TTL)*time.Second, time.Duration(time.Now().UnixNano()))
					return []Record{r}
				}
			}
		}
	}
	return nil
}

func (d *DNSService) resolveRecursive(ctx ServiceContext, name string) []Record {
	return d.resolveRecursiveFrom(ctx, name, d.roots, 0)
}

func (d *DNSService) resolveRecursiveFrom(ctx ServiceContext, name string, servers []RootHint, depth int) []Record {
	if depth > 10 {
		return nil // prevent infinite recursion
	}
	
	for _, server := range servers {
		recs := d.queryAndFollow(ctx, server.IP, name, TypeA, depth)
		if len(recs) > 0 {
			return recs
		}
	}
	return nil
}

func (d *DNSService) queryAndFollow(ctx ServiceContext, serverIP netip.Addr, name string, qtype uint16, depth int) []Record {
	query := d.buildQuery(name, qtype)
	
	// Send UDP query to server
	resp, err := d.sendUDPQuery(ctx, serverIP, query)
	if err != nil || len(resp) == 0 {
		return nil
	}
	
	msg, err := ParseDNSMessage(resp)
	if err != nil {
		return nil
	}
	
	// Check if we got an answer
	if len(msg.Answers) > 0 {
		for _, ans := range msg.Answers {
			if ans.Type == qtype {
				clk := ctx.Clock.(interface{ Now() time.Duration })
				d.cache.Set(fmt.Sprintf("%s|%d", name, qtype), []Record{ans}, time.Duration(ans.TTL)*time.Second, clk.Now())
				return []Record{ans}
			}
		}
	}
	
	// Check for referral (authority section has NS records)
	if len(msg.Authority) > 0 {
		// Extract NS records and their glue A records
		var nsRecords []Record
		glue := make(map[string]netip.Addr)
		
		for _, auth := range msg.Authority {
			if auth.Type == TypeNS {
				nsRecords = append(nsRecords, auth)
			} else if auth.Type == TypeA {
				// Glue record
				if ip, ok := d.parseAData(auth.Data); ok {
					glue[auth.Name] = ip
				}
			}
		}
		
		// Follow referral - query NS servers
		if len(nsRecords) > 0 {
			// Build list of servers to query
			var nextServers []RootHint
			for _, ns := range nsRecords {
				nsName := string(ns.Data)
				if ip, ok := glue[nsName]; ok {
					nextServers = append(nextServers, RootHint{Name: nsName, IP: ip})
				}
			}
			// If no glue, would need to resolve NS names first (simplified for now)
			if len(nextServers) > 0 {
				return d.resolveRecursiveFrom(ctx, name, nextServers, depth+1)
			}
		}
	}
	
	return nil
}

func (d *DNSService) parseAData(data []byte) (netip.Addr, bool) {
	if len(data) != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{data[0], data[1], data[2], data[3]}), true
}

func (d *DNSService) buildQuery(name string, qtype uint16) []byte {
	id := uint16(rand.Intn(65535))
	msg := DNSMessage{
		ID: id,
		Flags: 0x0100, // RD=1 (recursion desired)
		QDCount: 1,
		Questions: []DNSQuestion{
			{Name: name, Type: qtype, Class: ClassIN},
		},
	}
	return msg.Pack()
}

func (d *DNSService) sendUDPQuery(ctx ServiceContext, serverIP netip.Addr, query []byte) ([]byte, error) {
	// Create UDP socket - type assert Stack to access ListenUDP
	stack, ok := ctx.Stack.(interface{ ListenUDP(uint16) (UDPSocket, error) })
	if !ok {
		return nil, fmt.Errorf("stack does not support ListenUDP")
	}
	// Create UDP socket
	sock, err := stack.ListenUDP(0)
	if err != nil {
		return nil, err
	}
	defer sock.Close()
	
	// Send query to server
	if err := sock.SendTo(serverIP, 53, query); err != nil {
		return nil, err
	}
	
	// Read response (synchronous in our model)
	_, _, data, ok := sock.RecvFrom()
	if !ok {
		return nil, fmt.Errorf("no response from DNS server")
	}
	return data, nil
}

func (d *DNSService) nameMatch(qname, zone, rname string) bool {
	if rname == "@" {
		return qname == zone
	}
	return strings.HasPrefix(qname, rname+".") && strings.HasSuffix(qname, "."+zone)
}
