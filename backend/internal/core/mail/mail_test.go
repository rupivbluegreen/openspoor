package mail

import (
	"context"
	"testing"
)

func TestNew(t *testing.T) {
	if s, err := New("", ""); s != nil || err != nil {
		t.Fatalf("unset: %v %v", s, err)
	}
	for _, bad := range []string{"http://relay.example.com", "smtp://", "relay.example.com:25"} {
		if _, err := New(bad, "noreply@example.com"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := New("smtp://relay.example.com", ""); err == nil {
		t.Error("missing sender accepted")
	}
	s, err := New("smtp://user:pw@relay.example.com", "noreply@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.(*smtpSender).addr; got != "relay.example.com:587" {
		t.Fatalf("default port: %s", got)
	}
	if err := s.Send(context.Background(), "a@example.com\r\nBcc: x@example.com", "hi", "body"); err == nil {
		t.Fatal("header injection in the recipient was not refused")
	}
}
