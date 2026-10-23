package verify

import (
	"strings"
	"testing"
)

func TestParseAcceptAndScores(t *testing.T) {
	r := parseAccept("text/html;q=0.5, application/json, */*;q=0.1, ,text/*;q=bad")
	if r[0].value != "application/json" || r[0].q != 1 || r[1].value != "text/*" || r[len(r)-1].value != "*/*" {
		t.Fatalf("%+v", r)
	}
	if mimeScore(nil, "text/html") != 1 || mimeScore(r, "") != 1 {
		t.Fatal("no header or no type accepts everything")
	}
	if mimeScore(parseAccept("text/html"), "TEXT/HTML") != 1 || mimeScore(parseAccept("text/html"), "application/pdf") != 0 {
		t.Fatal("exact")
	}
	if mimeScore(parseAccept("text/*;q=0.4"), "text/plain") != 0.4 || mimeScore(parseAccept("*/*;q=0.2"), "image/png") != 0.2 {
		t.Fatal("wildcards")
	}
	// The most specific range decides, not the highest q.
	if got := mimeScore(parseAccept("text/html;q=0.1, */*"), "text/html"); got != 0.1 {
		t.Fatalf("specific wins: %v", got)
	}
	al := parseAccept("nl-NL,nl;q=0.8,en;q=0.5")
	if langScore(nil, "en") != 1 || langScore(al, "") != 0.001 {
		t.Fatal("empty cases")
	}
	if langScore(al, "nl-NL") != 1 || langScore(al, "nl") <= langScore(al, "en") || langScore(al, "de") != 0 {
		t.Fatalf("language ranking: nl-NL=%v nl=%v en=%v de=%v", langScore(al, "nl-NL"), langScore(al, "nl"), langScore(al, "en"), langScore(al, "de"))
	}
	if langScore(parseAccept("nl-BE"), "nl-NL") <= 0 || langScore(parseAccept("*"), "de") <= 0 {
		t.Fatal("primary subtag and wildcard")
	}
}

func TestNegotiate(t *testing.T) {
	cands := []Resolved{
		{LinkType: TypeLeaflet, Href: "https://x/en", Lang: "en", Type: "text/html"},
		{LinkType: TypeLeaflet, Href: "https://x/nl", Lang: "nl", Type: "text/html"},
		{LinkType: TypeLeaflet, Href: "https://x/pdf", Lang: "nl", Type: "application/pdf"},
		{LinkType: TypeLeaflet, Href: "https://x/any"},
	}
	pick := func(accept, lang string) string {
		l, ok := Negotiate(cands, accept, lang)
		if !ok {
			return ""
		}
		return l.Href
	}
	if pick("text/html", "nl") != "https://x/nl" || pick("application/pdf", "nl") != "https://x/pdf" || pick("text/html", "en-GB") != "https://x/en" {
		t.Fatal("language and type")
	}
	if pick("", "") != "https://x/en" {
		t.Fatalf("no preference: first candidate, got %s", pick("", ""))
	}
	if pick("image/png", "") != "https://x/any" { // only the link without a type accepts it
		t.Fatalf("typeless link: %s", pick("image/png", ""))
	}
	if _, ok := Negotiate(nil, "", ""); ok {
		t.Fatal("no candidates")
	}
	if _, ok := Negotiate(cands[:1], "application/pdf", ""); ok {
		t.Fatal("not acceptable")
	}
	// Equal matches: the brand owner's default wins.
	two := []Resolved{{Href: "a", Type: "text/html"}, {Href: "b", Type: "text/html", Default: true}}
	if l, _ := Negotiate(two, "text/html", ""); l.Href != "b" {
		t.Fatalf("default: %+v", l)
	}
}

func TestLinksetAndHeaders(t *testing.T) {
	links := []Resolved{
		{LinkType: TypePIP, Href: "https://b/pip", Type: "text/html", Lang: "en", Title: "Product"},
		{LinkType: TypePIP, Href: "https://b/pip-nl", Lang: "nl"},
		{LinkType: TypeLeaflet, Href: "https://b/leaflet"},
		{LinkType: "custom:x", Href: "https://b/x"},
	}
	ls := Linkset("https://r/01/1", links)
	ctx := ls["linkset"].([]map[string]any)[0]
	if ctx["anchor"] != "https://r/01/1" || len(ctx["https://gs1.org/voc/pip"].([]map[string]any)) != 2 || ctx["openspoor:leaflet"] == nil || ctx["custom:x"] == nil {
		t.Fatalf("%+v", ctx)
	}
	first := ctx["https://gs1.org/voc/pip"].([]map[string]any)[0]
	if first["hreflang"].([]string)[0] != "en" || first["type"] != "text/html" || first["title"] != "Product" {
		t.Fatalf("%+v", first)
	}
	h := linkHeader(append(links, Resolved{LinkType: TypePIP, Href: "https://b/<bad>"}, Resolved{LinkType: TypePIP, Href: "https://b/t", Title: "a \"quoted\"\nline"}), "https://r/01/1?linkType=all", 10)
	if len(h) != 6 || !strings.HasPrefix(h[0], `<https://r/01/1?linkType=all>; rel="linkset"`) ||
		h[1] != `<https://b/pip>; rel="https://gs1.org/voc/pip"; type="text/html"; hreflang="en"; title="Product"` {
		t.Fatalf("%q", h)
	}
	for _, v := range h {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("header injection: %q", v)
		}
	}
	if len(linkHeader(links, "u", 1)) != 2 {
		t.Fatal("max")
	}
}

