package mockhub

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPServer is an in-process SFTP server with an in-memory file system, listening on
// 127.0.0.1, for integration tests and demos of the SFTP connector. It generates a fresh
// ed25519 host key; Fingerprint is the value to pin.
type SFTPServer struct {
	Addr        string
	Port        int
	Fingerprint string
	HostKey     ssh.Signer

	ln       net.Listener
	handlers sftp.Handlers
	wg       sync.WaitGroup
}

// NewSFTPServer starts a server accepting user with password and/or the given public key
// (either may be empty/nil).
func NewSFTPServer(user, password string, authorized ssh.PublicKey) (*SFTPServer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if password != "" && c.User() == user && string(p) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if authorized != nil && c.User() == user && string(k.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &SFTPServer{Addr: ln.Addr().String(), Port: ln.Addr().(*net.TCPAddr).Port, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		HostKey: signer, ln: ln, handlers: sftp.InMemHandler()}
	s.wg.Add(1)
	go s.accept(cfg)
	return s, nil
}

// Close stops accepting connections.
func (s *SFTPServer) Close() error {
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *SFTPServer) accept(cfg *ssh.ServerConfig) {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c, cfg)
	}
}

func (s *SFTPServer) serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = c.Close() }()
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, in, err := nc.Accept()
		if err != nil {
			continue
		}
		go func(in <-chan *ssh.Request) {
			for r := range in {
				_ = r.Reply(r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp", nil)
			}
		}(in)
		go func() {
			srv := sftp.NewRequestServer(ch, s.handlers)
			_ = srv.Serve()
			_ = srv.Close()
		}()
	}
}
