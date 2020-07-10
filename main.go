package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type strategyT int

const (
	sAssigned = iota
	sResolv
)

type ifT struct {
	name     string
	label    string
	strategy strategyT
	interval time.Duration
}

// argvT : command line arguments
type argvT struct {
	iface   []ifT
	domain  string
	apikey  string
	ttl     int
	dryrun  bool
	verbose int
	stdout  *log.Logger
	stderr  *log.Logger
}

const (
	version = "0.1.0"
)

var (
	errNoValidAddresses = fmt.Errorf("no valid addresses")
	errInvalidAddress   = fmt.Errorf("invalid address")
)

func strategy(str string) (strategyT, error) {
	switch str {
	case "assign":
		fallthrough
	case "assigned":
		return sAssigned, nil
	case "resolve":
		fallthrough
	case "resolv":
		return sResolv, nil
	default:
		return sAssigned, fmt.Errorf("invalid strategy: %s", str)
	}
}

func toIf(arg []string) (ifs []ifT, err error) {
	//minute := time.Minute
	minute := 10 * time.Second
	for _, v := range arg {
		x := strings.Split(v, ":")
		switch len(x) {
		case 1:
			ifs = append(ifs, ifT{name: x[0], label: x[0], interval: minute})
		case 2:
			ifs = append(ifs, ifT{name: x[0], label: x[1], interval: minute})
		case 3:
			s, err := strategy(x[2])
			if err != nil {
				return ifs, err
			}
			ifs = append(ifs, ifT{name: x[0], label: x[1], strategy: s,
				interval: minute})
		case 4:
			s, err := strategy(x[2])
			if err != nil {
				return ifs, err
			}
			d, err := time.ParseDuration(x[3])
			if err != nil {
				return ifs, err
			}
			ifs = append(ifs, ifT{name: x[0], label: x[1], strategy: s, interval: d})
		default:
			return ifs, fmt.Errorf("invalid specification: %s", v)
		}
	}
	return ifs, err
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func args() *argvT {
	flag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, `%s v%s
Usage: %s [<option>] <interface> <...>

`, path.Base(os.Args[0]), version, os.Args[0])
		flag.PrintDefaults()
	}

	dryrun := flag.Bool("dryrun", false, "Do not update DNS")

	apikey := flag.String(
		"apikey",
		getenv("DNSUP_APIKEY", "<unset>"),
		"Gandi APIKEY",
	)

	env_ttl := getenv("DNSUP_TTL", "300")
	default_ttl, err := strconv.ParseInt(env_ttl, 10, 64)
	if err != nil {
		fmt.Printf("invalid ttl: DNSUP_TTL: %s\n", env_ttl)
		os.Exit(1)
	}

	ttl := flag.Int(
		"ttl",
		int(default_ttl),
		"DNS TTL",
	)

	verbose := flag.Int(
		"verbose",
		0,
		"Debug output",
	)

	flag.Parse()

	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(1)
	}

	ifs, err := toIf(flag.Args()[1:])
	if err != nil {
		flag.Usage()
		fmt.Println(err)
		os.Exit(1)
	}

	return &argvT{
		dryrun:  *dryrun,
		iface:   ifs,
		domain:  flag.Args()[:1][0],
		ttl:     *ttl,
		apikey:  *apikey,
		verbose: *verbose,
		stdout:  log.New(os.Stdout, "", 0),
		stderr:  log.New(os.Stderr, "", 0),
	}
}

func main() {
	argv := args()
	errch := make(chan error)
	for _, ift := range argv.iface {
		go run(argv, ift, errch)
	}
	err := <-errch
	argv.stderr.Fatalln(err)
}

func run(argv *argvT, ift ifT, errch chan<- error) {
	if argv.verbose > 0 {
		argv.stderr.Printf("polling: %+v\n", ift)
	}

	ticker := time.Tick(ift.interval)
	var p string
loop:
	for {
		select {
		case <-ticker:
			ip, err := ipaddr(ift.name)
			if err != nil {
				errch <- err
				break loop
			}
			n, err := resolv(argv, ift, ip)
			if err != nil {
				argv.stderr.Fatalln(err)
				errch <- err
				break loop
			}
			if argv.verbose > 0 {
				argv.stderr.Println(ift.label, argv.domain, n)
			}
			if p == n {
				continue
			}
			p = n
			if argv.dryrun {
				continue
			}
			if err := publish(argv, ift.label, n); err != nil {
				errch <- err
				break loop
			}
		}
	}
}

func ipaddr(name string) (n []net.IP, err error) {
	i, err := net.InterfaceByName(name)
	if err != nil {
		return n, err
	}
	addr, err := i.Addrs()
	for _, v := range addr {
		ip, _, err := net.ParseCIDR(v.String())
		if err != nil {
			return n, err
		}
		if !ip.IsGlobalUnicast() {
			continue
		}
		n = append(n, ip)
	}
	return n, nil
}

func resolv(argv *argvT, ift ifT, addr []net.IP) (string, error) {
	if len(addr) == 0 {
		return "", nil
	}
	pub := true
	for _, a := range addr {
		if a.To4() == nil {
			pub = false
		}
		switch ift.strategy {
		case sAssigned:
			return a.String(), nil
		case sResolv:
			r := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network,
					address string) (net.Conn, error) {
					d := net.Dialer{
						LocalAddr: &net.UDPAddr{IP: a},
						Timeout:   time.Millisecond * time.Duration(10000),
					}
					return d.DialContext(ctx, "udp", "ns1.google.com:53")
					//return d.DialContext(ctx, "udp", "resolver1.opendns.com:53")
				},
			}
			ctx := context.Background()
			ipaddr, err := r.LookupTXT(ctx, "o-o.myaddr.l.google.com")
			//ipaddr, err := r.LookupHost(ctx, "myip.opendns.com")
			if err != nil {
				return "", err
			}
			if len(ipaddr) == 0 {
				return "", errInvalidAddress
			}
			if net.ParseIP(ipaddr[0]) == nil {
				return ipaddr[0], errInvalidAddress
			}
			fmt.Println("ip:", ipaddr)
			if !pub {
				return ipaddr[0], fmt.Errorf("unsupported:ipv6:%s", ipaddr[0])
			}
			return ipaddr[0], nil
		}
	}
	return "", errNoValidAddresses
}

func publish(argv *argvT, label, ipaddr string) error {
	u := fmt.Sprintf("https://dns.api.gandi.net/api/v5/domains/%s/records/%s/A",
		argv.domain,
		label,
	)

	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("X-Api-Key", argv.apikey)

	body := fmt.Sprintf(
		"{\"rrset_ttl\": %d, \"rrset_values\":[\"%s\"]}",
		argv.ttl,
		ipaddr,
	)

	ctx := context.Background()
	r, err := http.NewRequestWithContext(
		ctx,
		"PUT",
		u,
		bytes.NewBufferString(body),
	)
	if err != nil {
		return err
	}

	r.Header = h

	if argv.verbose > 0 {
		fmt.Printf("%+v\n", r)
	}

	if argv.dryrun {
		return nil
	}

	c := &http.Client{}
	resp, err := c.Do(r)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	rbody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Println(string(rbody))
	return nil
}
