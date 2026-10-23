// Package sftpdrop is the SFTP integration connector: an outbound EPCIS file drop to a
// partner's SFTP server and the session primitives the host uses to poll the partner's
// inbound folder (list, read, archive).
//
// Security: the server's host key must be pinned by its SHA-256 fingerprint (as printed by
// `ssh-keygen -lf`); unknown or changed host keys are always rejected. Passwords, private
// keys and passphrases are instance secrets, sealed at rest by the host with the tenant
// keyring and passed to this package decrypted only for the duration of a call.
package sftpdrop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openspoor/openspoor/internal/core/egress"
	"io"
	"net"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/pkg/edi"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// ID is the connector id.
const ID = "sftp"

// MaxFileSize caps inbound files read into memory.
const MaxFileSize = 64 << 20

// Endpoint is one partner SFTP server.
type Endpoint struct {
	Host       string
	Port       int
	User       string
	Password   string
	PrivateKey string // OpenSSH or PEM
	Passphrase string
	// HostKey is the pinned SHA-256 fingerprint, "SHA256:<base64>".
	HostKey string
	Timeout time.Duration
}

// ErrHostKey is returned (wrapped in *HostKeyError) when the server's key is not the pinned one.
var ErrHostKey = errors.New("sftp: host key does not match the pinned fingerprint")

// HostKeyError reports the fingerprint the server presented.
type HostKeyError struct{ Presented string }

func (e *HostKeyError) Error() string {
	return "sftp: unknown host key " + e.Presented + " (it does not match the pinned fingerprint; verify it with the partner before pinning it)"
}

// Is makes errors.Is(err, ErrHostKey) work.
func (e *HostKeyError) Is(target error) bool { return target == ErrHostKey }

// NormalizeFingerprint accepts "SHA256:<base64>" or the bare base64 and returns "SHA256:<base64>".
func NormalizeFingerprint(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "SHA256:"))
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawStdEncoding.DecodeString(s); err != nil || len(b) != sha256.Size {
		return "", errors.New("host key fingerprint must be a SHA-256 fingerprint like SHA256:AbC… (ssh-keygen -lf)")
	}
	return "SHA256:" + s, nil
}

// EndpointFromConfig builds and validates an endpoint from instance config.
func EndpointFromConfig(cfg connector.Config) (Endpoint, error) {
	ep := Endpoint{Host: strings.TrimSpace(cfg["host"]), User: cfg["username"], Password: cfg["password"], PrivateKey: cfg["privateKey"],
		Passphrase: cfg["passphrase"], Port: 22, Timeout: 20 * time.Second}
	if ep.Host == "" {
		return ep, errors.New("host is required")
	}
	if p := strings.TrimSpace(cfg["port"]); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return ep, errors.New("port must be 1-65535")
		}
		ep.Port = n
	}
	if ep.User == "" {
		return ep, errors.New("username is required")
	}
	if ep.Password == "" && ep.PrivateKey == "" {
		return ep, errors.New("a password or a private key is required")
	}
	if ep.PrivateKey != "" {
		if _, err := ep.signer(); err != nil {
			return ep, err
		}
	}
	fp, err := NormalizeFingerprint(cfg["hostKeyFingerprint"])
	if err != nil {
		return ep, err
	}
	ep.HostKey = fp
	return ep, nil
}

func (ep Endpoint) signer() (ssh.Signer, error) {
	var s ssh.Signer
	var err error
	if ep.Passphrase != "" {
		s, err = ssh.ParsePrivateKeyWithPassphrase([]byte(ep.PrivateKey), []byte(ep.Passphrase))
	} else {
		s, err = ssh.ParsePrivateKey([]byte(ep.PrivateKey))
	}
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	return s, nil
}

func (ep Endpoint) addr() string { return net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)) }

// pinned returns a callback that accepts only the pinned key.
func pinned(want string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return &HostKeyError{Presented: got}
		}
		return nil
	}
}

func dialTCP(ctx context.Context, ep Endpoint) (net.Conn, error) {
	conn, err := egress.DialContext(ctx, "tcp", ep.addr(), ep.Timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(ep.Timeout))
	return conn, nil
}

// Session is an authenticated SFTP session.
type Session struct {
	ssh *ssh.Client
	c   *sftp.Client
	cwd string
}

