package link

import "net/http"

// CheckClientCert exposes checkClientCert to the black-box tests.
var CheckClientCert = checkClientCert

// ClientFor exposes the per-peer HTTP client to the black-box tests.
func (m *Module) ClientFor(pin string) (*http.Client, error) { return m.clientFor(pin) }
