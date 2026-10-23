package verify_test

import (
	"net/http"
	"testing"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestPublicLimitIsPerForwardedClient(t *testing.T) {
	e := testkit.New(t, func(c *config.Config) { c.TrustedProxies = 1 })
	hit := func(client string) int {
		return e.Call("", "POST", "/api/v1/playground/validate", map[string]any{"document": "<x"}, "X-Forwarded-For", client).Status
	}
	limited := false
	for i := 0; i < 80 && !limited; i++ {
		limited = hit("198.51.100.7") == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("the noisy client was never limited")
	}
	if got := hit("198.51.100.8"); got == http.StatusTooManyRequests {
		t.Fatal("another client behind the same proxy shares the budget")
	}
	if got := hit("203.0.113.1, 198.51.100.7"); got != http.StatusTooManyRequests {
		t.Fatalf("forged entry: %d", got)
	}
}