// Dial connects, verifies the pinned host key and authenticates.
func Dial(ctx context.Context, ep Endpoint) (*Session, error) {
	if ep.Timeout == 0 {
		ep.Timeout = 20 * time.Second
	}
	var auths []ssh.AuthMethod
	if ep.PrivateKey != "" {
		s, err := ep.signer()
		if err != nil {
			return nil, err
		}
		auths = append(auths, ssh.PublicKeys(s))
	}
	if ep.Password != "" {
		auths = append(auths, ssh.Password(ep.Password))
	}
	conn, err := dialTCP(ctx, ep)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, ep.addr(), &ssh.ClientConfig{User: ep.User, Auth: auths, HostKeyCallback: pinned(ep.HostKey), Timeout: ep.Timeout})
	if err != nil {
		_ = conn.Close()
		var hk *HostKeyError
		if errors.As(err, &hk) {
			return nil, hk
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	sc, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	cwd, err := sc.Getwd()
	if err != nil || cwd == "" {
		cwd = "/"
	}
	return &Session{ssh: client, c: sc, cwd: cwd}, nil
}

var errProbe = errors.New("probe")

// Probe returns the SHA-256 fingerprint of the server's host key without authenticating, so
// an administrator can compare it with the one the partner communicated out of band.
func Probe(ctx context.Context, host string, port int) (string, error) {
	ep := Endpoint{Host: host, Port: port, Timeout: 10 * time.Second}
	conn, err := dialTCP(ctx, ep)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	var fp string
	_, _, _, err = ssh.NewClientConn(conn, ep.addr(), &ssh.ClientConfig{User: "probe", HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		fp = ssh.FingerprintSHA256(k)
		return errProbe
	}})
	if fp == "" {
		return "", fmt.Errorf("no host key received: %w", err)
	}
	return fp, nil
}

// Close ends the session.
func (s *Session) Close() error {
	_ = s.c.Close()
	return s.ssh.Close()
}

// Abs resolves dir against the login directory.
func (s *Session) Abs(dir string) string {
	if path.IsAbs(dir) {
		return path.Clean(dir)
	}
	return path.Join(s.cwd, dir)
}

// MkdirAll creates dir and its parents.
func (s *Session) MkdirAll(dir string) error {
	dir = s.Abs(dir)
	if fi, err := s.c.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	if parent := path.Dir(dir); parent != dir {
		if err := s.MkdirAll(parent); err != nil {
			return err
		}
	}
	return s.c.Mkdir(dir)
}

// Put writes data to dir/name atomically (upload to a .part file, then rename) and returns the path.
func (s *Session) Put(dir, name string, data []byte) (string, error) {
	if err := s.MkdirAll(dir); err != nil {
		return "", err
	}
	final := path.Join(s.Abs(dir), name)
	tmp := final + ".part"
	f, err := s.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if _, err := s.c.Stat(final); err == nil {
		_ = s.c.Remove(tmp)
		return final, nil // same idempotency key, already delivered
	}
	if err := s.c.Rename(tmp, final); err != nil {
		return "", err
	}
	return final, nil
}

