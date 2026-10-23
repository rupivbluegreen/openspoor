package sftpdrop_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/connectors-sdk/contracttest"
	"github.com/openspoor/openspoor/internal/connectors/mockhub"
	"github.com/openspoor/openspoor/internal/connectors/sftpdrop"
)

func server(t *testing.T, authorized ssh.PublicKey) *mockhub.SFTPServer {
	t.Helper()
	s, err := mockhub.NewSFTPServer("partner", "pw", authorized)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func cfgFor(s *mockhub.SFTPServer) connector.Config {
	return connector.Config{"host": "127.0.0.1", "port": strconv.Itoa(s.Port), "username": "partner", "password": "pw",
		"hostKeyFingerprint": s.Fingerprint, "outboundDir": "outbound", "inboundDir": "inbound", "archiveDir": "archive", "errorDir": "error"}
}

func event(id, step string, at time.Time) connector.Event {
	raw := `{"type":"ObjectEvent","eventID":"` + id + `","eventTime":"` + at.Format(time.RFC3339) + `","eventTimeZoneOffset":"+00:00","action":"OBSERVE","bizStep":"` + step + `","epcList":["urn:epc:id:sgtin:9506000.013435.` + id + `"]}`
	return connector.Event{EventID: id, BizStep: step, EventTime: at, Raw: []byte(raw), Packs: []connector.Pack{{GTIN: "09506000134352", Serial: id}}}
}

func TestContract(t *testing.T) {
	s := server(t, nil)
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	contracttest.Run(t, sftpdrop.New(), cfgFor(s), []contracttest.Case{
		{Name: "shipping file", Event: event("e1", "shipping", at), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "receiving file", Event: event("e2", "receiving", at.Add(time.Hour)), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "no raw event", Event: connector.Event{EventID: "x", BizStep: "shipping"}, WantPayloads: 0},
	})
}

func TestOutboundDropXMLAndInboundSession(t *testing.T) {
	s := server(t, nil)
	cfg := cfgFor(s)
	cfg["outboundFormat"] = "xml"
	c := sftpdrop.New()
	ctx := context.Background()
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pl, err := c.MapBatch(ctx, []connector.Event{event("b", "shipping", at.Add(time.Minute)), event("a", "shipping", at), {EventID: "noraw"}})
	if err != nil || len(pl) != 1 || !strings.Contains(string(pl[0].Body), `"eventList":[{`) || strings.Index(string(pl[0].Body), `"a"`) > strings.Index(string(pl[0].Body), `"b"`) {
		t.Fatalf("batch: %v %+v", err, pl)
	}
	sub, err := c.Submit(ctx, cfg, pl[0])
	if err != nil || !strings.HasSuffix(sub.Ref, ".xml") || !strings.HasPrefix(sub.Ref, "/outbound/openspoor-") {
		t.Fatalf("submit: %v %+v", err, sub)
	}
	// Same key again: idempotent, no second file.
	if again, err := c.Submit(ctx, cfg, pl[0]); err != nil || again.Ref != sub.Ref {
		t.Fatalf("resubmit: %v %+v", err, again)
	}
	ep, _ := sftpdrop.EndpointFromConfig(cfg)
	sess, err := sftpdrop.Dial(ctx, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	files, err := sess.List("outbound")
	if err != nil || len(files) != 1 {
		t.Fatalf("list: %v %+v", err, files)
	}
	b, err := sess.Read(files[0].Path, sftpdrop.MaxFileSize)
	if err != nil || !strings.Contains(string(b), "<epcis:EPCISDocument") && !strings.Contains(string(b), "EPCISDocument") {
		t.Fatalf("read: %v %s", err, b)
	}
	if _, err := sess.Read(files[0].Path, 10); err == nil {
		t.Fatal("size cap must apply")
	}
	if _, err := sess.Read("/missing", 10); err == nil {
		t.Fatal("missing file must fail")
	}
	// Move twice with the same name: the second gets a timestamp prefix.
	p1, err := sess.Move(files[0].Path, "archive", time.Now())
	if err != nil || p1 != "/archive/"+files[0].Name {
		t.Fatalf("move: %v %s", err, p1)
	}
	if _, err := sess.Put("outbound", files[0].Name, []byte("x")); err != nil {
		t.Fatal(err)
	}
	p2, err := sess.Move("/outbound/"+files[0].Name, "/archive", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil || !strings.HasPrefix(p2, "/archive/20260102T030405") {
		t.Fatalf("move collision: %v %s", err, p2)
	}
	if _, err := sess.Put("outbound/.hidden-dir-is-fine", ".hidden", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if fs, _ := sess.List("outbound/.hidden-dir-is-fine"); len(fs) != 0 {
		t.Fatalf("hidden files must be skipped: %+v", fs)
	}
	if _, err := sess.List("nope"); err == nil {
		t.Fatal("listing a missing dir must fail")
	}
	if err := sess.MkdirAll("archive/" + files[0].Name); err == nil {
		t.Fatal("mkdir over a file must fail")
	}
	st, _ := c.PollStatus(ctx, cfg, sub)
	if st.State != connector.Accepted {
		t.Fatalf("poll: %+v", st)
	}
	al := c.HandleRejection(ctx, connector.Rejection{Message: "boom"})
	if al.Reason != "boom" || al.SuggestedFix == "" {
		t.Fatalf("alert: %+v", al)
	}
}

func TestHostKeyPinning(t *testing.T) {
	s := server(t, nil)
	cfg := cfgFor(s)
	other, err := mockhub.NewSFTPServer("partner", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	cfg["hostKeyFingerprint"] = other.Fingerprint // a valid fingerprint of a different server
	ep, err := sftpdrop.EndpointFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sftpdrop.Dial(context.Background(), ep)
	var hk *sftpdrop.HostKeyError
	if !errors.Is(err, sftpdrop.ErrHostKey) || !errors.As(err, &hk) || hk.Presented != s.Fingerprint || !strings.Contains(err.Error(), s.Fingerprint) {
		t.Fatalf("unknown host key must be rejected: %v", err)
	}
	// An empty pin is never accepted, even when calling Dial directly.
	ep.HostKey = ""
	if _, err := sftpdrop.Dial(context.Background(), ep); !errors.Is(err, sftpdrop.ErrHostKey) {
		t.Fatalf("empty pin: %v", err)
	}
	// Submit refuses too.
	if _, err := sftpdrop.New().Submit(context.Background(), cfg, connector.Payload{Key: "k", Body: []byte(`{}`)}); !errors.Is(err, sftpdrop.ErrHostKey) {
		t.Fatalf("submit with wrong key: %v", err)
	}
	fp, err := sftpdrop.Probe(context.Background(), "127.0.0.1", s.Port)
	if err != nil || fp != s.Fingerprint {
		t.Fatalf("probe: %v %s", err, fp)
	}
	if _, err := sftpdrop.Probe(context.Background(), "127.0.0.1", 1); err == nil {
		t.Fatal("probe of a closed port must fail")
	}
}

func TestPrivateKeyAuth(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	s := server(t, sshPub)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgFor(s)
	delete(cfg, "password")
	cfg["privateKey"] = string(pem.EncodeToMemory(block))
	ep, err := sftpdrop.EndpointFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatalf("key auth: %v", err)
	}
	if sess.Abs("x") != "/x" || sess.Abs("/a/../b") != "/b" {
		t.Fatal("abs")
	}
	_ = sess.Close()

	enc, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	cfg["privateKey"] = string(pem.EncodeToMemory(enc))
	if _, err := sftpdrop.EndpointFromConfig(cfg); err == nil {
		t.Fatal("encrypted key without passphrase must fail")
	}
	cfg["passphrase"] = "secret"
	ep, err = sftpdrop.EndpointFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err = sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatalf("passphrase key auth: %v", err)
	}
	_ = sess.Close()
	// Wrong password.
	cfg = cfgFor(s)
	cfg["password"] = "nope"
	ep, _ = sftpdrop.EndpointFromConfig(cfg)
	if _, err := sftpdrop.Dial(context.Background(), ep); err == nil {
		t.Fatal("wrong password must fail")
	}
	ep.PrivateKey = "garbage"
	if _, err := sftpdrop.Dial(context.Background(), ep); err == nil {
		t.Fatal("bad key must fail")
	}
	ep.PrivateKey = ""
	ep.Port = closedPort(t)
	if _, err := sftpdrop.Dial(context.Background(), ep); err == nil {
		t.Fatal("closed port must fail")
	}
}

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func TestValidate(t *testing.T) {
	c := sftpdrop.New()
	good := connector.Config{"host": "h", "username": "u", "password": "p", "hostKeyFingerprint": "SHA256:" + strings.Repeat("A", 43), "inboundDir": "in", "archiveDir": "arch"}
	if err := c.Validate(good); err != nil {
		t.Fatal(err)
	}
	if fp, err := sftpdrop.NormalizeFingerprint(strings.Repeat("A", 43) + "="); err != nil || fp != "SHA256:"+strings.Repeat("A", 43) {
		t.Fatalf("normalize: %v %s", err, fp)
	}
	for name, mut := range map[string]func(connector.Config){
		"no host":      func(c connector.Config) { c["host"] = " " },
		"bad port":     func(c connector.Config) { c["port"] = "70000" },
		"no user":      func(c connector.Config) { c["username"] = "" },
		"no auth":      func(c connector.Config) { c["password"] = "" },
		"bad key":      func(c connector.Config) { c["privateKey"] = "x" },
		"no pin":       func(c connector.Config) { c["hostKeyFingerprint"] = "" },
		"md5 pin":      func(c connector.Config) { c["hostKeyFingerprint"] = "MD5:aa:bb" },
		"format":       func(c connector.Config) { c["outboundFormat"] = "csv" },
		"dotdot":       func(c connector.Config) { c["outboundDir"] = "../etc" },
		"same archive": func(c connector.Config) { c["archiveDir"] = "in" },
	} {
		cfg := connector.Config{}
		for k, v := range good {
			cfg[k] = v
		}
		mut(cfg)
		if err := c.Validate(cfg); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if c.Outbound(connector.Config{}) {
		t.Fatal("no outbound dir means no drop")
	}
	if _, err := c.Submit(context.Background(), connector.Config{}, connector.Payload{}); err == nil {
		t.Fatal("disabled outbound must fail")
	}
	if _, err := c.Submit(context.Background(), connector.Config{"outboundDir": "o"}, connector.Payload{}); err == nil {
		t.Fatal("invalid endpoint must fail")
	}
	bad := connector.Config{"outboundDir": "o", "host": "h", "username": "u", "password": "p", "hostKeyFingerprint": "SHA256:" + strings.Repeat("A", 43)}
	if _, err := c.Submit(context.Background(), bad, connector.Payload{Key: "k", Body: []byte(`not json`)}); err == nil {
		t.Fatal("non-JSON body must fail to render")
	}
	bad["outboundFormat"] = "xml"
	if _, err := c.Submit(context.Background(), bad, connector.Payload{Key: "k", Body: []byte(`{}`)}); err == nil {
		t.Fatal("non-EPCIS body must fail to render as XML")
	}
	if n, b, err := sftpdrop.Render(connector.Config{"filePrefix": "acme"}, connector.Payload{Key: "k", Body: []byte(`{"a":1}`)}); err != nil || n != "acme-k.json" || !strings.Contains(string(b), "\n") {
		t.Fatalf("render: %v %s", err, n)
	}
}
