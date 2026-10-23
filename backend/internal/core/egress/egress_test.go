package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyCheck(t *testing.T) {
	strict := Policy{}
	priv := Policy{AllowPrivate: true}
	cidr, err := NewPolicy(false, []string{"10.1.0.0/16", "169.254.169.254"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ip          string
		strict      string // expected refusal class under the default policy, "" when allowed
		allowPriv   string
		allowByCIDR string
	}{
		{"93.184.216.34", "", "", ""},
		{"2606:2800:220:1::1", "", "", ""},
		{"127.0.0.1", "loopback", "", "loopback"},
		{"::1", "loopback", "", "loopback"},
		{"::ffff:127.0.0.1", "loopback", "", "loopback"},
		{"10.0.0.5", "private", "", "private"},
		{"10.1.2.3", "private", "", ""}, // allowed by the CIDR
		{"172.16.0.1", "private", "", "private"},
		{"192.168.1.1", "private", "", "private"},
		{"fd12:3456::1", "private", "", "private"},
		{"169.254.1.1", "link-local", "", "link-local"},
		{"fe80::1", "link-local", "", "link-local"},
		{"100.64.0.1", "shared", "", "shared"},
		{"169.254.169.254", "metadata", "metadata", ""}, // only an explicit CIDR opens it
		{"fd00:ec2::254", "metadata", "metadata", "metadata"},
		{"100.100.100.200", "metadata", "metadata", "metadata"},
		{"::ffff:169.254.169.254", "metadata", "metadata", ""},
		{"224.0.0.1", "multicast", "multicast", "multicast"},
		{"ff02::1", "multicast", "multicast", "multicast"},
		{"0.0.0.0", "unspecified", "unspecified", "unspecified"},
		{"::", "unspecified", "unspecified", "unspecified"},
		{"0.1.2.3", "reserved", "reserved", "reserved"},
		{"240.0.0.1", "reserved", "reserved", "reserved"},
		{"192.0.2.1", "reserved", "reserved", "reserved"},
		{"64:ff9b::7f00:1", "reserved", "reserved", "reserved"}, // NAT64 embedding 127.0.0.1
		{"2002:7f00:1::1", "reserved", "reserved", "reserved"},  // 6to4 embedding 127.0.0.1
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.ip)
		for _, p := range []struct {
			name string
			pol  Policy
			want string
		}{{"strict", strict, c.strict}, {"allow-private", priv, c.allowPriv}, {"cidr", cidr, c.allowByCIDR}} {
			err := p.pol.Check(a)
			var be *BlockedError
			switch {
			case p.want == "" && err != nil:
				t.Errorf("%s %s: refused: %v", p.name, c.ip, err)
			case p.want != "" && !errors.As(err, &be):
				t.Errorf("%s %s: allowed, want refusal %s", p.name, c.ip, p.want)
			case p.want != "" && (be.Reason != p.want || !errors.Is(err, ErrBlocked)):
				t.Errorf("%s %s: reason %q, want %q", p.name, c.ip, be.Reason, p.want)
			}
		}
	}
}

func TestParseCIDRs(t *testing.T) {
	ps, err := ParseCIDRs([]string{" 10.0.0.9/8 ", "192.168.1.5", "fd00::/8", ""})
	if err != nil || len(ps) != 3 || ps[0].String() != "10.0.0.0/8" || ps[1].String() != "192.168.1.5/32" {
		t.Fatalf("%v %v", ps, err)
	}
	if _, err := ParseCIDRs([]string{"not-a-network"}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestValidateURL(t *testing.T) {
	p := Policy{}
	for _, bad := range []string{"", "ftp://example.com/", "http://127.0.0.1/x", "https://[::1]/", "http://169.254.169.254/latest/meta-data",
		"https://user:pw@example.com/", "//example.com", "http://10.0.0.1:8080/"} {
		if _, err := p.ValidateURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"https://example.com/hook", "http://example.com:8080/x", "https://93.184.216.34/"} {
		if _, err := p.ValidateURL(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	if _, err := (Policy{AllowPrivate: true}).ValidateURL("http://127.0.0.1:9/"); err != nil {
		t.Errorf("allow-private: %v", err)
	}
}

func TestDefaultIsStrictAndSwappable(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	SetDefault(Policy{})
	if Default().AllowPrivate {
		t.Fatal("default must be strict")
	}
	SetDefault(Policy{AllowPrivate: true})
	if !Default().AllowPrivate {
		t.Fatal("SetDefault ignored")
	}
}

func hit(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	res, err := c.Get(url)
	if err == nil {
		_ = res.Body.Close()
	}
	return err
}

func TestClientRefusesLoopbackAndHonoursAllowList(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { served.Add(1) }))
	t.Cleanup(srv.Close)

	if err := hit(t, Policy{}.Client(2*time.Second), srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("strict policy reached a loopback server: %v", err)
	}
	if served.Load() != 0 {
		t.Fatal("the request was sent")
	}
	if err := hit(t, Policy{AllowPrivate: true}.Client(2*time.Second), srv.URL); err != nil {
		t.Fatalf("allow-private: %v", err)
	}
	p, _ := NewPolicy(false, []string{"127.0.0.0/8"})
	if err := hit(t, p.Client(2*time.Second), srv.URL); err != nil {
		t.Fatalf("cidr allow list: %v", err)
	}
	// The process-wide client follows SetDefault at connect time, even for clients made before it.
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	c := Client(2 * time.Second)
	SetDefault(Policy{})
	if err := hit(t, c, srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("default client: %v", err)
	}
	SetDefault(Policy{AllowPrivate: true})
	if err := hit(t, c, srv.URL); err != nil {
		t.Fatalf("default client after SetDefault: %v", err)
	}
}

func TestRedirectToInternalAddressIsRefused(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { internalHits.Add(1) }))
	t.Cleanup(internal.Close)
	// The first hop is allowed (a CIDR names only the redirecting server's exact address and port is not
	// part of CIDR, so use a second loopback address for the target).
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:"+portOf(internal.URL)+"/secret", http.StatusFound)
	}))
	t.Cleanup(front.Close)
	// Allow only 127.0.0.1: the redirect target 127.0.0.2 is refused when the client dials it.
	p, _ := NewPolicy(false, []string{"127.0.0.1/32"})
	err := hit(t, p.Client(2*time.Second), front.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect not refused: %v", err)
	}
	if internalHits.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
}

