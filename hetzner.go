package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/dmotylev/goproperties"
	"github.com/dmotylev/hetzner/api"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

const paidTimeFormat = "2006-01-02"

const (
	KindNil = iota
	KindHost
	KindManagement
	KindNetwork
	KindVirtual
	KindFailover
)

type Kind byte

func (k Kind) String() string {
	switch k {
	case KindNil:
		return "<nil>"
	case KindManagement:
		return "Management"
	case KindHost:
		return "Host"
	case KindVirtual:
		return "Virtual"
	case KindNetwork:
		return "Network"
	case KindFailover:
		return "Failover"
	default:
		return "kind_" + strconv.Itoa(int(k))
	}
}

type Node struct {
	// Identity
	Ip net.IP // (String)  IP address
	// Network info
	Ptr          string           // (String) PTR record
	Separate_mac net.HardwareAddr // Separate MAC address, if not set null
	Server_ip    net.IP           // Server main IP address
	Subnet       *net.IPNet       // Subnet
	Gateway      net.IP           // Subnet gateway
	// Hetzner product info
	Dc            string // (String)  Datacentre number
	Product       string // (String)  Server product name
	Server_name   string // (String)  Server name
	Server_number int    // (Integer) Server id
	// Billing info
	Throttled  bool      // (Boolean) Bandwidth limit status
	Status     string    // (String)  Server order status (ready or in process)
	Cancelled  bool      // (Boolean) Status of server cancellation
	Failover   bool      // (Boolean) True if net is a failover net
	Flatrate   bool      // (Boolean) Indicates if the server has a traffic flatrate (traffic overusage will not be charged but the bandwith will be reduced) or not (traffic overusage will be charged)
	Locked     bool      // (Boolean) Status of locking
	Paid_until time.Time // Paid until date
	// Traffic limits
	Traffic_warnings bool   // (Boolean) True if traffic warnings are enabled
	Traffic          string // (String)  Free traffic quota
	Traffic_daily    int    // (Integer) Daily traffic limit in MB
	Traffic_hourly   int    // (Integer) Hourly traffic limit in MB
	Traffic_monthly  int    // (Integer) Monthly traffic limit in GB
	// Failover
	Active_server_ip net.IP // (String)	Main IP of current destination server
	// Misc
	Kind Kind
}

func (n *Node) Clone() *Node {
	return &Node{
		Ip:               n.Ip,
		Ptr:              n.Ptr,
		Separate_mac:     n.Separate_mac,
		Server_ip:        n.Server_ip,
		Subnet:           n.Subnet,
		Gateway:          n.Gateway,
		Dc:               n.Dc,
		Product:          n.Product,
		Server_name:      n.Server_name,
		Server_number:    n.Server_number,
		Throttled:        n.Throttled,
		Status:           n.Status,
		Cancelled:        n.Cancelled,
		Failover:         n.Failover,
		Flatrate:         n.Flatrate,
		Locked:           n.Locked,
		Paid_until:       n.Paid_until,
		Traffic:          n.Traffic,
		Traffic_daily:    n.Traffic_daily,
		Traffic_hourly:   n.Traffic_hourly,
		Traffic_monthly:  n.Traffic_monthly,
		Traffic_warnings: n.Traffic_warnings,
		Active_server_ip: n.Active_server_ip,
		Kind:             n.Kind,
	}
}

type Nodes []*Node

func (n Nodes) Len() int { return len(n) }

func (n Nodes) Swap(i, j int) { n[i], n[j] = n[j], n[i] }

func (n Nodes) Less(i, j int) bool {
	if n[i].Server_number == n[j].Server_number {
		return n[i].Kind < n[j].Kind
	}
	return n[i].Server_number < n[j].Server_number
}

