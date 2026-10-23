package httpx_test

import (
	"net/http/httptest"
	"testing"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

func TestClientIP(t *testing.T) {
	for _, c := range []struct {
		name    string
		remote  string
		xff     []string
		proxies int
		want    string
	}{
		{"peer without proxies, header ignored", "192.0.2.1:5000", []string{"198.51.100.9"}, 0, "192.0.2.1"},
		{"one proxy: last entry", "192.0.2.1:5000", []string{"203.0.113.5, 198.51.100.9"}, 1, "198.51.100.9"},
		{"two proxies: second from the right", "192.0.2.1:5000", []string{"203.0.113.5", "198.51.100.9, 10.0.0.1"}, 2, "198.51.100.9"},
		{"more proxies than entries", "192.0.2.1:5000", []string{"198.51.100.9"}, 3, "198.51.100.9"},
		{"no header", "192.0.2.1:5000", nil, 1, "192.0.2.1"},
		{"garbage entry", "192.0.2.1:5000", []string{"not-an-ip"}, 1, "192.0.2.1"},
		{"no port in remote", "192.0.2.1", nil, 0, "192.0.2.1"},
		{"ipv6", "[2001:db8::1]:443", []string{"2001:db8::2"}, 1, "2001:db8::2"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, h := range c.xff {
			r.Header.Add("X-Forwarded-For", h)
		}
		if got := httpx.ClientIP(r, c.proxies); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
