package sftpdrop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openspoor/openspoor/internal/connectors/sftpdrop"
	"github.com/openspoor/openspoor/internal/core/egress"
	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
)

// An SFTP host is tenant input: the session must not connect to internal addresses.
func TestSFTPDialRefusesInternalHost(t *testing.T) {
	s := server(t, nil)
	cfg := cfgFor(s)
	ep, err := sftpdrop.EndpointFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	egresstest.Strict(t)
	if _, err := sftpdrop.Dial(context.Background(), ep); !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("dial: %v", err)
	}
	// An operator may allow exactly that network.
	p, _ := egress.NewPolicy(false, []string{"127.0.0.1/32"})
	egresstest.Set(t, p)
	sess, err := sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatalf("allow-listed dial: %v", err)
	}
	_ = sess.Close()
}
