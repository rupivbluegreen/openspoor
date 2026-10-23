package as2

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// MDNInput describes a receipt to send.
type MDNInput struct {
	MessageID         string // the receipt's own Message-ID
	OriginalMessageID string
	// To is the AS2 identifier of the sender being answered.
	To string
	// MIC is the received content MIC (empty for an error receipt).
	MIC string
	// ErrorCode is a disposition modifier (ErrAuthentication, ...); empty means processed.
	ErrorCode string
	Text      string
	Sign      bool
	Now       time.Time
}

const reportingUA = "OpenSpoor AS2"

// BuildMDN builds a receipt (RFC 3798): multipart/report, wrapped in multipart/signed when
// signing. Receipts are never encrypted.
func BuildMDN(st Station, in MDNInput) (*Wire, error) {
	if !ValidID(st.ID) || !ValidID(in.To) || !ValidID(in.MessageID) || !ValidID(in.OriginalMessageID) {
		return nil, errors.New("as2: invalid identifier in receipt")
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	disp := "automatic-action/MDN-sent-automatically; processed"
	text := in.Text
	if in.ErrorCode != "" {
		disp += "/error: " + in.ErrorCode
	}
	if text == "" {
		text = "The message was received and processed."
		if in.ErrorCode != "" {
			text = "The message could not be accepted: " + in.ErrorCode + "."
		}
	}
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	var fields strings.Builder
	fields.WriteString("Reporting-UA: " + reportingUA + "\r\n")
	fields.WriteString("Original-Recipient: rfc822; " + st.ID + "\r\n")
	fields.WriteString("Final-Recipient: rfc822; " + st.ID + "\r\n")
	fields.WriteString("Original-Message-ID: <" + in.OriginalMessageID + ">\r\n")
	if in.ErrorCode == "" && in.MIC != "" {
		fields.WriteString("Received-Content-MIC: " + in.MIC + ", " + MICAlgorithm + "\r\n")
	}
	fields.WriteString("Disposition: " + disp + "\r\n")
	b := boundary()
	var rep []byte
	rep = append(rep, "--"+b+"\r\nContent-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n"+text+"\r\n"...)
	rep = append(rep, "--"+b+"\r\nContent-Type: message/disposition-notification\r\nContent-Transfer-Encoding: 7bit\r\n\r\n"+fields.String()...)
	rep = append(rep, "\r\n--"+b+"--\r\n"...)
	reportCT := `multipart/report; report-type=disposition-notification; boundary="` + b + `"`
	ct, body := reportCT, rep
	if in.Sign {
		entity := append([]byte("Content-Type: "+reportCT+"\r\n\r\n"), rep...)
		var err error
		if ct, body, err = signedMultipart(entity, st); err != nil {
			return nil, err
		}
	}
	return &Wire{Body: body, Header: map[string]string{
		"AS2-Version": Version, "AS2-From": st.ID, "AS2-To": in.To, "Message-ID": "<" + in.MessageID + ">",
		"MIME-Version": "1.0", "Date": rfc1123(in.Now), "Content-Type": ct, "Content-Transfer-Encoding": "binary",
	}}, nil
}

// MDN is a parsed receipt.
type MDN struct {
	OriginalMessageID string
	From, To          string
	// MIC is the Received-Content-MIC value (base64, without the algorithm).
	MIC    string
	MICAlg string
	// Processed is true for a "processed" disposition with no error modifier.
	Processed   bool
	Disposition string
	ErrorCode   string
	Signed      bool
}

// IsMDN reports whether an HTTP message carries a receipt rather than a payload: a
// multipart/report, or a multipart/signed whose signed content is one.
func IsMDN(h http.Header, body []byte) bool {
	mt, p := mediaType(h.Get("Content-Type"))
	switch mt {
	case "multipart/report":
		return true
	case "multipart/signed":
		parts, err := splitMultipart(body, p["boundary"])
		if err != nil || len(parts) == 0 {
			return false
		}
		eh, _, err := splitEntity(parts[0])
		if err != nil {
			return false
		}
		inner, _ := mediaType(eh.Get("Content-Type"))
		return inner == "multipart/report"
	}
	return false
}

// ParseMDN parses and verifies a receipt from peer. An unsigned receipt is rejected when
// requireSigned (a signed receipt was requested).
func ParseMDN(h http.Header, body []byte, peer Peer, requireSigned bool, now time.Time) (*MDN, error) {
	from := strings.TrimSpace(h.Get("AS2-From"))
	to := strings.TrimSpace(h.Get("AS2-To"))
	ct := h.Get("Content-Type")
	mt, params := mediaType(ct)
	m := &MDN{From: from, To: to}
	rparams := params
	rbody := body
	if mt == "multipart/signed" {
		parts, err := splitMultipart(body, params["boundary"])
		if err != nil || len(parts) != 2 {
			return nil, errors.New("as2: malformed signed receipt")
		}
		if f := verifyDetached(parts[0], parts[1], peer, now); f != nil {
			return nil, f
		}
		m.Signed = true
		eh, eb, err := splitEntity(parts[0])
		if err != nil {
			return nil, err
		}
		rbody = eb
		mt, rparams = mediaType(eh.Get("Content-Type"))
	}
	if mt != "multipart/report" {
		return nil, errors.New("as2: receipt is not a multipart/report")
	}
	if requireSigned && !m.Signed {
		return nil, errors.New("as2: a signed receipt was requested but the receipt is not signed")
	}
	parts, err := splitMultipart(rbody, rparams["boundary"])
	if err != nil || len(parts) < 2 {
		return nil, errors.New("as2: malformed receipt report")
	}
	var fields textproto.MIMEHeader
	for _, p := range parts {
		ph, pb, err := splitEntity(p)
		if err != nil {
			continue
		}
		if t, _ := mediaType(ph.Get("Content-Type")); t == "message/disposition-notification" {
			fields, err = textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(pb, "\r\n"), "\r\n\r\n"...)))).ReadMIMEHeader()
			if err != nil {
				return nil, errors.New("as2: malformed disposition fields")
			}
		}
	}
	if fields == nil {
		return nil, errors.New("as2: receipt has no disposition notification")
	}
	m.OriginalMessageID = stripAngles(fields.Get("Original-Message-ID"))
	if mic := fields.Get("Received-Content-MIC"); mic != "" {
		a, b, _ := strings.Cut(mic, ",")
		m.MIC, m.MICAlg = strings.TrimSpace(a), strings.ToLower(strings.TrimSpace(b))
	}
	m.Disposition = fields.Get("Disposition")
	_, tail, _ := strings.Cut(m.Disposition, ";")
	tail = strings.TrimSpace(tail)
	status, mod, _ := strings.Cut(tail, "/")
	m.Processed = strings.EqualFold(status, "processed") && mod == ""
	if mod != "" {
		_, code, _ := strings.Cut(mod, ":")
		m.ErrorCode = strings.TrimSpace(code)
	}
	if m.OriginalMessageID == "" {
		return nil, errors.New("as2: receipt has no Original-Message-ID")
	}
	return m, nil
}
