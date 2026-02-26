[![Go Reference](https://pkg.go.dev/badge/go.iscode.ca/dnsup.svg)](https://pkg.go.dev/go.iscode.ca/dnsup)

# SYNOPSIS

dnsup [*options*] <domain> <interface>

# DESCRIPTION

A dynamic DNS client to monitor the public IP address of an interface and
publish using the [Cloudflare API](https://developers.cloudflare.com/api/operations/dns-records-for-a-zone-update-dns-record).

# BUILDING

```bash
go install github.com/msantos/dnsup@latest
```

To build a reproducible executable from the git repository:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-w"
```

# EXAMPLES

```bash
dnsup example.com eth0:host1:inet6 eth1:host2:resolv
```

# ARGUMENTS

The command line arguments consist of colon delimited strings:

```
<interface>:<label>:<strategy>[:<poll-interval>]
```

Supported strategies are:

inet
: synonym for inet4

inet4
: return the IPv4 address for interface

inet6
: return the IPv6 address for interface

resolv
: synonym for resolv4

resolv4
: resolve the external IPv4 address of the interface using DNS

resolv6
: resolve the external IPv6 address of the interface using DNS

# OPTIONS

apikey *string*
: Cloudflare scoped API token

dryrun
: Do not update DNS

poll-interval *duration*
: IP address discovery poll interval (default 1m0s)

service *string*
: Service for discovering IP address: Akamai, Google, OpenDNS (default "google")

ttl *int*
: DNS TTL (default 300)

verbose *int*
: Debug output

# ENVIRONMENT VARIABLES

CLOUDFLARE_API_TOKEN
: scoped API token with edit DNS zone capability