func TestLinkInputValidation(t *testing.T) {
	ok := linkInput{GTIN: "9506000134352", LinkType: "gs1:pip", Href: "https://brand.example/p"}
	if err := ok.validate(); err != nil || ok.GTIN != "09506000134352" || ok.MIMEType != "text/html" || len(ok.Audiences) != 1 || ok.Audiences[0] != AudConsumer {
		t.Fatalf("%v %+v", err, ok)
	}
	ok2 := linkInput{GTIN: "09506000134352", LinkType: "https://gs1.org/voc/safetyInfo", Href: "https://b/s", Audiences: []string{"authority", "authority", "trading_partner"}, Lang: "pt-BR", MIMEType: "application/pdf"}
	if err := ok2.validate(); err != nil || ok2.LinkType != TypeSafetyInfo || len(ok2.Audiences) != 2 {
		t.Fatalf("%v %+v", err, ok2)
	}
	if err := (&linkInput{GTIN: "09506000134352", LinkType: "custom:ordering", Href: "https://b/o"}).validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]linkInput{
		"gtin":     {GTIN: "12345678901234", LinkType: "gs1:pip", Href: "https://b/p"},
		"lot":      {GTIN: "09506000134352", Lot: strings.Repeat("x", 21), LinkType: "gs1:pip", Href: "https://b/p"},
		"type":     {GTIN: "09506000134352", LinkType: "gs1:madeUp", Href: "https://b/p"},
		"custom":   {GTIN: "09506000134352", LinkType: "custom:Bad Name", Href: "https://b/p"},
		"audience": {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/p", Audiences: []string{"public"}},
		"mime":     {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/p", MIMEType: "html"},
		"lang":     {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/p", Lang: "English"},
		"http":     {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "http://b/p"},
		"js":       {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "javascript:alert(1)"},
		"nohost":   {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https:///p"},
		"space":    {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/a b"},
		"long":     {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/" + strings.Repeat("a", 2000)},
		"title":    {GTIN: "09506000134352", LinkType: "gs1:pip", Href: "https://b/p", Title: strings.Repeat("t", 201)},
	}
	for name, in := range bad {
		if err := in.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseKeyAndPaths(t *testing.T) {
	k, err := parseKey("01/9506000134352/10/A%2F7/21/S1")
	if err != nil || k.GTIN != "09506000134352" || k.Lot != "A/7" || k.Serial != "S1" || k.path() != "/01/09506000134352/10/A%2F7/21/S1" {
		t.Fatalf("%+v %v", k, err)
	}
	if k, _ = parseKey("/01/09506000134352"); k.path() != "/01/09506000134352" {
		t.Fatal(k.path())
	}
	for _, p := range []string{"01/12345678901234", "02/5", "x"} {
		if _, err := parseKey(p); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if _, err := parseKey("00/095060001234567890"); err == nil || !strings.Contains(err.Error(), "GTIN") {
		t.Fatalf("SSCC: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	if !hasAudience(Resolved{Audiences: []string{AudConsumer}}, AudAuthority) || hasAudience(Resolved{Audiences: []string{AudAuthority}}, AudTradingPartner) || !hasAudience(Resolved{Audiences: []string{AudTradingPartner}}, AudTradingPartner) {
		t.Fatal("hasAudience")
	}
	if relation(TypePIP) != "https://gs1.org/voc/pip" || relation(TypeLeaflet) != "openspoor:leaflet" || relation("other") != "other" {
		t.Fatal("relation")
	}
	if !wantsMedia("text/html, application/linkset+json;q=0.5", "application/linkset+json") || wantsMedia("*/*", "application/json") || wantsMedia("application/json;q=0", "application/json") {
		t.Fatal("wantsMedia")
	}
	if got := redirectAccept("application/json, text/html;q=0.5, application/linkset+json"); got != "text/html;q=0.500" {
		t.Fatalf("redirectAccept %q", got)
	}
	links := []Resolved{{LinkType: TypeLeaflet, Href: "l"}, {LinkType: TypePIP, Href: "p"}, {LinkType: TypeConsumerVerification, Href: "v"}, {LinkType: TypeSafetyInfo, Href: "s", Default: true}}
	if c := defaultCandidates(links); len(c) != 1 || c[0].Href != "s" {
		t.Fatalf("default flag first: %+v", c)
	}
	if c := defaultCandidates(links[:3]); c[0].Href != "v" {
		t.Fatalf("verification second: %+v", c)
	}
	if c := defaultCandidates(links[:2]); c[0].Href != "p" {
		t.Fatalf("pip third: %+v", c)
	}
	if c := defaultCandidates(links[:1]); len(c) != 1 || defaultCandidates(nil) != nil {
		t.Fatal("fallback")
	}
	if !hasType(links, TypePIP) || hasType(links, TypeRecallStatus) {
		t.Fatal("hasType")
	}
	if _, ok := knownType("custom:ok_1"); !ok {
		t.Fatal("custom")
	}
}
