// Package egresstest sets the outbound policy in tests. Tests that start local servers need
// loopback to be reachable; tests of the guard itself need the strict default.
package egresstest

import (
	"testing"

	"github.com/openspoor/openspoor/internal/core/egress"
)

// Set installs p as the process-wide policy for the test and restores the previous one afterwards.
func Set(t testing.TB, p egress.Policy) {
	t.Helper()
	prev := egress.Default()
	egress.SetDefault(p)
	t.Cleanup(func() { egress.SetDefault(prev) })
}

// Strict is the production default.
func Strict(t testing.TB) { Set(t, egress.Policy{}) }

// Main is for TestMain of packages whose tests talk to local servers: it allows private addresses
// for the whole run; tests of the guard call Strict.
func Main(m *testing.M) int {
	egress.SetDefault(egress.Policy{AllowPrivate: true})
	return m.Run()
}
