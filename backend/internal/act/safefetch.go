package act

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// SSRF-safe fetching for the regulatory-source watcher. A tenant supplies the URL, so the server
// must never be made to reach its own network: only https on the default port, no credentials in
// the URL, and every address actually dialled (after DNS, and after each redirect) must be a
// public one. Loopback and plain http are allowed only with the module's test-only flag.

const (
	maxWatchBytes   = 2 << 20 // largest response read
	watchTimeout    = 10 * time.Second
	maxWatchRedirct = 3
)

var blockedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this" network
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved
		"64:ff9b::/96",    // NAT64
		"100::/64",        // discard-only
		"2001:db8::/32",   // documentation
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsBlockedIP reports whether an address must not be fetched: loopback, private (RFC 1918 and
// unique local), link-local (including cloud metadata addresses), unspecified, multicast and other
// reserved ranges. IPv4-mapped IPv6 addresses are judged as the IPv4 address they carry.
func IsBlockedIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range blockedNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ValidateWatchURL checks a source URL without any network access. insecure (tests and local
// development only) also allows http, any port and loopback addresses.
func ValidateWatchURL(raw string, insecure bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errors.New("url must be an absolute https URL")
	}
	if u.Scheme != "https" && (!insecure || u.Scheme != "http") {
		return nil, errors.New("only https URLs can be watched")
	}
	if u.User != nil {
		return nil, errors.New("the URL must not contain credentials")
	}
	if p := u.Port(); p != "" && p != "443" && !insecure {
		return nil, errors.New("only the default https port is allowed")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("url must have a host")
	}
	if ip := net.ParseIP(host); ip != nil && IsBlockedIP(ip) && (!insecure || !ip.IsLoopback()) {
		return nil, errors.New("the address is not public and cannot be watched")
	}
	return u, nil
}

// newWatchClient returns an HTTP client that refuses non-public addresses at dial time, which also
// covers DNS answers and redirects, and limits redirects to a few validated hops.
func newWatchClient(insecure bool) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || (IsBlockedIP(ip) && (!insecure || !ip.IsLoopback())) {
			return fmt.Errorf("refusing to connect to non-public address %s", host)
		}
		return nil
	}}
	return &http.Client{
		Timeout: watchTimeout,
		Transport: &http.Transport{ // no proxy: the dial check must see the real target
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: watchTimeout,
			MaxIdleConns:          2,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxWatchRedirct {
				return errors.New("too many redirects")
			}
			_, err := ValidateWatchURL(req.URL.String(), insecure)
			return err
		},
	}
}

// fetchWatched downloads a source with the size limit. It returns the body and its media type.
func fetchWatched(ctx context.Context, c *http.Client, raw string, insecure bool) ([]byte, string, error) {
	u, err := ValidateWatchURL(raw, insecure)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "OpenSpoor-SourceWatch/1")
	req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml,application/json,application/xml;q=0.9,application/pdf;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the source answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWatchBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxWatchBytes {
		return nil, "", fmt.Errorf("the source is larger than %d MiB", maxWatchBytes>>20)
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return body, strings.ToLower(strings.TrimSpace(ct)), nil
}
