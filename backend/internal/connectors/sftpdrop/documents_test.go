package sftpdrop

import (
	"strings"
	"testing"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/pkg/edi"
)

func TestDocumentFile(t *testing.T) {
	order := edi.Document{Type: "order", Number: "PO 7/1", Buyer: edi.Party{Name: "B", GLN: "0614141000005"}, Seller: edi.Party{Name: "S", GLN: "9506000000008"},
		Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 3, UOM: "EA", UnitPrice: 1}}}
	rfq := edi.Document{Type: "rfq", Number: "RFQ-1", Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 3, UOM: "EA"}}}
	cfg := connector.Config{}

	name, data, format, err := DocumentFile(cfg, order)
	if err != nil || format != "x12" || !strings.HasPrefix(name, "openspoor-order-PO_7_1-") || !strings.HasSuffix(name, ".x12") || !strings.HasPrefix(string(data), "ISA*") {
		t.Fatalf("auto order: %v %s %s", err, name, format)
	}
	again, _, _, _ := DocumentFile(cfg, order)
	if again != name {
		t.Errorf("the name is deterministic for the same content: %s vs %s", again, name)
	}
	order.Lines[0].Quantity = 4
	if changed, _, _, _ := DocumentFile(cfg, order); changed == name {
		t.Error("changed content, same name")
	}

	name, data, format, err = DocumentFile(connector.Config{"documentFormat": "auto", "filePrefix": "acme"}, rfq)
	if err != nil || format != "edifact" || !strings.HasPrefix(name, "acme-rfq-RFQ-1-") || !strings.HasSuffix(name, ".edi") || !strings.Contains(string(data), "UNH+1+REQOTE") {
		t.Fatalf("auto rfq: %v %s %s", err, name, format)
	}
	if _, _, f, err := DocumentFile(connector.Config{"documentFormat": "edifact"}, order); err != nil || f != "edifact" {
		t.Errorf("explicit EDIFACT: %v %s", err, f)
	}
	for name, c := range map[string]struct {
		cfg connector.Config
		doc edi.Document
		msg string
	}{
		"no x12 writer":  {connector.Config{"documentFormat": "x12"}, rfq, "no X12 writer"},
		"no edifact":     {connector.Config{}, edi.Document{Type: "grocery_order", Number: "G"}, "no EDIFACT writer"},
		"unknown type":   {connector.Config{}, edi.Document{Type: "nope"}, "unknown document type"},
		"unknown format": {connector.Config{"documentFormat": "xml"}, order, "unknown document format"},
	} {
		if _, _, _, err := DocumentFile(c.cfg, c.doc); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDocumentSettingsValidate(t *testing.T) {
	base := connector.Config{"host": "h", "username": "u", "password": "p", "hostKeyFingerprint": "SHA256:" + strings.Repeat("A", 43)}
	with := func(kv ...string) connector.Config {
		c := connector.Config{}
		for k, v := range base {
			c[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			c[kv[i]] = kv[i+1]
		}
		return c
	}
	c := New()
	pid := "0198c1c0-0000-7000-8000-000000000001"
	if err := c.Validate(with("documentDir", "documents", "partnerId", pid, "documentFormat", "edifact")); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, cfg := range map[string]connector.Config{
		"format":         with("documentFormat", "pdf"),
		"partner":        with("partnerId", "nope"),
		"dir no partner": with("documentDir", "documents"),
		"dotdot":         with("documentDir", "../x", "partnerId", pid),
	} {
		if err := c.Validate(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if DocumentFolder(with("documentDir", "d")) || !DocumentFolder(with("documentDir", "d", "partnerId", pid)) {
		t.Error("DocumentFolder needs both settings")
	}
}