// File is one remote file.
type File struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// List returns the regular files in dir, oldest first, skipping hidden and partial (.part) files.
func (s *Session) List(dir string) ([]File, error) {
	dir = s.Abs(dir)
	infos, err := s.c.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []File
	for _, fi := range infos {
		n := fi.Name()
		if !fi.Mode().IsRegular() || strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".part") {
			continue
		}
		out = append(out, File{Name: n, Path: path.Join(dir, n), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.Before(out[j].ModTime)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Read returns a file's content, refusing files larger than max bytes.
func (s *Session) Read(p string, max int64) ([]byte, error) {
	f, err := s.c.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return b, nil
}

// Move renames p into dir (created if needed). If a file with that name already exists
// there, the moved file gets a UTC timestamp prefix. It returns the new path.
func (s *Session) Move(p, dir string, now time.Time) (string, error) {
	if err := s.MkdirAll(dir); err != nil {
		return "", err
	}
	target := path.Join(s.Abs(dir), path.Base(p))
	if _, err := s.c.Stat(target); err == nil {
		target = path.Join(s.Abs(dir), now.UTC().Format("20060102T150405.000000Z")+"-"+path.Base(p))
	}
	if err := s.c.Rename(p, target); err != nil {
		return "", err
	}
	return target, nil
}

// Connector is the outbound file drop (connector.Connector and connector.BatchMapper).
type Connector struct {
	Dial func(ctx context.Context, ep Endpoint) (*Session, error)
}

// New creates the connector.
func New() *Connector { return &Connector{Dial: Dial} }

// Info implements connector.Connector.
func (c *Connector) Info() connector.Info {
	return connector.Info{
		ID: ID, Name: "SFTP file exchange", Country: "*",
		Description: "Drops captured EPCIS events as files on a partner's SFTP server and polls the partner's folder for EPCIS or EDI files (captured, then archived).",
		Triggers:    []string{connector.AllBizSteps},
		Spec:        "SSH File Transfer Protocol (draft-ietf-secsh-filexfer-02) over SSH (RFC 4251-4254); EPCIS 2.0 JSON-LD / XML; ANSI X12 and UN/EDIFACT via pkg/edi",
		Extra:       map[string]any{"category": "integration", "inbound": true},
		Config: []connector.ConfigField{
			{Key: "host", Label: "Host", Required: true},
			{Key: "port", Label: "Port", Default: "22"},
			{Key: "username", Label: "Username", Required: true},
			{Key: "password", Label: "Password", Secret: true, Help: "Password or private key."},
			{Key: "privateKey", Label: "Private key", Secret: true, Help: "OpenSSH or PEM private key; stored sealed."},
			{Key: "passphrase", Label: "Key passphrase", Secret: true},
			{Key: "hostKeyFingerprint", Label: "Host key fingerprint", Required: true,
				Help: "SHA256:… as printed by ssh-keygen -lf; confirm it with the partner. Connections to servers presenting any other key are refused."},
			{Key: "outboundDir", Label: "Outbound folder", Default: "outbound", Help: "Captured events are written here. Empty disables the outbound drop."},
			{Key: "outboundFormat", Label: "Outbound format", Default: "json", Help: "json (EPCIS 2.0 JSON-LD) or xml (EPCIS 2.0 XML)."},
			{Key: "bizSteps", Label: "Business steps", Help: "Comma-separated CBV business steps to send; empty sends every event."},
			{Key: "filePrefix", Label: "File name prefix", Default: "openspoor"},
			{Key: "partnerId", Label: "Partner (B2B documents)", Help: "Open Spoor partner id. B2B documents sent to this partner are dropped as EDI files in the document folder. Empty: EPCIS only."},
			{Key: "documentDir", Label: "Document folder", Help: "B2B documents (X12 or EDIFACT) for the partner above are written here. Empty disables the document drop."},
			{Key: "documentFormat", Label: "Document format", Default: "auto", Help: "x12, edifact or auto (X12 where the document type has an X12 writer, otherwise EDIFACT)."},
			{Key: "inboundDir", Label: "Inbound folder", Default: "inbound", Help: "Polled for EPCIS (.json, .jsonld, .xml) and EDI (X12, EDIFACT) files. Empty disables polling."},
			{Key: "archiveDir", Label: "Archive folder", Default: "archive", Help: "Processed inbound files are moved here."},
			{Key: "errorDir", Label: "Error folder", Default: "error", Help: "Inbound files that fail are moved here and raise an alert."},
		},
	}
}

// Validate checks an instance configuration (called by the host on save).
func (c *Connector) Validate(cfg connector.Config) error {
	if _, err := EndpointFromConfig(cfg); err != nil {
		return err
	}
	if f := cfg["outboundFormat"]; f != "" && f != "json" && f != "xml" {
		return errors.New("outboundFormat must be json or xml")
	}
	if f := cfg["documentFormat"]; f != "" && f != "auto" && f != "x12" && f != "edifact" {
		return errors.New("documentFormat must be auto, x12 or edifact")
	}
	if id := cfg["partnerId"]; id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("partnerId must be a partner id (UUID)")
		}
	}
	if cfg["documentDir"] != "" && cfg["partnerId"] == "" {
		return errors.New("documentDir needs partnerId: the partner whose documents are dropped there")
	}
	for _, k := range []string{"outboundDir", "inboundDir", "archiveDir", "errorDir", "documentDir"} {
		if strings.Contains(cfg[k], "..") {
			return errors.New(k + " must not contain ..")
		}
	}
	if cfg["inboundDir"] != "" && (cfg["archiveDir"] == "" || cfg["archiveDir"] == cfg["inboundDir"]) {
		return errors.New("archiveDir is required and must differ from inboundDir")
	}
	return nil
}

// DocumentFolder reports whether the instance drops B2B documents (documentDir and partnerId set).
func DocumentFolder(cfg connector.Config) bool {
	return cfg["documentDir"] != "" && cfg["partnerId"] != ""
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// DocumentFile renders a B2B document as the file to drop: the file name (prefix, type, number
// and a short content hash, so a corrected document with the same number is a new file and a
// resend of the same content is idempotent), the content and the format used. The format is the
// configured documentFormat; "auto" is X12 where the document type has an X12 writer, otherwise
// EDIFACT. It is an error when the type has no writer in the chosen format.
func DocumentFile(cfg connector.Config, d edi.Document) (name string, data []byte, format string, err error) {
	t, ok := edi.Lookup(d.Type)
	if !ok {
		return "", nil, "", fmt.Errorf("unknown document type %q", d.Type)
	}
	format = cfg["documentFormat"]
	if format == "" || format == "auto" {
		format = "edifact"
		if t.CanWriteX12() {
			format = "x12"
		}
	}
	var out, ext string
	switch format {
	case "x12":
		out, ext = edi.X12(d, edi.Envelope{}), ".x12"
	case "edifact":
		out, ext = edi.EDIFACT(d, edi.Envelope{}), ".edi"
	default:
		return "", nil, "", fmt.Errorf("unknown document format %q", format)
	}
	if out == "" {
		return "", nil, "", fmt.Errorf("document type %s has no %s writer", d.Type, strings.ToUpper(format))
	}
	prefix := cfg["filePrefix"]
	if prefix == "" {
		prefix = "openspoor"
	}
	sum := sha256.Sum256([]byte(out))
	base := strings.Trim(unsafeName.ReplaceAllString(d.Type+"-"+d.Number, "_"), "_")
	return prefix + "-" + base + "-" + hex.EncodeToString(sum[:4]) + ext, []byte(out), format, nil
}

// Outbound reports whether the instance drops files (outboundDir set).
func (c *Connector) Outbound(cfg connector.Config) bool { return cfg["outboundDir"] != "" }

// Map implements connector.Connector (a one-event batch).
func (c *Connector) Map(ctx context.Context, e connector.Event) ([]connector.Payload, error) {
	return c.MapBatch(ctx, []connector.Event{e})
}

// MapBatch implements connector.BatchMapper: one EPCIS document per capture.
func (c *Connector) MapBatch(_ context.Context, events []connector.Event) ([]connector.Payload, error) {
	var evs []connector.Event
	for _, e := range events {
		if len(e.Raw) > 0 && json.Valid(e.Raw) {
			evs = append(evs, e)
		}
	}
	if len(evs) == 0 {
		return nil, nil
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if !evs[i].EventTime.Equal(evs[j].EventTime) {
			return evs[i].EventTime.Before(evs[j].EventTime)
		}
		return evs[i].EventID < evs[j].EventID
	})
	raws := make([]json.RawMessage, len(evs))
	idsJoined := make([]string, len(evs))
	var packs []connector.Pack
	for i, e := range evs {
		raws[i] = e.Raw
		idsJoined[i] = e.EventID
		packs = append(packs, e.Packs...)
	}
	doc := map[string]any{"@context": []any{lib.DefaultContext}, "type": "EPCISDocument", "schemaVersion": "2.0",
		"creationDate": evs[len(evs)-1].EventTime.UTC().Format(time.RFC3339Nano), "epcisBody": map[string]any{"eventList": raws}}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(ID + "|" + strings.Join(idsJoined, "|")))
	return []connector.Payload{{Kind: "epcis-document", Key: hex.EncodeToString(h[:12]), Body: b, Packs: packs}}, nil
}

