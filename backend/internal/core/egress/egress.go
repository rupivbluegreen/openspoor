// Package egress is the single guard for outbound connections to destinations a tenant or a remote
// party can influence: webhook callbacks, connector endpoints (REST, SFTP, hub), federation peers,
// DSCSA HTTPS and AS2 transports and regulatory-source watching.
//
// The check runs where it cannot be bypassed: in the dialer's Control hook, which the Go runtime
// calls with the numeric address of every individual connection attempt, after DNS resolution and
// for every redirect hop. A hostname that resolves to a public address when it is validated and to
// an internal one when it is dialled (DNS rebinding) is therefore refused at dial time.
//
// By default these destinations are refused: loopback, private (RFC 1918, unique local), link-local,
// carrier-grade NAT, multicast, unspecified, reserved and documentation ranges, and cloud metadata
// addresses. Two settings relax this for development and air-gapped installations:
//
//	OPENSPOOR_EGRESS_ALLOW_PRIVATE=true  allows loopback, private, link-local and carrier-grade NAT addresses
//	OPENSPOOR_EGRESS_ALLOW_CIDRS=a,b     allows exactly these networks, whatever their class
//
// Metadata addresses stay refused under ALLOW_PRIVATE and are reachable only when a CIDR names them.
// Mail (SMTP), object storage (S3), the message bus and the secret store are operator configuration,
// not tenant input, and do not use this package. Outbound HTTP proxies are not used: the check must
// see the real target.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Policy says which destinations may be reached. The zero value is the strict default.
type Policy struct {
	// AllowPrivate permits loopback, private, link-local and carrier-grade NAT addresses.
	AllowPrivate bool
	// Allow permits these networks explicitly, including cloud metadata addresses and reserved ranges.
	Allow []netip.Prefix
}

// BlockedError is returned when a destination is refused.
type BlockedError struct {
	Addr   netip.Addr
	Reason string // loopback, private, link-local, shared, multicast, unspecified, metadata, reserved
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("egress to %s refused: %s address", e.Addr, e.Reason)
}

// ErrBlocked matches every BlockedError with errors.Is.
var ErrBlocked = errors.New("egress destination refused")

// Is makes errors.Is(err, ErrBlocked) true.
func (e *BlockedError) Is(target error) bool { return target == ErrBlocked }

var blocked = promauto.NewCounterVec(prometheus.CounterOpts{Name: "openspoor_egress_blocked_total",
	Help: "Outbound connections refused by the egress guard, by address class"}, []string{"reason"})

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

var (
	// metadataNets are cloud instance-metadata endpoints; they carry credentials.
	metadataNets = mustPrefixes("169.254.169.254/32", "169.254.170.2/32", "fd00:ec2::254/128", "100.100.100.200/32", "192.0.0.192/32")
	// sharedNets is carrier-grade NAT, internal in practice on most clouds and clusters.
	sharedNets = mustPrefixes("100.64.0.0/10")
	// reservedNets never carry a legitimate public counterparty. Translation prefixes that embed an
	// IPv4 address (NAT64, 6to4, Teredo) are included, because the embedded address is not checked.
	reservedNets = mustPrefixes("0.0.0.0/8", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16")
)

func inAny(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// classify returns the address class that makes a refusable, or "" for a public address.
func classify(a netip.Addr) string {
	switch {
	case inAny(a, metadataNets):
		return "metadata"
	case a.IsUnspecified():
		return "unspecified"
	case a.IsLoopback():
		return "loopback"
	case a.IsMulticast():
		return "multicast"
	case a.IsLinkLocalUnicast():
		return "link-local"
	case a.IsPrivate():
		return "private"
	case inAny(a, sharedNets):
		return "shared"
	case inAny(a, reservedNets):
		return "reserved"
	}
	return ""
}

// Check reports why ip may not be connected to under p, or nil. IPv4-mapped IPv6 addresses are
// judged as the IPv4 address they carry.
func (p Policy) Check(ip netip.Addr) error {
	a := ip.Unmap().WithZone("")
	if !a.IsValid() {
		return &BlockedError{Addr: ip, Reason: "invalid"}
	}
	if inAny(a, p.Allow) {
		return nil
	}
	switch c := classify(a); c {
	case "":
		return nil
	case "loopback", "private", "link-local", "shared":
		if p.AllowPrivate {
			return nil
		}
		return &BlockedError{Addr: a, Reason: c}
	default:
		return &BlockedError{Addr: a, Reason: c}
	}
}

// CheckIP is Check for a net.IP.
func (p Policy) CheckIP(ip net.IP) error {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return &BlockedError{Reason: "invalid"}
	}
	return p.Check(a)
}

// Control is a net.Dialer Control hook that refuses connection attempts to addresses the policy does
// not allow. The runtime passes the numeric address of the attempt, so DNS answers, every address of
// a multi-record answer and every redirect are all checked at the moment of connecting.
func (p Policy) Control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("egress: dial target %q is not a numeric address", host)
	}
	if err := p.Check(a); err != nil {
		var be *BlockedError
		if errors.As(err, &be) {
			blocked.WithLabelValues(be.Reason).Inc()
			slog.Warn("egress refused", "network", network, "addr", be.Addr.String(), "class", be.Reason)
		}
		return err
	}
	return nil
}

