package as2

import (
	"bytes"
	"compress/zlib"
	"encoding/asn1"
	"strings"
	"testing"
)

func TestCompressRoundTripAndLimits(t *testing.T) {
	ent, err := compressEntity([]byte("Content-Type: text/plain\r\n\r\nhello hello hello"))
	if err != nil {
		t.Fatal(err)
	}
	_, der, _ := strings.Cut(string(ent), "\r\n\r\n")
	out, err := decompressEntity([]byte(der))
	if err != nil || !strings.HasSuffix(string(out), "hello hello hello") {
		t.Fatalf("%q %v", out, err)
	}
	if !isCompressed(compressedCT) || isCompressed("application/pkcs7-mime; smime-type=enveloped-data") || isCompressed("text/plain") {
		t.Fatal("isCompressed")
	}

	build := func(alg asn1.ObjectIdentifier, content []byte) []byte {
		b, err := asn1.Marshal(cmsCompressedInfo{Type: oidCompressedData, Content: cmsCompressedData{Compression: cmsAlgID{Algorithm: alg},
			Content: cmsEncapContent{Type: oidData, Content: content}}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	_, _ = zw.Write(make([]byte, MaxBody+10))
	_ = zw.Close()
	for name, in := range map[string][]byte{
		"not asn1":      []byte("junk"),
		"wrong type":    mustMarshal(t, cmsCompressedInfo{Type: oidData, Content: cmsCompressedData{Compression: cmsAlgID{Algorithm: oidZlib}, Content: cmsEncapContent{Type: oidData, Content: []byte{1}}}}),
		"other algo":    build(asn1.ObjectIdentifier{1, 2, 3}, []byte{1}),
		"bad zlib":      build(oidZlib, []byte("not zlib")),
		"truncated":     build(oidZlib, zb.Bytes()[:20]),
		"decompression": build(oidZlib, zb.Bytes()),
	} {
		if _, err := decompressEntity(in); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := asn1.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
