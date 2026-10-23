package as2

import (
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
)

// Outgoing describes a message to send.
type Outgoing struct {
	MessageID   string // without angle brackets; NewMessageID makes one
	Subject     string
	Filename    string
	ContentType string
	Payload     []byte
	Sign        bool
	Encrypt     bool
	// MDN is the receipt mode requested: MDNNone, MDNSync or MDNAsync. A requested receipt is
	// always a signed one (signed-receipt-protocol=required); AsyncURL is where an async receipt
	// is delivered.
	MDN      string
	AsyncURL string
	Now      time.Time
}

func rfc1123(t time.Time) string { return t.UTC().Format(time.RFC1123) }

// Build signs and/or encrypts the payload and returns the HTTP message for peer.
func Build(st Station, peer Peer, o Outgoing) (*Wire, error) {
	if !ValidID(st.ID) || !ValidID(peer.ID) {
		return nil, errors.New("as2: AS2 identifiers must be 1-128 characters of letters, digits and . _ : @ -")
	}
	if !ValidID(o.MessageID) {
		return nil, errors.New("as2: invalid Message-ID")
	}
	if o.ContentType == "" || strings.ContainsAny(o.ContentType, "\r\n") || strings.ContainsAny(o.Subject, "\r\n") {
		return nil, errors.New("as2: invalid content type or subject")
	}
	if o.MDN == "" {
		o.MDN = MDNNone
	}
	if o.MDN == MDNAsync && (o.AsyncURL == "" || strings.ContainsAny(o.AsyncURL, "\r\n <>")) {
		return nil, errors.New("as2: async receipt needs a delivery URL")
	}
	if o.Encrypt && peer.Cert == nil {
		return nil, errors.New("as2: no partner certificate to encrypt to")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	entity := entityBytes(o.ContentType, o.Filename, o.Payload)
	w := &Wire{Header: map[string]string{}, MIC: micOf(entity)}
	var ct string
	var body []byte
	content := entity // what travels (before encryption), including its headers
	if o.Sign {
		sct, sbody, err := signedMultipart(entity, st)
		if err != nil {
			return nil, err
		}
		content = append([]byte("Content-Type: "+sct+"\r\n\r\n"), sbody...)
		ct, body = sct, sbody
	} else {
		ct, body = o.ContentType, o.Payload
	}
	if o.Encrypt {
		enc, err := pkcs7.Encrypt(content, []*x509.Certificate{peer.Cert})
		if err != nil {
			return nil, fmt.Errorf("as2: encrypt: %w", err)
		}
		ct, body = `application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"`, enc
	}
	h := w.Header
	h["AS2-Version"] = Version
	h["AS2-From"] = st.ID
	h["AS2-To"] = peer.ID
	h["Message-ID"] = "<" + o.MessageID + ">"
	h["MIME-Version"] = "1.0"
	h["Date"] = rfc1123(o.Now)
	h["Content-Type"] = ct
	h["Content-Transfer-Encoding"] = "binary"
	if o.Subject != "" {
		h["Subject"] = o.Subject
	}
	if o.Filename != "" && !o.Sign && !o.Encrypt {
		h["Content-Disposition"] = `attachment; filename="` + safeFilename(o.Filename) + `"`
	}
	if o.MDN != MDNNone {
		h["Disposition-Notification-To"] = st.ID
		h["Disposition-Notification-Options"] = "signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, " + MICAlgorithm
		if o.MDN == MDNAsync {
			h["Receipt-Delivery-Option"] = o.AsyncURL
		}
	}
	w.Body = body
	return w, nil
}
