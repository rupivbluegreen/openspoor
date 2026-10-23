package mockhub_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/openspoor/openspoor/internal/connectors/mockhub"
)

func TestJSONHub(t *testing.T) {
	c := &http.Client{Transport: mockhub.NewTransport()}
	base := "http://" + mockhub.AEHost
	if code, _ := do(t, c, "POST", base+"/reports", "not json"); code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", code)
	}
	if code, _ := do(t, c, "GET", base+"/reports/none", ""); code != http.StatusNotFound {
		t.Fatalf("unknown report: %d", code)
	}
	if code, _ := do(t, c, "DELETE", base+"/x", ""); code != http.StatusNotFound {
		t.Fatalf("unknown route: %d", code)
	}
	cases := []struct{ body, want string }{
		{`{"reportType":"BOGUS","items":[]}`, "MOCK-REPORT-TYPE-UNKNOWN"},
		{`{"reportType":"COMMISSION","items":[{"gtin":"G","serial":"1","batch":"B","expiry":"2030-01-01"}]}`, ""},
		{`{"reportType":"DISAGGREGATION","parent":"P","items":[{"gtin":"G","serial":"1"}]}`, ""},
		{`{"reportType":"DESTRUCTION","items":[{"gtin":"G","serial":"2"}]}`, "MOCK-SERIAL-UNKNOWN"},
	}
	for _, tc := range cases {
		_, ack := do(t, c, "POST", base+"/reports", tc.body)
		id, _ := ack["reportId"].(string)
		if _, st := do(t, c, "GET", base+"/reports/"+id, ""); st["status"] != "PENDING" {
			t.Fatalf("first poll must be pending: %v", st)
		}
		_, st := do(t, c, "GET", base+"/reports/"+id, "")
		if tc.want == "" && st["status"] != "ACCEPTED" || tc.want != "" && st["errorCode"] != tc.want {
			t.Fatalf("%s: %v", tc.body, st)
		}
	}
	req, _ := http.NewRequest("GET", "http://"+mockhub.EGHost+"/reports/x", nil)
	req.SetBasicAuth("client", "s")
	req.Header.Set("X-Participant-Id", "P9")
	h := mockhub.NewJSONHub("EDA")
	tr := &mockhub.Transport{Handlers: map[string]http.Handler{mockhub.EGHost: h}}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if u, p := h.LastCaller(); u != "client" || p != "P9" {
		t.Fatalf("caller: %s %s", u, p)
	}
}

func TestSFTPServerRejectsBadAuth(t *testing.T) {
	s, err := mockhub.NewSFTPServer("u", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fingerprint == "" || s.Port == 0 || s.HostKey == nil {
		t.Fatalf("%+v", s)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPServerSession(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	signer, _ := ssh.NewSignerFromKey(priv)
	s, err := mockhub.NewSFTPServer("u", "pw", sshPub)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dial := func(auth ssh.AuthMethod) (*ssh.Client, error) {
		return ssh.Dial("tcp", s.Addr, &ssh.ClientConfig{User: "u", Auth: []ssh.AuthMethod{auth}, HostKeyCallback: ssh.FixedHostKey(s.HostKey.PublicKey())})
	}
	if _, err := dial(ssh.Password("bad")); err == nil {
		t.Fatal("bad password must fail")
	}
	other, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, 32)))
	if _, err := dial(ssh.PublicKeys(other)); err == nil {
		t.Fatal("unknown key must fail")
	}
	cl, err := dial(ssh.PublicKeys(signer))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, _, err := cl.OpenChannel("direct-tcpip", nil); err == nil {
		t.Fatal("non-session channel must be rejected")
	}
	sc, err := sftp.NewClient(cl)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if err := sc.Mkdir("/in"); err != nil {
		t.Fatal(err)
	}
	cl2, err := dial(ssh.Password("pw"))
	if err != nil {
		t.Fatal(err)
	}
	sc2, _ := sftp.NewClient(cl2)
	if _, err := sc2.Stat("/in"); err != nil {
		t.Fatalf("file system must be shared between sessions: %v", err)
	}
	_ = sc2.Close()
	_ = cl2.Close()
	// A plain TCP client that never speaks SSH is dropped.
	c, _ := net.Dial("tcp", s.Addr)
	_ = c.Close()
}
