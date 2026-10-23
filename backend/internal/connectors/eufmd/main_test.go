package eufmd_test

import (
	"os"
	"testing"

	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
)

// These tests talk to servers on loopback; the guard's own behaviour is tested with egresstest.Strict.
func TestMain(m *testing.M) { os.Exit(egresstest.Main(m)) }
