// Copyright (c) 2020-2022, Michael Santos <michael.santos@gmail.com>
//
// Permission to use, copy, modify, and/or distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
// ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
// ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
// OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
package main

import (
	"bytes"
	"context"
	"errors"
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

//go:generate stringer -type=Strategy
type Strategy int

const (
	inet4 Strategy = iota
	inet6
	resolv4
	resolv6
)

type ifT struct {
	name     string
	label    string
	strategy Strategy
	interval time.Duration
}

type argvT struct {
	iface        []ifT
	domain       string
	apikey       string
	service      string
	ttl          int
	pollInterval time.Duration
	dryrun       bool
	verbose      int
	stdout       *log.Logger
	stderr       *log.Logger
}

const (
	version = "0.1.1"

	Akamai     = "akamai"
	Cloudflare = "cloudflare"
	Google     = "google"
	OpenDNS    = "opendns"
)

var (
	errNoValidAddresses     = errors.New("no valid addresses")
	errInvalidAddress       = errors.New("invalid address")
	errInvalidStrategy      = errors.New("invalid strategy")
	errInvalidSpecification = errors.New("invalid specification")
)

func strategy(str string) (Strategy, error) {
	switch str {
	case "inet":
		fallthrough
	case "inet4":
		return inet4, nil
	case "inet6":
		return inet6, nil
	case "resolv":
		fallthrough
	case "resolv4":
		return resolv4, nil
	case "resolv6":
		return resolv6, nil
	default:
		return inet4, fmt.Errorf("%w: %s", errInvalidStrategy, str)
	}
}

func toIf(arg []string, interval time.Duration) (ifs []ifT, err error) {
	for _, v := range arg {
		x := strings.Split(v, ":")
		switch len(x) {
		case 1:
			ifs = append(ifs, ifT{name: x[0], label: x[0], interval: interval})
		case 2:
			ifs = append(ifs, ifT{name: x[0], label: x[1], interval: interval})
		case 3:
			s, err := strategy(x[2])
			if err != nil {
				return ifs, err
			}
			ifs = append(ifs, ifT{
				name: x[0], label: x[1], strategy: s,
				interval: interval,
			})
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
			return ifs, fmt.Errorf("%w: %s", errInvalidSpecification, v)
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
Usage: %s [<option>] <domain> <interface> <...>

`, path.Base(os.Args[0]), version, os.Args[0])
		flag.PrintDefaults()
	}

	dryrun := flag.Bool("dryrun", false, "Do not update DNS")

	apikey := flag.String(
		"apikey",
		getenv("DNSUP_APIKEY", ""),
		"Gandi APIKEY",
	)

	service := flag.String(
		"service",
		Google,
		"Service for discovering IP address: Akamai, Cloudflare, Google, OpenDNS",
	)

	envTTL := getenv("DNSUP_TTL", "300")
	defaultTTL, err := strconv.ParseInt(envTTL, 10, 64)
	if err != nil {
		fmt.Printf("invalid ttl: DNSUP_TTL: %s\n", envTTL)
		os.Exit(1)
	}

	ttl := flag.Int(
		"ttl",
		int(defaultTTL),
		"DNS TTL",
	)

	pollInterval := flag.Duration(
		"poll-interval",
		1*time.Minute,
		"IP address discovery poll interval",
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

	ifs, err := toIf(flag.Args()[1:], *pollInterval)
	if err != nil {
		flag.Usage()
		fmt.Println(err)
		os.Exit(1)
	}

	*service = strings.ToLower(*service)
	switch *service {
	case Akamai:
	case Cloudflare:
	case Google:
	case OpenDNS:
	default:
		flag.Usage()
		os.Exit(1)
	}

	return &argvT{
		dryrun:       *dryrun,
		iface:        ifs,
		domain:       flag.Args()[:1][0],
		ttl:          *ttl,
		pollInterval: *pollInterval,
		apikey:       *apikey,
		service:      *service,
		verbose:      *verbose,
		stdout:       log.New(os.Stdout, "", 0),
		stderr:       log.New(os.Stderr, "", 0),
	}
}

func main() {
	argv := args()
	errch := make(chan error)
	for _, ift := range argv.iface {
		go argv.run(ift, errch)
	}
	err := <-errch
	argv.stderr.Fatalln(err)
}

func (argv *argvT) run(ift ifT, errch chan<- error) {
	if argv.verbose > 0 {
		argv.stderr.Printf("polling: %+v\n", ift)
	}

	ticker := time.Tick(ift.interval)
	var p string

	for range ticker {
		ip, err := ipaddr(ift.name)
		if err != nil {
			errch <- err
			return
		}
		n, err := argv.resolv(ift, ip)
		if err != nil {
			argv.stderr.Printf("resolv: %+v: %s\n", ift, err)
			continue
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
		if err := argv.publish(ift.label, n); err != nil {
			argv.stderr.Printf("publish: %+v: %s\n", ift, err)
			continue
		}
	}
}

func ipaddr(name string) (n []net.IP, err error) {
	i, err := net.InterfaceByName(name)
	if err != nil {
		return n, err
	}
	addr, err := i.Addrs()
	if err != nil {
		return n, err
	}
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

func (argv *argvT) resolv(ift ifT, addr []net.IP) (string, error) {
	if len(addr) == 0 {
		return "", nil
	}
	for _, address := range addr {
		a := address // local scope

		var r net.Resolver
		r.PreferGo = true

		switch ift.strategy {
		case resolv4:
			r.Dial = func(ctx context.Context, network,
				address string) (net.Conn, error) {
				d := net.Dialer{
					LocalAddr: &net.UDPAddr{IP: a},
					Timeout:   ift.interval,
				}
				return d.DialContext(ctx, "udp", argv.nameserver())
			}
		case resolv6:
			r.Dial = func(ctx context.Context, network,
				address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: ift.interval,
				}
				return d.DialContext(ctx, "udp6", argv.nameserver())
			}
		}

		switch ift.strategy {
		case inet4:
			if a.To4() == nil {
				continue
			}
			fmt.Println(ift.strategy, a)
			return a.String(), nil
		case inet6:
			if a.To4() != nil {
				continue
			}
			fmt.Println(ift.strategy, a)
			return a.String(), nil
		case resolv4:
			fallthrough
		case resolv6:
			ctx := context.Background()
			ipaddr, err := argv.lookup(ctx, &r)
			if err != nil {
				if argv.verbose > 0 {
					argv.stderr.Println(a, err)
				}
				continue
			}
			if len(ipaddr) == 0 {
				if argv.verbose > 0 {
					argv.stderr.Println(a, errInvalidAddress)
				}
				continue
			}
			if net.ParseIP(ipaddr[0]) == nil {
				if argv.verbose > 0 {
					argv.stderr.Println(a, errInvalidAddress)
				}
				continue
			}
			fmt.Println(ift.strategy, ipaddr)
			return ipaddr[0], nil
		}
	}
	return "", errNoValidAddresses
}

func (argv *argvT) publish(label, ipaddr string) error {
	ip := net.ParseIP(ipaddr)
	if ip == nil {
		return nil
	}
	rtype := "A"
	if ip.To4() == nil {
		rtype = "AAAA"
	}
	u := fmt.Sprintf("https://dns.api.gandi.net/api/v5/domains/%s/records/%s/%s",
		argv.domain,
		label,
		rtype,
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

func (argv *argvT) nameserver() string {
	switch argv.service {
	case Akamai:
		return "ns1-1.akamaitech.net:53"
	case Cloudflare:
		return "armando.ns.cloudflare.com:53"
	case Google:
		return "ns1.google.com:53"
	case OpenDNS:
		return "resolver1.opendns.com:53"
	default:
		panic("unsupported service")
	}
}

func (argv *argvT) lookup(ctx context.Context, r *net.Resolver) ([]string, error) {
	switch argv.service {
	case Akamai:
		return r.LookupHost(ctx, "whoami.akamai.net")
	case Cloudflare:
		return r.LookupTXT(ctx, "whoami.cloudflare.com")
	case Google:
		return r.LookupTXT(ctx, "o-o.myaddr.l.google.com")
	case OpenDNS:
		return r.LookupHost(ctx, "myip.opendns.com")
	default:
		panic("unsupported service")
	}
}
