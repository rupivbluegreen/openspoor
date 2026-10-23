package as2

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"mime"
	"net/textproto"
	"strings"
)

// splitEntity splits a MIME entity into its header block and body.
func splitEntity(b []byte) (textproto.MIMEHeader, []byte, error) {
	if bytes.HasPrefix(b, []byte("\r\n")) {
		return textproto.MIMEHeader{}, b[2:], nil
	}
	i := bytes.Index(b, []byte("\r\n\r\n"))
	if i < 0 {
		return nil, nil, errors.New("as2: MIME entity has no header terminator")
	}
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(b[:i+4]))).ReadMIMEHeader()
	if err != nil {
		return nil, nil, errors.New("as2: malformed MIME headers")
	}
	return h, b[i+4:], nil
}

// splitMultipart returns the raw bytes of each part of a multipart body, exactly as they appear
// between the delimiters (a signature covers those bytes, so they must not be re-encoded).
func splitMultipart(body []byte, boundary string) ([][]byte, error) {
	if boundary == "" {
		return nil, errors.New("as2: multipart without a boundary")
	}
	delim := []byte("--" + boundary)
	crlfDelim := append([]byte("\r\n"), delim...)
	var rest []byte
	switch {
	case bytes.HasPrefix(body, delim):
		rest = body[len(delim):]
	default:
		i := bytes.Index(body, crlfDelim)
		if i < 0 {
			return nil, errors.New("as2: multipart boundary not found")
		}
		rest = body[i+len(crlfDelim):]
	}
	var parts [][]byte
	for {
		if bytes.HasPrefix(rest, []byte("--")) {
			return parts, nil
		}
		nl := bytes.Index(rest, []byte("\r\n"))
		if nl < 0 || len(bytes.TrimSpace(rest[:nl])) != 0 {
			return nil, errors.New("as2: malformed multipart delimiter")
		}
		rest = rest[nl+2:]
		end := bytes.Index(rest, crlfDelim)
		if end < 0 {
			return nil, errors.New("as2: multipart is not terminated")
		}
		parts = append(parts, rest[:end])
		rest = rest[end+len(crlfDelim):]
		if len(parts) > 8 {
			return nil, errors.New("as2: too many multipart parts")
		}
	}
}

// decodeCTE decodes a body by its Content-Transfer-Encoding (binary, 7bit, 8bit and base64).
func decodeCTE(cte string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "", "binary", "7bit", "8bit":
		return body, nil
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		if err != nil {
			return nil, errors.New("as2: invalid base64 body")
		}
		return out[:n], nil
	}
	return nil, errors.New("as2: unsupported Content-Transfer-Encoding " + cte)
}

func mediaType(v string) (string, map[string]string) {
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.SplitN(v, ";", 2)[0])), map[string]string{}
	}
	return mt, params
}

func safeFilename(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// entityBytes renders the payload as a MIME entity; the message integrity check covers it.
// TODO(spec): for a message that is neither signed nor encrypted, RFC 4130 does not pin down
// which header lines the integrity check covers; both ends here use Content-Type,
// Content-Transfer-Encoding: binary and Content-Disposition. Confirm against partner profiles.
func entityBytes(contentType, filename string, payload []byte) []byte {
	var b []byte
	b = append(b, "Content-Type: "+contentType+"\r\nContent-Transfer-Encoding: binary\r\n"...)
	if filename != "" {
		b = append(b, "Content-Disposition: attachment; filename=\""+safeFilename(filename)+"\"\r\n"...)
	}
	b = append(b, "\r\n"...)
	return append(b, payload...)
}

func mimeParams(v string) (string, map[string]string, error) {
	mt, p, err := mime.ParseMediaType(v)
	return mt, p, err
}