// Render returns the file name and content for a payload in the configured format.
func Render(cfg connector.Config, p connector.Payload) (string, []byte, error) {
	prefix := cfg["filePrefix"]
	if prefix == "" {
		prefix = "openspoor"
	}
	if cfg["outboundFormat"] == "xml" {
		doc, err := lib.ParseJSON(p.Body)
		if err != nil {
			return "", nil, err
		}
		b, err := lib.WriteXML(doc)
		return prefix + "-" + p.Key + ".xml", b, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, p.Body, "", "  "); err != nil {
		return "", nil, err
	}
	return prefix + "-" + p.Key + ".json", buf.Bytes(), nil
}

// Submit implements connector.Connector: uploads the file to the outbound folder.
func (c *Connector) Submit(ctx context.Context, cfg connector.Config, p connector.Payload) (connector.Submission, error) {
	if !c.Outbound(cfg) {
		return connector.Submission{}, errors.New("outbound file drop is disabled (no outbound folder)")
	}
	ep, err := EndpointFromConfig(cfg)
	if err != nil {
		return connector.Submission{}, err
	}
	name, data, err := Render(cfg, p)
	if err != nil {
		return connector.Submission{}, err
	}
	s, err := c.Dial(ctx, ep)
	if err != nil {
		return connector.Submission{}, err
	}
	defer func() { _ = s.Close() }()
	where, err := s.Put(cfg["outboundDir"], name, data)
	if err != nil {
		return connector.Submission{}, err
	}
	return connector.Submission{Ref: where}, nil
}

// PollStatus implements connector.Connector. A file on the partner's server is delivered;
// the partner's pickup is not observable over SFTP.
func (c *Connector) PollStatus(_ context.Context, _ connector.Config, s connector.Submission) (connector.Status, error) {
	raw, _ := json.Marshal(map[string]string{"delivered": s.Ref})
	return connector.Status{State: connector.Accepted, Raw: raw}, nil
}

// HandleRejection implements connector.Connector.
func (c *Connector) HandleRejection(_ context.Context, r connector.Rejection) connector.Alert {
	return connector.Alert{Severity: "error", Title: "SFTP file drop failed", Reason: strings.TrimPrefix(r.Code+": "+r.Message, ": "), Packs: r.Packs, Retryable: true,
		SuggestedFix: "Check the SFTP host, credentials, pinned host key and folder permissions in the connector settings, then resubmit."}
}