func portOf(u string) string {
	_, p, _ := net.SplitHostPort(u[len("http://"):])
	return p
}

func TestCheckRedirect(t *testing.T) {
	mk := func(u string) *http.Request { r, _ := http.NewRequest(http.MethodGet, u, nil); return r }
	if err := CheckRedirect(mk("ftp://x/"), []*http.Request{mk("https://a/")}); err == nil {
		t.Error("ftp redirect accepted")
	}
	if err := CheckRedirect(mk("http://x/"), []*http.Request{mk("https://a/")}); err == nil {
		t.Error("https to http downgrade accepted")
	}
	if err := CheckRedirect(mk("https://x/"), []*http.Request{mk("http://a/")}); err != nil {
		t.Errorf("upgrade refused: %v", err)
	}
	via := make([]*http.Request, MaxRedirects+1)
	for i := range via {
		via[i] = mk("https://a/")
	}
	if err := CheckRedirect(mk("https://x/"), via); err == nil {
		t.Error("redirect loop accepted")
	}
}

// fakeDNS answers every A query for any name with the address in answer and AAAA with an empty
// answer, so a test can change what a host name resolves to between validation and dialling.
func fakeDNS(t *testing.T, answer *atomic.Value) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 13 {
				continue
			}
			q := buf[:n]
			end := 12
			for end < n && q[end] != 0 {
				end += int(q[end]) + 1
			}
			end += 5 // root label, type and class
			if end > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[end-4 : end-2])
			resp := append([]byte{}, q[:end]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available, no error
			resp[6], resp[7], resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0, 0, 0
			if qtype == 1 {
				ip := netip.MustParseAddr(answer.Load().(string)).As4()
				resp[7] = 1
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
				resp = append(resp, ip[:]...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
}

// TestDNSRebinding: a name that looks harmless when the URL is validated (names are not resolved
// then) resolves to loopback by the time the connection is made. The refusal happens at dial time.
func TestDNSRebinding(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { served.Add(1) }))
	t.Cleanup(srv.Close)
	var answer atomic.Value
	answer.Store("93.184.216.34") // public at validation time
	res := fakeDNS(t, &answer)

	clientFor := func(p Policy) *http.Client {
		d := dialer(func() Policy { return p }, time.Second)
		d.Resolver = res
		return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true}, CheckRedirect: CheckRedirect}
	}
	target := "http://rebind.example.test:" + portOf(srv.URL) + "/hook"
	strict := Policy{}
	if _, err := strict.ValidateURL(target); err != nil {
		t.Fatalf("a host name must pass static validation: %v", err)
	}
	answer.Store("127.0.0.1") // the attacker's DNS now points inside
	if err := hit(t, clientFor(strict), target); !errors.Is(err, ErrBlocked) {
		t.Fatalf("rebound name reached loopback: %v", err)
	}
	if served.Load() != 0 {
		t.Fatal("request reached the internal server")
	}
	// Same name and resolver succeed when the operator allows private addresses: the refusal above was the guard.
	if err := hit(t, clientFor(Policy{AllowPrivate: true}), target); err != nil {
		t.Fatalf("allow-private through DNS: %v", err)
	}
	if served.Load() != 1 {
		t.Fatalf("served %d", served.Load())
	}
	// Metadata through DNS stays refused even with AllowPrivate.
	answer.Store("169.254.169.254")
	if err := hit(t, clientFor(Policy{AllowPrivate: true}), "http://metadata.example.test/latest"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadata via DNS: %v", err)
	}
}

func TestDialContextForNonHTTPProtocols(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	SetDefault(Policy{})
	if _, err := DialContext(context.Background(), "tcp", ln.Addr().String(), time.Second); !errors.Is(err, ErrBlocked) {
		t.Fatalf("strict: %v", err)
	}
	SetDefault(Policy{AllowPrivate: true})
	c, err := DialContext(context.Background(), "tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}