// ParseCIDRs parses a list of CIDR blocks; a bare address means a single host.
func ParseCIDRs(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("egress allow list: %q is not a CIDR block or address", s)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// NewPolicy builds a Policy from the configuration values.
func NewPolicy(allowPrivate bool, cidrs []string) (Policy, error) {
	ps, err := ParseCIDRs(cidrs)
	return Policy{AllowPrivate: allowPrivate, Allow: ps}, err
}

var current atomic.Pointer[Policy]

// Default returns the process-wide policy: strict until SetDefault is called.
func Default() Policy {
	if p := current.Load(); p != nil {
		return *p
	}
	return Policy{}
}

// SetDefault installs the process-wide policy, read by every client made with Client, Transport and
// Dialer at the moment of each connection. The application sets it once at startup; tests that talk
// to local servers set it and restore the previous value.
func SetDefault(p Policy) { current.Store(&p) }

// DialTimeout bounds one connection attempt.
const DialTimeout = 10 * time.Second

// Dialer returns a dialer that applies the process-wide policy to every connection attempt.
func Dialer(timeout time.Duration) *net.Dialer { return dialer(Default, timeout) }

// DialContext connects with the process-wide policy; for non-HTTP protocols such as SFTP.
func DialContext(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	return Dialer(timeout).DialContext(ctx, network, address)
}

func dialer(get func() Policy, timeout time.Duration) *net.Dialer {
	if timeout <= 0 {
		timeout = DialTimeout
	}
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error { return get().Control(network, address, c) }}
}

func transport(get func() Policy) *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // the dial check must see the real target
		DialContext:           dialer(get, DialTimeout).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   4,
	}
}

// NewTransport returns an HTTP transport that applies the process-wide policy to every connection.
// Callers that need TLS settings (certificate pinning) adjust TLSClientConfig on the result.
func NewTransport() *http.Transport { return transport(Default) }

// MaxRedirects is how many redirects a guarded client follows.
const MaxRedirects = 5

// CheckRedirect refuses redirects to schemes other than http and https, downgrades from https to
// http, and chains longer than MaxRedirects. Addresses are checked at dial time.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return fmt.Errorf("redirect to scheme %q refused", req.URL.Scheme)
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return errors.New("redirect from https to http refused")
	}
	return nil
}

// Client returns an HTTP client for outbound calls to tenant- or partner-supplied destinations.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: NewTransport(), CheckRedirect: CheckRedirect}
}

// Client is the client of a fixed policy, independent of the process-wide one.
func (p Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport(func() Policy { return p }), CheckRedirect: CheckRedirect}
}

// ValidateURL checks an outbound URL without network access, so a bad destination is rejected when
// it is saved: http or https, a host, no credentials, and a literal IP address the policy allows.
// A host name passes here and is checked when it is dialled, after DNS resolution.
func (p Policy) ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("must be an absolute http(s) URL")
	}
	if u.User != nil {
		return nil, errors.New("the URL must not contain credentials")
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		if err := p.Check(a); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// ValidateURL checks raw against the process-wide policy.
func ValidateURL(raw string) (*url.URL, error) { return Default().ValidateURL(raw) }
