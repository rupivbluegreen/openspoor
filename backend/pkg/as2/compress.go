package as2

import (
	"bytes"
	"compress/zlib"
	"encoding/asn1"
	"errors"
	"io"
	"strings"
)

// Compression of the MIME entity before signing and encryption, in the style of RFC 5402
// (CMS CompressedData, zlib). It is off by default and chosen per partner.
// TODO(spec): RFC 5402 style compression (AS2 1.1) is described from secondary sources; the
// order of compress / sign / encrypt, which entity the message integrity check covers (here the
// compressed entity, as it is the signed first part) and the partner profiles that expect it are
// not verified against a primary specification or a partner's implementation.

var (
	oidCompressedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 9}
	oidZlib           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 3, 8}
	oidData           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
)

type cmsAlgID struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type cmsEncapContent struct {
	Type    asn1.ObjectIdentifier
	Content []byte `asn1:"explicit,tag:0,optional"`
}

type cmsCompressedData struct {
	Version     int
	Compression cmsAlgID
	Content     cmsEncapContent
}

type cmsCompressedInfo struct {
	Type    asn1.ObjectIdentifier
	Content cmsCompressedData `asn1:"explicit,tag:0"`
}

const compressedCT = `application/pkcs7-mime; smime-type=compressed-data; name="smime.p7z"`

// compressEntity wraps a MIME entity (headers and body) in a CompressedData message and returns
// it as a MIME entity.
func compressEntity(entity []byte) ([]byte, error) {
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	if _, err := zw.Write(entity); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	der, err := asn1.Marshal(cmsCompressedInfo{Type: oidCompressedData, Content: cmsCompressedData{Version: 0, Compression: cmsAlgID{Algorithm: oidZlib},
		Content: cmsEncapContent{Type: oidData, Content: zb.Bytes()}}})
	if err != nil {
		return nil, err
	}
	out := []byte("Content-Type: " + compressedCT + "\r\nContent-Transfer-Encoding: binary\r\nContent-Disposition: attachment; filename=\"smime.p7z\"\r\n\r\n")
	return append(out, der...), nil
}

// isCompressed reports whether a MIME entity's Content-Type marks compressed data.
func isCompressed(contentType string) bool {
	mt, p := mediaType(contentType)
	return (mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime") && strings.EqualFold(p["smime-type"], "compressed-data")
}

// decompressEntity reads a CompressedData message (DER) and returns the MIME entity inside,
// bounded to MaxBody.
func decompressEntity(der []byte) ([]byte, error) {
	var ci cmsCompressedInfo
	if _, err := asn1.Unmarshal(der, &ci); err != nil || !ci.Type.Equal(oidCompressedData) {
		return nil, errors.New("as2: not a CMS compressed-data message")
	}
	cd := ci.Content
	if !cd.Compression.Algorithm.Equal(oidZlib) {
		return nil, errors.New("as2: unsupported compression algorithm")
	}
	zr, err := zlib.NewReader(bytes.NewReader(cd.Content.Content))
	if err != nil {
		return nil, errors.New("as2: malformed compressed content")
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, MaxBody+1))
	if err != nil {
		return nil, errors.New("as2: malformed compressed content")
	}
	if len(out) > MaxBody {
		return nil, errors.New("as2: decompressed message is too large")
	}
	return out, nil
}