func unbrace(s string) string {
	if len(s) > 1 && (s[0] == '\'' || s[0] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

func getHetznerData(username, password string) (Nodes, error) {
	api.SetBasicAuth(username, password)

	// load raw data
	var (
		rservers  []api.Server
		rips      []api.Ip
		rsubnets  []api.Subnet
		rrdns     []api.Rdns
		rfailover []api.Failover
		wg        sync.WaitGroup
	)
	errc := make(chan error, 5)
	wg.Add(5)
	go func() {
		defer wg.Done()
		errc <- api.Get("/server", &rservers)
	}()
	go func() {
		defer wg.Done()
		errc <- api.Get("/ip", &rips)
	}()
	go func() {
		defer wg.Done()
		errc <- api.Get("/subnet", &rsubnets)
	}()
	go func() {
		defer wg.Done()
		errc <- api.Get("/rdns", &rrdns)
	}()
	go func() {
		defer wg.Done()
		errc <- api.Get("/failover", &rfailover)
	}()
	wg.Wait()
	for i := 0; i < cap(errc); i++ {
		if err := <-errc; err != nil {
			return nil, err
		}
	}

	// build nodes
	nodes := make(map[string]*Node) // Ip.String() as a key

	for _, e := range rservers {
		paid_until, _ := time.Parse(paidTimeFormat, e.Paid_until)
		n := Node{
			// copy of Server
			Ip:            net.IP(e.Server_ip),
			Server_number: e.Server_number,
			Server_name:   e.Server_name,
			Product:       e.Product,
			Dc:            e.Dc,
			Traffic:       e.Traffic,
			Flatrate:      e.Flatrate,
			Status:        e.Status,
			Throttled:     e.Throttled,
			Cancelled:     e.Cancelled,
			Paid_until:    paid_until,
			// Additional fields
			Server_ip: net.IP(e.Server_ip),
			Kind:      KindHost,
		}
		nodes[n.Ip.String()] = &n
	}

	for _, e := range rips {
		n, found := nodes[e.Ip.String()]
		if !found {
			// find host node by Server_ip, make a copy of it and set Ip
			if host, found := nodes[e.Server_ip.String()]; found {
				if !host.Ip.Equal(net.IP(e.Ip)) {
					n = host.Clone()
					n.Ip = net.IP(e.Ip)
					n.Kind = KindManagement
					nodes[n.Ip.String()] = n
				} else {
					n = host
				}
			} else {
				panic("no host found for " + e.Ip.String())
			}
		}
		n.Locked = e.Locked
		n.Separate_mac, _ = net.ParseMAC(e.Separate_mac)
		n.Traffic_warnings = e.Traffic_warnings
		n.Traffic_hourly = e.Traffic_hourly
		n.Traffic_daily = e.Traffic_daily
		n.Traffic_monthly = e.Traffic_monthly
	}

	subnets := make([]*Node, 0, len(rsubnets))
	for _, e := range rsubnets {
		if _, found := nodes[e.Ip.String()]; found {
			panic("subnet conflicted with node " + e.Ip.String()) // should not happen
		}
		server := nodes[e.Server_ip.String()] // it is unlikely that subnet exists without host
		n := server.Clone()
		n.Ip = net.IP(e.Ip)
		n.Kind = KindNetwork
		n.Gateway = net.IP(e.Gateway)
		n.Failover = e.Failover
		n.Locked = e.Locked
		n.Traffic_warnings = e.Traffic_warnings
		n.Traffic_hourly = e.Traffic_hourly
		n.Traffic_daily = e.Traffic_daily
		n.Traffic_monthly = e.Traffic_monthly
		bits := 8 * net.IPv4len
		if v := n.Ip.To4(); v == nil {
			bits = 8 * net.IPv6len
		}
		n.Subnet = &net.IPNet{IP: n.Ip, Mask: net.CIDRMask(e.Mask, bits)}

		nodes[n.Ip.String()] = n
		subnets = append(subnets, n)
	}

BindPtr:
	for _, e := range rrdns {
		ip := net.IP(e.Ip)
		ptr := e.Ptr
		if n, found := nodes[ip.String()]; found {
			n.Ptr = ptr
			continue
		}
		for _, s := range subnets {
			if s.Subnet.Contains(ip) {
				host := nodes[s.Server_ip.String()]
				n := host.Clone()
				n.Ip = ip
				n.Ptr = ptr
				n.Kind = KindVirtual
				nodes[n.Ip.String()] = n
				continue BindPtr
			}
		}
		panic(fmt.Sprintf("should not reach that point: loosen record %s PTR %s", ip, ptr))
	}

	for _, e := range rfailover {
		// failover is a /32 network assigned to server
		n, found := nodes[e.Ip.String()]
		if !found || !(n.Kind == KindNetwork && n.Failover == true) {
			panic("no subnet for failover " + e.Ip.String())
		}
		n.Kind = KindFailover
		n.Active_server_ip = net.IP(e.Active_server_ip)
	}

	// convert map to list sorted by server_number+kind
	l := Nodes{}
	for _, n := range nodes {
		l = append(l, n)
	}
	sort.Sort(l)
	return l, nil
}

type field struct {
	format  string
	factory func(*Node) interface{}
}

var (
	batchMode   bool
	ofields     string
	okinds      string
	delimiter   string
	knownFields = map[string]field{
		"server_number": field{"%d", func(n *Node) interface{} { return n.Server_number }},
		"kind":          field{"%s", func(n *Node) interface{} { return n.Kind }},
		"ip":            field{"%s", func(n *Node) interface{} { return n.Ip }},
		"subnet": field{"%s", func(n *Node) interface{} {
			if n.Subnet == nil {
				return ""
			}
			return n.Subnet
		}},
		"gateway":          field{"%s", func(n *Node) interface{} { return n.Gateway }},
		"server_name":      field{"%s", func(n *Node) interface{} { return n.Server_name }},
		"ptr":              field{"%s", func(n *Node) interface{} { return n.Ptr }},
		"server_ip":        field{"%s", func(n *Node) interface{} { return n.Server_ip }},
		"separate_mac":     field{"%s", func(n *Node) interface{} { return n.Separate_mac }},
		"dc":               field{"%s", func(n *Node) interface{} { return n.Dc }},
		"product":          field{"%s", func(n *Node) interface{} { return n.Product }},
		"status":           field{"%s", func(n *Node) interface{} { return n.Status }},
		"cancelled":        field{"%t", func(n *Node) interface{} { return n.Cancelled }},
		"locked":           field{"%t", func(n *Node) interface{} { return n.Locked }},
		"paid_until":       field{"%s", func(n *Node) interface{} { return n.Paid_until.Format(paidTimeFormat) }},
		"failover":         field{"%t", func(n *Node) interface{} { return n.Failover }},
		"flatrate":         field{"%t", func(n *Node) interface{} { return n.Flatrate }},
		"throttled":        field{"%t", func(n *Node) interface{} { return n.Throttled }},
		"traffic_warnings": field{"%t", func(n *Node) interface{} { return n.Traffic_warnings }},
		"traffic":          field{"%s", func(n *Node) interface{} { return n.Traffic }},
		"traffic_daily":    field{"%d", func(n *Node) interface{} { return n.Traffic_daily }},
		"traffic_hourly":   field{"%d", func(n *Node) interface{} { return n.Traffic_hourly }},
		"traffic_monthly":  field{"%d", func(n *Node) interface{} { return n.Traffic_monthly }},
		"active_server_ip": field{"%s", func(n *Node) interface{} { return n.Active_server_ip }},
	}
)

func main() {
	flag.BoolVar(&batchMode, "batch", false, "output mode handy for batch processing")
	flag.StringVar(&delimiter, "delimiter", ",", "field delimiter for batch mode output")
	flag.StringVar(&ofields, "fields", "server_number,kind,ip,subnet,gateway,server_name,ptr,server_ip,separate_mac,dc,product,status,cancelled,locked,paid_until,failover,flatrate,throttled,traffic_warnings,traffic,traffic_daily,traffic_hourly,traffic_monthly,active_server_ip", "comma separated list of output fields")
	flag.StringVar(&okinds, "kinds", "host,management,network,virtual,failover", "comma separated list of output kinds")
	flag.Parse()

	log.SetFlags(0)

	// compile arguments
	if !batchMode {
		delimiter = "\t"
	}
	names := strings.Split(ofields, ",")
	header := strings.Join(names, "\t")
	format := func(a []string) string {
		l := make([]string, 0, len(names))
		for _, name := range names {
			f, found := knownFields[strings.ToLower(strings.TrimSpace(name))]
			if !found {
				log.Fatalf("unknown field %s", name)
			}
			l = append(l, f.format)
		}
		return strings.Join(l, delimiter) + "\n"
	}(names)
	args := func(n *Node) []interface{} {
		l := make([]interface{}, 0, len(names))
		for _, name := range names {
			// all names are known on this line
			l = append(l, knownFields[strings.ToLower(strings.TrimSpace(name))].factory(n))
		}
		return l
	}
	kinds := make(map[Kind]bool)
	for _, k := range strings.Split(okinds, ",") {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "host":
			kinds[KindHost] = true
		case "management":
			kinds[KindManagement] = true
		case "network":
			kinds[KindNetwork] = true
		case "virtual":
			kinds[KindVirtual] = true
		case "failover":
			kinds[KindFailover] = true
		}
	}

	// load credentials
	rc, err := properties.Load(os.ExpandEnv("$HOME/.hetzner.rc"))

	if err != nil {
		log.Fatalf("no credentials: %s", err)
	}

	// load nodes data
	var nodes Nodes
	nodes, err = getHetznerData(unbrace(rc["login"]), unbrace(rc["password"]))

	if err != nil {
		log.Fatal(err)
	}

	// output data
	var w interface {
		io.Writer
		Flush() error
	}

	if batchMode {
		w = bufio.NewWriter(os.Stdout)
	} else {
		w = tabwriter.NewWriter(os.Stdout, 0, 2, 1, ' ', 0)
		fmt.Fprintln(w, header)
	}
	for _, n := range nodes {
		if _, found := kinds[n.Kind]; !found {
			continue
		}
		a := args(n)
		fmt.Fprintf(w, format, a...)
	}
	w.Flush()
}
