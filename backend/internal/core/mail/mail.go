// Package mail sends plain-text e-mail through an SMTP relay configured per installation
// (OPENSPOOR_SMTP_URL, e.g. smtp://user:pass@relay.example.com:587). Without it, New returns nil
// and callers skip sending. net/smtp upgrades to TLS with STARTTLS when the relay offers it.
package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// Sender sends one message.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// New parses an smtp:// URL; it returns nil when rawURL is empty.
func New(rawURL, from string) (Sender, error) {
	if rawURL == "" {
		return nil, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "smtp" || u.Host == "" {
		return nil, fmt.Errorf("OPENSPOOR_SMTP_URL must look like smtp://user:pass@host:587")
	}
	if from == "" {
		return nil, errors.New("OPENSPOOR_MAIL_FROM is required with OPENSPOOR_SMTP_URL")
	}
	s := &smtpSender{addr: u.Host, from: from}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		s.addr = net.JoinHostPort(u.Host, "587")
	}
	if u.User != nil {
		pw, _ := u.User.Password()
		s.auth = smtp.PlainAuth("", u.User.Username(), pw, u.Hostname())
	}
	return s, nil
}

type smtpSender struct {
	addr, from string
	auth       smtp.Auth
}

func (s *smtpSender) Send(_ context.Context, to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("header injection")
	}
	msg := "From: " + s.from + "\r\nTo: " + to + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")
	return smtp.SendMail(s.addr, s.auth, s.from, []string{to}, []byte(msg))
}
